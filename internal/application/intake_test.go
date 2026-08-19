package application_test

import (
	"context"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
)

func TestIntakeDerivesTrustedRequesterFromInboundEnvelope(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	id, _, err := application.NewIntake(store).Accept(context.Background(), domain.InboundMessage{
		AppID: "cli_real", TenantKey: "tenant_real", UserID: "ou_real", MessageID: "message", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
		Requester: domain.Principal{AppID: "cli_forged", TenantKey: "tenant_forged", UserID: "ou_forged"},
	})
	if err != nil {
		t.Fatal(err)
	}

	investigation, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := (domain.Principal{AppID: "cli_real", TenantKey: "tenant_real", UserID: "ou_real"})
	if investigation.Request.Requester != want {
		t.Fatalf("requester was not derived from the trusted envelope: got %#v want %#v", investigation.Request.Requester, want)
	}
}
