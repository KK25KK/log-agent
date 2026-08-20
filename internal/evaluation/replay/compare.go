package replay

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
)

const ComparisonSchemaVersion = "evaluation-replay-comparison-v1"

var ErrComparisonIncomparable = errors.New("evaluation replay snapshots are incomparable")

// ComparisonStatus distinguishes a real apples-to-apples comparison from a
// fail-closed boundary result. INCOMPARABLE results intentionally contain no
// deltas or transitions.
type ComparisonStatus string

const (
	ComparisonComparable   ComparisonStatus = "COMPARABLE"
	ComparisonIncomparable ComparisonStatus = "INCOMPARABLE"
)

// IncompatibilityCode is closed so an untrusted snapshot cannot turn the
// comparison output into a free-form diagnostic channel.
type IncompatibilityCode string

const (
	IncompatibleDatasetSchema      IncompatibilityCode = "DATASET_SCHEMA_MISMATCH"
	IncompatibleDatasetID          IncompatibilityCode = "DATASET_ID_MISMATCH"
	IncompatibleDatasetFingerprint IncompatibilityCode = "DATASET_FINGERPRINT_MISMATCH"
	IncompatibleDataBoundary       IncompatibilityCode = "DATA_BOUNDARY_MISMATCH"
	IncompatibleExecutorProfile    IncompatibilityCode = "EXECUTOR_PROFILE_MISMATCH"
	IncompatibleCaseSet            IncompatibilityCode = "CASE_SET_MISMATCH"
)

type MetricCategory string

const (
	MetricCategoryQuality MetricCategory = "QUALITY"
	MetricCategoryCost    MetricCategory = "COST"
	MetricCategoryTool    MetricCategory = "TOOL"
	MetricCategoryTrace   MetricCategory = "TRACE"
	MetricCategoryLatency MetricCategory = "LATENCY_OBSERVATION"
)

type MetricDirection string

const (
	MetricHigherIsBetter MetricDirection = "HIGHER_IS_BETTER"
	MetricLowerIsBetter  MetricDirection = "LOWER_IS_BETTER"
	MetricObservational  MetricDirection = "OBSERVATIONAL"
)

type GateState string

const (
	GateAbsent GateState = "ABSENT"
	GatePassed GateState = "PASSED"
	GateFailed GateState = "FAILED"
)

type VersionChange struct {
	Field     string `json:"field"`
	Base      string `json:"base"`
	Candidate string `json:"candidate"`
}

type StatusChange struct {
	Base      string `json:"base"`
	Candidate string `json:"candidate"`
	Changed   bool   `json:"changed"`
}

type GateTransition struct {
	Code      string    `json:"code"`
	Base      GateState `json:"base"`
	Candidate GateState `json:"candidate"`
	Regressed bool      `json:"regressed"`
	Recovered bool      `json:"recovered"`
}

type CaseTransitions struct {
	NewlyFailed []string `json:"newly_failed"`
	Recovered   []string `json:"recovered"`
	StillFailed []string `json:"still_failed"`
}

type MetricDelta struct {
	Code      string          `json:"code"`
	Category  MetricCategory  `json:"category"`
	Direction MetricDirection `json:"direction"`
	Base      float64         `json:"base"`
	Candidate float64         `json:"candidate"`
	Delta     float64         `json:"delta"`
	Regressed bool            `json:"regressed"`
	Improved  bool            `json:"improved"`
}

type IntegerDelta struct {
	Base      int64 `json:"base"`
	Candidate int64 `json:"candidate"`
	Delta     int64 `json:"delta"`
}

type ToolDelta struct {
	Name            domain.AgentSpanName `json:"name"`
	TerminalSpans   IntegerDelta         `json:"terminal_spans"`
	FailedSpans     IntegerDelta         `json:"failed_spans"`
	SkippedSpans    IntegerDelta         `json:"skipped_spans"`
	IncompleteSpans IntegerDelta         `json:"incomplete_spans"`
	LogicalCalls    IntegerDelta         `json:"logical_calls"`
	ProviderCalls   IntegerDelta         `json:"provider_calls"`
	ProcessedBytes  IntegerDelta         `json:"processed_bytes"`
	Regressed       bool                 `json:"regressed"`
}

