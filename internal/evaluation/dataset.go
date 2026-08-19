package evaluation

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"logagent/internal/domain"
)

const (
	DatasetSchemaVersion = "evaluation-dataset-v1"
	SyntheticDatasetID   = "synthetic-m5a-v1"
	SyntheticDataSource  = "SYNTHETIC_MOCK"

	minEvaluationCases = 5
	maxEvaluationCases = 100

	ExpectedLogicalSLSCalls  = 2
	ExpectedProviderAPICalls = 2 * domain.ErrorAnalysisAPICalls

	// Each logical observation is currently limited to 16 MiB by the M1 mock
	// policy. The evaluation gate fixes the total current+baseline ceiling here;
	// a dataset may choose a lower per-case ceiling, but cannot raise this one.
	MaxProcessedBytesPerCase int64 = 32 * 1024 * 1024
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

//go:embed fixtures/synthetic-v1.json
var fixtureFiles embed.FS

// Dataset is a versioned evaluation artifact. Its provenance fields are
// intentionally redundant: a malformed or relabelled real-data artifact must
// not be able to pass as the built-in synthetic M5-A dataset.
type Dataset struct {
	SchemaVersion          string           `json:"schema_version"`
	DatasetID              string           `json:"dataset_id"`
	DataSource             string           `json:"data_source"`
	RealIncidentCount      int              `json:"real_incident_count"`
	ExpertLabelCount       int              `json:"expert_label_count"`
	CredentialsRequired    bool             `json:"credentials_required"`
	ExternalNetworkCalls   int              `json:"external_network_calls"`
	ProductionClaimAllowed bool             `json:"production_claim_allowed"`
	Cases                  []EvaluationCase `json:"cases"`

	Fingerprint string `json:"-"`
}

// EvaluationCase contains only synthetic fixtures and expected labels. The
// callback passed to Evaluate must treat it as immutable.
type EvaluationCase struct {
	ID          string                      `json:"id"`
	Description string                      `json:"description"`
	Request     domain.InvestigationRequest `json:"request"`
	Current     domain.QueryResult          `json:"current"`
	Baseline    domain.QueryResult          `json:"baseline"`
	ChangeSet   domain.ChangeSet            `json:"change_set"`
	Expected    ExpectedResult              `json:"expected"`
}

type ExpectedResult struct {
	Outcome                   string                     `json:"outcome"`
	ConclusiveFindingCodes    []string                   `json:"conclusive_finding_codes"`
	NonconclusiveFindingCodes []string                   `json:"nonconclusive_finding_codes"`
	Recommendations           []ExpectedRecommendation   `json:"recommendations"`
	CauseStatus               domain.CauseAnalysisStatus `json:"cause_status"`
	CauseVerdicts             []ExpectedCauseVerdict     `json:"cause_verdicts"`
	LogicalSLSCalls           int                        `json:"logical_sls_calls"`
	ProviderAPICalls          int                        `json:"provider_api_calls"`
	ChangeSourceCalls         int                        `json:"change_source_calls"`
	MaxProcessedBytes         int64                      `json:"max_processed_bytes"`
}

type ExpectedRecommendation struct {
	Code          string   `json:"code"`
	EvidenceNames []string `json:"evidence_names"`
}

type ExpectedCauseVerdict struct {
	ChangeID string              `json:"change_id"`
	Verdict  domain.CauseVerdict `json:"verdict"`
}

// LoadSyntheticV1 loads the repository-owned, credential-free fixture.
func LoadSyntheticV1() (Dataset, error) {
	payload, err := fixtureFiles.ReadFile("fixtures/synthetic-v1.json")
	if err != nil {
		return Dataset{}, fmt.Errorf("read embedded synthetic dataset: %w", err)
	}
	return ParseDataset(payload)
}

// ParseDataset performs strict JSON decoding and validates the complete M5-A
// synthetic-data contract. Unknown fields and trailing JSON are rejected.
func ParseDataset(payload []byte) (Dataset, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return Dataset{}, errors.New("evaluation dataset is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("decode evaluation dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Dataset{}, errors.New("decode evaluation dataset: trailing JSON value")
		}
		return Dataset{}, fmt.Errorf("decode evaluation dataset trailing content: %w", err)
	}
	if err := ValidateDataset(dataset); err != nil {
		return Dataset{}, err
	}
	fingerprint, err := normalizedFingerprint(dataset)
	if err != nil {
		return Dataset{}, err
	}
	dataset.Fingerprint = fingerprint
	return dataset, nil
}

