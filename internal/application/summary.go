package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const (
	maxSummaryFindings        = 8
	maxSummaryEvidence        = 4
	maxSummaryRecommendations = 4
	maxSummaryNotes           = 4
	maxSummaryTextRunes       = 480
	SummaryQuotaPolicyVersion = "tenant-summary-quota-v1"
)

type SummaryService struct {
	provider    ports.ReportSummarizer
	timeout     time.Duration
	now         func() time.Time
	quotaStore  ports.SummaryQuotaStore
	quotaPolicy domain.SummaryQuotaPolicy
}

type SummaryServiceOption func(*SummaryService) error

func WithSummaryQuota(store ports.SummaryQuotaStore, policy domain.SummaryQuotaPolicy) SummaryServiceOption {
	return func(service *SummaryService) error {
		if store == nil {
			return errors.New("summary quota store is required")
		}
		if err := validateSummaryQuotaPolicy(policy); err != nil {
			return err
		}
		service.quotaStore = store
		service.quotaPolicy = policy
		return nil
	}
}

func NewSummaryService(provider ports.ReportSummarizer, timeout time.Duration, now func() time.Time, options ...SummaryServiceOption) (*SummaryService, error) {
	if provider == nil || timeout <= 0 || now == nil {
		return nil, errors.New("summary provider, positive timeout, and clock are required")
	}
	service := &SummaryService{provider: provider, timeout: timeout, now: now}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("summary service option is required")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

type summaryQuotaUsageIdentity struct {
	TenantID        string `json:"tenant_id"`
	InvestigationID string `json:"investigation_id"`
	PromptVersion   string `json:"prompt_version"`
}

// Enrich never changes the deterministic report fields and never fails the
// investigation. Invalid/provider output becomes an explicit deterministic
// fallback summary.
func (service *SummaryService) Enrich(ctx context.Context, requester domain.Principal, evidence []domain.Evidence, report domain.Report) domain.Report {
	if err := ValidateEngineOutput(report.InvestigationID, evidence, report); err != nil {
		return report
	}
	input := BuildSummaryInput(report)
	if err := validateSummaryInput(input); err != nil {
		return attachFallbackSummary(report, service.now().UTC())
	}
	reservation, ok := service.reserveSummaryQuota(ctx, requester, report.InvestigationID)
	if !ok {
		return attachFallbackSummary(report, service.now().UTC())
	}
	runCtx, cancel := context.WithTimeout(ctx, service.timeout)
	result, err := service.provider.Summarize(runCtx, input)
	cancel()
	if err != nil {
		service.settleSummaryQuota(reservation, domain.QuotaUnknown, 0, 0, reservation.ReservedTokens, "summary_external_outcome_unknown")
		return attachFallbackSummary(report, service.now().UTC())
	}
	if !validSummaryTokenUsage(result) {
		service.settleSummaryQuota(reservation, domain.QuotaUnknown, 0, 0, reservation.ReservedTokens, "summary_token_usage_invalid")
		return attachFallbackSummary(report, service.now().UTC())
	}
	if err := service.settleSummaryQuota(reservation, domain.QuotaSettled, result.InputTokens, result.OutputTokens, result.TotalTokens, "summary_succeeded"); err != nil {
		return attachFallbackSummary(report, service.now().UTC())
	}
	if service.quotaStore != nil && result.TotalTokens > reservation.ReservedTokens {
		return attachFallbackSummary(report, service.now().UTC())
	}
	summary, err := resolveProviderSummary(report, result, service.now().UTC())
	if err != nil {
		return attachFallbackSummary(report, service.now().UTC())
	}
	report.Summary = &summary
	return report
}

func (service *SummaryService) reserveSummaryQuota(ctx context.Context, requester domain.Principal, investigationID string) (domain.SummaryQuotaReservation, bool) {
	if service.quotaStore == nil {
		return domain.SummaryQuotaReservation{}, true
	}
	if !requester.Complete() || !safeSummaryIdentifier(investigationID, 1, 256) {
		return domain.SummaryQuotaReservation{}, false
	}
	tenantID := domain.TrustedTenantID(requester)
	usageKey, err := fingerprint.JSON(summaryQuotaUsageIdentity{
		TenantID: tenantID, InvestigationID: investigationID, PromptVersion: domain.EvidenceSummaryPromptVersion,
	})
	if err != nil {
		return domain.SummaryQuotaReservation{}, false
	}
	now := service.now().UTC()
	windowStart := fixedQuotaWindowStart(now, service.quotaPolicy.Window)
	reservation := domain.SummaryQuotaReservation{
		UsageKey: usageKey, TenantID: tenantID, InvestigationID: investigationID,
		PromptVersion: domain.EvidenceSummaryPromptVersion,
		WindowStart:   windowStart, WindowEnd: windowStart.Add(service.quotaPolicy.Window),
		ReservedTokens: service.quotaPolicy.ReservedTokensPerRequest,
		Status:         domain.QuotaReserved, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.quotaStore.ReserveSummaryQuota(ctx, reservation, service.quotaPolicy); err != nil {
		return domain.SummaryQuotaReservation{}, false
	}
	return reservation, true
}

func (service *SummaryService) settleSummaryQuota(
	reservation domain.SummaryQuotaReservation,
	status domain.QuotaReservationStatus,
	inputTokens, outputTokens, totalTokens int64,
	reasonCode string,
) error {
	if service.quotaStore == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return service.quotaStore.SettleSummaryQuota(
		settleCtx, reservation.UsageKey, status,
		inputTokens, outputTokens, totalTokens, reasonCode, service.now().UTC(),
	)
}

func validateSummaryQuotaPolicy(policy domain.SummaryQuotaPolicy) error {
	if policy.Version != SummaryQuotaPolicyVersion || policy.Window < time.Minute || policy.Window > 24*time.Hour ||
		policy.MaxRequests <= 0 || policy.MaxTokens <= 0 || policy.ReservedTokensPerRequest <= 0 ||
		policy.ReservedTokensPerRequest > policy.MaxTokens {
		return errors.New("summary quota policy is invalid")
	}
	return nil
}

func validSummaryTokenUsage(result domain.SummaryProviderResult) bool {
	const maxInt64 = int64(^uint64(0) >> 1)
	return result.InputTokens >= 0 && result.OutputTokens >= 0 && result.TotalTokens >= 0 &&
		result.InputTokens <= maxInt64-result.OutputTokens && result.TotalTokens >= result.InputTokens+result.OutputTokens &&
		(result.Mode != domain.SummaryModeModel || result.TotalTokens > 0)
}

func attachFallbackSummary(report domain.Report, generatedAt time.Time) domain.Report {
	summary := fallbackReportSummary(report, generatedAt)
	if err := ValidateReportSummary(report, *summary); err == nil {
		report.Summary = summary
	}
	return report
}

func validateSummaryInput(input domain.SummaryInput) error {
	if !safeSummaryIdentifier(input.Outcome, 1, 96) || len(input.Findings) == 0 || len(input.Findings) > maxSummaryFindings ||
		len(input.Evidence) == 0 || len(input.Evidence) > maxSummaryEvidence || len(input.Recommendations) > maxSummaryRecommendations {
		return errors.New("summary input envelope is invalid")
	}
	knownEvidence := make(map[string]struct{}, len(input.Evidence))
	for _, item := range input.Evidence {
		if !safeSummaryIdentifier(item.ID, 1, 128) || !safeSummaryIdentifier(item.Name, 1, 32) || item.ErrorCount < 0 || item.TopErrorCount < 0 || item.TopErrorCount > item.ErrorCount ||
			(item.TopError != "" && !safeGeneratedSummaryText(item.TopError, 1, maxSummaryTextRunes)) {
			return errors.New("summary input evidence is unsafe")
		}
		knownEvidence[item.ID] = struct{}{}
	}
	for _, finding := range input.Findings {
		if !safeSummaryIdentifier(finding.Code, 1, 96) || !safeGeneratedSummaryText(finding.Statement, 1, maxSummaryTextRunes) ||
			finding.Confidence < 0 || finding.Confidence > 1 || len(finding.EvidenceIDs) == 0 || len(finding.EvidenceIDs) > maxSummaryEvidence {
			return errors.New("summary input finding is unsafe")
		}
		if err := validateSummaryReferences(finding.EvidenceIDs, knownEvidence); err != nil {
			return err
		}
	}
	for _, recommendation := range input.Recommendations {
		if !safeSummaryIdentifier(recommendation.Code, 1, 96) || !safeGeneratedSummaryText(recommendation.Statement, 1, maxSummaryTextRunes) {
			return errors.New("summary input recommendation is unsafe")
		}
		if err := validateSummaryReferences(recommendation.EvidenceIDs, knownEvidence); err != nil {
			return err
		}
	}
	if input.CauseAnalysis != nil {
		if len(input.CauseAnalysis.Hypotheses) > domain.MaxChangeEvents || len(input.CauseAnalysis.Missing) > domain.MaxChangeEvents {
			return errors.New("summary input cause analysis exceeds limits")
		}
		for _, hypothesis := range input.CauseAnalysis.Hypotheses {
			if !safeSummaryIdentifier(hypothesis.ID, 1, 128) || !safeGeneratedSummaryText(hypothesis.Statement, 1, maxSummaryTextRunes) || len(hypothesis.Limitations) > 8 {
				return errors.New("summary input hypothesis is unsafe")
			}
			for _, limitation := range hypothesis.Limitations {
				if !safeGeneratedSummaryText(limitation, 1, maxSummaryTextRunes) {
					return errors.New("summary input limitation is unsafe")
				}
			}
		}
		for _, missing := range input.CauseAnalysis.Missing {
			if !safeSummaryIdentifier(missing, 1, 128) {
				return errors.New("summary input missing-input code is unsafe")
			}
		}
	}
	return nil
}

func BuildSummaryInput(report domain.Report) domain.SummaryInput {
	input := domain.SummaryInput{Outcome: report.Outcome}
	for index, finding := range report.Findings {
		if index >= maxSummaryFindings {
			break
		}
		input.Findings = append(input.Findings, domain.SummaryInputFinding{
			Code: finding.Code, Statement: finding.Statement, Confidence: finding.Confidence,
			Conclusive: finding.Conclusive, EvidenceIDs: append([]string(nil), finding.EvidenceIDs...),
		})
	}
	for index, evidence := range report.Evidence {
		if index >= maxSummaryEvidence {
			break
		}
		input.Evidence = append(input.Evidence, domain.SummaryInputEvidence{
			ID: evidence.ID, Name: evidence.Name, Complete: evidence.Complete, Truncated: evidence.Truncated,
			ErrorCount: evidence.ErrorCount, TopError: evidence.TopError, TopErrorCount: evidence.TopErrorCount,
		})
	}
	for index, recommendation := range report.Recommendations {
		if index >= maxSummaryRecommendations {
			break
		}
		input.Recommendations = append(input.Recommendations, domain.SummaryInputRecommendation{
			Code: recommendation.Code, Statement: recommendation.Statement,
			EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...),
		})
	}
	if report.CauseAnalysis != nil {
		cause := &domain.SummaryInputCauseAnalysis{
			Status:  report.CauseAnalysis.Status,
			Missing: append([]string(nil), report.CauseAnalysis.MissingInputs...),
		}
		for _, hypothesis := range report.CauseAnalysis.Hypotheses {
			cause.Hypotheses = append(cause.Hypotheses, domain.SummaryInputHypothesis{
				ID: hypothesis.ID, Statement: hypothesis.Statement, Verdict: hypothesis.Verdict,
				Confidence: hypothesis.Confidence, Limitations: append([]string(nil), hypothesis.Limitations...),
			})
		}
		input.CauseAnalysis = cause
	}
	return input
}

func resolveProviderSummary(report domain.Report, result domain.SummaryProviderResult, generatedAt time.Time) (domain.ReportSummary, error) {
	if result.Mode != domain.SummaryModeMock && result.Mode != domain.SummaryModeModel {
		return domain.ReportSummary{}, errors.New("summary provider mode is invalid")
	}
	if !safeSummaryIdentifier(result.Provider, 1, 64) || !safeSummaryIdentifier(result.Model, 1, 128) ||
		result.PromptVersion != domain.EvidenceSummaryPromptVersion || !summaryHash(result.PromptFingerprint) ||
		result.InputTokens < 0 || result.OutputTokens < 0 || result.TotalTokens < result.InputTokens+result.OutputTokens || result.LatencyMillis < 0 {
		return domain.ReportSummary{}, errors.New("summary provider metadata is invalid")
	}
	draft := result.Draft
	if !safeGeneratedSummaryText(draft.Phenomenon, 1, maxSummaryTextRunes) || len(draft.PhenomenonEvidenceIDs) == 0 || len(draft.PhenomenonEvidenceIDs) > maxSummaryEvidence ||
		len(draft.EvidenceNotes) == 0 || len(draft.EvidenceNotes) > maxSummaryNotes || len(draft.RecommendationCodes) > maxSummaryRecommendations {
		return domain.ReportSummary{}, errors.New("summary draft shape is invalid")
	}
	evidence := make(map[string]struct{}, len(report.Evidence))
	for _, item := range report.Evidence {
		evidence[item.ID] = struct{}{}
	}
	if err := validateSummaryReferences(draft.PhenomenonEvidenceIDs, evidence); err != nil {
		return domain.ReportSummary{}, err
	}
	for _, note := range draft.EvidenceNotes {
		if !safeGeneratedSummaryText(note.Statement, 1, maxSummaryTextRunes) {
			return domain.ReportSummary{}, errors.New("summary evidence note is unsafe")
		}
		if err := validateSummaryReferences(note.EvidenceIDs, evidence); err != nil {
			return domain.ReportSummary{}, err
		}
	}

	hypotheses := make(map[string]domain.CauseHypothesis)
	if report.CauseAnalysis != nil {
		for _, hypothesis := range report.CauseAnalysis.Hypotheses {
			hypotheses[hypothesis.ID] = hypothesis
		}
	}
	possibleCause := ""
	limitations := []string{"AI 摘要只解释已治理证据，不改变确定性结论、原因判断或权限。"}
	if draft.CauseHypothesisID != "" {
		hypothesis, exists := hypotheses[draft.CauseHypothesisID]
		if !exists || hypothesis.Verdict != domain.CauseVerdictSupportedCandidate {
			return domain.ReportSummary{}, errors.New("summary selected an unsupported cause hypothesis")
		}
		possibleCause = hypothesis.Statement
		limitations = append(limitations, hypothesis.Limitations...)
	}

	recommendations := make(map[string]domain.Recommendation, len(report.Recommendations))
	for _, recommendation := range report.Recommendations {
		recommendations[recommendation.Code] = recommendation
	}
	nextSteps := make([]domain.SummaryNextStep, 0, len(draft.RecommendationCodes))
	seenCodes := make(map[string]struct{}, len(draft.RecommendationCodes))
	for _, code := range draft.RecommendationCodes {
		recommendation, exists := recommendations[code]
		if !exists {
			return domain.ReportSummary{}, errors.New("summary invented a recommendation code")
		}
		if _, duplicate := seenCodes[code]; duplicate {
			return domain.ReportSummary{}, errors.New("summary duplicated a recommendation code")
		}
		seenCodes[code] = struct{}{}
		nextSteps = append(nextSteps, domain.SummaryNextStep{
			Code: code, Statement: recommendation.Statement,
			EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...),
		})
	}
	if len(report.Recommendations) > 0 && len(nextSteps) == 0 {
		return domain.ReportSummary{}, errors.New("summary omitted all deterministic recommendations")
	}

	summary := domain.ReportSummary{
		Status: domain.SummaryGenerated, Mode: result.Mode,
		Provider: result.Provider, Model: result.Model, RequestID: boundedSummaryRequestID(result.RequestID),
		PromptVersion: result.PromptVersion, PromptFingerprint: result.PromptFingerprint,
		Phenomenon: draft.Phenomenon, PhenomenonEvidenceIDs: append([]string(nil), draft.PhenomenonEvidenceIDs...),
		PossibleCause: possibleCause, CauseHypothesisID: draft.CauseHypothesisID,
		EvidenceNotes: cloneSummaryNotes(draft.EvidenceNotes), Limitations: boundedSummaryLimitations(limitations), NextSteps: nextSteps,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, TotalTokens: result.TotalTokens,
		LatencyMillis: result.LatencyMillis, GeneratedAt: generatedAt,
	}
	if err := ValidateReportSummary(report, summary); err != nil {
		return domain.ReportSummary{}, err
	}
	return summary, nil
}

