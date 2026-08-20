package replay

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
)

func TestSnapshotRoundTripAndTamperDetection(t *testing.T) {
	report := validReport(t, "evalrun_roundtrip")
	snapshot, err := New(report, nil, nil, time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseStrict(payload)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ContentHash != snapshot.ContentHash || parsed.EvaluationRunID != report.EvaluationRunID || parsed.FailureCode != FailureNone {
		t.Fatalf("unexpected parsed snapshot: %#v", parsed)
	}

	tampered := strings.Replace(string(payload), `"status":"PASSED"`, `"status":"FAILED"`, 1)
	if _, err := ParseStrict([]byte(tampered)); !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("tampered error=%v, want %v", err, ErrSnapshotTampered)
	}
}

func TestSnapshotPreservesFailedGateWithoutRawError(t *testing.T) {
	report := validReport(t, "evalrun_failed")
	report.Status = evaluation.EvaluationFailed
	report.Gates = []evaluation.GateResult{{Code: "case_pass_rate", Passed: false, Actual: "0", Expected: "= 1"}}
	snapshot, err := New(report, &evaluation.GateError{Failed: []string{"case_pass_rate"}}, nil, time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FailureCode != FailureGate {
		t.Fatalf("failure code=%q, want %q", snapshot.FailureCode, FailureGate)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "evaluation gate failed") {
		t.Fatal("snapshot persisted a raw execution error")
	}
}

func TestSnapshotRejectsUnknownFieldsAndIncompatibleDataset(t *testing.T) {
	report := validReport(t, "evalrun_strict")
	snapshot, err := New(report, nil, nil, time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(payload), "{", `{"secret":"forbidden",`, 1)
	if _, err := ParseStrict([]byte(withUnknown)); err == nil {
		t.Fatal("unknown snapshot field was accepted")
	}

	dataset := evaluation.Dataset{
		SchemaVersion: evaluation.DatasetSchemaVersion,
		DatasetID:     "different-dataset",
		DataSource:    evaluation.SyntheticDataSource,
		Fingerprint:   strings.Repeat("c", 64),
	}
	if err := ValidateSourceCompatibility(snapshot, dataset); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("compatibility error=%v, want %v", err, ErrIncompatible)
	}
}

func TestSnapshotRejectsInvalidReplayReference(t *testing.T) {
	report := validReport(t, "evalrun_child")
	_, err := New(report, nil, &SourceReference{EvaluationRunID: report.EvaluationRunID, ContentHash: strings.Repeat("a", 64)}, time.Now().UTC())
	if err == nil {
		t.Fatal("self-referencing replay snapshot was accepted")
	}
}

func TestSnapshotRejectsRedundantVersionDrift(t *testing.T) {
	report := validReport(t, "evalrun_version_drift")
	report.Versions.GraphVersion = "different-graph"
	if _, err := New(report, nil, nil, time.Now().UTC()); err == nil {
		t.Fatal("snapshot accepted inconsistent version metadata")
	}
}

func validReport(t *testing.T, evaluationRunID string) evaluation.EvaluationReport {
	t.Helper()
	datasetHash := strings.Repeat("a", 64)
	manifest := domain.AgentVersionManifest{
		DatasetSchemaVersion: evaluation.DatasetSchemaVersion,
		DatasetID:            evaluation.SyntheticDatasetID,
		DatasetFingerprint:   datasetHash,
		GraphVersion:         "graph-v1",
		TemplateID:           domain.ErrorAnalysisTemplateID,
		TemplateVersion:      domain.ErrorAnalysisTemplateVersion,
		PolicyVersion:        "policy-v1",
		CauseVersion:         domain.CauseConfidenceMethod,
		EvaluationVersion:    evaluation.GatePolicyVersion,
		TraceSchemaVersion:   domain.AgentTraceSchemaVersion,
		ReplaySchemaVersion:  domain.ReplaySchemaVersion,
		ExecutorProfile:      evaluation.SyntheticMockExecutorProfile,
	}
	versionHash, err := manifest.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 8, 20, 7, 0, 0, 0, time.UTC)
	trace := domain.AgentTrace{
		SchemaVersion: domain.AgentTraceSchemaVersion, EvaluationRunID: evaluationRunID,
		TraceID: "trace_test", RunID: "run_test", CaseID: "case_test", VersionFingerprint: versionHash, Complete: true,
		Events: []domain.AgentEvent{
			{
				SchemaVersion: domain.AgentTraceSchemaVersion, EventID: strings.Repeat("b", 64), Sequence: 1,
				EvaluationRunID: evaluationRunID, TraceID: "trace_test", RunID: "run_test", CaseID: "case_test", VersionFingerprint: versionHash,
				SpanID: "engine", Layer: domain.AgentLayerRun, Name: domain.AgentSpanEngineRun, Phase: domain.AgentPhaseStarted, OccurredAt: startedAt,
			},
			{
				SchemaVersion: domain.AgentTraceSchemaVersion, EventID: strings.Repeat("c", 64), Sequence: 2,
				EvaluationRunID: evaluationRunID, TraceID: "trace_test", RunID: "run_test", CaseID: "case_test", VersionFingerprint: versionHash,
				SpanID: "engine", Layer: domain.AgentLayerRun, Name: domain.AgentSpanEngineRun, Phase: domain.AgentPhaseSucceeded, OccurredAt: startedAt.Add(time.Millisecond), DurationMilliseconds: 1,
			},
		},
	}
	return evaluation.EvaluationReport{
		EvaluationRunID: evaluationRunID, EvaluationVersion: evaluation.GatePolicyVersion,
		DatasetID: evaluation.SyntheticDatasetID, DatasetVersion: evaluation.DatasetSchemaVersion, DatasetFingerprint: datasetHash,
		Versions: evaluation.VersionInfo{
			GraphVersion: "graph-v1", QueryTemplateID: domain.ErrorAnalysisTemplateID,
			QueryTemplateVersion: domain.ErrorAnalysisTemplateVersion, QueryPolicyVersion: "policy-v1",
			CauseMethod: domain.CauseConfidenceMethod, ExecutorProfile: evaluation.SyntheticMockExecutorProfile,
		},
		VersionManifest: manifest, VersionFingerprint: versionHash,
		DataBoundary: evaluation.DataBoundary{DataSource: evaluation.SyntheticDataSource},
		Policy:       evaluation.GatePolicy{Version: evaluation.GatePolicyVersion},
		Status:       evaluation.EvaluationPassed,
		Cases:        []evaluation.CaseResult{{ID: "case_test", Passed: true, AgentTrace: trace}},
	}
}
