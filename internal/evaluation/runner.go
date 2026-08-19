package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

const GatePolicyVersion = "m5a-synthetic-gate-v1"

var ErrGateFailed = errors.New("evaluation gate failed")

// ExecutionStats is produced by the all-Mock runtime around the real engine.
// The evaluator independently checks QuerySpecs and cross-checks calls/bytes
// against report Evidence instead of trusting a single counter.
type ExecutionStats struct {
	// EngineEvidence is the first value returned by InvestigationEngine.Run.
	// Production Worker validation consumes this slice, so the evaluator must
	// not substitute report.Evidence for it.
	EngineEvidence     []domain.Evidence  `json:"-"`
	QuerySpecs         []domain.QuerySpec `json:"query_specs,omitempty"`
	QueryContractValid bool               `json:"query_contract_valid"`
	LogicalSLSCalls    int                `json:"logical_sls_calls"`
	ProviderAPICalls   int                `json:"provider_api_calls"`
	ChangeSourceCalls  int                `json:"change_source_calls"`
	ProcessedBytes     int64              `json:"processed_bytes"`
}

type ExecuteCase func(context.Context, EvaluationCase) (domain.Report, ExecutionStats, error)

// GatePolicy is output metadata, not evaluator input. Evaluate always uses
// fixedGatePolicy, so fixture authors cannot lower a threshold.
type GatePolicy struct {
	Version                     string  `json:"version"`
	MinCasePassRate             float64 `json:"min_case_pass_rate"`
	MinOutcomeAccuracy          float64 `json:"min_outcome_accuracy"`
	MinFindingExactAccuracy     float64 `json:"min_finding_exact_accuracy"`
	MinRecommendationAccuracy   float64 `json:"min_recommendation_exact_accuracy"`
	MinProductionOutputAccuracy float64 `json:"min_production_output_accuracy"`
	MinEvidenceContractAccuracy float64 `json:"min_evidence_contract_accuracy"`
	MinQueryContractAccuracy    float64 `json:"min_query_contract_accuracy"`
	MaxMisleadingRate           float64 `json:"max_misleading_rate"`
	MinConclusiveRecall         float64 `json:"min_conclusive_recall"`
	MinEvidenceCoverage         float64 `json:"min_evidence_coverage"`
	MinCauseExactAccuracy       float64 `json:"min_cause_exact_accuracy"`
	MinCauseVerdictAccuracy     float64 `json:"min_cause_verdict_accuracy"`
	MaxUnexpectedCauseVerdicts  int     `json:"max_unexpected_cause_verdicts"`
	MaxCallBudgetBreaches       int     `json:"max_call_budget_breaches"`
	MaxCostBudgetBreaches       int     `json:"max_cost_budget_breaches"`
	ExpectedLogicalSLSCalls     int     `json:"expected_logical_sls_calls"`
	ExpectedProviderAPICalls    int     `json:"expected_provider_api_calls"`
	MaxChangeSourceCalls        int     `json:"max_change_source_calls"`
	MaxProcessedBytesPerCase    int64   `json:"max_processed_bytes_per_case"`
}

var fixedGatePolicy = GatePolicy{
	Version:                     GatePolicyVersion,
	MinCasePassRate:             1,
	MinOutcomeAccuracy:          1,
	MinFindingExactAccuracy:     1,
	MinRecommendationAccuracy:   1,
	MinProductionOutputAccuracy: 1,
	MinEvidenceContractAccuracy: 1,
	MinQueryContractAccuracy:    1,
	MaxMisleadingRate:           0,
	MinConclusiveRecall:         1,
	MinEvidenceCoverage:         1,
	MinCauseExactAccuracy:       1,
	MinCauseVerdictAccuracy:     1,
	MaxUnexpectedCauseVerdicts:  0,
	MaxCallBudgetBreaches:       0,
	MaxCostBudgetBreaches:       0,
	ExpectedLogicalSLSCalls:     ExpectedLogicalSLSCalls,
	ExpectedProviderAPICalls:    ExpectedProviderAPICalls,
	MaxChangeSourceCalls:        1,
	MaxProcessedBytesPerCase:    MaxProcessedBytesPerCase,
}

type EvaluationStatus string

const (
	EvaluationPassed EvaluationStatus = "PASSED"
	EvaluationFailed EvaluationStatus = "FAILED"
)

type EvaluationReport struct {
	EvaluationVersion  string           `json:"evaluation_version"`
	DatasetID          string           `json:"dataset_id"`
	DatasetVersion     string           `json:"dataset_version"`
	DatasetFingerprint string           `json:"dataset_fingerprint"`
	Versions           VersionInfo      `json:"versions"`
	DataBoundary       DataBoundary     `json:"data_boundary"`
	Policy             GatePolicy       `json:"gate_policy"`
	Status             EvaluationStatus `json:"status"`
	Metrics            Metrics          `json:"metrics"`
	Gates              []GateResult     `json:"gates"`
	Cases              []CaseResult     `json:"cases"`
}

// VersionInfo makes every synthetic score traceable to deterministic product
// contracts. PromptUsed remains false until an actual LLM path exists.
type VersionInfo struct {
	GraphVersion         string `json:"graph_version"`
	QueryTemplateID      string `json:"query_template_id"`
	QueryTemplateVersion string `json:"query_template_version"`
	QueryPolicyVersion   string `json:"query_policy_version"`
	CauseMethod          string `json:"cause_method"`
	PromptUsed           bool   `json:"prompt_used"`
	PromptVersion        string `json:"prompt_version,omitempty"`
}

func DefaultVersionInfo() VersionInfo {
	return VersionInfo{
		GraphVersion:         "error-spike-investigation-v1",
		QueryTemplateID:      domain.ErrorAnalysisTemplateID,
		QueryTemplateVersion: domain.ErrorAnalysisTemplateVersion,
		QueryPolicyVersion:   "synthetic-policy-v1",
		CauseMethod:          domain.CauseConfidenceMethod,
		PromptUsed:           false,
	}
}

