package slsmock

import (
	"context"
	"fmt"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	mockService     = "order-service"
	mockEnvironment = "prod"
	mockResourceID  = "mock/order-service/prod"
)

// Catalog is a default-deny, single-resource catalog for local integration
// tests. It deliberately binds one complete trusted principal instead of
// treating user-owned service or SLS identifiers as configuration.
type Catalog struct {
	allowedPrincipal domain.Principal
	resource         domain.LogResource
}

// NewCatalog returns the fixed local resource catalog. An incomplete principal
// can be supplied, but it will never be authorized by Allowed.
func NewCatalog(allowedPrincipal domain.Principal) *Catalog {
	return NewCatalogForTemplate(allowedPrincipal, domain.ErrorAnalysisTemplateID)
}

// NewCatalogForTemplate creates the same fixed logical resource with one
// registered template contract for end-to-end capability tests.
func NewCatalogForTemplate(allowedPrincipal domain.Principal, templateID string) *Catalog {
	return &Catalog{
		allowedPrincipal: allowedPrincipal,
		resource:         fixedResourceForTemplate(templateID),
	}
}

// Resolve maps only the fixed order-service/prod logical scope.
func (c *Catalog) Resolve(ctx context.Context, service, environment string) (domain.LogResource, error) {
	if err := ctx.Err(); err != nil {
		return domain.LogResource{}, err
	}
	if service != mockService || environment != mockEnvironment {
		return domain.LogResource{}, fmt.Errorf("resolve mock log resource %q/%q: %w", service, environment, ports.ErrNotFound)
	}
	return cloneResource(c.resource), nil
}

// Allowed requires an exact match of app, tenant, user, and fixed resource ID.
func (c *Catalog) Allowed(ctx context.Context, principal domain.Principal, resourceID string) bool {
	return ctx.Err() == nil &&
		principal.Complete() &&
		c.allowedPrincipal.Complete() &&
		principal == c.allowedPrincipal &&
		resourceID == c.resource.ID
}

func fixedResource() domain.LogResource {
	return fixedResourceForTemplate(domain.ErrorAnalysisTemplateID)
}

func fixedResourceForTemplate(templateID string) domain.LogResource {
	contract, ok := domain.QueryTemplateByID(templateID)
	if !ok {
		contract, _ = domain.QueryTemplateByID(domain.ErrorAnalysisTemplateID)
	}
	resource := domain.LogResource{
		ID:              mockResourceID,
		CatalogVersion:  "mock-catalog-v1",
		Service:         mockService,
		Environment:     mockEnvironment,
		Endpoint:        "mock://sls",
		Project:         "mock-project",
		LogStore:        "mock-logstore",
		TemplateVersion: contract.Version,
		Selectors: []domain.LogSelector{
			{Field: "service", Value: mockService},
			{Field: "env", Value: mockEnvironment},
		},
		ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"},
	}
	if contract.Dimensional {
		resource.ErrorField = "error_message"
		resource.InstanceField = "pod_name"
	}
	return resource
}

func cloneResource(resource domain.LogResource) domain.LogResource {
	resource.Selectors = append([]domain.LogSelector(nil), resource.Selectors...)
	return resource
}

var _ ports.ResourceCatalog = (*Catalog)(nil)
