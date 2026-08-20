package summaryeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"logagent/internal/application"
	"logagent/internal/domain"
)

const (
	ReportSchemaVersion = "summary-evaluation-report-v1"
	GatePolicyVersion   = "summary-evaluation-gate-v1"
)

var ErrGateFailed = errors.New("summary evaluation gate failed")

// Observation is deliberately excluded from JSON. It carries the in-process
// values needed to prove that the summary path did not alter deterministic
// evidence or send governed identifiers to a provider.
type Observation struct {
	Requester            domain.Principal     `json:"-"`
	Evidence             []domain.Evidence    `json:"-"`
	Before               domain.Report        `json:"-"`
	After                domain.Report        `json:"-"`
	CapturedInput        *domain.SummaryInput `json:"-"`
	ProviderCalls        int                  `json:"-"`
	InputTokens          int64                `json:"-"`
	OutputTokens         int64                `json:"-"`
	TotalTokens          int64                `json:"-"`
	ExternalNetworkCalls int                  `json:"-"`
	CredentialsRequired  bool                 `json:"-"`
}

type ExecuteCase func(context.Context, Case) (Observation, error)

type DataBoundary struct {
	DataSource             string `json:"data_source"`
	RealIncidentCount      int    `json:"real_incident_count"`
	ExpertLabelCount       int    `json:"expert_label_count"`
	CredentialsRequired    bool   `json:"credentials_required"`
	ExternalNetworkCalls   int    `json:"external_network_calls"`
	ProductionClaimAllowed bool   `json:"production_claim_allowed"`
}

type GatePolicy struct {
	Version                           string  `json:"version"`
	MinCasePassRate                   float64 `json:"min_case_pass_rate"`
	MinProductionOutputAccuracy       float64 `json:"min_production_output_accuracy"`
	MinDeterministicIntegrityAccuracy float64 `json:"min_deterministic_integrity_accuracy"`
	MinSummaryContractAccuracy        float64 `json:"min_summary_contract_accuracy"`
	MinInputPrivacyAccuracy           float64 `json:"min_input_privacy_accuracy"`
	MinFallbackAccuracy               float64 `json:"min_fallback_accuracy"`
	MaxProviderCallBudgetBreaches     int     `json:"max_provider_call_budget_breaches"`
	MaxTokens                         int64   `json:"max_tokens"`
	MaxExternalNetworkCalls           int     `json:"max_external_network_calls"`
	MaxCredentialsRequiredCases       int     `json:"max_credentials_required_cases"`
}

var fixedGatePolicy = GatePolicy{
	Version:                           GatePolicyVersion,
	MinCasePassRate:                   1,
	MinProductionOutputAccuracy:       1,
	MinDeterministicIntegrityAccuracy: 1,
	MinSummaryContractAccuracy:        1,
	MinInputPrivacyAccuracy:           1,
	MinFallbackAccuracy:               1,
	MaxProviderCallBudgetBreaches:     0,
	MaxTokens:                         0,
	MaxExternalNetworkCalls:           0,
	MaxCredentialsRequiredCases:       0,
}

type EvaluationStatus string

const (
	EvaluationPassed EvaluationStatus = "PASSED"
	EvaluationFailed EvaluationStatus = "FAILED"
)

type Report struct {
	SchemaVersion         string           `json:"schema_version"`
	EvaluationVersion     string           `json:"evaluation_version"`
	EvaluationRunID       string           `json:"evaluation_run_id"`
	DatasetID             string           `json:"dataset_id"`
	DatasetSchemaVersion  string           `json:"dataset_schema_version"`
	DatasetFingerprint    string           `json:"dataset_fingerprint"`
	PromptVersion         string           `json:"prompt_version"`
	MockPromptFingerprint string           `json:"mock_prompt_fingerprint"`
	DataBoundary          DataBoundary     `json:"data_boundary"`
	Policy                GatePolicy       `json:"gate_policy"`
	Status                EvaluationStatus `json:"status"`
	Metrics               Metrics          `json:"metrics"`
	Gates                 []GateResult     `json:"gates"`
	Cases                 []CaseResult     `json:"cases"`
}

