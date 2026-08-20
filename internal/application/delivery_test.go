package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type deliveryStoreStub struct {
	delivery   domain.DeliveryJob
	claimed    bool
	completed  bool
	failed     bool
	dead       bool
	retryAt    time.Time
	remoteID   string
	failReason string
}

func (s *deliveryStoreStub) ClaimDelivery(context.Context, string, time.Time, time.Duration) (domain.DeliveryJob, bool, error) {
	if s.claimed {
		return domain.DeliveryJob{}, false, nil
	}
	s.claimed = true
	return s.delivery, true, nil
}

func (s *deliveryStoreStub) CompleteDelivery(_ context.Context, _ domain.DeliveryJob, remoteID string, _ time.Time) error {
	s.completed = true
	s.remoteID = remoteID
	return nil
}

func (s *deliveryStoreStub) FailDelivery(_ context.Context, _ domain.DeliveryJob, failure domain.DeliveryFailure, retryAt time.Time, dead bool, _ time.Time) error {
	s.failed = true
	s.dead = dead
	s.retryAt = retryAt
	s.failReason = failure.ReasonCode
	return nil
}

type deliverySenderStub struct {
	remoteID string
	err      error
	sawLimit bool
}

func (s *deliverySenderStub) Deliver(ctx context.Context, _ domain.DeliveryJob) (string, error) {
	_, s.sawLimit = ctx.Deadline()
	return s.remoteID, s.err
}

func TestDeliveryWorkerCompletesSuccessfulSend(t *testing.T) {
	t.Parallel()
	store := &deliveryStoreStub{delivery: domain.DeliveryJob{
		ID: "delivery-1", Attempt: 1, Investigation: domain.Investigation{ID: "inv-1"},
	}}
	sender := &deliverySenderStub{remoteID: "om_card"}
	worker, err := application.NewDeliveryWorker(store, sender, "delivery-worker", time.Minute, 5*time.Second, 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run, err := worker.RunOne(context.Background())
	if err != nil || !run || !store.completed || store.failed || store.remoteID != "om_card" || !sender.sawLimit {
		t.Fatalf("unexpected success: run=%v err=%v store=%+v sender=%+v", run, err, store, sender)
	}
}

func TestDeliveryWorkerBoundsRetryAndMarksExhaustionDead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		attempt int
		dead    bool
	}{
		{name: "retryable", attempt: 1, dead: false},
		{name: "exhausted", attempt: 3, dead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &deliveryStoreStub{delivery: domain.DeliveryJob{
				ID: "delivery-fail", Attempt: test.attempt, Investigation: domain.Investigation{ID: "inv-fail"},
			}}
			sender := &deliverySenderStub{err: ports.NewOperationError(domain.FailureRetryable, "feishu_transport_retryable", errors.New("temporary remote failure"))}
			worker, err := application.NewDeliveryWorker(store, sender, "delivery-worker", time.Minute, 5*time.Second, 3, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			run, runErr := worker.RunOne(context.Background())
			if !run || runErr == nil || !store.failed || store.completed || store.dead != test.dead {
				t.Fatalf("unexpected failure handling: run=%v err=%v store=%+v", run, runErr, store)
			}
			if store.failReason != "feishu_transport_retryable" || store.retryAt.IsZero() {
				t.Fatalf("unsafe or missing retry state: %+v", store)
			}
		})
	}
}

func TestDeliveryWorkerMarksPermanentFailureDeadImmediately(t *testing.T) {
	t.Parallel()
	store := &deliveryStoreStub{delivery: domain.DeliveryJob{
		ID: "delivery-permanent", Attempt: 1, Investigation: domain.Investigation{ID: "inv-permanent"},
	}}
	sender := &deliverySenderStub{err: ports.NewOperationError(domain.FailurePermanent, "feishu_delivery_contract_invalid", errors.New("invalid card"))}
	worker, err := application.NewDeliveryWorker(store, sender, "delivery-worker", time.Minute, 5*time.Second, 5, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if run, runErr := worker.RunOne(context.Background()); !run || runErr == nil || !store.dead || store.failReason != "feishu_delivery_contract_invalid" {
		t.Fatalf("permanent failure was retried: run=%v err=%v store=%+v", run, runErr, store)
	}
}
