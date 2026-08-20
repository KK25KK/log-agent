package main

import (
	"context"
	"testing"
	"time"

	"logagent/internal/adapters/feedbackfs"
	"logagent/internal/adapters/replayfs"
	"logagent/internal/evaluation/rollout"
	"logagent/internal/testsupport/evaluationsnapshot"
)

func TestSyntheticFeedbackAndRolloutRehearsalCLIApplicationFlow(t *testing.T) {
	ctx := context.Background()
	snapshotStore, err := replayfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	feedbackStore, err := feedbackfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base, err := evaluationsnapshot.Passed(ctx, time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := evaluationsnapshot.Passed(ctx, time.Date(2026, 8, 20, 11, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshotStore.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := snapshotStore.Append(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	summary, err := executeSyntheticFeedbackSeed(ctx, snapshotStore, feedbackStore, candidate.EvaluationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RecordCount != 10 || summary.ActiveCount != 10 {
		t.Fatalf("unexpected seeded feedback summary: %#v", summary)
	}
	repeated, err := executeSyntheticFeedbackSeed(ctx, snapshotStore, feedbackStore, candidate.EvaluationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.RecordCount != summary.RecordCount || repeated.ActiveCount != summary.ActiveCount {
		t.Fatalf("feedback seeding was not idempotent: first=%#v repeated=%#v", summary, repeated)
	}
	decision, err := executeRolloutRehearsal(
		ctx, snapshotStore, feedbackStore, base.EvaluationRunID, candidate.EvaluationRunID,
		rollout.DefaultPolicy(), rollout.PhasePreflight,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != rollout.StatusPassed || decision.ProductionActionAllowed {
		t.Fatalf("unexpected rehearsal decision: %#v", decision)
	}
}

func TestRolloutCommandOptionsFailClosed(t *testing.T) {
	if _, _, _, err := parseFeedbackSeedOptions([]string{"--snapshot-dir", t.TempDir(), "--feedback-dir", t.TempDir()}); err == nil {
		t.Fatal("feedback-seed accepted a missing run ID")
	}
	if _, _, _, _, _, err := parseRolloutRehearseOptions([]string{
		"--snapshot-dir", t.TempDir(), "--feedback-dir", t.TempDir(), "--base-run-id", "base", "--candidate-run-id", "candidate", "--phase", "live",
	}); err == nil {
		t.Fatal("rollout-rehearse accepted a live phase")
	}
	snapshotDir, feedbackDir, baseID, candidateID, phase, err := parseRolloutRehearseOptions([]string{
		"--snapshot-dir", t.TempDir(), "--feedback-dir", t.TempDir(), "--base-run-id", "base", "--candidate-run-id", "candidate",
	})
	if err != nil || snapshotDir == "" || feedbackDir == "" || baseID != "base" || candidateID != "candidate" || phase != rollout.PhasePreflight {
		t.Fatalf("valid rollout options were rejected: snapshot=%q feedback=%q base=%q candidate=%q phase=%q err=%v", snapshotDir, feedbackDir, baseID, candidateID, phase, err)
	}
}
