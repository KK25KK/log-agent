package resourcecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestCatalogResolveAndAuthorize(t *testing.T) {
	catalog := loadTestCatalog(t, validCatalog())
	principal := domain.Principal{AppID: "cli_test", TenantKey: "tenant_test", UserID: "ou_user"}

	resource, err := catalog.Resolve(context.Background(), "order-service", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if resource.ID != "order-prod" || resource.CatalogVersion != "2026-08-18.1" || resource.InstanceField != "pod_name" {
		t.Fatalf("unexpected resource: %#v", resource)
	}

	tests := []struct {
		name       string
		principal  domain.Principal
		resourceID string
		want       bool
	}{
		{name: "exact principal and resource", principal: principal, resourceID: "order-prod", want: true},
		{name: "different app", principal: domain.Principal{AppID: "cli_other", TenantKey: principal.TenantKey, UserID: principal.UserID}, resourceID: "order-prod"},
		{name: "different tenant", principal: domain.Principal{AppID: principal.AppID, TenantKey: "tenant_other", UserID: principal.UserID}, resourceID: "order-prod"},
		{name: "different user", principal: domain.Principal{AppID: principal.AppID, TenantKey: principal.TenantKey, UserID: "ou_other"}, resourceID: "order-prod"},
		{name: "incomplete principal", principal: domain.Principal{AppID: principal.AppID, TenantKey: principal.TenantKey}, resourceID: "order-prod"},
		{name: "unknown resource", principal: principal, resourceID: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := catalog.Allowed(context.Background(), test.principal, test.resourceID); got != test.want {
				t.Fatalf("Allowed()=%v, want %v", got, test.want)
			}
		})
	}

	_, err = catalog.Resolve(context.Background(), "unknown", "prod")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown scope error=%v, want ErrNotFound", err)
	}
}

func TestCatalogHonorsCancellation(t *testing.T) {
	catalog := loadTestCatalog(t, validCatalog())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := catalog.Resolve(ctx, "order-service", "prod"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error=%v, want context.Canceled", err)
	}
	principal := domain.Principal{AppID: "cli_test", TenantKey: "tenant_test", UserID: "ou_user"}
	if catalog.Allowed(ctx, principal, "order-prod") {
		t.Fatal("Allowed() granted access with a cancelled context")
	}
}

func TestCatalogResourcesReturnsDefensiveCopy(t *testing.T) {
	catalog := loadTestCatalog(t, validCatalog())
	first := catalog.Resources()
	first[0].ID = "changed"
	first[0].Selectors[0].Field = "changed"

	second := catalog.Resources()
	if second[0].ID != "order-prod" || second[0].Selectors[0].Field != "service_name" {
		t.Fatalf("catalog resources were mutated through returned slice: %#v", second[0])
	}
	resolved, err := catalog.Resolve(context.Background(), "order-service", "prod")
	if err != nil {
		t.Fatal(err)
	}
	resolved.Selectors[0].Field = "changed-again"
	resolvedAgain, err := catalog.Resolve(context.Background(), "order-service", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedAgain.Selectors[0].Field != "service_name" {
		t.Fatalf("catalog resource was mutated through Resolve(): %#v", resolvedAgain)
	}
}

func TestCatalogRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*catalogFile)
		want   string
	}{
		{
			name: "missing version",
			mutate: func(config *catalogFile) {
				config.Version = ""
			},
			want: "version",
		},
		{
			name: "invalid opaque resource ID",
			mutate: func(config *catalogFile) {
				config.Resources[0].ID = "order prod"
			},
			want: "resource ID",
		},
		{
			name: "duplicate resource ID",
			mutate: func(config *catalogFile) {
				duplicate := config.Resources[0]
				duplicate.Service = "payment-service"
				config.Resources = append(config.Resources, duplicate)
			},
			want: "duplicate resource ID",
		},
		{
			name: "duplicate logical scope",
			mutate: func(config *catalogFile) {
				duplicate := config.Resources[0]
				duplicate.ID = "order-prod-copy"
				config.Resources = append(config.Resources, duplicate)
			},
			want: "duplicate service/environment",
		},
		{
			name: "non HTTPS endpoint",
			mutate: func(config *catalogFile) {
				config.Resources[0].Endpoint = "http://cn-hangzhou.log.aliyuncs.com"
			},
			want: "HTTPS",
		},
		{
			name: "invalid project name",
			mutate: func(config *catalogFile) {
				config.Resources[0].Project = "Project.With.Path"
			},
			want: "project",
		},
		{
			name: "invalid logstore name",
			mutate: func(config *catalogFile) {
				config.Resources[0].LogStore = "logstore/path"
			},
			want: "logstore",
		},
		{
			name: "unsupported template version",
			mutate: func(config *catalogFile) {
				config.Resources[0].TemplateVersion = "error-summary-v1"
			},
			want: "template_version",
		},
		{
			name: "invalid selector field",
			mutate: func(config *catalogFile) {
				config.Resources[0].Selectors[0].Field = "9service"
			},
			want: "selectors[0].field",
		},
		{
			name: "invalid error field",
			mutate: func(config *catalogFile) {
				config.Resources[0].ErrorField = "error.code"
			},
			want: "error_field",
		},
		{
			name: "missing instance field",
			mutate: func(config *catalogFile) {
				config.Resources[0].InstanceField = ""
			},
			want: "instance_field",
		},
		{
			name: "instance field duplicates error field",
			mutate: func(config *catalogFile) {
				config.Resources[0].InstanceField = config.Resources[0].ErrorField
			},
			want: "must differ from error_field",
		},
		{
			name: "instance field duplicates selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].InstanceField = "service_name"
			},
			want: "instance_field duplicates selector",
		},
		{
			name: "instance field duplicates error selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].InstanceField = "level"
			},
			want: "instance_field duplicates error_selector",
		},
		{
			name: "missing error selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].ErrorSelector = domain.LogSelector{}
			},
			want: "error_selector.field",
		},
		{
			name: "error selector duplicates scope selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].ErrorSelector = domain.LogSelector{Field: "service_name", Value: "ERROR"}
			},
			want: "error_selector duplicates",
		},
		{
			name: "error field duplicates scope selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].ErrorField = "service_name"
			},
			want: "error_field duplicates selector",
		},
		{
			name: "error field duplicates error selector",
			mutate: func(config *catalogFile) {
				config.Resources[0].ErrorField = "level"
			},
			want: "error_field duplicates error_selector",
		},
		{
			name: "duplicate selector field",
			mutate: func(config *catalogFile) {
				config.Resources[0].Selectors = append(config.Resources[0].Selectors, domain.LogSelector{Field: "service_name", Value: "other"})
			},
			want: "duplicate selector field",
		},
		{
			name: "selector control characters",
			mutate: func(config *catalogFile) {
				config.Resources[0].Selectors[0].Value = "order-service\n| SELECT *"
			},
			want: "control characters",
		},
		{
			name: "incomplete principal",
			mutate: func(config *catalogFile) {
				config.Bindings[0].Principal.UserID = ""
			},
			want: "principal must include",
		},
		{
			name: "unknown binding resource",
			mutate: func(config *catalogFile) {
				config.Bindings[0].ResourceIDs = []string{"unknown"}
			},
			want: "unknown resource",
		},
		{
			name: "duplicate principal binding",
			mutate: func(config *catalogFile) {
				config.Bindings = append(config.Bindings, config.Bindings[0])
			},
			want: "duplicate binding",
		},
		{
			name: "duplicate resource in binding",
			mutate: func(config *catalogFile) {
				config.Bindings[0].ResourceIDs = []string{"order-prod", "order-prod"}
			},
			want: "duplicate resource",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validCatalog()
			test.mutate(&config)
			_, err := Load(writeCatalog(t, config))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "unknown field", content: `{"version":"v1","resources":[],"bindings":[],"unexpected":true}`, want: "unknown field"},
		{name: "trailing value", content: `{"version":"v1","resources":[],"bindings":[]} {}`, want: "exactly one JSON value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestExampleCatalogIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "sls-resources.example.json")
	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("load example catalog: %v", err)
	}
	if len(catalog.Resources()) != 1 {
		t.Fatalf("example resource count=%d, want 1", len(catalog.Resources()))
	}
}

func TestDAMPilotExampleCatalogIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "sls-resources.dam-pilot.example.json")
	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("load DAM pilot example catalog: %v", err)
	}
	resources := catalog.Resources()
	if len(resources) != 1 || resources[0].ID != "dam-server-test-count" || resources[0].LogStore != "2016-hyper-dam-file" || resources[0].TemplateVersion != domain.ErrorCountTemplateVersion {
		t.Fatalf("unexpected DAM pilot resources: %#v", resources)
	}
}

func TestCatalogAcceptsCountOnlyTemplateWithoutDimensions(t *testing.T) {
	config := validCatalog()
	config.Resources[0].TemplateVersion = domain.ErrorCountTemplateVersion
	config.Resources[0].ErrorField = ""
	config.Resources[0].InstanceField = ""
	catalog := loadTestCatalog(t, config)
	resource, err := catalog.Resolve(context.Background(), "order-service", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if resource.ErrorField != "" || resource.InstanceField != "" {
		t.Fatalf("unexpected dimensions: %#v", resource)
	}
}

func TestCatalogRejectsDimensionsForCountOnlyTemplate(t *testing.T) {
	config := validCatalog()
	config.Resources[0].TemplateVersion = domain.ErrorCountTemplateVersion
	config.Resources[0].InstanceField = ""
	_, err := Load(writeCatalog(t, config))
	if err == nil || !strings.Contains(err.Error(), "must not configure") {
		t.Fatalf("expected count-only dimension rejection, got %v", err)
	}
}

func validCatalog() catalogFile {
	return catalogFile{
		Version: "2026-08-18.1",
		Resources: []resourceConfig{{
			ID:              "order-prod",
			Service:         "order-service",
			Environment:     "prod",
			Endpoint:        "https://cn-hangzhou.log.aliyuncs.com",
			Project:         "example-project",
			LogStore:        "application-log",
			TemplateVersion: domain.ErrorAnalysisTemplateVersion,
			Selectors: []domain.LogSelector{
				{Field: "service_name", Value: "order-service"},
				{Field: "environment", Value: "prod"},
			},
			ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"},
			ErrorField:    "error_code",
			InstanceField: "pod_name",
		}},
		Bindings: []bindingConfig{{
			Principal:   domain.Principal{AppID: "cli_test", TenantKey: "tenant_test", UserID: "ou_user"},
			ResourceIDs: []string{"order-prod"},
		}},
	}
}

func loadTestCatalog(t *testing.T, config catalogFile) *Catalog {
	t.Helper()
	catalog, err := Load(writeCatalog(t, config))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func writeCatalog(t *testing.T, config catalogFile) string {
	t.Helper()
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