type FailureCodeDelta struct {
	Code      domain.AgentFailureCode `json:"code"`
	Base      int64                   `json:"base"`
	Candidate int64                   `json:"candidate"`
	Delta     int64                   `json:"delta"`
}

// Comparison is a bounded, deterministic projection of two immutable replay
// snapshots. It never embeds reports, traces, free-form errors, or provider
// data. Regressions contains only fixed metric/status codes and validated gate
// identifiers.
type Comparison struct {
	SchemaVersion          string                `json:"schema_version"`
	Status                 ComparisonStatus      `json:"status"`
	Base                   SourceReference       `json:"base"`
	Candidate              SourceReference       `json:"candidate"`
	IncompatibilityCodes   []IncompatibilityCode `json:"incompatibility_codes,omitempty"`
	VersionChanges         []VersionChange       `json:"version_changes,omitempty"`
	EvaluationStatus       *StatusChange         `json:"evaluation_status,omitempty"`
	SnapshotFailureCode    *StatusChange         `json:"snapshot_failure_code,omitempty"`
	GateTransitions        []GateTransition      `json:"gate_transitions,omitempty"`
	CaseTransitions        *CaseTransitions      `json:"case_transitions,omitempty"`
	MetricDeltas           []MetricDelta         `json:"metric_deltas,omitempty"`
	ToolDeltas             []ToolDelta           `json:"tool_deltas,omitempty"`
	AgentFailureCodeDeltas []FailureCodeDelta    `json:"agent_failure_code_deltas,omitempty"`
	Regressions            []string              `json:"regressions,omitempty"`
}

type metricSpec struct {
	code      string
	category  MetricCategory
	direction MetricDirection
	value     func(evaluation.Metrics) float64
}

var comparisonMetrics = []metricSpec{
	{"case_pass_rate", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.CasePassRate }},
	{"outcome_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.OutcomeAccuracy }},
	{"finding_exact_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.FindingExactAccuracy }},
	{"recommendation_exact_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.RecommendationExactAccuracy }},
	{"production_output_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.ProductionOutputAccuracy }},
	{"evidence_contract_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.EvidenceContractAccuracy }},
	{"query_contract_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.QueryContractAccuracy }},
	{"trace_contract_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.TraceContractAccuracy }},
	{"misleading_rate", MetricCategoryQuality, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return m.MisleadingRate }},
	{"conclusive_recall", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.ConclusiveRecall }},
	{"evidence_coverage", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.EvidenceCoverage }},
	{"cause_exact_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.CauseExactAccuracy }},
	{"cause_verdict_accuracy", MetricCategoryQuality, MetricHigherIsBetter, func(m evaluation.Metrics) float64 { return m.CauseVerdictAccuracy }},
	{"execution_failures", MetricCategoryQuality, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.ExecutionFailures) }},
	{"logical_sls_calls", MetricCategoryTool, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.LogicalSLSCalls) }},
	{"provider_api_calls", MetricCategoryTool, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.ProviderAPICalls) }},
	{"change_source_calls", MetricCategoryTool, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.ChangeSourceCalls) }},
	{"call_budget_breaches", MetricCategoryTool, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.CallBudgetBreaches) }},
	{"processed_bytes", MetricCategoryCost, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.ProcessedBytes) }},
	{"cost_budget_breaches", MetricCategoryCost, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.CostBudgetBreaches) }},
	{"trace_events", MetricCategoryTrace, MetricObservational, func(m evaluation.Metrics) float64 { return float64(m.TraceEvents) }},
	{"trace_tool_spans", MetricCategoryTrace, MetricObservational, func(m evaluation.Metrics) float64 { return float64(m.TraceToolSpans) }},
	{"trace_dropped_events", MetricCategoryTrace, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.TraceDroppedEvents) }},
	{"trace_contract_failures", MetricCategoryTrace, MetricLowerIsBetter, func(m evaluation.Metrics) float64 { return float64(m.TraceContractFailures) }},
	{"latency_p50_milliseconds", MetricCategoryLatency, MetricObservational, func(m evaluation.Metrics) float64 { return float64(m.LatencyP50Milliseconds) }},
	{"latency_p95_milliseconds", MetricCategoryLatency, MetricObservational, func(m evaluation.Metrics) float64 { return float64(m.LatencyP95Milliseconds) }},
	{"latency_max_milliseconds", MetricCategoryLatency, MetricObservational, func(m evaluation.Metrics) float64 { return float64(m.LatencyMaxMilliseconds) }},
}