type Metrics struct {
	TotalCases                     int     `json:"total_cases"`
	ExecutedCases                  int     `json:"executed_cases"`
	PassedCases                    int     `json:"passed_cases"`
	ExecutionFailures              int     `json:"execution_failures"`
	CasePassRate                   float64 `json:"case_pass_rate"`
	ProductionOutputAccuracy       float64 `json:"production_output_accuracy"`
	DeterministicIntegrityAccuracy float64 `json:"deterministic_integrity_accuracy"`
	SummaryContractAccuracy        float64 `json:"summary_contract_accuracy"`
	InputPrivacyAccuracy           float64 `json:"input_privacy_accuracy"`
	FallbackAccuracy               float64 `json:"fallback_accuracy"`
	ExpectedProviderCalls          int     `json:"expected_provider_calls"`
	ActualProviderCalls            int     `json:"actual_provider_calls"`
	ProviderCallBudgetBreaches     int     `json:"provider_call_budget_breaches"`
	InputTokens                    int64   `json:"input_tokens"`
	OutputTokens                   int64   `json:"output_tokens"`
	TotalTokens                    int64   `json:"total_tokens"`
	ExternalNetworkCalls           int     `json:"external_network_calls"`
	CredentialsRequiredCases       int     `json:"credentials_required_cases"`
	LatencyP50Milliseconds         int64   `json:"latency_p50_milliseconds"`
	LatencyP95Milliseconds         int64   `json:"latency_p95_milliseconds"`
	LatencyMaxMilliseconds         int64   `json:"latency_max_milliseconds"`
}

type CaseResult struct {
	ID                       string               `json:"id"`
	BaseCaseID               string               `json:"base_case_id"`
	Passed                   bool                 `json:"passed"`
	FailureCodes             []string             `json:"failure_codes,omitempty"`
	ExpectedStatus           domain.SummaryStatus `json:"expected_status"`
	ActualStatus             domain.SummaryStatus `json:"actual_status,omitempty"`
	ExpectedMode             domain.SummaryMode   `json:"expected_mode"`
	ActualMode               domain.SummaryMode   `json:"actual_mode,omitempty"`
	ProviderBehavior         ProviderBehavior     `json:"provider_behavior"`
	ReportMutation           ReportMutation       `json:"report_mutation"`
	ExpectedCauseSelected    bool                 `json:"expected_cause_selected"`
	ActualCauseSelected      bool                 `json:"actual_cause_selected"`
	ProductionOutputValid    bool                 `json:"production_output_valid"`
	DeterministicIntegrity   bool                 `json:"deterministic_integrity"`
	SummaryContractPassed    bool                 `json:"summary_contract_passed"`
	InputPrivacyPassed       bool                 `json:"input_privacy_passed"`
	FallbackPassed           bool                 `json:"fallback_passed"`
	ExpectedProviderCalls    int                  `json:"expected_provider_calls"`
	ActualProviderCalls      int                  `json:"actual_provider_calls"`
	ProviderCallBudgetPassed bool                 `json:"provider_call_budget_passed"`
	InputTokens              int64                `json:"input_tokens"`
	OutputTokens             int64                `json:"output_tokens"`
	TotalTokens              int64                `json:"total_tokens"`
	ExternalNetworkCalls     int                  `json:"external_network_calls"`
	CredentialsRequired      bool                 `json:"credentials_required"`
	DurationMilliseconds     int64                `json:"duration_milliseconds"`
}