func fallbackReportSummary(report domain.Report, generatedAt time.Time) *domain.ReportSummary {
	summary := &domain.ReportSummary{
		Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback,
		Provider: "deterministic_fallback", Model: "not_applicable",
		PromptVersion:     domain.EvidenceSummaryPromptVersion,
		PromptFingerprint: strings.Repeat("0", sha256.Size*2),
		GeneratedAt:       generatedAt,
		Limitations:       []string{"模型摘要不可用；当前内容直接来自已验证的确定性报告。"},
	}
	if len(report.Findings) > 0 {
		if safeGeneratedSummaryText(report.Findings[0].Statement, 1, maxSummaryTextRunes) {
			summary.Phenomenon = report.Findings[0].Statement
		} else {
			summary.Phenomenon = "确定性调查报告已生成；模型摘要因安全校验未使用。"
		}
		seenEvidence := make(map[string]struct{}, len(report.Findings[0].EvidenceIDs))
		for _, evidenceID := range report.Findings[0].EvidenceIDs {
			if len(summary.PhenomenonEvidenceIDs) >= maxSummaryEvidence {
				break
			}
			if _, duplicate := seenEvidence[evidenceID]; duplicate {
				continue
			}
			seenEvidence[evidenceID] = struct{}{}
			summary.PhenomenonEvidenceIDs = append(summary.PhenomenonEvidenceIDs, evidenceID)
		}
	}
	if summary.Phenomenon == "" {
		summary.Phenomenon = "当前没有可生成摘要的确定性结论。"
		for _, item := range report.Evidence {
			summary.PhenomenonEvidenceIDs = append(summary.PhenomenonEvidenceIDs, item.ID)
		}
	}
	if report.CauseAnalysis != nil {
		for _, hypothesis := range report.CauseAnalysis.Hypotheses {
			if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && safeGeneratedSummaryText(hypothesis.Statement, 1, maxSummaryTextRunes) {
				summary.PossibleCause = hypothesis.Statement
				summary.CauseHypothesisID = hypothesis.ID
				summary.Limitations = append(summary.Limitations, hypothesis.Limitations...)
				break
			}
		}
	}
	for index, item := range report.Evidence {
		if index >= maxSummaryNotes {
			break
		}
		quality := "完整"
		if !item.Complete || item.Truncated {
			quality = "不完整"
		}
		name := item.Name
		if !safeSummaryIdentifier(name, 1, 32) {
			name = "证据"
		}
		summary.EvidenceNotes = append(summary.EvidenceNotes, domain.SummaryEvidenceNote{
			Statement:   fmt.Sprintf("%s 窗口证据%s，错误数为 %d。", name, quality, item.ErrorCount),
			EvidenceIDs: []string{item.ID},
		})
	}
	for index, recommendation := range report.Recommendations {
		if index >= maxSummaryRecommendations {
			break
		}
		summary.NextSteps = append(summary.NextSteps, domain.SummaryNextStep{
			Code: recommendation.Code, Statement: recommendation.Statement,
			EvidenceIDs: append([]string(nil), recommendation.EvidenceIDs...),
		})
	}
	return summary
}

