package command

import (
	"errors"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestParseInvestigation(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 30, 45, 123, time.UTC)
	request, err := ParseInvestigation(" /investigate order-service prod 30m ", now)
	if err != nil {
		t.Fatal(err)
	}
	if request.Service != "order-service" || request.Environment != "prod" {
		t.Fatalf("unexpected scope: %#v", request)
	}
	if request.TemplateID != domain.ErrorAnalysisTemplateID {
		t.Fatalf("unexpected default template: %q", request.TemplateID)
	}
	wantEnd := time.Date(2026, 8, 18, 10, 30, 35, 0, time.UTC)
	if !request.EndTime.Equal(wantEnd) || !request.StartTime.Equal(wantEnd.Add(-30*time.Minute)) {
		t.Fatalf("unexpected time range: %#v", request)
	}
}

func TestParseInvestigationAcceptsCountTemplate(t *testing.T) {
	request, err := ParseInvestigation("/investigate dam-server test 10m error_count_v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if request.TemplateID != domain.ErrorCountTemplateID {
		t.Fatalf("unexpected template: %q", request.TemplateID)
	}
	if _, err := ParseInvestigation("/investigate dam-server test 10m arbitrary", time.Now()); !errors.Is(err, ErrUsage) {
		t.Fatalf("unknown template should fail with usage, got %v", err)
	}
}

func TestParseInvestigationUsesConfiguredIngestionWatermark(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 30, 45, 123, time.UTC)
	request, err := ParseInvestigationWithGrace("/investigate order-service prod 30m", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wantEnd := time.Date(2026, 8, 18, 10, 30, 15, 0, time.UTC)
	if !request.EndTime.Equal(wantEnd) || request.EndTime.Sub(request.StartTime) != 30*time.Minute {
		t.Fatalf("unexpected watermarked range: %#v", request)
	}
	if _, err := ParseInvestigationWithGrace("/investigate order-service prod 30m", now, time.Second); err == nil {
		t.Fatal("unsafe ingestion grace was accepted")
	}
}

func TestParseInvestigationRejectsUnsafeWindow(t *testing.T) {
	_, err := ParseInvestigation("/investigate order-service prod 25h", time.Now())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("want usage error, got %v", err)
	}
}
