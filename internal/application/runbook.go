package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	runbookMissingConclusiveSpike     = "conclusive_error_spike"
	runbookMissingDeterministicAdvice = "deterministic_recommendations"
	runbookMissingResourceIdentity    = "governed_resource_identity"
	runbookMissingSourceDisabled      = "runbook_source_disabled"
	runbookMissingSourceAvailable     = "runbook_source_available"
	runbookMissingValidSet            = "valid_runbook_set"
	runbookMissingCompleteSet         = "complete_runbook_set"
	runbookMissingUntruncatedSet      = "untruncated_runbook_set"
	runbookMissingMatch               = "runbook_match"

	// A small clock-skew allowance keeps independently maintained knowledge
	// catalogs usable without allowing a source to claim an arbitrarily fresh
	// revision after either the investigation report or trusted service clock.
	maxRunbookFutureSkew  = 5 * time.Minute
	defaultRunbookTimeout = 5 * time.Second
)

// RunbookService enriches a validated deterministic report with bounded,
// human-review-only guidance. Source failures deliberately degrade only this
// optional projection and never invalidate the investigation itself.
type RunbookService struct {
	source     ports.RunbookSource
	catalog    ports.ResourceCatalog
	dataSource domain.RunbookGuidanceDataSource
	timeout    time.Duration
	now        func() time.Time
}

type RunbookServiceOption func(*RunbookService) error

func WithRunbookClock(now func() time.Time) RunbookServiceOption {
	return func(service *RunbookService) error {
		if now == nil {
			return errors.New("runbook clock is required")
		}
		service.now = now
		return nil
	}
}

func WithRunbookTimeout(timeout time.Duration) RunbookServiceOption {
	return func(service *RunbookService) error {
		if timeout <= 0 {
			return errors.New("runbook timeout must be positive")
		}
		service.timeout = timeout
		return nil
	}
}

func NewRunbookService(
	source ports.RunbookSource,
	catalog ports.ResourceCatalog,
	dataSource domain.RunbookGuidanceDataSource,
	options ...RunbookServiceOption,
) (*RunbookService, error) {
	if source == nil {
		return nil, errors.New("runbook source is required")
	}
	if catalog == nil {
		return nil, errors.New("runbook resource catalog is required")
	}
	if !domain.ValidateRunbookGuidanceDataSource(dataSource) {
		return nil, errors.New("runbook data source is invalid")
	}
	service := &RunbookService{
		source: source, catalog: catalog, dataSource: dataSource,
		timeout: defaultRunbookTimeout, now: time.Now,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("runbook service option is required")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (service *RunbookService) Enrich(ctx context.Context, evidence []domain.Evidence, report domain.Report) (domain.Report, error) {
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !reportHasConclusiveErrorSpike(report) {
		report.RunbookGuidance = &domain.RunbookGuidance{
			Status:        domain.RunbookGuidanceSkippedNoTrigger,
			DataSource:    service.dataSource,
			MethodVersion: domain.RunbookGuidanceVersion,
		}
		return report, nil
	}

	evidenceByID := runbookEvidenceMap(evidence)
	resourceID, ok := governedRunbookResourceFromMap(evidenceByID)
	if !ok {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingResourceIdentity)
		return report, nil
	}
	job, ok := runJobFromContext(ctx)
	if !ok {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingResourceIdentity)
		return report, nil
	}
	current, baseline, pairOK := governedRunbookEvidencePair(evidenceByID)
	if !pairOK || !runbookEvidenceMatchesRequest(current, baseline, job.Request) {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingResourceIdentity)
		return report, nil
	}
	resource, err := service.catalog.Resolve(ctx, job.Request.Service, job.Request.Environment)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return report, ctxErr
		}
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingResourceIdentity)
		return report, nil
	}
	if resource.ID != resourceID || resource.Service != job.Request.Service || resource.Environment != job.Request.Environment ||
		domain.ValidateResourceID(resource.ID) != nil || !service.catalog.Allowed(ctx, job.Request.Requester, resource.ID) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return report, ctxErr
		}
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingResourceIdentity)
		return report, nil
	}
	recommendations := governedRunbookRecommendations(evidenceByID, report)
	if len(recommendations.codes) == 0 {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingDeterministicAdvice)
		return report, nil
	}

	codes := recommendations.codes
	if len(codes) > domain.MaxRunbookEntries {
		codes = codes[:domain.MaxRunbookEntries]
	}
	query := domain.RunbookQuery{
		ResourceID:          resourceID,
		RecommendationCodes: append([]string(nil), codes...),
		Limit:               domain.MaxRunbookEntries,
	}
	if err := domain.ValidateRunbookQuery(query); err != nil {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingValidSet)
		return report, nil
	}

	lookupCtx, cancelLookup := context.WithTimeout(ctx, service.timeout)
	set, err := service.source.Lookup(lookupCtx, query)
	lookupErr := lookupCtx.Err()
	cancelLookup()
	if err != nil || lookupErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return report, ctxErr
		}
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingSourceAvailable)
		return report, nil
	}
	if err := domain.ValidateRunbookSet(set, query); err != nil {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingValidSet)
		return report, nil
	}
	trustedNow := service.now().UTC()
	if !runbookEntriesWithinReferenceTime(set.Entries, report.GeneratedAt) ||
		!runbookEntriesWithinReferenceTime(set.Entries, trustedNow) {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingValidSet)
		return report, nil
	}
	if set.ReasonCode == domain.RunbookReasonDisabled {
		report.RunbookGuidance = service.unavailableRunbookGuidance(runbookMissingSourceDisabled)
		return report, nil
	}

	guidance, err := buildRunbookGuidance(set, recommendations.evidenceByCode, service.dataSource)
	if err != nil {
		return report, fmt.Errorf("build governed runbook guidance: %w", err)
	}
	report.RunbookGuidance = guidance
	return report, nil
}