func ValidateReportSummary(report domain.Report, summary domain.ReportSummary) error {
	if summary.Status != domain.SummaryGenerated && summary.Status != domain.SummaryFallback {
		return errors.New("report summary status is invalid")
	}
	if summary.Mode != domain.SummaryModeMock && summary.Mode != domain.SummaryModeModel && summary.Mode != domain.SummaryModeFallback {
		return errors.New("report summary mode is invalid")
	}
	if summary.Status == domain.SummaryGenerated && summary.Mode == domain.SummaryModeFallback {
		return errors.New("generated report summary cannot use fallback mode")
	}
	if summary.Status == domain.SummaryFallback && (summary.Mode != domain.SummaryModeFallback || summary.Provider != "deterministic_fallback" || summary.Model != "not_applicable" || summary.RequestID != "" || summary.InputTokens != 0 || summary.OutputTokens != 0 || summary.TotalTokens != 0 || summary.LatencyMillis != 0) {
		return errors.New("fallback report summary metadata is invalid")
	}
	if !safeSummaryIdentifier(summary.Provider, 1, 64) || !safeSummaryIdentifier(summary.Model, 1, 128) ||
		summary.PromptVersion != domain.EvidenceSummaryPromptVersion || !summaryHash(summary.PromptFingerprint) || summary.GeneratedAt.IsZero() ||
		summary.InputTokens < 0 || summary.OutputTokens < 0 || summary.TotalTokens < summary.InputTokens+summary.OutputTokens || summary.LatencyMillis < 0 ||
		!safeGeneratedSummaryText(summary.Phenomenon, 1, maxSummaryTextRunes) {
		return errors.New("report summary envelope is invalid")
	}
	evidence := make(map[string]struct{}, len(report.Evidence))
	for _, item := range report.Evidence {
		evidence[item.ID] = struct{}{}
	}
	if err := validateSummaryReferences(summary.PhenomenonEvidenceIDs, evidence); err != nil {
		return err
	}
	if len(summary.EvidenceNotes) > maxSummaryNotes || len(summary.NextSteps) > maxSummaryRecommendations || len(summary.Limitations) > 8 {
		return errors.New("report summary collections exceed limits")
	}
	for _, note := range summary.EvidenceNotes {
		if !safeGeneratedSummaryText(note.Statement, 1, maxSummaryTextRunes) {
			return errors.New("report summary evidence note is invalid")
		}
		if err := validateSummaryReferences(note.EvidenceIDs, evidence); err != nil {
			return err
		}
	}
	recommendations := make(map[string]domain.Recommendation, len(report.Recommendations))
	for _, item := range report.Recommendations {
		recommendations[item.Code] = item
	}
	seenSteps := make(map[string]struct{}, len(summary.NextSteps))
	for _, step := range summary.NextSteps {
		item, exists := recommendations[step.Code]
		_, duplicate := seenSteps[step.Code]
		if !exists || duplicate || item.Statement != step.Statement || !sameStringSet(item.EvidenceIDs, step.EvidenceIDs) {
			return errors.New("report summary next step diverges from deterministic recommendation")
		}
		seenSteps[step.Code] = struct{}{}
	}
	if summary.CauseHypothesisID != "" {
		found := false
		if report.CauseAnalysis != nil {
			for _, hypothesis := range report.CauseAnalysis.Hypotheses {
				if hypothesis.ID == summary.CauseHypothesisID && hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && hypothesis.Statement == summary.PossibleCause {
					found = true
				}
			}
		}
		if !found {
			return errors.New("report summary cause diverges from deterministic hypothesis")
		}
	} else if summary.PossibleCause != "" {
		return errors.New("report summary cause lacks a hypothesis reference")
	}
	for _, limitation := range summary.Limitations {
		if !safeSummaryText(limitation, 1, maxSummaryTextRunes) {
			return errors.New("report summary limitation is invalid")
		}
	}
	return nil
}

