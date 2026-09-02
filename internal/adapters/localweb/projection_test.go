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
	}}
	view := projectReport(report)
	if view.Trace == nil || view.Trace.TotalEvents != 1 || len(view.Trace.Events) != 1 || view.Trace.Events[0].Message != "[TRACE_ID] failed" {
		t.Fatalf("unexpected Trace projection: %#v", view.Trace)
	}
}
