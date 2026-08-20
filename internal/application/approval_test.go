package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestApprovalRequiresSeparationAndConsumesExactlyOnce(t *testing.T) {
	t.Parallel()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	requester := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "requester"}
	approver := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "approver"}
	request := domain.InvestigationRequest{
		Service: "order-service", Environment: "prod",
		StartTime: now.Add(-time.Hour), EndTime: now, Requester: requester,
	}
	inbound := domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "approval-message", ChatID: "chat", UserID: "requester", ReceivedAt: now,
	}
	if _, created, err := store.AcceptOnce(ctx, inbound, request, "inv-approval", "job-approval"); err != nil || !created {
		t.Fatalf("accept approval investigation: created=%v err=%v", created, err)
	}
	clock := now
	service, err := application.NewApprovalService(store, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	payloadHash := application.ApprovalPayloadHash([]byte("bounded-action-v1"))
	created, err := service.Request(ctx, "approval-1", "inv-approval", domain.HighRiskRemediation, payloadHash, requester, time.Hour)
	if err != nil || created.Status != domain.ApprovalPending {
		t.Fatalf("create approval: request=%+v err=%v", created, err)
	}
	if _, err := service.Decide(ctx, created.ID, true, requester); !errors.Is(err, ports.ErrApprovalInvalid) {
		t.Fatalf("self approval was accepted: %v", err)
	}
	approved, err := service.Decide(ctx, created.ID, true, approver)
	if err != nil || approved.Status != domain.ApprovalApproved {
		t.Fatalf("approve request: request=%+v err=%v", approved, err)
	}
	consumed, err := service.Consume(ctx, created.ID, payloadHash)
	if err != nil || consumed.Status != domain.ApprovalConsumed {
		t.Fatalf("consume approval: request=%+v err=%v", consumed, err)
	}
	if _, err := service.Consume(ctx, created.ID, payloadHash); !errors.Is(err, ports.ErrApprovalInvalid) {
		t.Fatalf("approval was consumed twice: %v", err)
	}
}