func validateSummaryReferences(ids []string, evidence map[string]struct{}) error {
	if len(ids) == 0 || len(ids) > maxSummaryEvidence {
		return errors.New("summary evidence references are empty or oversized")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := evidence[id]; !exists {
			return errors.New("summary references unknown evidence")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("summary duplicates an evidence reference")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, value := range left {
		values[value]++
	}
	for _, value := range right {
		values[value]--
	}
	for _, count := range values {
		if count != 0 {
			return false
		}
	}
	return true
}

func cloneSummaryNotes(notes []domain.SummaryEvidenceNote) []domain.SummaryEvidenceNote {
	cloned := make([]domain.SummaryEvidenceNote, len(notes))
	for index, note := range notes {
		cloned[index] = domain.SummaryEvidenceNote{Statement: note.Statement, EvidenceIDs: append([]string(nil), note.EvidenceIDs...)}
	}
	return cloned
}

func boundedSummaryLimitations(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if len(result) >= 8 || !safeSummaryText(value, 1, maxSummaryTextRunes) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func boundedSummaryRequestID(value string) string {
	if safeSummaryIdentifier(value, 0, 256) {
		return value
	}
	return ""
}

func safeSummaryIdentifier(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("_-.:/", character) {
			continue
		}
		return false
	}
	return true
}

func safeSummaryText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

var (
	summarySensitivePattern    = regexp.MustCompile("(?i)(bearer\\s+[A-Za-z0-9._~+/=-]{8,}|LTAI[A-Za-z0-9]{12,}|eyJ[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+|https?://|```|\\bcurl\\b|\\bkubectl\\b|\\brm\\s+-)")
	summaryUnsafeActionPattern = regexp.MustCompile(`(?i)(自动|立即|直接).{0,8}(删除|回滚|重启|扩容|执行|修改)|delete\s|rollback\s|restart\s|execute\s`)
)

func safeGeneratedSummaryText(value string, minimum, maximum int) bool {
	return safeSummaryText(value, minimum, maximum) && !summarySensitivePattern.MatchString(value) && !summaryUnsafeActionPattern.MatchString(value)
}

func summaryHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
