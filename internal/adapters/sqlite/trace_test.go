package sqlite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestStartedTraceCheckpointBecomesOutcomeUnknownAfterLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	investigationID, _, err := application.NewIntake(store).Accept(ctx, domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", UserID: "user", MessageID: "trace-checkpoint", ChatID: "chat", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "dam-server", Environment: "test", TemplateID: domain.TraceSearchTemplateID,
		TraceID: "trace-12345678", StartTime: now.Add(-10 * time.Minute), EndTime: now.Add(-10 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := store.ClaimNext(ctx, "worker-1", now, time.Second)
	if err != nil || !claimed || job.InvestigationID != investigationID {
		t.Fatalf("first claim: job=%#v claimed=%v err=%v", job, claimed, err)
	}
	inputHash := strings.Repeat("a", 64)
	decision, err := store.PrepareTraceQueryStep(ctx, job, "dam-server", inputHash, now)
	if err != nil || decision.Action != domain.QueryStepExecute {
		t.Fatalf("start Trace checkpoint: %#v err=%v", decision, err)
	}
	recovered, claimed, err := store.ClaimNext(ctx, "worker-2", now.Add(2*time.Second), time.Second)
	if err != nil || !claimed || recovered.Attempt != 2 {
		t.Fatalf("recovery claim: job=%#v claimed=%v err=%v", recovered, claimed, err)
	}
	_, err = store.PrepareTraceQueryStep(ctx, recovered, "dam-server", inputHash, now.Add(2*time.Second))
	if !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("started external read was retried automatically: %v", err)
	}
	_, err = store.PrepareTraceQueryStep(ctx, recovered, "dam-server", inputHash, now.Add(2*time.Second))
	if !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("unknown Trace checkpoint was not durable: %v", err)
	}
}

func TestCompletedTraceCheckpointIsReusedWithoutAnotherRead(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	investigationID, _, err := application.NewIntake(store).Accept(ctx, domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", UserID: "user", MessageID: "trace-reuse", ChatID: "chat", ReceivedAt: now,
	}, domain.InvestigationRequest{Service: "dam-server", Environment: "test", StartTime: now.Add(-time.Minute), EndTime: now.Add(-10 * time.Second)},
	)
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := store.ClaimNext(ctx, "worker", now, time.Minute)
	if err != nil || !claimed || job.InvestigationID != investigationID {
		t.Fatalf("claim: %#v %v %v", job, claimed, err)
	}
	inputHash := strings.Repeat("a", 64)
	if _, err := store.PrepareTraceQueryStep(ctx, job, "dam-server", inputHash, now); err != nil {
		t.Fatal(err)
	}
	result := domain.TraceMemberResult{
		QueryID: "query-1", QuerySpecHash: strings.Repeat("b", 64), GroupID: "dam-trace-test", MemberID: "dam-server",
		TemplateID: domain.TraceSearchTemplateID, TemplateVersion: domain.TraceSearchTemplateVersion, PolicyVersion: domain.TracePolicyVersion,
		GovernanceFingerprint: strings.Repeat("c", 64), TraceIDFingerprint: strings.Repeat("d", 64),
		StartTime: now.Add(-time.Minute), EndTime: now.Add(-10 * time.Second), Status: domain.TraceMemberZeroHit,
		Complete: true, ZeroHit: true, Progress: "Complete", UsageKnown: true, APICalls: 1,
	}
	if err := store.CompleteTraceQueryStep(ctx, job, "dam-server", inputHash, result, now); err != nil {
		t.Fatal(err)
	}
	decision, err := store.PrepareTraceQueryStep(ctx, job, "dam-server", inputHash, now)
	if err != nil || decision.Action != domain.QueryStepReuse || decision.Result == nil || decision.Result.QueryID != result.QueryID {
		t.Fatalf("checkpoint was not reused: %#v err=%v", decision, err)
	}
}
