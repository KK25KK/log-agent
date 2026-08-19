package slsmock

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

var fixturePrincipal = domain.Principal{
	AppID:     "mock-app",
	TenantKey: "mock-tenant",
	UserID:    "mock-user",
}

func TestCatalogResolvesOnlyFixedScopeAndUsesExactFullPrincipalACL(t *testing.T) {
	catalog := NewCatalog(fixturePrincipal)
	resource, err := catalog.Resolve(context.Background(), mockService, mockEnvironment)
	if err != nil {
		t.Fatal(err)
	}

	want := fixedResource()
	if !reflect.DeepEqual(resource, want) {
		t.Fatalf("unexpected fixed resource:\n got=%#v\nwant=%#v", resource, want)
	}
	if !catalog.Allowed(context.Background(), fixturePrincipal, resource.ID) {
		t.Fatal("constructor-bound complete principal was denied")
	}

	tests := []struct {
		name       string
		principal  domain.Principal
		resourceID string
	}{
		{name: "other app", principal: domain.Principal{AppID: "other", TenantKey: fixturePrincipal.TenantKey, UserID: fixturePrincipal.UserID}, resourceID: resource.ID},
		{name: "other tenant", principal: domain.Principal{AppID: fixturePrincipal.AppID, TenantKey: "other", UserID: fixturePrincipal.UserID}, resourceID: resource.ID},
		{name: "other user", principal: domain.Principal{AppID: fixturePrincipal.AppID, TenantKey: fixturePrincipal.TenantKey, UserID: "other"}, resourceID: resource.ID},
		{name: "incomplete principal", principal: domain.Principal{AppID: fixturePrincipal.AppID, TenantKey: fixturePrincipal.TenantKey}, resourceID: resource.ID},
		{name: "other resource", principal: fixturePrincipal, resourceID: "mock/other/prod"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if catalog.Allowed(context.Background(), test.principal, test.resourceID) {
				t.Fatal("default-deny catalog authorized a non-exact binding")
			}
		})
	}

	if NewCatalog(domain.Principal{}).Allowed(context.Background(), domain.Principal{}, resource.ID) {
		t.Fatal("an incomplete constructor binding became authorized")
	}
	if _, err := catalog.Resolve(context.Background(), "unknown-service", mockEnvironment); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown scope should fail closed with not found, got %v", err)
	}

	resource.Selectors[0].Value = "mutated"
	again, err := catalog.Resolve(context.Background(), mockService, mockEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if again.Selectors[0].Value != mockService {
		t.Fatalf("caller mutated catalog-owned selectors: %#v", again.Selectors)
	}
}

func TestBackendSchemaContainsEveryFixedField(t *testing.T) {
	backend := newFixtureBackend(t)
	schema, err := backend.GetSchema(context.Background(), fixedResource())
	if err != nil {
		t.Fatal(err)
	}

	wantFields := []string{"service", "env", "level", "error_message", "pod_name"}
	if len(schema.Fields) != len(wantFields) || schema.Fingerprint == "" || schema.FetchedAt.IsZero() {
		t.Fatalf("incomplete mock schema metadata: %#v", schema)
	}
	for _, field := range wantFields {
		if _, exists := schema.Fields[field]; !exists {
			t.Fatalf("fixed field %q is missing: %#v", field, schema.Fields)
		}
	}
	for _, field := range []string{"error_message", "pod_name"} {
		if got := schema.Fields[field]; got.Type != "text" || !got.DocValue {
			t.Fatalf("field %q must support text statistics, got %#v", field, got)
		}
	}

	unknown := fixedResource()
	unknown.Project = "caller-controlled-project"
	if _, err := backend.GetSchema(context.Background(), unknown); !errors.Is(err, ports.ErrQueryDenied) {
		t.Fatalf("unknown resource should fail closed, got %v", err)
	}
}

