package localweb

import (
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestProjectReportExposesOnlyGovernedTraceProjection(t *testing.T) {
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	report := domain.Report{TraceInvestigation: &domain.TraceInvestigation{
		Status: domain.TraceInvestigationComplete, Complete: true, TraceIDFingerprint: strings.Repeat("a", 64),
		Members:       []domain.TraceMemberSummary{{MemberID: "dam-server", Status: domain.TraceMemberComplete, EventCount: 1}},
		Events:        []domain.TraceEvent{{ID: "event", MemberID: "dam-server", EventTime: now, Message: "[TRACE_ID] failed", MessageFingerprint: strings.Repeat("b", 64)}},
		TotalAPICalls: 1, TotalProcessedRows: 1, TotalProcessedBytes: 100,
		AnchorSet: &domain.RuntimeAnchorSet{Version: domain.RuntimeAnchorVersion, Status: domain.RuntimeAnchorsComplete,
			Anchors: []domain.RuntimeAnchor{{ID: "anchor", Kind: domain.RuntimeAnchorRoute, Value: "GET /dam/job"}}},
	}, CodeInvestigation: &domain.CodeInvestigation{
		Version: domain.CodeEvidenceVersion, Status: domain.CodeInvestigationComplete, Complete: true,
		Deployment:   &domain.DeploymentEvidence{Status: domain.DeploymentComplete, RepositoryID: "dam", CommitSHA: strings.Repeat("c", 40)},
		Matches:      []domain.CodeMatch{{File: "internal/payment/client.go", MatchLine: 87, Snippet: "return err"}},
		ChangedFiles: []string{"internal/payment/client.go"}, Limitations: []string{"human review"},
	}}
	view := projectReport(report)
	if view.Trace == nil || view.Trace.TotalEvents != 1 || len(view.Trace.Events) != 1 || view.Trace.Events[0].Message != "[TRACE_ID] failed" ||
		view.Trace.AnchorSet == nil || len(view.Trace.AnchorSet.Anchors) != 1 {
		t.Fatalf("unexpected Trace projection: %#v", view.Trace)
	}
	if view.Code == nil || len(view.Code.Matches) != 1 || view.Code.Matches[0].Snippet != "return err" || view.Code.Deployment == report.CodeInvestigation.Deployment {
		t.Fatalf("unexpected code projection: %#v", view.Code)
	}
}