type DataBoundary struct {
	DataSource             string `json:"data_source"`
	RealIncidentCount      int    `json:"real_incident_count"`
	ExpertLabelCount       int    `json:"expert_label_count"`
	CredentialsRequired    bool   `json:"credentials_required"`
	ExternalNetworkCalls   int    `json:"external_network_calls"`
	ProductionClaimAllowed bool   `json:"production_claim_allowed"`
}

type Metrics struct {
	TotalCases                   int     `json:"total_cases"`
	ExecutedCases                int     `json:"executed_cases"`
	PassedCases                  int     `json:"passed_cases"`
	ExecutionFailures            int     `json:"execution_failures"`
	CasePassRate                 float64 `json:"case_pass_rate"`
	OutcomeAccuracy              float64 `json:"outcome_accuracy"`
	FindingExactAccuracy         float64 `json:"finding_exact_accuracy"`
	RecommendationExactAccuracy  float64 `json:"recommendation_exact_accuracy"`
	RecommendationExactFailures  int     `json:"recommendation_exact_failures"`
	ProductionOutputAccuracy     float64 `json:"production_output_accuracy"`
	ProductionOutputFailures     int     `json:"production_output_failures"`
	EvidenceContractAccuracy     float64 `json:"evidence_contract_accuracy"`
	EvidenceContractFailures     int     `json:"evidence_contract_failures"`
	QueryContractAccuracy        float64 `json:"query_contract_accuracy"`
	ExpectedConclusiveFindings   int     `json:"expected_conclusive_findings"`
	ActualConclusiveFindings     int     `json:"actual_conclusive_findings"`
	MatchedConclusiveFindings    int     `json:"matched_conclusive_findings"`
	UnexpectedConclusiveFindings int     `json:"unexpected_conclusive_findings"`
	MissingConclusiveFindings    int     `json:"missing_conclusive_findings"`
	MisleadingRate               float64 `json:"misleading_rate"`
	ConclusiveRecall             float64 `json:"conclusive_recall"`
	GroundingItems               int     `json:"grounding_items"`
	ValidGroundingItems          int     `json:"valid_grounding_items"`
	EvidenceCoverage             float64 `json:"evidence_coverage"`
	CauseExactAccuracy           float64 `json:"cause_exact_accuracy"`
	ExpectedCauseVerdicts        int     `json:"expected_cause_verdicts"`
	MatchedCauseVerdicts         int     `json:"matched_cause_verdicts"`
	UnexpectedCauseVerdicts      int     `json:"unexpected_cause_verdicts"`
	MissingCauseVerdicts         int     `json:"missing_cause_verdicts"`
	CauseVerdictAccuracy         float64 `json:"cause_verdict_accuracy"`
	LogicalSLSCalls              int     `json:"logical_sls_calls"`
	ProviderAPICalls             int     `json:"provider_api_calls"`
	ChangeSourceCalls            int     `json:"change_source_calls"`
	ProcessedBytes               int64   `json:"processed_bytes"`
	CallBudgetBreaches           int     `json:"call_budget_breaches"`
	CostBudgetBreaches           int     `json:"cost_budget_breaches"`
	LatencyP50Milliseconds       int64   `json:"latency_p50_milliseconds"`
	LatencyP95Milliseconds       int64   `json:"latency_p95_milliseconds"`
	LatencyMaxMilliseconds       int64   `json:"latency_max_milliseconds"`
}

type CaseResult struct {
	ID                         string                     `json:"id"`
	Passed                     bool                       `json:"passed"`
	FailureReasons             []string                   `json:"failure_reasons,omitempty"`
	ExpectedOutcome            string                     `json:"expected_outcome"`
	ActualOutcome              string                     `json:"actual_outcome,omitempty"`
	OutcomeCorrect             bool                       `json:"outcome_correct"`
	ExpectedConclusiveCodes    []string                   `json:"expected_conclusive_codes"`
	ActualConclusiveCodes      []string                   `json:"actual_conclusive_codes,omitempty"`
	ExpectedNonconclusiveCodes []string                   `json:"expected_nonconclusive_codes"`
	ActualNonconclusiveCodes   []string                   `json:"actual_nonconclusive_codes,omitempty"`
	FindingsExact              bool                       `json:"findings_exact"`
	ExpectedRecommendations    []ExpectedRecommendation   `json:"expected_recommendations"`
	ActualRecommendations      []ExpectedRecommendation   `json:"actual_recommendations,omitempty"`
	RecommendationsExact       bool                       `json:"recommendations_exact"`
	ProductionOutputValid      bool                       `json:"production_output_valid"`
	EvidenceContractPassed     bool                       `json:"evidence_contract_passed"`
	QueryContractPassed        bool                       `json:"query_contract_passed"`
	EvidenceCoveragePassed     bool                       `json:"evidence_coverage_passed"`
	ExpectedCauseStatus        domain.CauseAnalysisStatus `json:"expected_cause_status"`
	ActualCauseStatus          domain.CauseAnalysisStatus `json:"actual_cause_status,omitempty"`
	ExpectedCauseVerdicts      []ExpectedCauseVerdict     `json:"expected_cause_verdicts"`
	ActualCauseVerdicts        []ExpectedCauseVerdict     `json:"actual_cause_verdicts,omitempty"`
	CauseExact                 bool                       `json:"cause_exact"`
	LogicalSLSCalls            int                        `json:"logical_sls_calls"`
	ProviderAPICalls           int                        `json:"provider_api_calls"`
	ChangeSourceCalls          int                        `json:"change_source_calls"`
	ProcessedBytes             int64                      `json:"processed_bytes"`
	CallBudgetPassed           bool                       `json:"call_budget_passed"`
	CostBudgetPassed           bool                       `json:"cost_budget_passed"`
	DurationMilliseconds       int64                      `json:"duration_milliseconds"`
}

