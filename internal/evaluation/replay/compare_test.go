package replay

import (
	"math"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
)

func TestCompareReportsComparableRegressionsAndVersionChanges(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base", true)
	candidateReport := comparisonReport(t, "evalrun_candidate", false)
	setGraphVersion(t, &candidateReport, "graph-v2")
	candidateReport.Metrics.CasePassRate = 0
	candidateReport.Metrics.ProviderAPICalls = 44
	candidateReport.Metrics.ProcessedBytes = 80000
	candidateReport.Metrics.TraceDroppedEvents = 1
	candidateReport.Metrics.LatencyP95Milliseconds = 25
	candidateReport.Status = evaluation.EvaluationFailed
	candidateReport.Gates[0].Passed = false

	base := mustSnapshot(t, baseReport, nil)
	candidate := mustSnapshot(t, candidateReport, &evaluation.GateError{Failed: []string{"case_pass_rate"}})
	comparison, err := Compare(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Status != ComparisonComparable || len(comparison.IncompatibilityCodes) != 0 {
		t.Fatalf("unexpected comparison boundary: %#v", comparison)
	}
	if len(comparison.VersionChanges) != 1 || comparison.VersionChanges[0].Field != "graph_version" {
		t.Fatalf("version changes=%#v", comparison.VersionChanges)
	}
	if comparison.EvaluationStatus == nil || !comparison.EvaluationStatus.Changed || comparison.SnapshotFailureCode == nil || !comparison.SnapshotFailureCode.Changed {
		t.Fatalf("status changes were not reported: %#v", comparison)
	}
	if comparison.CaseTransitions == nil || len(comparison.CaseTransitions.NewlyFailed) != 1 || comparison.CaseTransitions.NewlyFailed[0] != "case_test" {
		t.Fatalf("case transitions=%#v", comparison.CaseTransitions)
	}
	if !containsString(comparison.Regressions, "metric:case_pass_rate") || !containsString(comparison.Regressions, "metric:provider_api_calls") || !containsString(comparison.Regressions, "metric:processed_bytes") || !containsString(comparison.Regressions, "metric:trace_dropped_events") || !containsString(comparison.Regressions, "newly_failed_cases") {
		t.Fatalf("regressions=%#v", comparison.Regressions)
	}
	if metricByCode(t, comparison.MetricDeltas, "latency_p95_milliseconds").Regressed {
		t.Fatal("observational latency was treated as a production regression")
	}
	if len(comparison.GateTransitions) != 1 || !comparison.GateTransitions[0].Regressed {
		t.Fatalf("gate transitions=%#v", comparison.GateTransitions)
	}
}

func TestCompareReportsRecoveredAndStableToolUsage(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_failed", false)
	baseReport.Status = evaluation.EvaluationFailed
	baseReport.Gates[0].Passed = false
	candidateReport := comparisonReport(t, "evalrun_candidate_recovered", true)

	base := mustSnapshot(t, baseReport, &evaluation.GateError{Failed: []string{"case_pass_rate"}})
	candidate := mustSnapshot(t, candidateReport, nil)
	comparison, err := Compare(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.CaseTransitions == nil || len(comparison.CaseTransitions.Recovered) != 1 || comparison.CaseTransitions.Recovered[0] != "case_test" {
		t.Fatalf("recovered cases=%#v", comparison.CaseTransitions)
	}
	if len(comparison.GateTransitions) != 1 || !comparison.GateTransitions[0].Recovered {
		t.Fatalf("gate transitions=%#v", comparison.GateTransitions)
	}
	if len(comparison.ToolDeltas) != 3 || len(comparison.AgentFailureCodeDeltas) != 6 {
		t.Fatalf("fixed tool/failure projections drifted: tools=%d failures=%d", len(comparison.ToolDeltas), len(comparison.AgentFailureCodeDeltas))
	}
}

func TestCompareReturnsDeltaFreeIncomparableResult(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_boundary", true)
	candidateReport := comparisonReport(t, "evalrun_candidate_boundary", true)
	candidateReport.DatasetFingerprint = strings.Repeat("d", 64)
	candidateReport.VersionManifest.DatasetFingerprint = candidateReport.DatasetFingerprint
	setManifestFingerprint(t, &candidateReport)
	candidateReport.DataBoundary.ExpertLabelCount = 1

	comparison, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil))
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Status != ComparisonIncomparable || !containsIncompatibility(comparison.IncompatibilityCodes, IncompatibleDatasetFingerprint) || !containsIncompatibility(comparison.IncompatibilityCodes, IncompatibleDataBoundary) {
		t.Fatalf("unexpected incomparable result: %#v", comparison)
	}
	if len(comparison.VersionChanges) != 0 || comparison.EvaluationStatus != nil || comparison.SnapshotFailureCode != nil || len(comparison.GateTransitions) != 0 || comparison.CaseTransitions != nil || len(comparison.MetricDeltas) != 0 || len(comparison.ToolDeltas) != 0 || len(comparison.AgentFailureCodeDeltas) != 0 || len(comparison.Regressions) != 0 {
		t.Fatalf("incomparable result leaked pseudo-deltas: %#v", comparison)
	}
}

