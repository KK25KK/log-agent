package runbookmock

import (
	"context"
	"errors"
	"testing"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestCatalogResolvesOnlyFixedMockScope(t *testing.T) {
	catalog := NewCatalog()
	resource, err := catalog.Resolve(context.Background(), mockService, mockEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if resource.ID != mockResourceID || resource.Service != mockService || resource.Environment != mockEnvironment {
		t.Fatalf("unexpected mock resource: %#v", resource)
	}
	if _, err := catalog.Resolve(context.Background(), "payment-service", mockEnvironment); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Resolve() error=%v, want ErrNotFound", err)
	}
}

func TestCatalogRequiresCompleteTrustedPrincipalAndExactResource(t *testing.T) {
	catalog := NewCatalog()
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"}
	if !catalog.Allowed(context.Background(), principal, mockResourceID) {
		t.Fatal("complete principal was denied fixed synthetic resource")
	}
	if catalog.Allowed(context.Background(), domain.Principal{}, mockResourceID) {
		t.Fatal("incomplete principal was authorized")
	}
	if catalog.Allowed(context.Background(), principal, "mock/payment-service/prod") {
		t.Fatal("principal was authorized for unknown resource")
	}
}

func TestCatalogHonorsCancellation(t *testing.T) {
	catalog := NewCatalog()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalog.Resolve(ctx, mockService, mockEnvironment); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error=%v, want context.Canceled", err)
	}
	if catalog.Allowed(ctx, domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"}, mockResourceID) {
		t.Fatal("cancelled authorization was allowed")
	}
}
