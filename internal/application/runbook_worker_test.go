package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
)

type runbookInjectingEngine struct{}

func (runbookInjectingEngine) Run(_ context.Context, investigationID string, _ domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	evidence := []domain.Evidence{{
		ID: "ev-engine", QueryID: "query-engine", QuerySpecHash: "hash-engine", Complete: true,
	}}
	return evidence, domain.Report{
		InvestigationID: investigationID,
		Outcome:         "completed",
		Findings: []domain.Finding{{
			Code: "completed", Statement: "completed", Confidence: 1, Conclusive: true, EvidenceIDs: []string{"ev-engine"},
		}},
		RunbookGuidance: &domain.RunbookGuidance{
			Status: domain.RunbookGuidanceSkippedNoTrigger, MethodVersion: domain.RunbookGuidanceVersion,
		},
		GeneratedAt: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC),
	}, nil
}

func TestWorkerRejectsRunbookGuidanceInjectedByEngine(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	intake := application.NewIntake(store)
	investigationID, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "engine-runbook-injection", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := application.NewWorker(
		store, runbookInjectingEngine{}, "worker-runbook-injection", time.Minute,
		application.WithWorkerClock(func() time.Time { return now.Add(time.Second) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	ran, runErr := worker.RunOne(context.Background())
	if !ran || runErr == nil || !strings.Contains(runErr.Error(), "before governed post-processing") {
		t.Fatalf("ran=%v err=%v", ran, runErr)
	}
	investigation, err := store.GetInvestigation(context.Background(), investigationID)
	if err != nil {
		t.Fatal(err)
	}
	if investigation.Status != domain.StatusFailed || investigation.Report != nil {
		t.Fatalf("engine-injected guidance crossed worker boundary: %#v", investigation)
	}
}