// ValidateDataset prevents an input artifact from weakening the fixed gate or
// being presented as real-world validation.
func ValidateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != DatasetSchemaVersion {
		return fmt.Errorf("unsupported evaluation schema version %q", dataset.SchemaVersion)
	}
	if dataset.DatasetID != SyntheticDatasetID {
		return fmt.Errorf("unsupported M5-A dataset ID %q", dataset.DatasetID)
	}
	if dataset.DataSource != SyntheticDataSource {
		return fmt.Errorf("M5-A data source must be %q", SyntheticDataSource)
	}
	if dataset.RealIncidentCount != 0 || dataset.ExpertLabelCount != 0 {
		return errors.New("synthetic M5-A dataset cannot claim real incidents or expert labels")
	}
	if dataset.CredentialsRequired || dataset.ExternalNetworkCalls != 0 || dataset.ProductionClaimAllowed {
		return errors.New("synthetic M5-A dataset must require no credentials or network and cannot allow production claims")
	}
	if len(dataset.Cases) < minEvaluationCases || len(dataset.Cases) > maxEvaluationCases {
		return fmt.Errorf("evaluation dataset must contain between %d and %d cases", minEvaluationCases, maxEvaluationCases)
	}

	seen := make(map[string]struct{}, len(dataset.Cases))
	coverage := scenarioCoverage{}
	for index, evaluationCase := range dataset.Cases {
		if _, duplicate := seen[evaluationCase.ID]; duplicate {
			return fmt.Errorf("evaluation case %q is duplicated", evaluationCase.ID)
		}
		seen[evaluationCase.ID] = struct{}{}
		if err := validateCase(evaluationCase); err != nil {
			return fmt.Errorf("evaluation case %d (%q): %w", index, evaluationCase.ID, err)
		}
		coverage.observe(evaluationCase)
	}
	if missing := coverage.missing(); len(missing) > 0 {
		return fmt.Errorf("evaluation dataset is missing required safety scenarios: %s", strings.Join(missing, ", "))
	}
	return nil
}

type scenarioCoverage struct {
	supported    bool
	noSpike      bool
	incomplete   bool
	refuted      bool
	inconclusive bool
}

func (coverage *scenarioCoverage) observe(evaluationCase EvaluationCase) {
	switch evaluationCase.Expected.Outcome {
	case "no_significant_spike":
		coverage.noSpike = true
	case "data_insufficient":
		if !evaluationCase.Current.Complete || evaluationCase.Current.Truncated || !evaluationCase.Baseline.Complete || evaluationCase.Baseline.Truncated {
			coverage.incomplete = true
		}
	}
	for _, verdict := range evaluationCase.Expected.CauseVerdicts {
		switch verdict.Verdict {
		case domain.CauseVerdictSupportedCandidate:
			coverage.supported = true
		case domain.CauseVerdictRefuted:
			coverage.refuted = true
		case domain.CauseVerdictInconclusive:
			coverage.inconclusive = true
		}
	}
}

func (coverage scenarioCoverage) missing() []string {
	missing := make([]string, 0, 5)
	if !coverage.supported {
		missing = append(missing, "supported change")
	}
	if !coverage.noSpike {
		missing = append(missing, "no spike")
	}
	if !coverage.incomplete {
		missing = append(missing, "incomplete observation")
	}
	if !coverage.refuted {
		missing = append(missing, "refuted change")
	}
	if !coverage.inconclusive {
		missing = append(missing, "inconclusive change")
	}
	return missing
}

func validateCase(evaluationCase EvaluationCase) error {
	if !identifierPattern.MatchString(evaluationCase.ID) {
		return errors.New("case ID is missing or invalid")
	}
	if err := validateText("case description", evaluationCase.Description, 512); err != nil {
		return err
	}
	request := evaluationCase.Request
	if err := validateText("service", request.Service, 128); err != nil {
		return err
	}
	if err := validateText("environment", request.Environment, 128); err != nil {
		return err
	}
	if !request.EndTime.After(request.StartTime) {
		return errors.New("request end time must be after start time")
	}
	if !request.Requester.Complete() {
		return errors.New("requester must contain trusted app, tenant, and user identities")
	}
	for name, value := range map[string]string{
		"requester app ID":     request.Requester.AppID,
		"requester tenant key": request.Requester.TenantKey,
		"requester user ID":    request.Requester.UserID,
	} {
		if err := validateText(name, value, 128); err != nil {
			return err
		}
	}
	if err := validateFixtureResult("current", evaluationCase.Current); err != nil {
		return err
	}
	if err := validateFixtureResult("baseline", evaluationCase.Baseline); err != nil {
		return err
	}
	if evaluationCase.Current.QueryID == evaluationCase.Baseline.QueryID {
		return errors.New("current and baseline fixture query IDs must differ")
	}
	if err := validateCrossWindowFixture(evaluationCase.Current, evaluationCase.Baseline); err != nil {
		return err
	}
	if err := validateChangeFixture(evaluationCase); err != nil {
		return err
	}
	if err := validateExpected(evaluationCase); err != nil {
		return err
	}
	return nil
}

