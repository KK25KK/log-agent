package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/adapters/replayfs"
	"logagent/internal/evaluation"
	"logagent/internal/evaluation/replay"
)

func TestExecuteSyntheticEvaluationPassesFixedGoldenSet(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := executeSyntheticEvaluation(context.Background(), dataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != evaluation.EvaluationPassed {
		t.Fatalf("evaluation status=%q, want %q", report.Status, evaluation.EvaluationPassed)
	}
	if report.DataBoundary.DataSource != evaluation.SyntheticDataSource || report.DataBoundary.ExternalNetworkCalls != 0 || report.DataBoundary.CredentialsRequired || report.DataBoundary.ProductionClaimAllowed {
		t.Fatalf("synthetic boundary was weakened: %#v", report.DataBoundary)
	}
	if report.Versions.PromptUsed || report.Versions.GraphVersion == "" || report.DatasetFingerprint == "" || report.EvaluationRunID == "" || report.VersionFingerprint == "" {
		t.Fatalf("evaluation versions are incomplete: %#v", report.Versions)
	}
	metrics := report.Metrics
	if metrics.TotalCases != 5 || metrics.PassedCases != 5 || metrics.CasePassRate != 1 || metrics.OutcomeAccuracy != 1 || metrics.FindingExactAccuracy != 1 || metrics.RecommendationExactAccuracy != 1 || metrics.RecommendationExactFailures != 0 || metrics.ProductionOutputAccuracy != 1 || metrics.ProductionOutputFailures != 0 || metrics.QueryContractAccuracy != 1 || metrics.EvidenceContractAccuracy != 1 || metrics.EvidenceContractFailures != 0 {
		t.Fatalf("unexpected core metrics: %#v", metrics)
	}
	if metrics.MisleadingRate != 0 || metrics.ConclusiveRecall != 1 || metrics.EvidenceCoverage != 1 || metrics.CauseExactAccuracy != 1 || metrics.CauseVerdictAccuracy != 1 || metrics.TraceContractAccuracy != 1 || metrics.TraceContractFailures != 0 || metrics.TraceEvents != 76 || metrics.TraceToolSpans != 13 || metrics.TraceDroppedEvents != 0 {
		t.Fatalf("unexpected safety metrics: %#v", metrics)
	}
	if metrics.LogicalSLSCalls != 10 || metrics.ProviderAPICalls != 40 || metrics.ChangeSourceCalls != 3 || metrics.ProcessedBytes != 78080 || metrics.CallBudgetBreaches != 0 || metrics.CostBudgetBreaches != 0 {
		t.Fatalf("unexpected call or cost metrics: %#v", metrics)
	}
	for _, result := range report.Cases {
		if !result.Passed || !result.ProductionOutputValid || !result.EvidenceContractPassed || !result.TraceContractPassed || !result.AgentTrace.Complete {
			t.Fatalf("case %q did not pass: %#v", result.ID, result)
		}
	}
}

func TestExecuteSyntheticEvaluationFailsClosedOnMisleadingFinding(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	// Removing one allowed label keeps the fixture structurally valid but makes
	// the real graph's deterministic finding unexpected.
	originalFingerprint := dataset.Fingerprint
	dataset.Cases[0].Expected.ConclusiveFindingCodes = []string{
		"error_spike",
		"error_pattern_share",
		"instance_distribution",
	}
	report, err := executeSyntheticEvaluation(context.Background(), dataset)
	if !errors.Is(err, evaluation.ErrGateFailed) {
		t.Fatalf("error=%v, want evaluation gate failure", err)
	}
	if report.Status != evaluation.EvaluationFailed || report.Metrics.UnexpectedConclusiveFindings != 1 || report.Metrics.MisleadingRate <= 0 {
		t.Fatalf("misleading finding did not fail closed: %#v", report.Metrics)
	}
	if report.DatasetFingerprint == originalFingerprint {
		t.Fatal("mutated evaluation labels retained the original dataset fingerprint")
	}
}

func TestExecuteSyntheticEvaluationUsesUniqueRunIdentityAndStableVersionFingerprint(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeSyntheticEvaluation(context.Background(), dataset)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executeSyntheticEvaluation(context.Background(), dataset)
	if err != nil {
		t.Fatal(err)
	}
	if first.EvaluationRunID == second.EvaluationRunID {
		t.Fatalf("evaluation run identity was reused: %q", first.EvaluationRunID)
	}
	if first.VersionFingerprint == "" || first.VersionFingerprint != second.VersionFingerprint {
		t.Fatalf("version fingerprint drifted across identical runs: first=%q second=%q", first.VersionFingerprint, second.VersionFingerprint)
	}
}

func TestExecuteArchivedEvaluationPersistsRunAndReplayLineage(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	store, err := replayfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC) }
	first, err := executeArchivedEvaluation(context.Background(), dataset, store, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), first.EvaluationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentHash != first.ContentHash || loaded.FailureCode != replay.FailureNone {
		t.Fatalf("unexpected persisted source: %#v", loaded)
	}
	if err := replay.ValidateSourceCompatibility(loaded, dataset); err != nil {
		t.Fatal(err)
	}
	reference := loaded.Reference()
	second, err := executeArchivedEvaluation(context.Background(), dataset, store, &reference, now)
	if err != nil {
		t.Fatal(err)
	}
	if second.EvaluationRunID == first.EvaluationRunID || second.ReplayOf == nil || *second.ReplayOf != reference {
		t.Fatalf("replay lineage is invalid: source=%#v child=%#v", reference, second.ReplayOf)
	}
	if second.Report.DataBoundary.ExternalNetworkCalls != 0 || second.Report.DataBoundary.CredentialsRequired || second.Report.VersionManifest.ExecutorProfile != evaluation.SyntheticMockExecutorProfile {
		t.Fatalf("replay escaped the synthetic boundary: %#v", second.Report.DataBoundary)
	}
}

func TestExecuteArchivedEvaluationPersistsGateFailure(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Cases[0].Expected.ConclusiveFindingCodes = []string{"error_spike"}
	store, err := replayfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := executeArchivedEvaluation(context.Background(), dataset, store, nil, func() time.Time {
		return time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	})
	if !errors.Is(err, evaluation.ErrGateFailed) {
		t.Fatalf("error=%v, want %v", err, evaluation.ErrGateFailed)
	}
	if snapshot.FailureCode != replay.FailureGate || snapshot.Report.Status != evaluation.EvaluationFailed {
		t.Fatalf("failed evaluation was not archived safely: %#v", snapshot)
	}
	if _, err := store.Load(context.Background(), snapshot.EvaluationRunID); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationAndReplayOptionParsingFailsClosed(t *testing.T) {
	if _, err := parseEvaluateOptions([]string{"unexpected"}); err == nil {
		t.Fatal("evaluate accepted a positional argument")
	}
	if _, _, err := parseReplayOptions([]string{"--snapshot-dir", t.TempDir()}); err == nil {
		t.Fatal("replay accepted a missing run ID")
	}
}
