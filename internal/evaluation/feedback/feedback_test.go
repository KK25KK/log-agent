package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/evaluation/replay"
	"logagent/internal/testsupport/evaluationsnapshot"
)

func TestSyntheticFixtureResolvesTwoReviewersPerCase(t *testing.T) {
	snapshot := passedSnapshot(t)
	records, err := BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(snapshot.Report.Cases)*2 {
		t.Fatalf("unexpected fixture count: %d", len(records))
	}
	summary, err := Resolve(snapshot, records)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DataSource != SyntheticDataSource || summary.ProductionAction || summary.RealReviewerCount != 0 || summary.ExternalNetworkCalls != 0 || summary.ActiveCount != len(records) {
		t.Fatalf("unsafe feedback summary boundary: %#v", summary)
	}
	for _, item := range summary.Cases {
		if len(item.ActiveFeedback) != 2 || item.ActiveFeedback[0].ReviewerRef == item.ActiveFeedback[1].ReviewerRef {
			t.Fatalf("Case %q does not have two independent reviewers: %#v", item.CaseID, item.ActiveFeedback)
		}
	}
}

func TestCorrectionPreservesHistoryAndChangesOnlyActiveProjection(t *testing.T) {
	snapshot := passedSnapshot(t)
	records, err := BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	parent := records[0]
	correction, err := NewRecord(snapshot, NewRecordInput{
		CaseID: parent.CaseID, ReviewerRef: parent.ReviewerRef, Verdict: VerdictUnsure,
		ReasonCode: ReasonInsufficientContext, Supersedes: parent.FeedbackID, CreatedAt: parent.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, correction)
	summary, err := Resolve(snapshot, records)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RecordCount != len(snapshot.Report.Cases)*2+1 || summary.ActiveCount != len(snapshot.Report.Cases)*2 {
		t.Fatalf("correction did not preserve append-only history: %#v", summary)
	}
	found := false
	for _, item := range summary.Cases {
		for _, active := range item.ActiveFeedback {
			if active.FeedbackID == parent.FeedbackID {
				t.Fatal("superseded feedback remained active")
			}
			if active.FeedbackID == correction.FeedbackID && active.Verdict == VerdictUnsure {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("correction was not present in the active projection")
	}
}

func TestResolveRejectsBranchedCrossBoundaryAndCyclicCorrections(t *testing.T) {
	snapshot := passedSnapshot(t)
	root, err := NewRecord(snapshot, NewRecordInput{
		CaseID: snapshot.Report.Cases[0].ID, ReviewerRef: "synthetic-reviewer-a", Verdict: VerdictSafe,
		ReasonCode: ReasonExpectedBehavior, CreatedAt: snapshot.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewRecord(snapshot, NewRecordInput{
		CaseID: root.CaseID, ReviewerRef: root.ReviewerRef, Verdict: VerdictUnsure,
		ReasonCode: ReasonInsufficientContext, Supersedes: root.FeedbackID, CreatedAt: root.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := NewRecord(snapshot, NewRecordInput{
		CaseID: root.CaseID, ReviewerRef: root.ReviewerRef, Verdict: VerdictUnsafe,
		ReasonCode: ReasonMisleadingConclusion, Supersedes: root.FeedbackID, CreatedAt: root.CreatedAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(snapshot, []Record{root, first, branch}); err == nil || !strings.Contains(err.Error(), "branches") {
		t.Fatalf("branched correction was accepted: %v", err)
	}
	crossReviewer, err := NewRecord(snapshot, NewRecordInput{
		CaseID: root.CaseID, ReviewerRef: "synthetic-reviewer-b", Verdict: VerdictSafe,
		ReasonCode: ReasonEvidenceGrounded, Supersedes: root.FeedbackID, CreatedAt: root.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(snapshot, []Record{root, crossReviewer}); err == nil || !strings.Contains(err.Error(), "crosses") {
		t.Fatalf("cross-reviewer correction was accepted: %v", err)
	}
	cyclicRoot := root
	cyclicRoot.Supersedes = first.FeedbackID
	cyclicRoot.ContentHash, err = cyclicRoot.bodyHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(snapshot, []Record{cyclicRoot, first}); err == nil {
		t.Fatal("cyclic correction history was accepted")
	}
}

func TestStrictParsingRejectsTamperUnknownTrailingAndOversizedPayloads(t *testing.T) {
	snapshot := passedSnapshot(t)
	record, err := NewRecord(snapshot, NewRecordInput{
		CaseID: snapshot.Report.Cases[0].ID, ReviewerRef: "synthetic-reviewer-a", Verdict: VerdictSafe,
		ReasonCode: ReasonExpectedBehavior, CreatedAt: snapshot.CreatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseStrict(payload)
	if err != nil || parsed.ContentHash != record.ContentHash {
		t.Fatalf("valid feedback did not round-trip: parsed=%#v err=%v", parsed, err)
	}
	tampered := append([]byte(nil), payload...)
	tampered = []byte(strings.Replace(string(tampered), `"reviewer_ref":"synthetic-reviewer-a"`, `"reviewer_ref":"synthetic-reviewer-c"`, 1))
	if _, err := ParseStrict(tampered); !errors.Is(err, ErrFeedbackTampered) {
		t.Fatalf("tampered feedback did not fail hash validation: %v", err)
	}
	unknown := append(payload[:len(payload)-1], []byte(`,"unexpected":true}`)...)
	if _, err := ParseStrict(unknown); err == nil {
		t.Fatal("unknown feedback field was accepted")
	}
	if _, err := ParseStrict(append(payload, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing feedback JSON was accepted")
	}
	if _, err := ParseStrict(make([]byte, MaxRecordBytes+1)); err == nil {
		t.Fatal("oversized feedback payload was accepted")
	}
}

func TestRecordRejectsUnknownCaseAndInvalidVerdictReason(t *testing.T) {
	snapshot := passedSnapshot(t)
	if _, err := NewRecord(snapshot, NewRecordInput{
		CaseID: "unknown-case", ReviewerRef: "synthetic-reviewer-a", Verdict: VerdictSafe,
		ReasonCode: ReasonExpectedBehavior, CreatedAt: snapshot.CreatedAt.Add(time.Second),
	}); err == nil {
		t.Fatal("unknown Case was accepted")
	}
	if _, err := NewRecord(snapshot, NewRecordInput{
		CaseID: snapshot.Report.Cases[0].ID, ReviewerRef: "synthetic-reviewer-a", Verdict: VerdictSafe,
		ReasonCode: ReasonUnsafeRecommendation, CreatedAt: snapshot.CreatedAt.Add(time.Second),
	}); err == nil {
		t.Fatal("invalid verdict/reason pair was accepted")
	}
}

func passedSnapshot(t *testing.T) replay.Snapshot {
	t.Helper()
	snapshot, err := evaluationsnapshot.Passed(context.Background(), time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