type GateResult struct {
	Code     string `json:"code"`
	Passed   bool   `json:"passed"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
}

type GateError struct{ Failed []string }

func (err *GateError) Error() string {
	return fmt.Sprintf("%s: %s", ErrGateFailed, strings.Join(err.Failed, ", "))
}

func (err *GateError) Is(target error) bool { return target == ErrGateFailed }

func Evaluate(ctx context.Context, dataset Dataset, execute ExecuteCase) (Report, error) {
	if execute == nil {
		return Report{}, errors.New("summary evaluation execute callback is required")
	}
	if err := ValidateDataset(dataset); err != nil {
		return Report{}, err
	}
	fingerprint, err := normalizedFingerprint(dataset)
	if err != nil {
		return Report{}, err
	}
	dataset.Fingerprint = fingerprint
	report := Report{
		SchemaVersion:         ReportSchemaVersion,
		EvaluationVersion:     GatePolicyVersion,
		EvaluationRunID:       "summary_eval_" + dataset.Fingerprint[:16],
		DatasetID:             dataset.DatasetID,
		DatasetSchemaVersion:  dataset.SchemaVersion,
		DatasetFingerprint:    dataset.Fingerprint,
		PromptVersion:         dataset.PromptVersion,
		MockPromptFingerprint: dataset.MockPromptFingerprint,
		DataBoundary: DataBoundary{
			DataSource: dataset.DataSource, RealIncidentCount: dataset.RealIncidentCount,
			ExpertLabelCount: dataset.ExpertLabelCount, CredentialsRequired: dataset.CredentialsRequired,
			ExternalNetworkCalls: dataset.ExternalNetworkCalls, ProductionClaimAllowed: dataset.ProductionClaimAllowed,
		},
		Policy: fixedGatePolicy,
	}
	durations := make([]int64, 0, len(dataset.Cases))
	productionPasses, integrityPasses, contractPasses, privacyPasses := 0, 0, 0, 0
	fallbackCases, fallbackPasses := 0, 0
	for _, evaluationCase := range dataset.Cases {
		startedAt := time.Now()
		observation, executeErr := execute(ctx, evaluationCase)
		result := evaluateCase(evaluationCase, dataset.MockPromptFingerprint, observation, executeErr)
		result.DurationMilliseconds = time.Since(startedAt).Milliseconds()
		durations = append(durations, result.DurationMilliseconds)
		report.Cases = append(report.Cases, result)
		report.Metrics.ExecutedCases++
		if executeErr != nil {
			report.Metrics.ExecutionFailures++
		}
		if result.Passed {
			report.Metrics.PassedCases++
		}
		if result.ProductionOutputValid {
			productionPasses++
		}
		if result.DeterministicIntegrity {
			integrityPasses++
		}
		if result.SummaryContractPassed {
			contractPasses++
		}
		if result.InputPrivacyPassed {
			privacyPasses++
		}
		if evaluationCase.Expected.Status == domain.SummaryFallback {
			fallbackCases++
			if result.FallbackPassed {
				fallbackPasses++
			}
		}
		report.Metrics.ExpectedProviderCalls += result.ExpectedProviderCalls
		report.Metrics.ActualProviderCalls += result.ActualProviderCalls
		if !result.ProviderCallBudgetPassed {
			report.Metrics.ProviderCallBudgetBreaches++
		}
		report.Metrics.InputTokens += result.InputTokens
		report.Metrics.OutputTokens += result.OutputTokens
		report.Metrics.TotalTokens += result.TotalTokens
		report.Metrics.ExternalNetworkCalls += result.ExternalNetworkCalls
		if result.CredentialsRequired {
			report.Metrics.CredentialsRequiredCases++
		}
	}
	report.Metrics.TotalCases = len(dataset.Cases)
	denominator := float64(report.Metrics.TotalCases)
	report.Metrics.CasePassRate = ratio(report.Metrics.PassedCases, denominator)
	report.Metrics.ProductionOutputAccuracy = ratio(productionPasses, denominator)
	report.Metrics.DeterministicIntegrityAccuracy = ratio(integrityPasses, denominator)
	report.Metrics.SummaryContractAccuracy = ratio(contractPasses, denominator)
	report.Metrics.InputPrivacyAccuracy = ratio(privacyPasses, denominator)
	report.Metrics.FallbackAccuracy = ratio(fallbackPasses, float64(fallbackCases))
	report.Metrics.LatencyP50Milliseconds = percentile(durations, .50)
	report.Metrics.LatencyP95Milliseconds = percentile(durations, .95)
	report.Metrics.LatencyMaxMilliseconds = percentile(durations, 1)
	report.Gates = buildGates(report.Metrics)
	report.Status = EvaluationPassed
	failed := make([]string, 0)
	for _, gate := range report.Gates {
		if !gate.Passed {
			report.Status = EvaluationFailed
			failed = append(failed, gate.Code)
		}
	}
	if len(failed) > 0 {
		return report, &GateError{Failed: failed}
	}
	return report, nil
}

func evaluateCase(evaluationCase Case, mockPromptFingerprint string, observation Observation, executeErr error) CaseResult {
	result := CaseResult{
		ID: evaluationCase.ID, BaseCaseID: evaluationCase.BaseCaseID,
		ExpectedStatus: evaluationCase.Expected.Status, ExpectedMode: evaluationCase.Expected.Mode,
		ProviderBehavior: evaluationCase.ProviderBehavior, ReportMutation: evaluationCase.ReportMutation,
		ExpectedCauseSelected: evaluationCase.Expected.CauseSelected,
		ExpectedProviderCalls: evaluationCase.Expected.ProviderCalls,
		ActualProviderCalls:   observation.ProviderCalls,
		InputTokens:           observation.InputTokens, OutputTokens: observation.OutputTokens, TotalTokens: observation.TotalTokens,
		ExternalNetworkCalls: observation.ExternalNetworkCalls, CredentialsRequired: observation.CredentialsRequired,
	}
	if executeErr != nil {
		result.FailureCodes = append(result.FailureCodes, "execution_error")
		return result
	}
	if observation.After.Summary != nil {
		result.ActualStatus = observation.After.Summary.Status
		result.ActualMode = observation.After.Summary.Mode
		result.ActualCauseSelected = observation.After.Summary.CauseHypothesisID != ""
	}
	beforeValid := application.ValidateEngineOutput(observation.Before.InvestigationID, observation.Evidence, observation.Before) == nil
	afterValid := application.ValidateEngineOutput(observation.After.InvestigationID, observation.Evidence, observation.After) == nil
	result.ProductionOutputValid = beforeValid && afterValid && reflect.DeepEqual(observation.Evidence, observation.Before.Evidence) && reflect.DeepEqual(observation.Evidence, observation.After.Evidence)
	if !result.ProductionOutputValid {
		result.FailureCodes = append(result.FailureCodes, "production_output_invalid")
	}
	result.DeterministicIntegrity = deterministicReportEqual(observation.Before, observation.After)
	if !result.DeterministicIntegrity {
		result.FailureCodes = append(result.FailureCodes, "deterministic_report_changed")
	}
	result.SummaryContractPassed = summaryContractPassed(evaluationCase, mockPromptFingerprint, observation)
	if !result.SummaryContractPassed {
		result.FailureCodes = append(result.FailureCodes, "summary_contract_failed")
	}
	result.InputPrivacyPassed = inputPrivacyPassed(evaluationCase, observation)
	if !result.InputPrivacyPassed {
		result.FailureCodes = append(result.FailureCodes, "summary_input_privacy_failed")
	}
	result.FallbackPassed = evaluationCase.Expected.Status != domain.SummaryFallback ||
		(observation.After.Summary != nil && observation.After.Summary.Status == domain.SummaryFallback && observation.After.Summary.Mode == domain.SummaryModeFallback)
	if !result.FallbackPassed {
		result.FailureCodes = append(result.FailureCodes, "fallback_contract_failed")
	}
	result.ProviderCallBudgetPassed = observation.ProviderCalls == evaluationCase.Expected.ProviderCalls
	if !result.ProviderCallBudgetPassed {
		result.FailureCodes = append(result.FailureCodes, "provider_call_budget_failed")
	}
	if observation.InputTokens != 0 || observation.OutputTokens != 0 || observation.TotalTokens != 0 ||
		observation.ExternalNetworkCalls != 0 || observation.CredentialsRequired {
		result.FailureCodes = append(result.FailureCodes, "offline_boundary_failed")
	}
	result.Passed = len(result.FailureCodes) == 0
	return result
}

func deterministicReportEqual(before, after domain.Report) bool {
	if before.Summary != nil || after.Summary == nil {
		return false
	}
	after.Summary = nil
	return reflect.DeepEqual(before, after)
}

func summaryContractPassed(evaluationCase Case, mockPromptFingerprint string, observation Observation) bool {
	summary := observation.After.Summary
	if summary == nil || summary.Status != evaluationCase.Expected.Status || summary.Mode != evaluationCase.Expected.Mode ||
		summary.PromptVersion != domain.EvidenceSummaryPromptVersion || summary.InputTokens != observation.InputTokens ||
		summary.OutputTokens != observation.OutputTokens || summary.TotalTokens != observation.TotalTokens {
		return false
	}
	if summary.Status == domain.SummaryGenerated && (summary.Provider != "summary_mock" || summary.Model != "deterministic_mock_v1" || summary.RequestID != "") {
		return false
	}
	if summary.Status == domain.SummaryGenerated && summary.PromptFingerprint != mockPromptFingerprint {
		return false
	}
	if summary.Status == domain.SummaryFallback && (summary.Provider != "deterministic_fallback" || summary.Model != "not_applicable" || summary.RequestID != "") {
		return false
	}
	if summary.Status == domain.SummaryFallback && summary.PromptFingerprint != strings.Repeat("0", 64) {
		return false
	}
	if evaluationCase.Expected.CauseSelected != (summary.CauseHypothesisID != "") {
		return false
	}
	if summary.CauseHypothesisID != "" && !supportedCauseMatches(observation.Before, *summary) {
		return false
	}
	if !nextStepsEqual(observation.Before.Recommendations, summary.NextSteps) {
		return false
	}
	serialized, err := json.Marshal(summary)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(serialized))
	return !strings.Contains(lower, "synthetic provider failure") && !strings.Contains(lower, "provider_error")
}

func supportedCauseMatches(report domain.Report, summary domain.ReportSummary) bool {
	if report.CauseAnalysis == nil {
		return false
	}
	for _, hypothesis := range report.CauseAnalysis.Hypotheses {
		if hypothesis.ID == summary.CauseHypothesisID {
			return hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && hypothesis.Statement == summary.PossibleCause
		}
	}
	return false
}

func nextStepsEqual(recommendations []domain.Recommendation, steps []domain.SummaryNextStep) bool {
	if len(recommendations) != len(steps) {
		return false
	}
	for index := range recommendations {
		if recommendations[index].Code != steps[index].Code || recommendations[index].Statement != steps[index].Statement ||
			!reflect.DeepEqual(recommendations[index].EvidenceIDs, steps[index].EvidenceIDs) {
			return false
		}
	}
	return true
}

func inputPrivacyPassed(evaluationCase Case, observation Observation) bool {
	if evaluationCase.Expected.ProviderCalls == 0 {
		return observation.CapturedInput == nil
	}
	if observation.CapturedInput == nil || !reflect.DeepEqual(*observation.CapturedInput, application.BuildSummaryInput(observation.Before)) {
		return false
	}
	payload, err := json.Marshal(observation.CapturedInput)
	if err != nil {
		return false
	}
	serialized := string(payload)
	for _, forbidden := range forbiddenInputValues(observation) {
		if forbidden != "" && strings.Contains(serialized, forbidden) {
			return false
		}
	}
	lower := strings.ToLower(serialized)
	return !strings.Contains(lower, "query_id") && !strings.Contains(lower, "query_spec") &&
		!strings.Contains(lower, "resource_id") && !strings.Contains(lower, "schema_fingerprint") &&
		!strings.Contains(lower, "policy_version") && !strings.Contains(lower, "governance_fingerprint") &&
		!strings.Contains(lower, "raw_log") && !strings.Contains(lower, "sql")
}

func forbiddenInputValues(observation Observation) []string {
	values := []string{observation.Requester.AppID, observation.Requester.TenantKey, observation.Requester.UserID}
	for _, item := range observation.Evidence {
		values = append(values, item.QueryID, item.QuerySpecHash, item.ResourceID, item.TemplateID,
			item.TemplateVersion, item.SchemaFingerprint, item.PolicyVersion, item.GovernanceFingerprint)
	}
	return values
}

func buildGates(metrics Metrics) []GateResult {
	return []GateResult{
		floatGate("case_pass_rate", metrics.CasePassRate, fixedGatePolicy.MinCasePassRate),
		floatGate("production_output_accuracy", metrics.ProductionOutputAccuracy, fixedGatePolicy.MinProductionOutputAccuracy),
		floatGate("deterministic_integrity_accuracy", metrics.DeterministicIntegrityAccuracy, fixedGatePolicy.MinDeterministicIntegrityAccuracy),
		floatGate("summary_contract_accuracy", metrics.SummaryContractAccuracy, fixedGatePolicy.MinSummaryContractAccuracy),
		floatGate("input_privacy_accuracy", metrics.InputPrivacyAccuracy, fixedGatePolicy.MinInputPrivacyAccuracy),
		floatGate("fallback_accuracy", metrics.FallbackAccuracy, fixedGatePolicy.MinFallbackAccuracy),
		intGate("provider_call_budget_breaches", metrics.ProviderCallBudgetBreaches, fixedGatePolicy.MaxProviderCallBudgetBreaches),
		int64Gate("total_tokens", metrics.TotalTokens, fixedGatePolicy.MaxTokens),
		intGate("external_network_calls", metrics.ExternalNetworkCalls, fixedGatePolicy.MaxExternalNetworkCalls),
		intGate("credentials_required_cases", metrics.CredentialsRequiredCases, fixedGatePolicy.MaxCredentialsRequiredCases),
	}
}

func floatGate(code string, actual, expected float64) GateResult {
	return GateResult{Code: code, Passed: actual >= expected, Actual: fmt.Sprintf("%.4f", actual), Expected: fmt.Sprintf(">= %.4f", expected)}
}

func intGate(code string, actual, expected int) GateResult {
	return GateResult{Code: code, Passed: actual <= expected, Actual: fmt.Sprint(actual), Expected: fmt.Sprintf("<= %d", expected)}
}

func int64Gate(code string, actual, expected int64) GateResult {
	return GateResult{Code: code, Passed: actual <= expected, Actual: fmt.Sprint(actual), Expected: fmt.Sprintf("<= %d", expected)}
}

func ratio(numerator int, denominator float64) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / denominator
}

func percentile(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := int(float64(len(ordered)-1)*percentile + .999999)
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}