func TestBackendReturnsExistingCurrentAndBaselineFixturesByExactWindow(t *testing.T) {
	backend := newFixtureBackend(t)
	if _, err := backend.GetSchema(context.Background(), fixedResource()); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		start          time.Time
		end            time.Time
		wantCount      int64
		wantTopError   string
		wantTopCount   int64
		wantQueryToken string
	}{
		{
			name:           "current",
			start:          fixtureCurrentStart,
			end:            fixtureCurrentEnd,
			wantCount:      120,
			wantTopError:   "payment_timeout",
			wantTopCount:   90,
			wantQueryToken: "mock-current-count-before,mock-current-patterns,mock-current-instances,mock-current-count-after",
		},
		{
			name:           "baseline",
			start:          fixtureCurrentStart.Add(-fixtureCurrentEnd.Sub(fixtureCurrentStart)),
			end:            fixtureCurrentStart,
			wantCount:      20,
			wantTopError:   "inventory_lock",
			wantTopCount:   10,
			wantQueryToken: "mock-baseline-count-before,mock-baseline-patterns,mock-baseline-instances,mock-baseline-count-after",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := backend.Execute(context.Background(), approvedFixtureQuery(test.start, test.end))
			if err != nil {
				t.Fatal(err)
			}
			if result.ErrorCount != test.wantCount || result.TopError != test.wantTopError || result.TopErrorCount != test.wantTopCount {
				t.Fatalf("fixture data drifted: %#v", result)
			}
			if result.QueryID != test.wantQueryToken || result.QuerySpecHash == "" {
				t.Fatalf("query identity metadata is incomplete: %#v", result)
			}
			if result.ResourceID != mockResourceID || result.TemplateID != domain.ErrorAnalysisTemplateID || result.TemplateVersion == "" || result.SchemaFingerprint == "" || result.PolicyVersion == "" {
				t.Fatalf("governance metadata is incomplete: %#v", result)
			}
			if result.APICalls != domain.ErrorAnalysisAPICalls || !result.Complete || result.Progress != "Complete" || !result.UsageKnown || !result.NanosecondOrderedKnown || !result.NanosecondOrdered {
				t.Fatalf("provider completion metadata is incomplete: %#v", result)
			}
			if result.ProcessedRows <= 0 || result.ProcessedBytes <= 0 || result.ElapsedMillisecond <= 0 || result.PatternLimit != domain.ErrorAnalysisPatternLimit || result.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
				t.Fatalf("provider usage metadata is incomplete: %#v", result)
			}
			assertNoRawLogs(t, result)
		})
	}

	if got, want := backend.Stats(), (Stats{SchemaCalls: 1, ExecuteCalls: 2, ProviderAPICalls: 8}); got != want {
		t.Fatalf("unexpected observable mock-provider calls: got=%#v want=%#v", got, want)
	}
}

func TestBackendRejectsEveryUnknownWindow(t *testing.T) {
	backend := newFixtureBackend(t)
	duration := fixtureCurrentEnd.Sub(fixtureCurrentStart)
	tests := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{name: "shifted by nanosecond", start: fixtureCurrentStart.Add(time.Nanosecond), end: fixtureCurrentEnd.Add(time.Nanosecond)},
		{name: "wrong duration", start: fixtureCurrentStart, end: fixtureCurrentEnd.Add(time.Second)},
		{name: "older adjacent window", start: fixtureCurrentStart.Add(-2 * duration), end: fixtureCurrentStart.Add(-duration)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := backend.Execute(context.Background(), approvedFixtureQuery(test.start, test.end))
			if err == nil {
				t.Fatalf("unknown window returned plausible evidence: %#v", result)
			}
			if !reflect.DeepEqual(result, domain.QueryResult{}) {
				t.Fatalf("failed-closed window leaked a partial result: %#v", result)
			}
		})
	}
}

func TestMockCatalogAndBackendHonorCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	catalog := NewCatalog(fixturePrincipal)
	if _, err := catalog.Resolve(ctx, mockService, mockEnvironment); !errors.Is(err, context.Canceled) {
		t.Fatalf("catalog ignored cancellation: %v", err)
	}
	if catalog.Allowed(ctx, fixturePrincipal, mockResourceID) {
		t.Fatal("ACL ignored cancellation")
	}

	backend := newFixtureBackend(t)
	if _, err := backend.GetSchema(ctx, fixedResource()); !errors.Is(err, context.Canceled) {
		t.Fatalf("schema lookup ignored cancellation: %v", err)
	}
	if _, err := backend.Execute(ctx, approvedFixtureQuery(fixtureCurrentStart, fixtureCurrentEnd)); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
}

func TestNewBackendRejectsInvalidCurrentWindow(t *testing.T) {
	if _, err := NewBackend(time.Time{}, fixtureCurrentEnd); err == nil {
		t.Fatal("zero current start was accepted")
	}
	if _, err := NewBackend(fixtureCurrentEnd, fixtureCurrentStart); err == nil {
		t.Fatal("backwards current window was accepted")
	}
}

var (
	fixtureCurrentStart = time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	fixtureCurrentEnd   = fixtureCurrentStart.Add(30 * time.Minute)
)

func newFixtureBackend(t *testing.T) *Backend {
	t.Helper()
	backend, err := NewBackend(fixtureCurrentStart, fixtureCurrentEnd)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func approvedFixtureQuery(start, end time.Time) domain.ApprovedQuery {
	return domain.ApprovedQuery{
		SpecHash:          "approved-mock-query",
		Resource:          fixedResource(),
		TemplateID:        domain.ErrorAnalysisTemplateID,
		PolicyVersion:     "query-policy-v2",
		SchemaFingerprint: "mock-schema-v2",
		StartTime:         start,
		EndTime:           end,
		MaxRows:           domain.ErrorAnalysisResultRows,
		MaxAPICalls:       domain.ErrorAnalysisAPICalls,
		PatternLimit:      domain.ErrorAnalysisPatternLimit,
		InstanceLimit:     domain.ErrorAnalysisInstanceLimit,
		ExpectedAPICalls:  domain.ErrorAnalysisAPICalls,
	}
}

func assertNoRawLogs(t *testing.T, result domain.QueryResult) {
	t.Helper()
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"logs", "raw_logs", "records", "messages"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("mock query result exposed raw provider data in %q", forbidden)
		}
	}
}