// Compare validates both snapshots, checks the explicit apples-to-apples
// boundary, and returns a deterministic comparison. Incompatible is a normal
// result rather than an error; malformed comparison-only structures still fail
// closed with an error.
func Compare(base, candidate Snapshot) (Comparison, error) {
	if err := base.Validate(); err != nil {
		return Comparison{}, fmt.Errorf("validate base evaluation replay snapshot: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return Comparison{}, fmt.Errorf("validate candidate evaluation replay snapshot: %w", err)
	}
	result := Comparison{
		SchemaVersion: ComparisonSchemaVersion,
		Status:        ComparisonComparable,
		Base:          base.Reference(),
		Candidate:     candidate.Reference(),
	}
	baseGates, err := gateStates(base.Report.Gates)
	if err != nil {
		return Comparison{}, fmt.Errorf("validate base evaluation gates: %w", err)
	}
	candidateGates, err := gateStates(candidate.Report.Gates)
	if err != nil {
		return Comparison{}, fmt.Errorf("validate candidate evaluation gates: %w", err)
	}
	if err := validateComparisonMetrics(base.Report.Metrics); err != nil {
		return Comparison{}, fmt.Errorf("validate base evaluation metrics: %w", err)
	}
	if err := validateComparisonMetrics(candidate.Report.Metrics); err != nil {
		return Comparison{}, fmt.Errorf("validate candidate evaluation metrics: %w", err)
	}
	incompatibilities := compatibilityCodes(base.Report, candidate.Report)
	if len(incompatibilities) > 0 {
		result.Status = ComparisonIncomparable
		result.IncompatibilityCodes = incompatibilities
		return result, nil
	}

	result.VersionChanges = compareVersions(base.Report.VersionManifest, candidate.Report.VersionManifest)
	result.EvaluationStatus = statusChange(string(base.Report.Status), string(candidate.Report.Status))
	result.SnapshotFailureCode = statusChange(string(base.FailureCode), string(candidate.FailureCode))
	result.GateTransitions = compareGates(baseGates, candidateGates)
	result.CaseTransitions = compareCases(base.Report.Cases, candidate.Report.Cases)
	result.MetricDeltas = compareMetrics(base.Report.Metrics, candidate.Report.Metrics)
	result.ToolDeltas, err = compareTools(base.Report.Cases, candidate.Report.Cases)
	if err != nil {
		return Comparison{}, fmt.Errorf("compare evaluation tool usage: %w", err)
	}
	result.AgentFailureCodeDeltas = compareFailureCodes(base.Report.Cases, candidate.Report.Cases)
	result.Regressions = collectRegressions(result)
	return result, nil
}

func compatibilityCodes(base, candidate evaluation.EvaluationReport) []IncompatibilityCode {
	codes := make([]IncompatibilityCode, 0, 6)
	if base.DatasetVersion != candidate.DatasetVersion || base.VersionManifest.DatasetSchemaVersion != candidate.VersionManifest.DatasetSchemaVersion {
		codes = append(codes, IncompatibleDatasetSchema)
	}
	if base.DatasetID != candidate.DatasetID || base.VersionManifest.DatasetID != candidate.VersionManifest.DatasetID {
		codes = append(codes, IncompatibleDatasetID)
	}
	if base.DatasetFingerprint != candidate.DatasetFingerprint || base.VersionManifest.DatasetFingerprint != candidate.VersionManifest.DatasetFingerprint {
		codes = append(codes, IncompatibleDatasetFingerprint)
	}
	if !reflect.DeepEqual(base.DataBoundary, candidate.DataBoundary) {
		codes = append(codes, IncompatibleDataBoundary)
	}
	if base.VersionManifest.ExecutorProfile != candidate.VersionManifest.ExecutorProfile {
		codes = append(codes, IncompatibleExecutorProfile)
	}
	if !equalCaseSet(base.Cases, candidate.Cases) {
		codes = append(codes, IncompatibleCaseSet)
	}
	return codes
}

func equalCaseSet(base, candidate []evaluation.CaseResult) bool {
	if len(base) != len(candidate) {
		return false
	}
	baseIDs := make([]string, len(base))
	candidateIDs := make([]string, len(candidate))
	for index := range base {
		baseIDs[index] = base[index].ID
	}
	for index := range candidate {
		candidateIDs[index] = candidate[index].ID
	}
	sort.Strings(baseIDs)
	sort.Strings(candidateIDs)
	return reflect.DeepEqual(baseIDs, candidateIDs)
}

func compareVersions(base, candidate domain.AgentVersionManifest) []VersionChange {
	fields := []struct {
		name      string
		base      string
		candidate string
	}{
		{"graph_version", base.GraphVersion, candidate.GraphVersion},
		{"template_id", base.TemplateID, candidate.TemplateID},
		{"template_version", base.TemplateVersion, candidate.TemplateVersion},
		{"policy_version", base.PolicyVersion, candidate.PolicyVersion},
		{"cause_version", base.CauseVersion, candidate.CauseVersion},
		{"evaluation_version", base.EvaluationVersion, candidate.EvaluationVersion},
		{"prompt_used", strconv.FormatBool(base.PromptUsed), strconv.FormatBool(candidate.PromptUsed)},
		{"prompt_version", base.PromptVersion, candidate.PromptVersion},
		{"prompt_fingerprint", base.PromptFingerprint, candidate.PromptFingerprint},
		{"model_provider", base.ModelProvider, candidate.ModelProvider},
		{"model_name", base.ModelName, candidate.ModelName},
	}
	changes := make([]VersionChange, 0)
	for _, field := range fields {
		if field.base != field.candidate {
			changes = append(changes, VersionChange{Field: field.name, Base: field.base, Candidate: field.candidate})
		}
	}
	return changes
}

func statusChange(base, candidate string) *StatusChange {
	return &StatusChange{Base: base, Candidate: candidate, Changed: base != candidate}
}

func gateStates(gates []evaluation.GateResult) (map[string]GateState, error) {
	states := make(map[string]GateState, len(gates))
	for _, gate := range gates {
		if !validGateCode(gate.Code) {
			return nil, errors.New("evaluation gate code is invalid")
		}
		if _, duplicate := states[gate.Code]; duplicate {
			return nil, errors.New("evaluation gate code is duplicated")
		}
		if gate.Passed {
			states[gate.Code] = GatePassed
		} else {
			states[gate.Code] = GateFailed
		}
	}
	return states, nil
}

func validGateCode(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func compareGates(base, candidate map[string]GateState) []GateTransition {
	codes := make([]string, 0, len(base)+len(candidate))
	seen := make(map[string]struct{}, len(base)+len(candidate))
	for code := range base {
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	for code := range candidate {
		if _, exists := seen[code]; !exists {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	transitions := make([]GateTransition, 0, len(codes))
	for _, code := range codes {
		baseState := base[code]
		if baseState == "" {
			baseState = GateAbsent
		}
		candidateState := candidate[code]
		if candidateState == "" {
			candidateState = GateAbsent
		}
		transitions = append(transitions, GateTransition{
			Code: code, Base: baseState, Candidate: candidateState,
			Regressed: gateRegressed(baseState, candidateState),
			Recovered: baseState == GateFailed && candidateState == GatePassed,
		})
	}
	return transitions
}

func gateRegressed(base, candidate GateState) bool {
	return candidate == GateAbsent && base != GateAbsent ||
		candidate == GateFailed && base != GateFailed
}

func compareCases(base, candidate []evaluation.CaseResult) *CaseTransitions {
	basePassed := make(map[string]bool, len(base))
	for _, result := range base {
		basePassed[result.ID] = result.Passed
	}
	transitions := &CaseTransitions{
		NewlyFailed: make([]string, 0),
		Recovered:   make([]string, 0),
		StillFailed: make([]string, 0),
	}
	for _, result := range candidate {
		switch {
		case basePassed[result.ID] && !result.Passed:
			transitions.NewlyFailed = append(transitions.NewlyFailed, result.ID)
		case !basePassed[result.ID] && result.Passed:
			transitions.Recovered = append(transitions.Recovered, result.ID)
		case !basePassed[result.ID] && !result.Passed:
			transitions.StillFailed = append(transitions.StillFailed, result.ID)
		}
	}
	sort.Strings(transitions.NewlyFailed)
	sort.Strings(transitions.Recovered)
	sort.Strings(transitions.StillFailed)
	return transitions
}

func compareMetrics(base, candidate evaluation.Metrics) []MetricDelta {
	deltas := make([]MetricDelta, 0, len(comparisonMetrics))
	for _, spec := range comparisonMetrics {
		baseValue := spec.value(base)
		candidateValue := spec.value(candidate)
		delta := round6(candidateValue - baseValue)
		regressed := spec.direction == MetricHigherIsBetter && delta < 0 || spec.direction == MetricLowerIsBetter && delta > 0
		improved := spec.direction == MetricHigherIsBetter && delta > 0 || spec.direction == MetricLowerIsBetter && delta < 0
		deltas = append(deltas, MetricDelta{
			Code: spec.code, Category: spec.category, Direction: spec.direction,
			Base: round6(baseValue), Candidate: round6(candidateValue), Delta: delta,
			Regressed: regressed, Improved: improved,
		})
	}
	return deltas
}

func validateComparisonMetrics(metrics evaluation.Metrics) error {
	rateMetrics := map[string]struct{}{
		"case_pass_rate": {}, "outcome_accuracy": {}, "finding_exact_accuracy": {},
		"recommendation_exact_accuracy": {}, "production_output_accuracy": {},
		"evidence_contract_accuracy": {}, "query_contract_accuracy": {},
		"trace_contract_accuracy": {}, "misleading_rate": {}, "conclusive_recall": {},
		"evidence_coverage": {}, "cause_exact_accuracy": {}, "cause_verdict_accuracy": {},
	}
	for _, spec := range comparisonMetrics {
		value := spec.value(metrics)
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("metric %s is negative or non-finite", spec.code)
		}
		if _, isRate := rateMetrics[spec.code]; isRate && value > 1 {
			return fmt.Errorf("metric %s is outside [0,1]", spec.code)
		}
	}
	return nil
}

type toolAggregate struct {
	terminalSpans, failedSpans, skippedSpans, incompleteSpans int64
	logicalCalls, providerCalls                               int64
	processedBytes                                            int64
}

func compareTools(base, candidate []evaluation.CaseResult) ([]ToolDelta, error) {
	baseValues, err := aggregateTools(base)
	if err != nil {
		return nil, err
	}
	candidateValues, err := aggregateTools(candidate)
	if err != nil {
		return nil, err
	}
	names := []domain.AgentSpanName{domain.AgentSpanSLSCurrent, domain.AgentSpanSLSBaseline, domain.AgentSpanChangeSourceList}
	deltas := make([]ToolDelta, 0, len(names))
	for _, name := range names {
		baseValue := baseValues[name]
		candidateValue := candidateValues[name]
		regressed := candidateValue.failedSpans > baseValue.failedSpans || candidateValue.incompleteSpans > baseValue.incompleteSpans || candidateValue.logicalCalls > baseValue.logicalCalls || candidateValue.providerCalls > baseValue.providerCalls || candidateValue.processedBytes > baseValue.processedBytes
		deltas = append(deltas, ToolDelta{
			Name:            name,
			TerminalSpans:   integerDelta(baseValue.terminalSpans, candidateValue.terminalSpans),
			FailedSpans:     integerDelta(baseValue.failedSpans, candidateValue.failedSpans),
			SkippedSpans:    integerDelta(baseValue.skippedSpans, candidateValue.skippedSpans),
			IncompleteSpans: integerDelta(baseValue.incompleteSpans, candidateValue.incompleteSpans),
			LogicalCalls:    integerDelta(baseValue.logicalCalls, candidateValue.logicalCalls),
			ProviderCalls:   integerDelta(baseValue.providerCalls, candidateValue.providerCalls),
			ProcessedBytes:  integerDelta(baseValue.processedBytes, candidateValue.processedBytes),
			Regressed:       regressed,
		})
	}
	return deltas, nil
}

func aggregateTools(cases []evaluation.CaseResult) (map[domain.AgentSpanName]toolAggregate, error) {
	values := make(map[domain.AgentSpanName]toolAggregate)
	for _, result := range cases {
		for _, event := range result.AgentTrace.Events {
			if event.Layer != domain.AgentLayerTool || event.Phase == domain.AgentPhaseStarted {
				continue
			}
			value := values[event.Name]
			value.terminalSpans++
			if event.Phase == domain.AgentPhaseFailed {
				value.failedSpans++
			}
			if event.Phase == domain.AgentPhaseSkipped {
				value.skippedSpans++
			}
			if event.ToolUsage != nil {
				if !event.ToolUsage.Complete {
					value.incompleteSpans++
				}
				if value.logicalCalls > math.MaxInt64-event.ToolUsage.LogicalCalls || value.providerCalls > math.MaxInt64-event.ToolUsage.ProviderCalls || value.processedBytes > math.MaxInt64-event.ToolUsage.ProcessedBytes {
					return nil, errors.New("agent tool usage total overflows int64")
				}
				value.logicalCalls += event.ToolUsage.LogicalCalls
				value.providerCalls += event.ToolUsage.ProviderCalls
				value.processedBytes += event.ToolUsage.ProcessedBytes
			}
			values[event.Name] = value
		}
	}
	return values, nil
}

func compareFailureCodes(base, candidate []evaluation.CaseResult) []FailureCodeDelta {
	baseCounts := failureCodeCounts(base)
	candidateCounts := failureCodeCounts(candidate)
	codes := []domain.AgentFailureCode{
		domain.AgentFailureCodeEngineRunFailed,
		domain.AgentFailureCodeGraphNodeFailed,
		domain.AgentFailureCodeToolFailed,
		domain.AgentFailureCodeContextCancelled,
		domain.AgentFailureCodeDeadlineExceeded,
		domain.AgentFailureCodeContractViolation,
	}
	deltas := make([]FailureCodeDelta, 0, len(codes))
	for _, code := range codes {
		deltas = append(deltas, FailureCodeDelta{Code: code, Base: baseCounts[code], Candidate: candidateCounts[code], Delta: candidateCounts[code] - baseCounts[code]})
	}
	return deltas
}

func failureCodeCounts(cases []evaluation.CaseResult) map[domain.AgentFailureCode]int64 {
	counts := make(map[domain.AgentFailureCode]int64)
	for _, result := range cases {
		for _, event := range result.AgentTrace.Events {
			if event.Phase == domain.AgentPhaseFailed {
				counts[event.FailureCode]++
			}
		}
	}
	return counts
}

func collectRegressions(comparison Comparison) []string {
	regressions := make([]string, 0)
	if comparison.EvaluationStatus.Base == string(evaluation.EvaluationPassed) && comparison.EvaluationStatus.Candidate == string(evaluation.EvaluationFailed) {
		regressions = append(regressions, "evaluation_status")
	}
	if comparison.SnapshotFailureCode.Base == string(FailureNone) && comparison.SnapshotFailureCode.Candidate != string(FailureNone) {
		regressions = append(regressions, "snapshot_failure_code")
	}
	if len(comparison.CaseTransitions.NewlyFailed) > 0 {
		regressions = append(regressions, "newly_failed_cases")
	}
	for _, transition := range comparison.GateTransitions {
		if transition.Regressed {
			regressions = append(regressions, "gate:"+transition.Code)
		}
	}
	for _, delta := range comparison.MetricDeltas {
		if delta.Regressed {
			regressions = append(regressions, "metric:"+delta.Code)
		}
	}
	for _, delta := range comparison.ToolDeltas {
		if delta.Regressed {
			regressions = append(regressions, "tool:"+string(delta.Name))
		}
	}
	for _, delta := range comparison.AgentFailureCodeDeltas {
		if delta.Delta > 0 {
			regressions = append(regressions, "agent_failure_code:"+string(delta.Code))
		}
	}
	sort.Strings(regressions)
	return regressions
}

func integerDelta(base, candidate int64) IntegerDelta {
	return IntegerDelta{Base: base, Candidate: candidate, Delta: candidate - base}
}

func round6(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
