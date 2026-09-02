package ports

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
)

var (
	ErrIntentForbidden = errors.New("intent resolution forbidden")
	ErrIntentInvalid   = errors.New("intent resolution invalid")
	ErrIntentExpired   = errors.New("intent resolution expired")
)

// InvestigationIntentParser is an LLM-shaped semantic parser. It cannot
// access storage, resources, SLS, Git, or the requester identity.
type InvestigationIntentParser interface {
	Parse(ctx context.Context, input domain.IntentProviderInput) (domain.IntentProviderResult, error)
}

// IntentCapabilitySource exposes only logical, ACL-filtered capabilities.
type IntentCapabilitySource interface {
	ListAllowedCapabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error)
}

// IntentResolutionStore owns parser idempotency and one-time confirmation.
type IntentResolutionStore interface {
	BeginIntentResolution(ctx context.Context, resolution domain.IntentResolution) (domain.IntentResolution, bool, error)
	CompleteIntentResolution(ctx context.Context, resolution domain.IntentResolution) error
	GetIntentResolution(ctx context.Context, resolutionID string) (domain.IntentResolution, error)
	ConfirmIntentResolution(ctx context.Context, resolutionID string, principal domain.Principal, investigationID string, now time.Time) (string, bool, error)
}