func TestCompareRejectsMalformedProjectionBeforeReturningIncomparable(t *testing.T) {
	tests := []struct {
		name string
		edit func(*evaluation.EvaluationReport)
	}{
		{
			name: "out-of-range metric",
			edit: func(report *evaluation.EvaluationReport) {
				report.Metrics.CasePassRate = 1.1
			},
		},
		{
			name: "duplicate gate",
			edit: func(report *evaluation.EvaluationReport) {
				report.Gates = append(report.Gates, report.Gates[0])
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseReport := comparisonReport(t, "evalrun_base_malformed", true)
			candidateReport := comparisonReport(t, "evalrun_candidate_malformed", true)
			candidateReport.DataBoundary.ExpertLabelCount = 1
			test.edit(&candidateReport)
			if _, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil)); err == nil {
				t.Fatal("comparison returned INCOMPARABLE before validating malformed projection")
			}
		})
	}
}

func TestCompareRejectsInvalidOrDuplicateGateCodes(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_gate", true)
	baseReport.Gates = append(baseReport.Gates, evaluation.GateResult{Code: "case_pass_rate", Passed: true})
	base := mustSnapshot(t, baseReport, nil)
	candidate := mustSnapshot(t, comparisonReport(t, "evalrun_candidate_gate", true), nil)
	if _, err := Compare(base, candidate); err == nil {
		t.Fatal("comparison accepted duplicate gate codes")
	}
}

func TestCompareRejectsInvalidComparableMetrics(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_metrics", true)
	candidateReport := comparisonReport(t, "evalrun_candidate_metrics", true)
	candidateReport.Metrics.CasePassRate = 1.1
	if _, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil)); err == nil {
		t.Fatal("comparison accepted an out-of-range quality metric")
	}
}

func TestCompareVersionsExcludesStrictReaderSchemaVersions(t *testing.T) {
	base := domain.AgentVersionManifest{
		GraphVersion:        "graph-v1",
		TraceSchemaVersion:  "agent-trace-v1",
		ReplaySchemaVersion: "evaluation-replay-v1",
	}
	candidate := base
	candidate.GraphVersion = "graph-v2"
	candidate.TraceSchemaVersion = "agent-trace-v2"
	candidate.ReplaySchemaVersion = "evaluation-replay-v2"

	changes := compareVersions(base, candidate)
	if len(changes) != 1 || changes[0].Field != "graph_version" {
		t.Fatalf("strict-reader schema versions leaked into comparable version changes: %#v", changes)
	}
}

func TestCompareGateTransitionsTreatRemovalAsRegression(t *testing.T) {
	tests := []struct {
		name      string
		base      GateState
		candidate GateState
		regressed bool
		recovered bool
	}{
		{name: "passed removed", base: GatePassed, candidate: GateAbsent, regressed: true},
		{name: "failed removed", base: GateFailed, candidate: GateAbsent, regressed: true},
		{name: "new failed", base: GateAbsent, candidate: GateFailed, regressed: true},
		{name: "passed failed", base: GatePassed, candidate: GateFailed, regressed: true},
		{name: "failed passed", base: GateFailed, candidate: GatePassed, recovered: true},
		{name: "new passed", base: GateAbsent, candidate: GatePassed},
		{name: "failed remains failed", base: GateFailed, candidate: GateFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := map[string]GateState{}
			candidate := map[string]GateState{}
			if test.base != GateAbsent {
				base["quality_gate"] = test.base
			}
			if test.candidate != GateAbsent {
				candidate["quality_gate"] = test.candidate
			}
			transitions := compareGates(base, candidate)
			if len(transitions) != 1 {
				t.Fatalf("transitions=%#v", transitions)
			}
			if transitions[0].Regressed != test.regressed || transitions[0].Recovered != test.recovered {
				t.Fatalf("transition=%#v", transitions[0])
			}
		})
	}
}

