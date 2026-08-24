package runbookmock

import (
	"context"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	mockService     = "order-service"
	mockEnvironment = "prod"
	mockResourceID  = "mock/order-service/prod"
)

// Catalog is the local-only authorization boundary for the generic Mock
// worker. The knowledge is synthetic and shared, so every complete trusted
// principal may access the one fixed logical resource; unknown scopes and
// incomplete identities are denied.
type Catalog struct{}

func NewCatalog() *Catalog {
	return &Catalog{}
}

func (catalog *Catalog) Resolve(ctx context.Context, service, environment string) (domain.LogResource, error) {
	if err := ctx.Err(); err != nil {
		return domain.LogResource{}, err
	}
	if service != mockService || environment != mockEnvironment {
		return domain.LogResource{}, ports.ErrNotFound
	}
	return domain.LogResource{ID: mockResourceID, Service: mockService, Environment: mockEnvironment}, nil
}

func (catalog *Catalog) Allowed(ctx context.Context, principal domain.Principal, resourceID string) bool {
	return ctx.Err() == nil && principal.Complete() && resourceID == mockResourceID
}

var _ ports.ResourceCatalog = (*Catalog)(nil)
