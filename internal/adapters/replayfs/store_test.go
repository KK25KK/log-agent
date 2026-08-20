package replayfs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/evaluation/replay"
)

func TestStoreAppendLoadAndDuplicateRejection(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, "evalrun_store")
	if err := store.Append(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), snapshot.EvaluationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentHash != snapshot.ContentHash || loaded.Report.VersionFingerprint != snapshot.Report.VersionFingerprint {
		t.Fatalf("loaded snapshot drifted: %#v", loaded)
	}
	if err := store.Append(context.Background(), snapshot); !errors.Is(err, replay.ErrDuplicateSnapshot) {
		t.Fatalf("duplicate append error=%v, want %v", err, replay.ErrDuplicateSnapshot)
	}
}

func TestStoreLoadRejectsTamperedAndUnsafeFiles(t *testing.T) {
	directory := t.TempDir()
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t, "evalrun_tampered")
	if err := store.Append(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, snapshot.EvaluationRunID+snapshotFileSuffix)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(payload), `"status": "PASSED"`, `"status": "FAILED"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), snapshot.EvaluationRunID); !errors.Is(err, replay.ErrSnapshotTampered) {
		t.Fatalf("tampered load error=%v, want %v", err, replay.ErrSnapshotTampered)
	}
	if _, err := store.Load(context.Background(), "../outside"); err == nil {
		t.Fatal("unsafe run ID was accepted")
	}
}

func testSnapshot(t *testing.T, evaluationRunID string) replay.Snapshot {
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
	events := []domain.AgentEvent{
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
	}
	report := evaluation.EvaluationReport{
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
		Cases: []evaluation.CaseResult{{
			ID: "case_test", Passed: true,
			AgentTrace: domain.AgentTrace{
				SchemaVersion: domain.AgentTraceSchemaVersion, EvaluationRunID: evaluationRunID,
				TraceID: "trace_test", RunID: "run_test", CaseID: "case_test", VersionFingerprint: versionHash, Complete: true, Events: events,
			},
		}},
	}
	snapshot, err := replay.New(report, nil, nil, time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