func validateFixtureResult(name string, result domain.QueryResult) error {
	prefix := name + " fixture"
	if result.QueryID == "" || !identifierPattern.MatchString(result.QueryID) {
		return fmt.Errorf("%s query ID is missing or invalid", prefix)
	}
	if result.QuerySpecHash != "" {
		return fmt.Errorf("%s query spec hash must be derived by the evaluated graph", prefix)
	}
	if err := domain.ValidateResourceID(result.ResourceID); err != nil {
		return fmt.Errorf("%s resource ID: %w", prefix, err)
	}
	if result.TemplateID != domain.ErrorAnalysisTemplateID || result.TemplateVersion != domain.ErrorAnalysisTemplateVersion {
		return fmt.Errorf("%s must use the fixed error-analysis template and version", prefix)
	}
	if err := validateText(prefix+" schema fingerprint", result.SchemaFingerprint, 128); err != nil {
		return err
	}
	if err := validateText(prefix+" policy version", result.PolicyVersion, 128); err != nil {
		return err
	}
	if !isSHA256(result.GovernanceFingerprint) {
		return fmt.Errorf("%s governance fingerprint must be a SHA-256 value", prefix)
	}
	if result.Complete {
		if result.Progress != "Complete" || result.Truncated || result.IncompleteReason != "" {
			return fmt.Errorf("%s complete-state fields are inconsistent", prefix)
		}
	} else if result.Progress != "Incomplete" || result.IncompleteReason == "" {
		return fmt.Errorf("%s incomplete-state fields are inconsistent", prefix)
	}
	if result.ProcessedRows < 0 || result.ProcessedBytes < 0 || result.ElapsedMillisecond < 0 {
		return fmt.Errorf("%s usage values cannot be negative", prefix)
	}
	if !result.UsageKnown {
		return fmt.Errorf("%s must carry synthetic usage metadata", prefix)
	}
	if result.APICalls != domain.ErrorAnalysisAPICalls {
		return fmt.Errorf("%s API calls=%d, want %d", prefix, result.APICalls, domain.ErrorAnalysisAPICalls)
	}
	if result.PatternLimit != domain.ErrorAnalysisPatternLimit || result.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
		return fmt.Errorf("%s result limits do not match the fixed template", prefix)
	}
	if result.ErrorCount < 0 || result.TopErrorCount < 0 || result.TopErrorCount > result.ErrorCount {
		return fmt.Errorf("%s error counts are inconsistent", prefix)
	}
	patternTotal, err := validateBuckets(prefix+" error patterns", result.ErrorPatterns, result.PatternLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	instanceTotal, err := validateBuckets(prefix+" instances", result.Instances, result.InstanceLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	if result.ErrorCount > 0 && (len(result.ErrorPatterns) == 0 || len(result.Instances) == 0) {
		return fmt.Errorf("%s non-zero errors require pattern and instance buckets", prefix)
	}
	if (result.ErrorPatternsExhaustive && patternTotal != result.ErrorCount) || (result.InstancesExhaustive && instanceTotal != result.ErrorCount) {
		return fmt.Errorf("%s exhaustive aggregates do not cover the error count", prefix)
	}
	if result.Complete && !result.Truncated && (result.ErrorPatternsExhaustive != (patternTotal == result.ErrorCount) || result.InstancesExhaustive != (instanceTotal == result.ErrorCount)) {
		return fmt.Errorf("%s complete-result exhaustiveness markers are inconsistent", prefix)
	}
	if len(result.ErrorPatterns) == 0 {
		if result.TopError != "" || result.TopErrorCount != 0 {
			return fmt.Errorf("%s top error is present without a pattern", prefix)
		}
	} else if result.TopError != result.ErrorPatterns[0].Label || result.TopErrorCount != result.ErrorPatterns[0].Count {
		return fmt.Errorf("%s top error does not match the first pattern", prefix)
	}
	return nil
}

func validateBuckets(name string, buckets []domain.CountBucket, limit int, total int64) (int64, error) {
	if len(buckets) > limit {
		return 0, fmt.Errorf("%s contains more than %d buckets", name, limit)
	}
	seen := make(map[string]struct{}, len(buckets))
	var sum int64
	for index, bucket := range buckets {
		if err := validateText(fmt.Sprintf("%s bucket %d label", name, index), bucket.Label, 256); err != nil {
			return 0, err
		}
		if bucket.Count <= 0 {
			return 0, fmt.Errorf("%s bucket %d count must be positive", name, index)
		}
		if _, duplicate := seen[bucket.Label]; duplicate {
			return 0, fmt.Errorf("%s contains duplicate label %q", name, bucket.Label)
		}
		seen[bucket.Label] = struct{}{}
		if sum > total-bucket.Count {
			return 0, fmt.Errorf("%s counts exceed total errors", name)
		}
		sum += bucket.Count
	}
	return sum, nil
}

func validateCrossWindowFixture(current, baseline domain.QueryResult) error {
	if current.ResourceID != baseline.ResourceID || current.TemplateID != baseline.TemplateID || current.TemplateVersion != baseline.TemplateVersion || current.SchemaFingerprint != baseline.SchemaFingerprint || current.PolicyVersion != baseline.PolicyVersion || current.GovernanceFingerprint != baseline.GovernanceFingerprint {
		return errors.New("current and baseline fixtures must share one governance identity")
	}
	return nil
}

func validateChangeFixture(evaluationCase EvaluationCase) error {
	changeSet := evaluationCase.ChangeSet
	if changeSet.ReasonCode == "change_source_disabled" {
		if changeSet.Complete || changeSet.Truncated || changeSet.SourceVersion != "" || len(changeSet.Events) != 0 {
			return errors.New("disabled change fixture must not contain source data")
		}
		return nil
	}
	if err := domain.ValidateChangeSourceVersion(changeSet.SourceVersion); err != nil {
		return fmt.Errorf("change fixture source version: %w", err)
	}
	if changeSet.Complete && changeSet.Truncated {
		return errors.New("complete change fixture cannot be truncated")
	}
	if len(changeSet.Events) > domain.MaxChangeEvents {
		return fmt.Errorf("change fixture exceeds %d events", domain.MaxChangeEvents)
	}
	seen := make(map[string]struct{}, len(changeSet.Events))
	duration := evaluationCase.Request.EndTime.Sub(evaluationCase.Request.StartTime)
	windowStart := evaluationCase.Request.StartTime.Add(-duration)
	windowEnd := evaluationCase.Request.EndTime
	for _, event := range changeSet.Events {
		if err := domain.ValidateChangeEvent(event); err != nil {
			return fmt.Errorf("change fixture event %q: %w", event.ID, err)
		}
		if event.ResourceID != evaluationCase.Current.ResourceID {
			return fmt.Errorf("change fixture event %q uses a different governed resource", event.ID)
		}
		if !event.StartedAt.Before(windowEnd) || !event.CompletedAt.After(windowStart) {
			return fmt.Errorf("change fixture event %q is outside the evaluated window", event.ID)
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return fmt.Errorf("change fixture event %q is duplicated", event.ID)
		}
		seen[event.ID] = struct{}{}
	}
	return nil
}

var allowedOutcomes = map[string]struct{}{
	"spike_detected":       {},
	"no_significant_spike": {},
	"data_insufficient":    {},
}

var allowedFindingConclusive = map[string]bool{
	"error_spike":           true,
	"no_significant_spike":  true,
	"error_pattern_share":   true,
	"new_error_pattern":     true,
	"instance_distribution": true,
}

var allowedFindingNonconclusive = map[string]bool{
	"data_insufficient":           true,
	"insufficient_baseline":       true,
	"sample_too_small":            true,
	"new_error_pattern_candidate": true,
}

func validateExpected(evaluationCase EvaluationCase) error {
	expected := evaluationCase.Expected
	if _, allowed := allowedOutcomes[expected.Outcome]; !allowed {
		return fmt.Errorf("expected outcome %q is unsupported", expected.Outcome)
	}
	if len(expected.ConclusiveFindingCodes)+len(expected.NonconclusiveFindingCodes) == 0 {
		return errors.New("expected finding labels cannot be empty")
	}
	if err := validateCodeList("conclusive finding", expected.ConclusiveFindingCodes, allowedFindingConclusive); err != nil {
		return err
	}
	if err := validateCodeList("nonconclusive finding", expected.NonconclusiveFindingCodes, allowedFindingNonconclusive); err != nil {
		return err
	}
	if err := validateExpectedRecommendations(expected.Recommendations); err != nil {
		return err
	}
	if expected.LogicalSLSCalls != ExpectedLogicalSLSCalls || expected.ProviderAPICalls != ExpectedProviderAPICalls {
		return fmt.Errorf("expected calls must remain fixed at %d logical and %d provider calls", ExpectedLogicalSLSCalls, ExpectedProviderAPICalls)
	}
	wantChangeCalls := 0
	if expected.Outcome == "spike_detected" {
		wantChangeCalls = 1
	}
	if expected.ChangeSourceCalls != wantChangeCalls {
		return fmt.Errorf("expected change-source calls=%d, want fixed value %d for outcome %q", expected.ChangeSourceCalls, wantChangeCalls, expected.Outcome)
	}
	if expected.MaxProcessedBytes <= 0 || expected.MaxProcessedBytes > MaxProcessedBytesPerCase {
		return fmt.Errorf("max processed bytes must be between 1 and %d", MaxProcessedBytesPerCase)
	}
	switch expected.CauseStatus {
	case domain.CauseAnalysisComplete, domain.CauseAnalysisInconclusive, domain.CauseAnalysisUnavailable, domain.CauseAnalysisSkippedNoSpike:
	default:
		return fmt.Errorf("expected cause status %q is unsupported", expected.CauseStatus)
	}
	changeIDs := make(map[string]struct{}, len(evaluationCase.ChangeSet.Events))
	for _, event := range evaluationCase.ChangeSet.Events {
		changeIDs[event.ID] = struct{}{}
	}
	seenVerdicts := make(map[string]struct{}, len(expected.CauseVerdicts))
	for _, verdict := range expected.CauseVerdicts {
		if _, exists := changeIDs[verdict.ChangeID]; !exists {
			return fmt.Errorf("expected cause verdict references unknown change %q", verdict.ChangeID)
		}
		if _, duplicate := seenVerdicts[verdict.ChangeID]; duplicate {
			return fmt.Errorf("expected cause verdict duplicates change %q", verdict.ChangeID)
		}
		seenVerdicts[verdict.ChangeID] = struct{}{}
		switch verdict.Verdict {
		case domain.CauseVerdictSupportedCandidate, domain.CauseVerdictRefuted, domain.CauseVerdictInconclusive:
		default:
			return fmt.Errorf("expected cause verdict %q is unsupported", verdict.Verdict)
		}
	}
	if expected.Outcome == "spike_detected" && !contains(expected.ConclusiveFindingCodes, "error_spike") {
		return errors.New("spike outcome requires a conclusive error_spike label")
	}
	if expected.Outcome == "no_significant_spike" && !contains(expected.ConclusiveFindingCodes, "no_significant_spike") {
		return errors.New("no-spike outcome requires a conclusive no_significant_spike label")
	}
	if expected.Outcome == "data_insufficient" && len(expected.NonconclusiveFindingCodes) == 0 {
		return errors.New("data-insufficient outcome requires a nonconclusive label")
	}
	return nil
}

func validateExpectedRecommendations(recommendations []ExpectedRecommendation) error {
	if len(recommendations) == 0 {
		return errors.New("expected recommendations cannot be empty")
	}
	seenCodes := make(map[string]struct{}, len(recommendations))
	for index, recommendation := range recommendations {
		if !identifierPattern.MatchString(recommendation.Code) {
			return fmt.Errorf("expected recommendation %d code is missing or invalid", index)
		}
		if _, duplicate := seenCodes[recommendation.Code]; duplicate {
			return fmt.Errorf("expected recommendation code %q is duplicated", recommendation.Code)
		}
		seenCodes[recommendation.Code] = struct{}{}
		if len(recommendation.EvidenceNames) == 0 {
			return fmt.Errorf("expected recommendation %q evidence names cannot be empty", recommendation.Code)
		}
		seenEvidenceNames := make(map[string]struct{}, len(recommendation.EvidenceNames))
		for _, evidenceName := range recommendation.EvidenceNames {
			if evidenceName != "current" && evidenceName != "baseline" {
				return fmt.Errorf("expected recommendation %q references unsupported evidence name %q", recommendation.Code, evidenceName)
			}
			if _, duplicate := seenEvidenceNames[evidenceName]; duplicate {
				return fmt.Errorf("expected recommendation %q duplicates evidence name %q", recommendation.Code, evidenceName)
			}
			seenEvidenceNames[evidenceName] = struct{}{}
		}
	}
	return nil
}

func validateCodeList(name string, codes []string, allowed map[string]bool) error {
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if !allowed[code] {
			return fmt.Errorf("%s code %q is unsupported", name, code)
		}
		if _, duplicate := seen[code]; duplicate {
			return fmt.Errorf("%s code %q is duplicated", name, code)
		}
		seen[code] = struct{}{}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateText(name, value string, maxBytes int) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and cannot have surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains control characters", name)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