func TestCompareReportsRemovedGateInRegressionSummary(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_gate_removed", true)
	candidateReport := comparisonReport(t, "evalrun_candidate_gate_removed", true)
	candidateReport.Gates = nil

	comparison, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.GateTransitions) != 1 || !comparison.GateTransitions[0].Regressed {
		t.Fatalf("gate transitions=%#v", comparison.GateTransitions)
	}
	if !containsString(comparison.Regressions, "gate:case_pass_rate") {
		t.Fatalf("regressions=%#v", comparison.Regressions)
	}
}

func TestCompareDetectsCaseSetMismatch(t *testing.T) {
	baseReport := comparisonReport(t, "evalrun_base_cases", true)
	candidateReport := comparisonReport(t, "evalrun_candidate_cases", true)
	candidateReport.Cases[0].ID = "case_other"
	candidateReport.Cases[0].AgentTrace.CaseID = "case_other"
	for index := range candidateReport.Cases[0].AgentTrace.Events {
		candidateReport.Cases[0].AgentTrace.Events[index].CaseID = "case_other"
	}
	comparison, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil))
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Status != ComparisonIncomparable || !containsIncompatibility(comparison.IncompatibilityCodes, IncompatibleCaseSet) {
		t.Fatalf("case mismatch result=%#v", comparison)
	}
}

func TestCompareCompatibilityCodesCoverEveryFrozenBoundary(t *testing.T) {
	tests := []struct {
		name string
		code IncompatibilityCode
		edit func(*evaluation.EvaluationReport)
	}{
		{name: "dataset schema", code: IncompatibleDatasetSchema, edit: func(report *evaluation.EvaluationReport) {
			report.DatasetVersion = "evaluation-dataset-v2"
			report.VersionManifest.DatasetSchemaVersion = report.DatasetVersion
		}},
		{name: "dataset ID", code: IncompatibleDatasetID, edit: func(report *evaluation.EvaluationReport) {
			report.DatasetID = "synthetic-other"
			report.VersionManifest.DatasetID = report.DatasetID
		}},
		{name: "executor profile", code: IncompatibleExecutorProfile, edit: func(report *evaluation.EvaluationReport) {
			report.Versions.ExecutorProfile = "SYNTHETIC_MOCK_V2"
			report.VersionManifest.ExecutorProfile = report.Versions.ExecutorProfile
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseReport := comparisonReport(t, "evalrun_boundary_base", true)
			candidateReport := comparisonReport(t, "evalrun_boundary_candidate", true)
			test.edit(&candidateReport)
			setManifestFingerprint(t, &candidateReport)
			comparison, err := Compare(mustSnapshot(t, baseReport, nil), mustSnapshot(t, candidateReport, nil))
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Status != ComparisonIncomparable || !containsIncompatibility(comparison.IncompatibilityCodes, test.code) {
				t.Fatalf("comparison=%#v", comparison)
			}
		})
	}
}

