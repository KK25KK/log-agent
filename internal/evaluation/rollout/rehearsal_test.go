package rollout

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/evaluation"
	"logagent/internal/evaluation/feedback"
	"logagent/internal/evaluation/replay"
	"logagent/internal/testsupport/evaluationsnapshot"
)

func TestRehearsalPassesCompatibleRunsWithCompleteAgreeingFeedback(t *testing.T) {
	base, candidate := snapshotPair(t)
	records := safeFeedback(t, candidate)
	decision, err := Rehearse(base, candidate, records, DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusPassed || decision.ProductionActionAllowed || decision.DataSource != feedback.SyntheticDataSource || len(decision.ReasonCodes) != 0 {
		t.Fatalf("unexpected passing rehearsal: %#v", decision)
	}
	if decision.Feedback.TotalCases != 5 || decision.Feedback.CoveredCases != 5 || decision.Feedback.SafeRecords != 10 {
		t.Fatalf("unexpected feedback counts: %#v", decision.Feedback)
	}
}

func TestRehearsalReturnsInsufficientForCoverageQuorumUnsureAndDisagreement(t *testing.T) {
	base, candidate := snapshotPair(t)
	decision, err := Rehearse(base, candidate, nil, DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusInsufficientEvidence || !hasReason(decision, ReasonFeedbackCoverage) || !hasReason(decision, ReasonReviewerQuorum) {
		t.Fatalf("missing feedback did not fail closed: %#v", decision)
	}

	records := safeFeedback(t, candidate)
	parent := records[0]
	correction, err := feedback.NewRecord(candidate, feedback.NewRecordInput{
		CaseID: parent.CaseID, ReviewerRef: parent.ReviewerRef, Verdict: feedback.VerdictUnsure,
		ReasonCode: feedback.ReasonInsufficientContext, Supersedes: parent.FeedbackID, CreatedAt: parent.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, correction)
	decision, err = Rehearse(base, candidate, records, DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusInsufficientEvidence || !hasReason(decision, ReasonReviewerUnsure) || !hasReason(decision, ReasonReviewerDisagreement) {
		t.Fatalf("unsure disagreement did not remain non-actionable: %#v", decision)
	}
}

func TestUnsafeFeedbackBlocksPreflightAndRecommendsOnlySimulatedRollback(t *testing.T) {
	base, candidate := snapshotPair(t)
	records := safeFeedback(t, candidate)
	caseID := records[0].CaseID
	for index := range records {
		if records[index].CaseID != caseID {
			continue
		}
		correction, err := feedback.NewRecord(candidate, feedback.NewRecordInput{
			CaseID: records[index].CaseID, ReviewerRef: records[index].ReviewerRef, Verdict: feedback.VerdictUnsafe,
			ReasonCode: feedback.ReasonMisleadingConclusion, Supersedes: records[index].FeedbackID,
			CreatedAt: records[index].CreatedAt.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, correction)
	}
	preflight, err := Rehearse(base, candidate, records, DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Status != StatusBlocked || !hasReason(preflight, ReasonUnsafeFeedback) {
		t.Fatalf("unsafe preflight was not blocked: %#v", preflight)
	}
	active, err := Rehearse(base, candidate, records, DefaultPolicy(), PhaseSimulatedActivePilot)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != StatusRollbackRecommended || !hasReason(active, ReasonUnsafeFeedback) || active.ProductionActionAllowed {
		t.Fatalf("simulated active pilot did not produce a non-actionable rollback recommendation: %#v", active)
	}
}

func TestCandidateFailureAndGateRegressionBlock(t *testing.T) {
	base, candidate := snapshotPair(t)
	report := candidate.Report
	report.Status = evaluation.EvaluationFailed
	report.Gates[0].Passed = false
	failed, err := replay.New(report, evaluation.ErrGateFailed, nil, candidate.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := Rehearse(base, failed, safeFeedback(t, failed), DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusBlocked || !hasReason(decision, ReasonCandidateFailed) || !hasReason(decision, ReasonComparisonRegression) || !hasReason(decision, ReasonRequiredGateFailed) {
		t.Fatalf("failed candidate did not block rehearsal: %#v", decision)
	}
}

func TestMissingGateBlocksAndIncompatibleRunsRemainInsufficient(t *testing.T) {
	base, candidate := snapshotPair(t)
	report := candidate.Report
	report.Gates = append([]evaluation.GateResult(nil), report.Gates[1:]...)
	missingGate, err := replay.New(report, nil, nil, candidate.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := Rehearse(base, missingGate, safeFeedback(t, missingGate), DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusBlocked || !hasReason(decision, ReasonRequiredGateMissing) || !hasReason(decision, ReasonComparisonRegression) {
		t.Fatalf("removed Gate did not block rehearsal: %#v", decision)
	}

	incompatibleReport := candidate.Report
	incompatibleReport.DataBoundary.ExpertLabelCount++
	incompatible, err := replay.New(incompatibleReport, nil, nil, candidate.CreatedAt.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decision, err = Rehearse(base, incompatible, safeFeedback(t, incompatible), DefaultPolicy(), PhasePreflight)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusInsufficientEvidence || decision.ComparisonStatus != replay.ComparisonIncomparable || !hasReason(decision, ReasonRunsIncomparable) {
		t.Fatalf("incompatible runs emitted a pseudo-decision: %#v", decision)
	}
}

func TestRehearsalRejectsInvalidPolicyPhaseAndFeedbackGraph(t *testing.T) {
	base, candidate := snapshotPair(t)
	policy := DefaultPolicy()
	policy.ProductionActionAllowed = true
	if _, err := Rehearse(base, candidate, safeFeedback(t, candidate), policy, PhasePreflight); err == nil {
		t.Fatal("actionable synthetic policy was accepted")
	}
	if _, err := Rehearse(base, candidate, safeFeedback(t, candidate), DefaultPolicy(), "LIVE"); err == nil {
		t.Fatal("live rehearsal phase was accepted")
	}
	records := safeFeedback(t, candidate)
	records = append(records, records[0])
	if _, err := Rehearse(base, candidate, records, DefaultPolicy(), PhasePreflight); err == nil {
		t.Fatal("duplicate feedback history was accepted")
	}
}

func TestNonPassedDecisionErrorRemainsDetectable(t *testing.T) {
	if !errors.Is(ErrRehearsalNotPassed, ErrRehearsalNotPassed) {
		t.Fatal("rehearsal sentinel is not detectable")
	}
}

func snapshotPair(t *testing.T) (replay.Snapshot, replay.Snapshot) {
	t.Helper()
	base, err := evaluationsnapshot.Passed(context.Background(), time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := evaluationsnapshot.Passed(context.Background(), time.Date(2026, 8, 20, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return base, candidate
}

func safeFeedback(t *testing.T, snapshot replay.Snapshot) []feedback.Record {
	t.Helper()
	records, err := feedback.BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func hasReason(decision Decision, expected ReasonCode) bool {
	for _, reason := range decision.ReasonCodes {
		if reason == expected {
			return true
		}
	}
	return false
}
