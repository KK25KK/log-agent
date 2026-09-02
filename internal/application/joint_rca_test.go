package application

import (
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestBuildJointRCAJoinsRuntimeDeploymentCodeAndChange(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	trace, code := jointRCATestInputs(true, domain.CodeInvestigationComplete)

	analysis := BuildJointRCA(trace, code, generatedAt)
	if analysis.Status != domain.JointRCAComplete || !analysis.HumanReviewOnly || len(analysis.Candidates) != 1 || len(analysis.Factors) != 5 || len(analysis.Actions) != 3 {
		t.Fatalf("unexpected joint RCA: %#v", analysis)
	}
	candidate := analysis.Candidates[0]
	if candidate.Verdict != domain.JointRCASupportedCandidate || candidate.ChangeRelation != domain.JointRCAChangeOverlap || candidate.Confidence != .75 {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
	if !strings.Contains(candidate.Statement, "internal/payment.go:42") || candidate.ConfidenceMethod != domain.JointRCAConfidenceMethod {
		t.Fatalf("candidate is not bound to the deployed location: %#v", candidate)
	}
	foundChangeSupport := false
	for _, factor := range analysis.Factors {
		if factor.Code == "recent_change_overlap" {
			foundChangeSupport = factor.Role == domain.JointRCAFactorSupport && factor.Result == domain.JointRCAFactorPass
		}
	}
	if !foundChangeSupport || analysis.Actions[2].Code != "REVIEW_TRUSTED_DIFF" || analysis.Actions[2].ExecutionMode != "HUMAN_REVIEW_ONLY" {
		t.Fatalf("change evidence or action missing: factors=%#v actions=%#v", analysis.Factors, analysis.Actions)
	}
	if err := validateJointRCA(analysis, trace, code); err != nil {
		t.Fatal(err)
	}

	again := BuildJointRCA(trace, code, generatedAt)
	if again.Candidates[0].ID != candidate.ID || again.Factors[0].ID != analysis.Factors[0].ID {
		t.Fatal("joint RCA IDs are not deterministic")
	}
}

func TestBuildJointRCAPreservesCounterevidenceAndPartialState(t *testing.T) {
	generatedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	trace, code := jointRCATestInputs(false, domain.CodeInvestigationPartial)
	code.Complete = false
	code.ReasonCode = domain.CodeReasonResultTruncated

	analysis := BuildJointRCA(trace, code, generatedAt)
	if analysis.Status != domain.JointRCAInconclusive || analysis.ReasonCode != domain.JointRCAReasonCodePartial || analysis.Candidates[0].Verdict != domain.JointRCACandidateUnknown || analysis.Candidates[0].Confidence != .45 {
		t.Fatalf("unexpected partial analysis: %#v", analysis)
	}
	if analysis.Candidates[0].ChangeRelation != domain.JointRCAChangeUnchanged {
		t.Fatalf("expected unchanged counterevidence: %#v", analysis.Candidates[0])
	}
	foundCounter := false
	for _, factor := range analysis.Factors {
		if factor.Code == "recent_change_overlap" {
			foundCounter = factor.Role == domain.JointRCAFactorCounter && factor.Result == domain.JointRCAFactorPass
		}
	}
	if !foundCounter || analysis.Actions[2].Code != "VERIFY_RUNTIME_DEPENDENCIES" {
		t.Fatalf("counterevidence was not preserved: factors=%#v actions=%#v", analysis.Factors, analysis.Actions)
	}
}

func TestBuildJointRCAFailsClosedForMissingOrConflictingEvidence(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	trace, code := jointRCATestInputs(true, domain.CodeInvestigationComplete)
	code.Status = domain.CodeInvestigationNoMatch
	code.Matches = nil
	if analysis := BuildJointRCA(trace, code, now); analysis.Status != domain.JointRCAInconclusive || analysis.ReasonCode != domain.JointRCAReasonNoCodeMatch || len(analysis.Candidates) != 0 {
		t.Fatalf("unexpected no-match result: %#v", analysis)
	}

	code.Status = domain.CodeInvestigationUnavailable
	code.ReasonCode = domain.CodeReasonDeploymentConflict
	code.Deployment = nil
	if analysis := BuildJointRCA(trace, code, now); analysis.Status != domain.JointRCANeedsReview || analysis.ReasonCode != domain.JointRCAReasonDeploymentReview {
		t.Fatalf("unexpected conflict result: %#v", analysis)
	}

	trace.Status, trace.Complete = domain.TraceInvestigationPartial, false
	if analysis := BuildJointRCA(trace, code, now); analysis.Status != domain.JointRCASkipped || analysis.ReasonCode != domain.JointRCAReasonTraceIncomplete {
		t.Fatalf("unexpected incomplete Trace result: %#v", analysis)
	}
}

func TestValidateJointRCARejectsPostBuildTampering(t *testing.T) {
	trace, code := jointRCATestInputs(true, domain.CodeInvestigationComplete)
	analysis := BuildJointRCA(trace, code, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	analysis.Candidates[0].Verdict = domain.JointRCACandidateRefuted
	if err := validateJointRCA(analysis, trace, code); err == nil {
		t.Fatal("expected deterministic validation to reject tampering")
	}
}

func jointRCATestInputs(changed bool, status domain.CodeInvestigationStatus) (*domain.TraceInvestigation, *domain.CodeInvestigation) {
	anchor := domain.RuntimeAnchor{ID: "anchor-1", Kind: domain.RuntimeAnchorStackFrame, EventID: "event-1", MemberID: "member-1", File: "internal/payment.go", Line: 42, Symbol: "charge", Fingerprint: strings.Repeat("a", 64)}
	trace := &domain.TraceInvestigation{
		Status: domain.TraceInvestigationComplete, Complete: true,
		AnchorSet: &domain.RuntimeAnchorSet{Version: domain.RuntimeAnchorVersion, Status: domain.RuntimeAnchorsComplete, Anchors: []domain.RuntimeAnchor{anchor}},
	}
	deployment := &domain.DeploymentEvidence{
		Version: domain.DeploymentEvidenceVersion, Status: domain.DeploymentComplete, Service: "dam-server", Environment: "test",
		RepositoryID: "dam", CommitSHA: strings.Repeat("a", 40), PreviousCommitSHA: strings.Repeat("b", 40),
		DeployedAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), SourceVersion: "catalog-v1",
	}
	deployment.Fingerprint, _ = domain.DeploymentEvidenceFingerprint(*deployment)
	match := domain.CodeMatch{
		ID: "code-match-1", Kind: domain.CodeMatchStackFrame, AnchorID: anchor.ID, RepositoryID: "dam",
		CommitSHA: deployment.CommitSHA, File: anchor.File, MatchLine: anchor.Line, ChangedSincePrevious: changed,
	}
	return trace, &domain.CodeInvestigation{
		Version: domain.CodeEvidenceVersion, Status: status, Complete: status == domain.CodeInvestigationComplete,
		Deployment: deployment, Matches: []domain.CodeMatch{match}, DiffChecked: true, GeneratedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
}