func TestToolAndAgentFailureAggregatesUseClosedTerminalEvents(t *testing.T) {
	candidateCases := []evaluation.CaseResult{
		{AgentTrace: domain.AgentTrace{Events: []domain.AgentEvent{
			{
				Layer: domain.AgentLayerTool, Name: domain.AgentSpanSLSCurrent, Phase: domain.AgentPhaseFailed,
				FailureCode: domain.AgentFailureCodeToolFailed,
				ToolUsage:   &domain.ToolUsage{LogicalCalls: 1, ProviderCalls: 4, ProcessedBytes: 1024},
			},
		}}},
	}
	tools, err := compareTools(nil, candidateCases)
	if err != nil {
		t.Fatal(err)
	}
	if tools[0].Name != domain.AgentSpanSLSCurrent || tools[0].TerminalSpans.Delta != 1 || tools[0].FailedSpans.Delta != 1 || tools[0].IncompleteSpans.Delta != 1 || tools[0].LogicalCalls.Delta != 1 || tools[0].ProviderCalls.Delta != 4 || tools[0].ProcessedBytes.Delta != 1024 || !tools[0].Regressed {
		t.Fatalf("tool deltas=%#v", tools)
	}
	failures := compareFailureCodes(nil, candidateCases)
	for _, delta := range failures {
		if delta.Code == domain.AgentFailureCodeToolFailed && delta.Delta == 1 {
			return
		}
	}
	t.Fatalf("failure-code deltas=%#v", failures)
}

func TestToolAggregationFailsClosedOnCounterOverflow(t *testing.T) {
	terminal := domain.AgentEvent{
		Layer: domain.AgentLayerTool, Name: domain.AgentSpanSLSCurrent, Phase: domain.AgentPhaseSucceeded,
		ToolUsage: &domain.ToolUsage{ProcessedBytes: math.MaxInt64, Complete: true},
	}
	cases := []evaluation.CaseResult{{AgentTrace: domain.AgentTrace{Events: []domain.AgentEvent{terminal, terminal}}}}
	if _, err := compareTools(nil, cases); err == nil {
		t.Fatal("tool aggregation accepted an overflowing usage total")
	}
}

func comparisonReport(t *testing.T, runID string, passed bool) evaluation.EvaluationReport {
	t.Helper()
	report := validReport(t, runID)
	report.Gates = []evaluation.GateResult{{Code: "case_pass_rate", Passed: passed, Actual: "1", Expected: "= 1"}}
	report.Cases[0].Passed = passed
	report.Metrics = evaluation.Metrics{
		TotalCases: 1, ExecutedCases: 1, PassedCases: 1,
		CasePassRate: 1, OutcomeAccuracy: 1, FindingExactAccuracy: 1, RecommendationExactAccuracy: 1,
		ProductionOutputAccuracy: 1, EvidenceContractAccuracy: 1, QueryContractAccuracy: 1,
		TraceContractAccuracy: 1, ConclusiveRecall: 1, EvidenceCoverage: 1,
		CauseExactAccuracy: 1, CauseVerdictAccuracy: 1,
		LogicalSLSCalls: 2, ProviderAPICalls: 40, ChangeSourceCalls: 1, ProcessedBytes: 78080,
		TraceEvents: 2, LatencyP50Milliseconds: 5, LatencyP95Milliseconds: 10, LatencyMaxMilliseconds: 12,
	}
	if !passed {
		report.Metrics.PassedCases = 0
		report.Metrics.CasePassRate = 0
	}
	return report
}

func mustSnapshot(t *testing.T, report evaluation.EvaluationReport, runErr error) Snapshot {
	t.Helper()
	snapshot, err := New(report, runErr, nil, time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func setGraphVersion(t *testing.T, report *evaluation.EvaluationReport, version string) {
	t.Helper()
	report.Versions.GraphVersion = version
	report.VersionManifest.GraphVersion = version
	setManifestFingerprint(t, report)
}

func setManifestFingerprint(t *testing.T, report *evaluation.EvaluationReport) {
	t.Helper()
	fingerprint, err := report.VersionManifest.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	report.VersionFingerprint = fingerprint
	for caseIndex := range report.Cases {
		report.Cases[caseIndex].AgentTrace.VersionFingerprint = fingerprint
		for eventIndex := range report.Cases[caseIndex].AgentTrace.Events {
			report.Cases[caseIndex].AgentTrace.Events[eventIndex].VersionFingerprint = fingerprint
		}
	}
}

func metricByCode(t *testing.T, deltas []MetricDelta, code string) MetricDelta {
	t.Helper()
	for _, delta := range deltas {
		if delta.Code == code {
			return delta
		}
	}
	t.Fatalf("metric %q was not reported", code)
	return MetricDelta{}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsIncompatibility(values []IncompatibilityCode, target IncompatibilityCode) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
