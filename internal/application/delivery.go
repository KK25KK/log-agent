package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

// DeliveryWorker sends one durable Feishu card update at a time.
type DeliveryWorker struct {
	store         ports.DeliveryStore
	sender        ports.DeliverySender
	workerID      string
	leaseDuration time.Duration
	sendTimeout   time.Duration
	maxAttempts   int
	retryBase     time.Duration
	now           func() time.Time
}

func NewDeliveryWorker(
	store ports.DeliveryStore,
	sender ports.DeliverySender,
	workerID string,
	leaseDuration, sendTimeout time.Duration,
	maxAttempts int,
	retryBase time.Duration,
) (*DeliveryWorker, error) {
	if store == nil || sender == nil {
		return nil, errors.New("delivery store and sender are required")
	}
	if workerID == "" || leaseDuration <= 0 || sendTimeout <= 0 || retryBase <= 0 || maxAttempts <= 0 {
		return nil, errors.New("delivery worker settings must be positive and worker ID must be set")
	}
	if leaseDuration <= sendTimeout {
		return nil, errors.New("delivery lease must exceed the send timeout")
	}
	return &DeliveryWorker{
		store: store, sender: sender, workerID: workerID,
		leaseDuration: leaseDuration, sendTimeout: sendTimeout,
		maxAttempts: maxAttempts, retryBase: retryBase, now: time.Now,
	}, nil
}

// RunOne returns false when no delivery is currently runnable.
func (w *DeliveryWorker) RunOne(ctx context.Context) (bool, error) {
	delivery, ok, err := w.store.ClaimDelivery(ctx, w.workerID, w.now().UTC(), w.leaseDuration)
	if err != nil || !ok {
		return ok, err
	}

	sendCtx, cancel := context.WithTimeout(ctx, w.sendTimeout)
	remoteMessageID, sendErr := w.sender.Deliver(sendCtx, delivery)
	cancel()
	if ctx.Err() != nil {
		// Leave the lease for reclaim. A send may already have reached Feishu;
		// the initial reply UUID and idempotent card patch limit duplicates.
		return true, ctx.Err()
	}
	now := w.now().UTC()
	if sendErr != nil {
		failure := ports.ClassifyOperationError(sendErr)
		if failure.Disposition == domain.FailureCancelled && ctx.Err() != nil {
			return true, ctx.Err()
		}
		dead := failure.Disposition == domain.FailurePermanent || delivery.Attempt >= w.maxAttempts
		retryAt := now.Add(w.retryDelay(delivery.Attempt))
		if err := w.store.FailDelivery(ctx, delivery, failure, retryAt, dead, now); err != nil {
			return true, fmt.Errorf("send Feishu delivery: %v; persist failure: %w", sendErr, err)
		}
		return true, fmt.Errorf("send Feishu delivery %q: %w", delivery.ID, sendErr)
	}
	if err := w.store.CompleteDelivery(ctx, delivery, remoteMessageID, now); err != nil {
		return true, fmt.Errorf("complete Feishu delivery %q: %w", delivery.ID, err)
	}
	return true, nil
}

func (w *DeliveryWorker) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := w.retryBase
	for index := 1; index < attempt && delay < time.Minute; index++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