type runbookRecommendationSet struct {
	codes          []string
	evidenceByCode map[string][]string
}

func governedRunbookRecommendations(evidence map[string]domain.Evidence, report domain.Report) runbookRecommendationSet {
	current, baseline, ok := governedRunbookEvidencePair(evidence)
	if !ok || !runbookHasDeterministicSpike(current, baseline) {
		return runbookRecommendationSet{}
	}

	expected := make(map[string][]string, 3)
	expected["inspect_top_error_pattern"] = []string{current.ID, baseline.ID}
	if runbookHasNewPatternCandidate(current, baseline) {
		expected["compare_recent_changes"] = []string{current.ID, baseline.ID}
	}
	if runbookHasHotInstance(current) {
		expected["inspect_hot_instance"] = []string{current.ID}
	}

	counts := make(map[string]int, len(report.Recommendations))
	actualEvidence := make(map[string][]string, len(report.Recommendations))
	for _, recommendation := range report.Recommendations {
		if _, governed := expected[recommendation.Code]; !governed {
			continue
		}
		counts[recommendation.Code]++
		actualEvidence[recommendation.Code] = recommendation.EvidenceIDs
	}

	result := runbookRecommendationSet{evidenceByCode: make(map[string][]string, len(expected))}
	for code, evidenceIDs := range expected {
		if counts[code] != 1 || !sameIDSet(actualEvidence[code], evidenceIDs) {
			continue
		}
		sortedEvidence := append([]string(nil), evidenceIDs...)
		sort.Strings(sortedEvidence)
		result.codes = append(result.codes, code)
		result.evidenceByCode[code] = sortedEvidence
	}
	sort.Strings(result.codes)
	return result
}

func runbookEvidenceMap(evidence []domain.Evidence) map[string]domain.Evidence {
	known := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		known[item.ID] = item
	}
	return known
}

func governedRunbookEvidenceComplete(item domain.Evidence) bool {
	return item.Complete && !item.Truncated && item.ID != "" && item.QueryID != "" &&
		validRunbookSHA256(item.QuerySpecHash) && item.ResourceID != "" &&
		item.TemplateID == domain.ErrorAnalysisTemplateID && item.TemplateVersion != "" &&
		item.SchemaFingerprint != "" && item.PolicyVersion != "" && validRunbookSHA256(item.GovernanceFingerprint) &&
		strings.EqualFold(item.Progress, "complete") && item.IncompleteReason == "" &&
		item.NanosecondOrderedKnown && item.NanosecondOrdered && item.UsageKnown &&
		item.APICalls == domain.ErrorAnalysisAPICalls && item.PatternLimit == domain.ErrorAnalysisPatternLimit &&
		item.InstanceLimit == domain.ErrorAnalysisInstanceLimit && !item.StartTime.IsZero() && item.StartTime.Before(item.EndTime)
}

func governedRunbookEvidencePair(evidence map[string]domain.Evidence) (domain.Evidence, domain.Evidence, bool) {
	current, baseline, err := causeObservationPair(evidence)
	if err != nil || !governedRunbookEvidenceComplete(current) || !governedRunbookEvidenceComplete(baseline) {
		return domain.Evidence{}, domain.Evidence{}, false
	}
	if current.ResourceID != baseline.ResourceID || current.TemplateID != baseline.TemplateID ||
		current.TemplateVersion != baseline.TemplateVersion || current.SchemaFingerprint != baseline.SchemaFingerprint ||
		current.PolicyVersion != baseline.PolicyVersion || current.GovernanceFingerprint != baseline.GovernanceFingerprint ||
		current.QuerySpecHash == baseline.QuerySpecHash || !baseline.EndTime.Equal(current.StartTime) ||
		current.EndTime.Sub(current.StartTime) != baseline.EndTime.Sub(baseline.StartTime) {
		return domain.Evidence{}, domain.Evidence{}, false
	}
	return current, baseline, true
}

func runbookEvidenceMatchesRequest(current, baseline domain.Evidence, request domain.InvestigationRequest) bool {
	if !request.Requester.Complete() || request.Service == "" || request.Environment == "" ||
		request.StartTime.IsZero() || !request.StartTime.Before(request.EndTime) {
		return false
	}
	window := request.EndTime.Sub(request.StartTime)
	return current.StartTime.Equal(request.StartTime) && current.EndTime.Equal(request.EndTime) &&
		baseline.StartTime.Equal(request.StartTime.Add(-window)) && baseline.EndTime.Equal(request.StartTime)
}

func validRunbookSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func runbookHasDeterministicSpike(current, baseline domain.Evidence) bool {
	if (current.ErrorCount > 0 && current.ErrorCount < 10) || (baseline.ErrorCount > 0 && baseline.ErrorCount < 5) {
		return false
	}
	if baseline.ErrorCount == 0 {
		return false
	}
	return float64(current.ErrorCount)/float64(baseline.ErrorCount) >= 2
}

func runbookHasNewPatternCandidate(current, baseline domain.Evidence) bool {
	if current.ErrorCount == 0 || len(current.ErrorPatterns) == 0 {
		return false
	}
	baselineLabels := make(map[string]struct{}, len(baseline.ErrorPatterns))
	for _, bucket := range baseline.ErrorPatterns {
		baselineLabels[bucket.Label] = struct{}{}
	}
	for _, bucket := range current.ErrorPatterns {
		if _, exists := baselineLabels[bucket.Label]; !exists {
			return true
		}
	}
	return false
}

func runbookHasHotInstance(current domain.Evidence) bool {
	return current.ErrorCount > 0 && len(current.Instances) > 0 &&
		float64(current.Instances[0].Count)/float64(current.ErrorCount)*100 >= 50
}

func runbookEntriesWithinReferenceTime(entries []domain.RunbookEntry, reference time.Time) bool {
	if reference.IsZero() {
		return false
	}
	latestAllowed := reference.Add(maxRunbookFutureSkew)
	for _, entry := range entries {
		if entry.UpdatedAt.After(latestAllowed) {
			return false
		}
	}
	return true
}

func buildRunbookGuidance(
	set domain.RunbookSet,
	evidenceByCode map[string][]string,
	dataSource domain.RunbookGuidanceDataSource,
) (*domain.RunbookGuidance, error) {
	guidance := &domain.RunbookGuidance{
		DataSource:      dataSource,
		MethodVersion:   domain.RunbookGuidanceVersion,
		SourceVersion:   set.SourceVersion,
		SourceComplete:  set.Complete,
		SourceTruncated: set.Truncated,
		Items:           make([]domain.RunbookGuidanceItem, 0, len(set.Entries)),
	}
	for _, entry := range set.Entries {
		fingerprint, err := domain.RunbookEntryFingerprint(entry)
		if err != nil {
			return nil, err
		}
		guidance.Items = append(guidance.Items, domain.RunbookGuidanceItem{
			EntryID:             entry.ID,
			Revision:            entry.Revision,
			Fingerprint:         fingerprint,
			Title:               entry.Title,
			Owner:               entry.OwnerTeam,
			UpdatedAt:           entry.UpdatedAt,
			RecommendationCodes: append([]string(nil), entry.MatchedRecommendationCodes...),
			EvidenceIDs:         runbookEvidenceUnion(entry.MatchedRecommendationCodes, evidenceByCode),
			Steps:               append([]domain.RunbookStep(nil), entry.Steps...),
			ExecutionMode:       domain.RunbookExecutionHumanReviewOnly,
		})
	}

	switch {
	case set.Complete && !set.Truncated && len(guidance.Items) > 0:
		guidance.Status = domain.RunbookGuidanceComplete
	case set.Complete && !set.Truncated:
		guidance.Status = domain.RunbookGuidanceNoMatch
	default:
		guidance.Status = domain.RunbookGuidanceInconclusive
		if !set.Complete {
			guidance.MissingInputs = append(guidance.MissingInputs, runbookMissingCompleteSet)
		}
		if set.Truncated {
			guidance.MissingInputs = append(guidance.MissingInputs, runbookMissingUntruncatedSet)
		}
		if len(guidance.Items) == 0 {
			guidance.MissingInputs = append(guidance.MissingInputs, runbookMissingMatch)
		}
		sort.Strings(guidance.MissingInputs)
	}
	return guidance, nil
}

func runbookEvidenceUnion(codes []string, evidenceByCode map[string][]string) []string {
	seen := make(map[string]struct{})
	for _, code := range codes {
		for _, evidenceID := range evidenceByCode[code] {
			seen[evidenceID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for evidenceID := range seen {
		result = append(result, evidenceID)
	}
	sort.Strings(result)
	return result
}

func reportHasConclusiveErrorSpike(report domain.Report) bool {
	if report.Outcome != "spike_detected" {
		return false
	}
	for _, finding := range report.Findings {
		if finding.Code == "error_spike" && finding.Conclusive {
			return true
		}
	}
	return false
}

func (service *RunbookService) unavailableRunbookGuidance(missingInput string) *domain.RunbookGuidance {
	return &domain.RunbookGuidance{
		Status:        domain.RunbookGuidanceUnavailable,
		DataSource:    service.dataSource,
		MethodVersion: domain.RunbookGuidanceVersion,
		MissingInputs: []string{missingInput},
	}
}
