package ports

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
)

var (
	ErrActionForbidden = errors.New("investigation action forbidden")
	ErrActionInvalid   = errors.New("investigation action invalid")
)

// DeliveryStore owns local, durable, lease-fenced card delivery state.
type DeliveryStore interface {
	ClaimDelivery(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (domain.DeliveryJob, bool, error)
	CompleteDelivery(ctx context.Context, delivery domain.DeliveryJob, remoteMessageID string, now time.Time) error
	FailDelivery(ctx context.Context, delivery domain.DeliveryJob, failure domain.DeliveryFailure, retryAt time.Time, dead bool, now time.Time) error
}

// DeliverySender is implemented by the Feishu adapter without leaking SDK types.
type DeliverySender interface {
	Deliver(ctx context.Context, delivery domain.DeliveryJob) (remoteMessageID string, err error)
}

// InteractionResolver binds a callback card to its persisted investigation.
type InteractionResolver interface {
	ResolveInteraction(ctx context.Context, appID, tenantKey, chatID, cardMessageID string) (string, error)
	ResolveActionReplay(ctx context.Context, appID, tenantKey, chatID, userID, eventID string, action domain.InvestigationAction) (string, error)
}