type GateResult struct {
	Code     string `json:"code"`
	Passed   bool   `json:"passed"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
}

type GateError struct {
	Failed []string
}

func (err *GateError) Error() string {
	return fmt.Sprintf("%s: %s", ErrGateFailed, strings.Join(err.Failed, ", "))
}

func (err *GateError) Is(target error) bool {
	return target == ErrGateFailed
}

// Evaluate runs every valid fixture, aggregates metrics, and applies the fixed
// gate. Execution errors are case failures rather than skipped cases.
func Evaluate(ctx context.Context, dataset Dataset, execute ExecuteCase) (EvaluationReport, error) {
	return EvaluateWithVersions(ctx, dataset, DefaultVersionInfo(), execute)
}

// EvaluateWithVersions is the assembly-facing entrypoint when the command
// owns an explicit graph or policy version. Latency is observational only and
// is deliberately absent from the fixed pass/fail gate.
func EvaluateWithVersions(ctx context.Context, dataset Dataset, versions VersionInfo, execute ExecuteCase) (EvaluationReport, error) {
	return evaluateWithVersions(ctx, dataset, versions, application.ValidateEngineOutput, execute)
}

type outputValidator func(string, []domain.Evidence, domain.Report) error

func evaluateWithVersions(ctx context.Context, dataset Dataset, versions VersionInfo, validateOutput outputValidator, execute ExecuteCase) (EvaluationReport, error) {
	if execute == nil {
		return EvaluationReport{}, errors.New("evaluation execute callback is required")
	}
	if validateOutput == nil {
		return EvaluationReport{}, errors.New("production output validator is required")
	}
	if err := ValidateDataset(dataset); err != nil {
		return EvaluationReport{}, err
	}
	if err := validateVersions(dataset, versions); err != nil {
		return EvaluationReport{}, err
	}
	fingerprint, err := normalizedFingerprint(dataset)
	if err != nil {
		return EvaluationReport{}, err
	}
	result := EvaluationReport{
		EvaluationVersion:  GatePolicyVersion,
		DatasetID:          dataset.DatasetID,
		DatasetVersion:     dataset.SchemaVersion,
		DatasetFingerprint: fingerprint,
		Versions:           versions,
		DataBoundary: DataBoundary{
			DataSource: dataset.DataSource, RealIncidentCount: dataset.RealIncidentCount,
			ExpertLabelCount: dataset.ExpertLabelCount, CredentialsRequired: dataset.CredentialsRequired,
			ExternalNetworkCalls: dataset.ExternalNetworkCalls, ProductionClaimAllowed: dataset.ProductionClaimAllowed,
		},
		Policy: fixedGatePolicy,
		Status: EvaluationFailed,
		Cases:  make([]CaseResult, 0, len(dataset.Cases)),
	}
	metrics := Metrics{TotalCases: len(dataset.Cases)}
	durations := make([]int64, 0, len(dataset.Cases))
	for _, evaluationCase := range dataset.Cases {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		caseResult, caseMetrics := evaluateOne(ctx, evaluationCase, validateOutput, execute)
		result.Cases = append(result.Cases, caseResult)
		durations = append(durations, caseResult.DurationMilliseconds)
		metrics.add(caseMetrics)
	}
	metrics.finish(durations)
	result.Metrics = metrics
	result.Gates = applyGates(metrics, fixedGatePolicy)
	failed := make([]string, 0)
	for _, gate := range result.Gates {
		if !gate.Passed {
			failed = append(failed, gate.Code)
		}
	}
	if len(failed) > 0 {
		return result, &GateError{Failed: failed}
	}
	result.Status = EvaluationPassed
	return result, nil
}

type metricDelta Metrics

func evaluateOne(ctx context.Context, evaluationCase EvaluationCase, validateOutput outputValidator, execute ExecuteCase) (CaseResult, metricDelta) {
	expected := evaluationCase.Expected
	result := CaseResult{
		ID: evaluationCase.ID, ExpectedOutcome: expected.Outcome,
		ExpectedConclusiveCodes:    append([]string(nil), expected.ConclusiveFindingCodes...),
		ExpectedNonconclusiveCodes: append([]string(nil), expected.NonconclusiveFindingCodes...),
		ExpectedRecommendations:    cloneExpectedRecommendations(expected.Recommendations),
		ExpectedCauseStatus:        expected.CauseStatus,
		ExpectedCauseVerdicts:      append([]ExpectedCauseVerdict(nil), expected.CauseVerdicts...),
	}
	delta := metricDelta{ExecutedCases: 1, ExpectedConclusiveFindings: len(expected.ConclusiveFindingCodes), ExpectedCauseVerdicts: len(expected.CauseVerdicts)}
	startedAt := time.Now()
	report, stats, err := execute(ctx, evaluationCase)
	result.DurationMilliseconds = time.Since(startedAt).Milliseconds()
	if err != nil {
		delta.ExecutionFailures = 1
		delta.MissingConclusiveFindings = len(expected.ConclusiveFindingCodes)
		delta.MissingCauseVerdicts = len(expected.CauseVerdicts)
		result.FailureReasons = []string{"execution_failed"}
		return result, delta
	}
	expectedInvestigationID := ""
	if len(stats.QuerySpecs) > 0 {
		expectedInvestigationID = stats.QuerySpecs[0].InvestigationID
	}
	result.ProductionOutputValid = validateOutput(expectedInvestigationID, stats.EngineEvidence, report) == nil
	if result.ProductionOutputValid {
		delta.ProductionOutputAccuracy = 1
	} else {
		delta.ProductionOutputFailures = 1
		result.FailureReasons = append(result.FailureReasons, "production_output_invalid")
	}
	result.ActualOutcome = report.Outcome
	result.OutcomeCorrect = report.Outcome == expected.Outcome
	if result.OutcomeCorrect {
		delta.OutcomeAccuracy = 1
	} else {
		result.FailureReasons = append(result.FailureReasons, "outcome_mismatch")
	}

	actualConclusive, actualNonconclusive := findingCodes(report.Findings)
	result.ActualConclusiveCodes = actualConclusive
	result.ActualNonconclusiveCodes = actualNonconclusive
	matched, unexpected, missing := compareMultiset(expected.ConclusiveFindingCodes, actualConclusive)
	delta.ActualConclusiveFindings = len(actualConclusive)
	delta.MatchedConclusiveFindings = matched
	delta.UnexpectedConclusiveFindings = unexpected
	delta.MissingConclusiveFindings = missing
	result.FindingsExact = multisetEqual(expected.ConclusiveFindingCodes, actualConclusive) && multisetEqual(expected.NonconclusiveFindingCodes, actualNonconclusive)
	if result.FindingsExact {
		delta.FindingExactAccuracy = 1
	} else {
		result.FailureReasons = append(result.FailureReasons, "finding_labels_mismatch")
	}

	actualRecommendations, recommendationsValid := recommendationContract(report.Recommendations, report.Evidence)
	result.ActualRecommendations = actualRecommendations
	result.RecommendationsExact = recommendationsValid && expectedRecommendationsEqual(expected.Recommendations, actualRecommendations)
	if result.RecommendationsExact {
		delta.RecommendationExactAccuracy = 1
	} else {
		delta.RecommendationExactFailures = 1
		result.FailureReasons = append(result.FailureReasons, "recommendation_labels_mismatch")
	}

	result.QueryContractPassed = stats.QueryContractValid && validQueryContract(evaluationCase.Request, report.InvestigationID, stats.QuerySpecs) && stats.LogicalSLSCalls == len(stats.QuerySpecs)
	if result.QueryContractPassed {
		delta.QueryContractAccuracy = 1
	} else {
		result.FailureReasons = append(result.FailureReasons, "query_contract_mismatch")
	}
	result.EvidenceContractPassed = reflect.DeepEqual(stats.EngineEvidence, report.Evidence) && validEvidenceContract(evaluationCase, stats.EngineEvidence, stats.QuerySpecs)
	if result.EvidenceContractPassed {
		delta.EvidenceContractAccuracy = 1
	} else {
		delta.EvidenceContractFailures = 1
		result.FailureReasons = append(result.FailureReasons, "evidence_contract_mismatch")
	}

	groundingTotal, groundingValid := groundingCoverage(report)
	if groundingTotal == 0 && len(expected.ConclusiveFindingCodes)+len(expected.NonconclusiveFindingCodes) > 0 {
		groundingTotal = 1
	}
	delta.GroundingItems = groundingTotal
	delta.ValidGroundingItems = groundingValid
	result.EvidenceCoveragePassed = groundingTotal == groundingValid
	if !result.EvidenceCoveragePassed {
		result.FailureReasons = append(result.FailureReasons, "evidence_coverage_incomplete")
	}

	actualStatus, actualVerdicts, causeValid := extractCause(report.CauseAnalysis)
	result.ActualCauseStatus = actualStatus
	result.ActualCauseVerdicts = actualVerdicts
	causeMatched, causeUnexpected, causeMissing := compareCause(expected.CauseVerdicts, actualVerdicts)
	delta.MatchedCauseVerdicts = causeMatched
	delta.UnexpectedCauseVerdicts = causeUnexpected
	delta.MissingCauseVerdicts = causeMissing
	result.CauseExact = causeValid && actualStatus == expected.CauseStatus && causeUnexpected == 0 && causeMissing == 0
	if result.CauseExact {
		delta.CauseExactAccuracy = 1
	} else {
		result.FailureReasons = append(result.FailureReasons, "cause_labels_mismatch")
	}

	evidenceCalls, evidenceBytes, usageValid := evidenceUsage(stats.EngineEvidence)
	result.LogicalSLSCalls = stats.LogicalSLSCalls
	result.ProviderAPICalls = stats.ProviderAPICalls
	result.ChangeSourceCalls = stats.ChangeSourceCalls
	result.ProcessedBytes = stats.ProcessedBytes
	delta.LogicalSLSCalls = stats.LogicalSLSCalls
	delta.ProviderAPICalls = stats.ProviderAPICalls
	delta.ChangeSourceCalls = stats.ChangeSourceCalls
	delta.ProcessedBytes = stats.ProcessedBytes
	result.CallBudgetPassed = stats.LogicalSLSCalls == ExpectedLogicalSLSCalls && stats.ProviderAPICalls == ExpectedProviderAPICalls && stats.ChangeSourceCalls == expected.ChangeSourceCalls && evidenceCalls == stats.ProviderAPICalls
	if !result.CallBudgetPassed {
		delta.CallBudgetBreaches = 1
		result.FailureReasons = append(result.FailureReasons, "call_budget_mismatch")
	}
	caseByteLimit := expected.MaxProcessedBytes
	if caseByteLimit > MaxProcessedBytesPerCase {
		caseByteLimit = MaxProcessedBytesPerCase
	}
	result.CostBudgetPassed = usageValid && stats.ProcessedBytes == evidenceBytes && stats.ProcessedBytes >= 0 && stats.ProcessedBytes <= caseByteLimit
	if !result.CostBudgetPassed {
		delta.CostBudgetBreaches = 1
		result.FailureReasons = append(result.FailureReasons, "cost_budget_mismatch")
	}

	result.Passed = result.OutcomeCorrect && result.FindingsExact && result.RecommendationsExact && result.ProductionOutputValid && result.QueryContractPassed && result.EvidenceContractPassed && result.EvidenceCoveragePassed && result.CauseExact && result.CallBudgetPassed && result.CostBudgetPassed
	if result.Passed {
		delta.PassedCases = 1
	}
	return result, delta
}

func (metrics *Metrics) add(delta metricDelta) {
	metrics.ExecutedCases += delta.ExecutedCases
	metrics.PassedCases += delta.PassedCases
	metrics.ExecutionFailures += delta.ExecutionFailures
	metrics.OutcomeAccuracy += delta.OutcomeAccuracy
	metrics.FindingExactAccuracy += delta.FindingExactAccuracy
	metrics.RecommendationExactAccuracy += delta.RecommendationExactAccuracy
	metrics.RecommendationExactFailures += delta.RecommendationExactFailures
	metrics.ProductionOutputAccuracy += delta.ProductionOutputAccuracy
	metrics.ProductionOutputFailures += delta.ProductionOutputFailures
	metrics.EvidenceContractAccuracy += delta.EvidenceContractAccuracy
	metrics.EvidenceContractFailures += delta.EvidenceContractFailures
	metrics.QueryContractAccuracy += delta.QueryContractAccuracy
	metrics.ExpectedConclusiveFindings += delta.ExpectedConclusiveFindings
	metrics.ActualConclusiveFindings += delta.ActualConclusiveFindings
	metrics.MatchedConclusiveFindings += delta.MatchedConclusiveFindings
	metrics.UnexpectedConclusiveFindings += delta.UnexpectedConclusiveFindings
	metrics.MissingConclusiveFindings += delta.MissingConclusiveFindings
	metrics.GroundingItems += delta.GroundingItems
	metrics.ValidGroundingItems += delta.ValidGroundingItems
	metrics.CauseExactAccuracy += delta.CauseExactAccuracy
	metrics.ExpectedCauseVerdicts += delta.ExpectedCauseVerdicts
	metrics.MatchedCauseVerdicts += delta.MatchedCauseVerdicts
	metrics.UnexpectedCauseVerdicts += delta.UnexpectedCauseVerdicts
	metrics.MissingCauseVerdicts += delta.MissingCauseVerdicts
	metrics.LogicalSLSCalls += delta.LogicalSLSCalls
	metrics.ProviderAPICalls += delta.ProviderAPICalls
	metrics.ChangeSourceCalls += delta.ChangeSourceCalls
	metrics.ProcessedBytes += delta.ProcessedBytes
	metrics.CallBudgetBreaches += delta.CallBudgetBreaches
	metrics.CostBudgetBreaches += delta.CostBudgetBreaches
}

func (metrics *Metrics) finish(durations []int64) {
	metrics.CasePassRate = ratio(metrics.PassedCases, metrics.TotalCases)
	metrics.OutcomeAccuracy = ratio(int(metrics.OutcomeAccuracy), metrics.TotalCases)
	metrics.FindingExactAccuracy = ratio(int(metrics.FindingExactAccuracy), metrics.TotalCases)
	metrics.RecommendationExactAccuracy = ratio(int(metrics.RecommendationExactAccuracy), metrics.TotalCases)
	metrics.ProductionOutputAccuracy = ratio(int(metrics.ProductionOutputAccuracy), metrics.TotalCases)
	metrics.EvidenceContractAccuracy = ratio(int(metrics.EvidenceContractAccuracy), metrics.TotalCases)
	metrics.QueryContractAccuracy = ratio(int(metrics.QueryContractAccuracy), metrics.TotalCases)
	metrics.MisleadingRate = ratioDefault(metrics.UnexpectedConclusiveFindings, metrics.ActualConclusiveFindings, 0)
	metrics.ConclusiveRecall = ratioZero(metrics.MatchedConclusiveFindings, metrics.ExpectedConclusiveFindings)
	metrics.EvidenceCoverage = ratioZero(metrics.ValidGroundingItems, metrics.GroundingItems)
	metrics.CauseExactAccuracy = ratio(int(metrics.CauseExactAccuracy), metrics.TotalCases)
	metrics.CauseVerdictAccuracy = ratioZero(metrics.MatchedCauseVerdicts, metrics.ExpectedCauseVerdicts)
	if len(durations) > 0 {
		sorted := append([]int64(nil), durations...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		metrics.LatencyP50Milliseconds = nearestRank(sorted, 50)
		metrics.LatencyP95Milliseconds = nearestRank(sorted, 95)
		metrics.LatencyMaxMilliseconds = sorted[len(sorted)-1]
	}
}

func nearestRank(sorted []int64, percentile int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (percentile*len(sorted)+99)/100 - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func ratioZero(numerator, denominator int) float64 {
	return ratioDefault(numerator, denominator, 1)
}

func ratioDefault(numerator, denominator int, defaultValue float64) float64 {
	if denominator == 0 {
		return defaultValue
	}
	return ratio(numerator, denominator)
}

func applyGates(metrics Metrics, policy GatePolicy) []GateResult {
	return []GateResult{
		gateInt("all_cases_executed", metrics.ExecutedCases, metrics.TotalCases),
		gateMin("case_pass_rate", metrics.CasePassRate, policy.MinCasePassRate),
		gateMin("outcome_accuracy", metrics.OutcomeAccuracy, policy.MinOutcomeAccuracy),
		gateMin("finding_exact_accuracy", metrics.FindingExactAccuracy, policy.MinFindingExactAccuracy),
		gateMin("recommendation_exact_accuracy", metrics.RecommendationExactAccuracy, policy.MinRecommendationAccuracy),
		gateMin("production_output_accuracy", metrics.ProductionOutputAccuracy, policy.MinProductionOutputAccuracy),
		gateMin("evidence_contract_accuracy", metrics.EvidenceContractAccuracy, policy.MinEvidenceContractAccuracy),
		gateMin("query_contract_accuracy", metrics.QueryContractAccuracy, policy.MinQueryContractAccuracy),
		gateMax("misleading_rate", metrics.MisleadingRate, policy.MaxMisleadingRate),
		gateMin("conclusive_recall", metrics.ConclusiveRecall, policy.MinConclusiveRecall),
		gateMin("evidence_coverage", metrics.EvidenceCoverage, policy.MinEvidenceCoverage),
		gateMin("cause_exact_accuracy", metrics.CauseExactAccuracy, policy.MinCauseExactAccuracy),
		gateMin("cause_verdict_accuracy", metrics.CauseVerdictAccuracy, policy.MinCauseVerdictAccuracy),
		gateIntMax("unexpected_cause_verdicts", metrics.UnexpectedCauseVerdicts, policy.MaxUnexpectedCauseVerdicts),
		gateIntMax("call_budget_breaches", metrics.CallBudgetBreaches, policy.MaxCallBudgetBreaches),
		gateIntMax("cost_budget_breaches", metrics.CostBudgetBreaches, policy.MaxCostBudgetBreaches),
	}
}

func gateMin(code string, actual, threshold float64) GateResult {
	return GateResult{Code: code, Passed: actual >= threshold, Actual: fmt.Sprintf("%.6f", actual), Expected: fmt.Sprintf(">= %.6f", threshold)}
}

func gateMax(code string, actual, threshold float64) GateResult {
	return GateResult{Code: code, Passed: actual <= threshold, Actual: fmt.Sprintf("%.6f", actual), Expected: fmt.Sprintf("<= %.6f", threshold)}
}

func gateInt(code string, actual, expected int) GateResult {
	return GateResult{Code: code, Passed: actual == expected, Actual: fmt.Sprint(actual), Expected: fmt.Sprintf("= %d", expected)}
}

func gateIntMax(code string, actual, expected int) GateResult {
	return GateResult{Code: code, Passed: actual <= expected, Actual: fmt.Sprint(actual), Expected: fmt.Sprintf("<= %d", expected)}
}

func normalizedFingerprint(dataset Dataset) (string, error) {
	// Fingerprint is derived output, never trusted caller input. Recompute it
	// from the current semantic dataset so a programmatic label or fixture
	// mutation cannot retain a stale, otherwise well-formed SHA-256 value.
	dataset.Fingerprint = ""
	payload, err := json.Marshal(dataset)
	if err != nil {
		return "", fmt.Errorf("encode evaluation dataset fingerprint: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validateVersions(dataset Dataset, versions VersionInfo) error {
	if err := validateText("graph version", versions.GraphVersion, 128); err != nil {
		return err
	}
	if versions.QueryTemplateID != domain.ErrorAnalysisTemplateID || versions.QueryTemplateVersion != domain.ErrorAnalysisTemplateVersion {
		return errors.New("evaluation versions do not match the fixed query template")
	}
	if err := validateText("query policy version", versions.QueryPolicyVersion, 128); err != nil {
		return err
	}
	if versions.CauseMethod != domain.CauseConfidenceMethod {
		return errors.New("evaluation cause method does not match the deterministic engine contract")
	}
	if versions.PromptUsed || versions.PromptVersion != "" {
		return errors.New("M5-A deterministic evaluation cannot claim a prompt or LLM version")
	}
	for _, evaluationCase := range dataset.Cases {
		if evaluationCase.Current.PolicyVersion != versions.QueryPolicyVersion || evaluationCase.Baseline.PolicyVersion != versions.QueryPolicyVersion {
			return fmt.Errorf("case %q policy version does not match evaluation version metadata", evaluationCase.ID)
		}
	}
	return nil
}

func validQueryContract(request domain.InvestigationRequest, investigationID string, specs []domain.QuerySpec) bool {
	if investigationID == "" || len(specs) != ExpectedLogicalSLSCalls {
		return false
	}
	byName := make(map[string]domain.QuerySpec, len(specs))
	for _, spec := range specs {
		if spec.Name != "current" && spec.Name != "baseline" {
			return false
		}
		if _, duplicate := byName[spec.Name]; duplicate {
			return false
		}
		if spec.InvestigationID != investigationID || spec.TemplateID != domain.ErrorAnalysisTemplateID || spec.Service != request.Service || spec.Environment != request.Environment || spec.Requester != request.Requester {
			return false
		}
		byName[spec.Name] = spec
	}
	current, currentOK := byName["current"]
	baseline, baselineOK := byName["baseline"]
	if !currentOK || !baselineOK || !current.StartTime.Equal(request.StartTime) || !current.EndTime.Equal(request.EndTime) {
		return false
	}
	duration := request.EndTime.Sub(request.StartTime)
	return baseline.StartTime.Equal(request.StartTime.Add(-duration)) && baseline.EndTime.Equal(request.StartTime)
}

func validEvidenceContract(evaluationCase EvaluationCase, evidence []domain.Evidence, specs []domain.QuerySpec) bool {
	if len(evidence) != ExpectedLogicalSLSCalls || len(specs) != ExpectedLogicalSLSCalls {
		return false
	}
	specByName := make(map[string]domain.QuerySpec, len(specs))
	for _, spec := range specs {
		if _, duplicate := specByName[spec.Name]; duplicate {
			return false
		}
		specByName[spec.Name] = spec
	}
	evidenceByName := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		if item.ID == "" {
			return false
		}
		if _, duplicate := evidenceByName[item.Name]; duplicate {
			return false
		}
		evidenceByName[item.Name] = item
	}
	fixtures := map[string]domain.QueryResult{
		"current":  evaluationCase.Current,
		"baseline": evaluationCase.Baseline,
	}
	for _, name := range []string{"current", "baseline"} {
		spec, specExists := specByName[name]
		actual, evidenceExists := evidenceByName[name]
		fixture, fixtureExists := fixtures[name]
		if !specExists || !evidenceExists || !fixtureExists {
			return false
		}
		expected, err := evidenceFromFixture(fixture, spec)
		if err != nil {
			return false
		}
		actual.ID = ""
		if !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func evidenceFromFixture(result domain.QueryResult, spec domain.QuerySpec) (domain.Evidence, error) {
	specHash, err := fingerprint.JSON(spec)
	if err != nil {
		return domain.Evidence{}, err
	}
	return domain.Evidence{
		QueryID:                 result.QueryID,
		QuerySpecHash:           specHash,
		ResourceID:              result.ResourceID,
		TemplateID:              result.TemplateID,
		TemplateVersion:         result.TemplateVersion,
		SchemaFingerprint:       result.SchemaFingerprint,
		PolicyVersion:           result.PolicyVersion,
		GovernanceFingerprint:   result.GovernanceFingerprint,
		Name:                    spec.Name,
		StartTime:               spec.StartTime,
		EndTime:                 spec.EndTime,
		Progress:                result.Progress,
		Complete:                result.Complete,
		Truncated:               result.Truncated,
		NanosecondOrderedKnown:  result.NanosecondOrderedKnown,
		NanosecondOrdered:       result.NanosecondOrdered,
		UsageKnown:              result.UsageKnown,
		IncompleteReason:        result.IncompleteReason,
		ProcessedRows:           result.ProcessedRows,
		ProcessedBytes:          result.ProcessedBytes,
		ElapsedMillisecond:      result.ElapsedMillisecond,
		APICalls:                result.APICalls,
		Redacted:                result.Redacted,
		ErrorCount:              result.ErrorCount,
		TopError:                result.TopError,
		TopErrorCount:           result.TopErrorCount,
		ErrorPatterns:           append([]domain.CountBucket(nil), result.ErrorPatterns...),
		Instances:               append([]domain.CountBucket(nil), result.Instances...),
		ErrorPatternsExhaustive: result.ErrorPatternsExhaustive,
		InstancesExhaustive:     result.InstancesExhaustive,
		PatternLimit:            result.PatternLimit,
		InstanceLimit:           result.InstanceLimit,
	}, nil
}

func findingCodes(findings []domain.Finding) (conclusive, nonconclusive []string) {
	for _, finding := range findings {
		if finding.Conclusive {
			conclusive = append(conclusive, finding.Code)
		} else {
			nonconclusive = append(nonconclusive, finding.Code)
		}
	}
	return conclusive, nonconclusive
}

func cloneExpectedRecommendations(values []ExpectedRecommendation) []ExpectedRecommendation {
	cloned := make([]ExpectedRecommendation, len(values))
	for index, value := range values {
		cloned[index] = ExpectedRecommendation{
			Code:          value.Code,
			EvidenceNames: append([]string(nil), value.EvidenceNames...),
		}
	}
	return cloned
}

func recommendationContract(recommendations []domain.Recommendation, evidence []domain.Evidence) ([]ExpectedRecommendation, bool) {
	if len(recommendations) == 0 {
		return nil, false
	}
	evidenceNames := make(map[string]string, len(evidence))
	for _, item := range evidence {
		if item.ID == "" || (item.Name != "current" && item.Name != "baseline") {
			return nil, false
		}
		if _, duplicate := evidenceNames[item.ID]; duplicate {
			return nil, false
		}
		evidenceNames[item.ID] = item.Name
	}

	actual := make([]ExpectedRecommendation, 0, len(recommendations))
	seenCodes := make(map[string]struct{}, len(recommendations))
	for _, recommendation := range recommendations {
		if recommendation.Code == "" || len(recommendation.EvidenceIDs) == 0 {
			return nil, false
		}
		if _, duplicate := seenCodes[recommendation.Code]; duplicate {
			return nil, false
		}
		seenCodes[recommendation.Code] = struct{}{}
		names := make([]string, 0, len(recommendation.EvidenceIDs))
		seenNames := make(map[string]struct{}, len(recommendation.EvidenceIDs))
		for _, evidenceID := range recommendation.EvidenceIDs {
			name, exists := evidenceNames[evidenceID]
			if !exists {
				return nil, false
			}
			if _, duplicate := seenNames[name]; duplicate {
				return nil, false
			}
			seenNames[name] = struct{}{}
			names = append(names, name)
		}
		sort.Strings(names)
		actual = append(actual, ExpectedRecommendation{Code: recommendation.Code, EvidenceNames: names})
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Code < actual[j].Code })
	return actual, true
}

func expectedRecommendationsEqual(expected, actual []ExpectedRecommendation) bool {
	canonicalExpected := cloneExpectedRecommendations(expected)
	for index := range canonicalExpected {
		sort.Strings(canonicalExpected[index].EvidenceNames)
	}
	sort.Slice(canonicalExpected, func(i, j int) bool { return canonicalExpected[i].Code < canonicalExpected[j].Code })
	return reflect.DeepEqual(canonicalExpected, actual)
}

func compareMultiset(expected, actual []string) (matched, unexpected, missing int) {
	expectedCounts := stringCounts(expected)
	actualCounts := stringCounts(actual)
	for code, count := range expectedCounts {
		actualCount := actualCounts[code]
		if actualCount < count {
			matched += actualCount
			missing += count - actualCount
		} else {
			matched += count
		}
	}
	for code, count := range actualCounts {
		if count > expectedCounts[code] {
			unexpected += count - expectedCounts[code]
		}
	}
	return matched, unexpected, missing
}

func multisetEqual(expected, actual []string) bool {
	_, unexpected, missing := compareMultiset(expected, actual)
	return unexpected == 0 && missing == 0
}

func stringCounts(values []string) map[string]int {
	counts := make(map[string]int, len(values))
	for _, value := range values {
		counts[value]++
	}
	return counts
}

func groundingCoverage(report domain.Report) (total, valid int) {
	evidenceCounts := make(map[string]int, len(report.Evidence))
	evidenceByID := make(map[string]domain.Evidence, len(report.Evidence))
	for _, item := range report.Evidence {
		evidenceCounts[item.ID]++
		evidenceByID[item.ID] = item
	}
	changeCounts := make(map[string]int)
	if report.CauseAnalysis != nil {
		changeCounts = make(map[string]int, len(report.CauseAnalysis.Changes))
		for _, change := range report.CauseAnalysis.Changes {
			changeCounts[change.ID]++
		}
	}
	for id, count := range evidenceCounts {
		if id == "" || count != 1 {
			total++
		}
	}
	for id, count := range changeCounts {
		if id == "" || count != 1 {
			total++
		}
	}
	for _, finding := range report.Findings {
		total++
		if validEvidenceReferences(finding.EvidenceIDs, evidenceCounts, evidenceByID, finding.Conclusive) {
			valid++
		}
	}
	for _, recommendation := range report.Recommendations {
		total++
		if validReferences(recommendation.EvidenceIDs, evidenceCounts) {
			valid++
		}
	}
	if report.CauseAnalysis != nil {
		// The ledger and hypothesis projection must form a closed, one-to-one
		// graph. An orphan entry with valid Evidence IDs is still invalid because
		// readers cannot discover it from the hypothesis it claims to test.
		ledgerCounts := make(map[string]int, len(report.CauseAnalysis.Ledger))
		ledgerRoles := make(map[string]domain.EvidenceTestRole, len(report.CauseAnalysis.Ledger))
		ledgerReferenceCounts := make(map[string]int, len(report.CauseAnalysis.Ledger))
		for _, entry := range report.CauseAnalysis.Ledger {
			ledgerCounts[entry.ID]++
			ledgerRoles[entry.ID] = entry.Role
		}
		for _, hypothesis := range report.CauseAnalysis.Hypotheses {
			for _, entryID := range hypothesis.SupportEntryIDs {
				ledgerReferenceCounts[entryID]++
			}
			for _, entryID := range hypothesis.CounterEntryIDs {
				ledgerReferenceCounts[entryID]++
			}
		}
		for _, entry := range report.CauseAnalysis.Ledger {
			total++
			if entry.ID != "" && ledgerCounts[entry.ID] == 1 && ledgerReferenceCounts[entry.ID] == 1 && len(entry.EvidenceIDs)+len(entry.ChangeEventIDs) > 0 && validEvidenceReferencesAllowEmpty(entry.EvidenceIDs, evidenceCounts, evidenceByID, true) && validReferencesAllowEmpty(entry.ChangeEventIDs, changeCounts) {
				valid++
			}
		}
		// Hypotheses reference ledger entries through support/counter IDs.
		for _, hypothesis := range report.CauseAnalysis.Hypotheses {
			total++
			refs := append(append([]string(nil), hypothesis.SupportEntryIDs...), hypothesis.CounterEntryIDs...)
			rolesValid := true
			for _, entryID := range hypothesis.SupportEntryIDs {
				rolesValid = rolesValid && ledgerRoles[entryID] == domain.EvidenceTestSupport
			}
			for _, entryID := range hypothesis.CounterEntryIDs {
				rolesValid = rolesValid && ledgerRoles[entryID] == domain.EvidenceTestCounter
			}
			if len(hypothesis.SupportEntryIDs) > 0 && len(hypothesis.CounterEntryIDs) > 0 && rolesValid && validReferences(refs, ledgerCounts) {
				valid++
			}
		}
	}
	return total, valid
}

func validEvidenceReferences(ids []string, counts map[string]int, evidence map[string]domain.Evidence, requireComplete bool) bool {
	return len(ids) > 0 && validEvidenceReferencesAllowEmpty(ids, counts, evidence, requireComplete)
}

func validEvidenceReferencesAllowEmpty(ids []string, counts map[string]int, evidence map[string]domain.Evidence, requireComplete bool) bool {
	if !validReferencesAllowEmpty(ids, counts) {
		return false
	}
	if !requireComplete {
		return true
	}
	for _, id := range ids {
		item := evidence[id]
		if !item.Complete || item.Truncated || !item.UsageKnown {
			return false
		}
	}
	return true
}

func validReferences(ids []string, counts map[string]int) bool {
	return len(ids) > 0 && validReferencesAllowEmpty(ids, counts)
}

func validReferencesAllowEmpty(ids []string, counts map[string]int) bool {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || counts[id] != 1 {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func extractCause(analysis *domain.CauseAnalysis) (domain.CauseAnalysisStatus, []ExpectedCauseVerdict, bool) {
	if analysis == nil {
		return "", nil, false
	}
	changeCounts := make(map[string]int, len(analysis.Changes))
	for _, change := range analysis.Changes {
		changeCounts[change.ID]++
	}
	hypothesisCounts := make(map[string]int, len(analysis.Hypotheses))
	for _, hypothesis := range analysis.Hypotheses {
		hypothesisCounts[hypothesis.ID]++
	}
	valid := true
	for _, entry := range analysis.Ledger {
		if hypothesisCounts[entry.HypothesisID] != 1 {
			valid = false
		}
	}
	verdicts := make([]ExpectedCauseVerdict, 0, len(analysis.Hypotheses))
	seenChanges := make(map[string]struct{}, len(analysis.Hypotheses))
	for _, hypothesis := range analysis.Hypotheses {
		if hypothesis.ID == "" || hypothesisCounts[hypothesis.ID] != 1 {
			valid = false
			continue
		}
		changeIDs := make(map[string]struct{})
		for _, entry := range analysis.Ledger {
			if entry.HypothesisID != hypothesis.ID {
				continue
			}
			for _, changeID := range entry.ChangeEventIDs {
				changeIDs[changeID] = struct{}{}
			}
		}
		if len(changeIDs) != 1 {
			valid = false
			continue
		}
		var changeID string
		for candidate := range changeIDs {
			changeID = candidate
		}
		if changeCounts[changeID] != 1 {
			valid = false
			continue
		}
		if _, duplicate := seenChanges[changeID]; duplicate {
			valid = false
			continue
		}
		seenChanges[changeID] = struct{}{}
		verdicts = append(verdicts, ExpectedCauseVerdict{ChangeID: changeID, Verdict: hypothesis.Verdict})
	}
	if len(seenChanges) != len(changeCounts) {
		valid = false
	}
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].ChangeID < verdicts[j].ChangeID })
	return analysis.Status, verdicts, valid
}

func compareCause(expected, actual []ExpectedCauseVerdict) (matched, unexpected, missing int) {
	expectedMap := make(map[string]domain.CauseVerdict, len(expected))
	actualMap := make(map[string]domain.CauseVerdict, len(actual))
	for _, item := range expected {
		expectedMap[item.ChangeID] = item.Verdict
	}
	for _, item := range actual {
		actualMap[item.ChangeID] = item.Verdict
	}
	for changeID, verdict := range expectedMap {
		if actualMap[changeID] == verdict {
			matched++
		} else {
			missing++
		}
	}
	for changeID, verdict := range actualMap {
		if expectedMap[changeID] != verdict {
			unexpected++
		}
	}
	return matched, unexpected, missing
}

func evidenceUsage(evidence []domain.Evidence) (calls int, bytes int64, valid bool) {
	valid = true
	for _, item := range evidence {
		if item.APICalls < 0 || item.ProcessedBytes < 0 || bytes > MaxProcessedBytesPerCase-item.ProcessedBytes {
			valid = false
			continue
		}
		calls += item.APICalls
		bytes += item.ProcessedBytes
	}
	return calls, bytes, valid
}
