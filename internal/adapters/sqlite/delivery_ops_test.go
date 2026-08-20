package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestDeadInitialReceiptCanBeAuditedAndSafelyReplayed(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-dlq", "job-dlq", "message-dlq", "", now)
	queued, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim receipt: ok=%v err=%v", ok, err)
	}
	failure := domain.DeliveryFailure{Disposition: domain.FailurePermanent, ReasonCode: "feishu_card_rejected"}
	if err := store.FailDelivery(ctx, queued, failure, now.Add(time.Second), true, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	dead, err := store.ListDeadDeliveries(ctx, 10)
	if err != nil || len(dead) != 1 || !dead[0].Replayable || dead[0].ReplayReason != "initial_receipt" {
		t.Fatalf("unexpected dead-letter projection: dead=%+v err=%v", dead, err)
	}
	attempts, err := store.ListDeliveryAttempts(ctx, queued.ID, 10)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != domain.DeliveryAttemptDead || attempts[0].Failure == nil || attempts[0].Failure.ReasonCode != failure.ReasonCode {
		t.Fatalf("unexpected attempt audit: attempts=%+v err=%v", attempts, err)
	}
	replay, err := store.ReplayDeadDelivery(ctx, queued.ID, "ops-user-1", now.Add(2*time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("replay dead receipt: result=%+v err=%v", replay, err)
	}
	reclaimed, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(3*time.Second), time.Minute)
	if err != nil || !ok || reclaimed.ID != queued.ID || reclaimed.Attempt != 1 {
		t.Fatalf("reclaim replayed receipt: delivery=%+v ok=%v err=%v", reclaimed, ok, err)
	}
}

func TestDeadProgressCannotReplayOverNewerProjection(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	acceptDeliveryFixture(t, store, "inv-stale-progress", "job-stale-progress", "message-stale-progress", "", now)
	receipt, _, _ := store.ClaimDelivery(ctx, "delivery-worker", now, time.Minute)
	if err := store.CompleteDelivery(ctx, receipt, "om_stale_progress", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNext(ctx, "investigation-worker", now.Add(2*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim investigation: ok=%v err=%v", ok, err)
	}
	running, ok, err := store.ClaimDelivery(ctx, "delivery-worker", now.Add(3*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim running: ok=%v err=%v", ok, err)
	}
	if err := store.FailDelivery(ctx, running, domain.DeliveryFailure{Disposition: domain.FailurePermanent, ReasonCode: "feishu_card_rejected"}, now.Add(4*time.Second), true, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishFailure(ctx, job, "provider unavailable", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplayDeadDelivery(ctx, running.ID, "ops-user-1", now.Add(6*time.Second)); !errors.Is(err, ports.ErrDeliveryReplayUnsafe) {
		t.Fatalf("stale progress replay was not rejected: %v", err)
	}
}
