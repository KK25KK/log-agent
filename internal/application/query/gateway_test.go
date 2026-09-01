package query

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type fakeCatalog struct {
	resource   domain.LogResource
	resolveErr error
	allowed    bool
}

func (f fakeCatalog) Resolve(_ context.Context, _, _ string) (domain.LogResource, error) {
	return f.resource, f.resolveErr
}

func (f fakeCatalog) Allowed(_ context.Context, _ domain.Principal, _ string) bool {
	return f.allowed
}

type fakeBackend struct {
	mu           sync.Mutex
	schema       domain.IndexSchema
	result       domain.QueryResult
	schemaErr    error
	executeErr   error
	schemaCalls  int
	execCalls    int
	approved     []domain.ApprovedQuery
	started      chan struct{}
	release      chan struct{}
	executeDelay time.Duration
}

func (f *fakeBackend) GetSchema(_ context.Context, _ domain.LogResource) (domain.IndexSchema, error) {
	f.mu.Lock()
	f.schemaCalls++
	f.mu.Unlock()
	return f.schema, f.schemaErr
}

func (f *fakeBackend) Execute(ctx context.Context, approved domain.ApprovedQuery) (domain.QueryResult, error) {
	f.mu.Lock()
	f.execCalls++
	f.approved = append(f.approved, approved)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return domain.QueryResult{}, ctx.Err()
		case <-f.release:
		}
	}
	if f.executeDelay > 0 {
		// Deliberately ignores ctx to verify that the gateway rejects a result
		// returned after its application deadline, regardless of transport.
		time.Sleep(f.executeDelay)
	}
	return f.result, f.executeErr
}

func (f *fakeBackend) lastApproved() domain.ApprovedQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.approved) == 0 {
		return domain.ApprovedQuery{}
	}
	return f.approved[len(f.approved)-1]
}

func (f *fakeBackend) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.schemaCalls, f.execCalls
}

type fakeAuditor struct {
	mu          sync.Mutex
	events      []domain.QueryAudit
	failOutcome string
}

func (f *fakeAuditor) RecordQueryAudit(_ context.Context, audit domain.QueryAudit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if audit.Outcome == f.failOutcome {
		return errors.New("audit unavailable")
	}
	f.events = append(f.events, audit)
	return nil
}

func TestGatewayExecutesAuthorizedFixedTemplate(t *testing.T) {
	backend := validBackend()
	backend.result.ErrorPatterns[0].Label = "payment failed for user@example.com from 10.1.2.3"
	backend.result.Instances[0].Label = "10.2.3.4"
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

	result, err := gateway.Execute(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.ResourceID != "order-prod" || result.QuerySpecHash == "" || !validFingerprint(result.GovernanceFingerprint) {
		t.Fatalf("unexpected governed result: %#v", result)
	}
	if result.TopError != "payment failed for [REDACTED_EMAIL] from [REDACTED_IP]" || !result.Redacted {
		t.Fatalf("sensitive aggregate label escaped: %#v", result)
	}
	if !result.ErrorPatterns[0].Redacted || result.Instances[0].Label != "[REDACTED_IP]" || !result.Instances[0].Redacted {
		t.Fatalf("bucket-level redaction markers were lost: %#v", result)
	}
	if _, calls := backend.calls(); calls != 1 {
		t.Fatalf("want one backend query, got %d", calls)
	}
	if backend.lastApproved().GovernanceFingerprint != result.GovernanceFingerprint {
		t.Fatalf("approved query/result governance mismatch: approved=%#v result=%#v", backend.lastApproved(), result)
	}
	if len(auditor.events) != 2 || auditor.events[0].Outcome != "STARTED" || auditor.events[1].Outcome != "SUCCEEDED" {
		t.Fatalf("unexpected audit lifecycle: %#v", auditor.events)
	}
	if auditor.events[1].ProviderRequestID != "req-count-before,req-patterns,req-instances,req-count-after" {
		t.Fatalf("provider request ID not audited: %#v", auditor.events[1])
	}
}

func TestGatewayDoesNotRelabelExecutionIDAsProviderRequestID(t *testing.T) {
	backend := validBackend()
	backend.result.ProviderRequestID = ""
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

	result, err := gateway.Execute(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryID == "" || len(auditor.events) != 2 || auditor.events[1].ProviderRequestID != "" {
		t.Fatalf("local execution ID was relabeled as a provider ID: result=%#v audit=%#v", result, auditor.events)
	}
}

func TestResolveQueryGovernanceMatchesExecutionWithoutReadingLogs(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

	governanceFingerprint, err := gateway.ResolveQueryGovernance(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !validFingerprint(governanceFingerprint) {
		t.Fatalf("invalid governance fingerprint %q", governanceFingerprint)
	}
	if schemaCalls, executeCalls := backend.calls(); schemaCalls != 1 || executeCalls != 0 {
		t.Fatalf("governance resolution read log rows: schema=%d execute=%d", schemaCalls, executeCalls)
	}
	if len(auditor.events) != 0 {
		t.Fatalf("successful metadata resolution emitted query lifecycle audit: %#v", auditor.events)
	}

	result, err := gateway.Execute(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.GovernanceFingerprint != governanceFingerprint {
		t.Fatalf("resolve/execute governance drift: resolved=%s result=%s", governanceFingerprint, result.GovernanceFingerprint)
	}
}

func TestQueryGovernanceFingerprintBindsCatalogPhysicalSchemaPolicyAndBudget(t *testing.T) {
	baseResource := testResource()
	baseSchema := validBackend().schema
	baseBudget := testBudget()
	base, err := queryGovernanceFingerprint(baseResource, domain.ErrorAnalysisTemplateID, PolicyVersion, baseSchema.Fingerprint, baseBudget)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		resource domain.LogResource
		schema   string
		policy   string
		budget   Budget
	}{
		"catalog version":  {resource: func() domain.LogResource { value := baseResource; value.CatalogVersion = "catalog-v2"; return value }(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"physical project": {resource: func() domain.LogResource { value := baseResource; value.Project = "project-migrated"; return value }(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"endpoint": {resource: func() domain.LogResource {
			value := baseResource
			value.Endpoint = "https://cn-shanghai.log.aliyuncs.com"
			return value
		}(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"logstore": {resource: func() domain.LogResource { value := baseResource; value.LogStore = "logstore-v2"; return value }(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"selector": {resource: func() domain.LogResource {
			value := baseResource
			value.Selectors = append([]domain.LogSelector(nil), value.Selectors...)
			value.Selectors[0].Value = "order-service-v2"
			return value
		}(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"indexed field":   {resource: func() domain.LogResource { value := baseResource; value.ErrorField = "error_code"; return value }(), schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: baseBudget},
		"schema":          {resource: baseResource, schema: "schema-v3", policy: PolicyVersion, budget: baseBudget},
		"policy":          {resource: baseResource, schema: baseSchema.Fingerprint, policy: "query-policy-v3", budget: baseBudget},
		"processed bytes": {resource: baseResource, schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: func() Budget { value := baseBudget; value.MaxProcessedBytes++; return value }()},
		"timeout":         {resource: baseResource, schema: baseSchema.Fingerprint, policy: PolicyVersion, budget: func() Budget { value := baseBudget; value.Timeout += time.Second; return value }()},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := queryGovernanceFingerprint(test.resource, domain.ErrorAnalysisTemplateID, test.policy, test.schema, test.budget)
			if err != nil {
				t.Fatal(err)
			}
			if !validFingerprint(got) || got == base {
				t.Fatalf("%s drift did not invalidate governance: base=%s got=%s", name, base, got)
			}
		})
	}
}

func TestResolveQueryGovernanceDenialIsAuditedBeforeLogRead(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: false}, backend, auditor, testBudget())

	_, err := gateway.ResolveQueryGovernance(context.Background(), testSpec())
	if !errors.Is(err, ports.ErrQueryDenied) {
		t.Fatalf("want query denial, got %v", err)
	}
	if schemaCalls, executeCalls := backend.calls(); schemaCalls != 0 || executeCalls != 0 {
		t.Fatalf("denied governance reached provider: schema=%d execute=%d", schemaCalls, executeCalls)
	}
	if len(auditor.events) != 1 || auditor.events[0].Outcome != "DENIED" || auditor.events[0].Reason != "acl_denied" {
		t.Fatalf("unexpected governance denial audit: %#v", auditor.events)
	}
}

func TestGatewayRejectsUnauthorizedRequestBeforeProviderCalls(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: false}, backend, auditor, testBudget())

	_, err := gateway.Execute(context.Background(), testSpec())
	if !errors.Is(err, ports.ErrQueryDenied) {
		t.Fatalf("want query denied, got %v", err)
	}
	schemaCalls, execCalls := backend.calls()
	if schemaCalls != 0 || execCalls != 0 {
		t.Fatalf("provider was called before authorization: schema=%d execute=%d", schemaCalls, execCalls)
	}
	if len(auditor.events) != 1 || auditor.events[0].Outcome != "DENIED" || auditor.events[0].Reason != "acl_denied" {
		t.Fatalf("unexpected denied audit: %#v", auditor.events)
	}
}

func TestGatewayRejectsWindowBeforeIngestionWatermark(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	gateway.now = func() time.Time { return now }
	spec := testSpec()
	spec.EndTime = now.Add(-gateway.budget.IngestionGrace).Add(time.Second)
	spec.StartTime = spec.EndTime.Add(-30 * time.Minute)

	_, err := gateway.Execute(context.Background(), spec)
	if !errors.Is(err, ports.ErrQueryBudgetExceeded) {
		t.Fatalf("fresh window was not rejected: %v", err)
	}
	if schemaCalls, queryCalls := backend.calls(); schemaCalls != 0 || queryCalls != 0 {
		t.Fatalf("provider called before ingestion watermark: schema=%d query=%d", schemaCalls, queryCalls)
	}
	if len(auditor.events) != 1 || auditor.events[0].Outcome != "DENIED" {
		t.Fatalf("fresh-window denial was not audited: %#v", auditor.events)
	}
}

func TestGatewayRejectsOverWindowBeforeProviderCalls(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{}
	budget := testBudget()
	budget.MaxWindow = time.Minute
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, budget)

	_, err := gateway.Execute(context.Background(), testSpec())
	if !errors.Is(err, ports.ErrQueryBudgetExceeded) {
		t.Fatalf("want budget error, got %v", err)
	}
	if schemaCalls, execCalls := backend.calls(); schemaCalls != 0 || execCalls != 0 {
		t.Fatalf("provider was called for over-window query: schema=%d execute=%d", schemaCalls, execCalls)
	}
}

func TestGatewayRejectsInvalidRequestsBeforeProviderCalls(t *testing.T) {
	tests := []struct {
		name       string
		catalog    fakeCatalog
		mutateSpec func(*domain.QuerySpec)
	}{
		{
			name:    "unknown resource",
			catalog: fakeCatalog{resolveErr: errors.New("resource not found")},
		},
		{
			name:    "incomplete principal",
			catalog: fakeCatalog{resource: testResource(), allowed: true},
			mutateSpec: func(spec *domain.QuerySpec) {
				spec.Requester.UserID = ""
			},
		},
		{
			name:    "unknown template",
			catalog: fakeCatalog{resource: testResource(), allowed: true},
			mutateSpec: func(spec *domain.QuerySpec) {
				spec.TemplateID = "caller_supplied_sql"
			},
		},
		{
			name:    "invalid time range",
			catalog: fakeCatalog{resource: testResource(), allowed: true},
			mutateSpec: func(spec *domain.QuerySpec) {
				spec.StartTime = spec.EndTime
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := validBackend()
			auditor := &fakeAuditor{}
			gateway := newTestGateway(t, test.catalog, backend, auditor, testBudget())
			spec := testSpec()
			if test.mutateSpec != nil {
				test.mutateSpec(&spec)
			}

			_, err := gateway.Execute(context.Background(), spec)
			if !errors.Is(err, ports.ErrQueryDenied) {
				t.Fatalf("want query denied, got %v", err)
			}
			if schemaCalls, execCalls := backend.calls(); schemaCalls != 0 || execCalls != 0 {
				t.Fatalf("provider called for rejected request: schema=%d execute=%d", schemaCalls, execCalls)
			}
			if len(auditor.events) != 1 || auditor.events[0].Outcome != "DENIED" {
				t.Fatalf("unexpected denied audit: %#v", auditor.events)
			}
		})
	}
}

func TestGatewayRejectsInvalidSchemaBeforeLogQuery(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]domain.IndexField)
	}{
		{name: "missing selector", mutate: func(fields map[string]domain.IndexField) { delete(fields, "service") }},
		{name: "missing error dimension", mutate: func(fields map[string]domain.IndexField) { delete(fields, "error_type") }},
		{name: "wrong error dimension type", mutate: func(fields map[string]domain.IndexField) {
			fields["error_type"] = domain.IndexField{Type: "long", DocValue: true}
		}},
		{name: "statistics disabled", mutate: func(fields map[string]domain.IndexField) {
			fields["error_type"] = domain.IndexField{Type: "text", DocValue: false}
		}},
		{name: "missing instance dimension", mutate: func(fields map[string]domain.IndexField) { delete(fields, "pod_name") }},
		{name: "wrong instance dimension type", mutate: func(fields map[string]domain.IndexField) {
			fields["pod_name"] = domain.IndexField{Type: "long", DocValue: true}
		}},
		{name: "instance statistics disabled", mutate: func(fields map[string]domain.IndexField) {
			fields["pod_name"] = domain.IndexField{Type: "text", DocValue: false}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := validBackend()
			test.mutate(backend.schema.Fields)
			auditor := &fakeAuditor{}
			gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

			_, err := gateway.Execute(context.Background(), testSpec())
			if !errors.Is(err, ports.ErrInvalidQuerySchema) {
				t.Fatalf("want schema error, got %v", err)
			}
			if schemaCalls, execCalls := backend.calls(); schemaCalls != 1 || execCalls != 0 {
				t.Fatalf("unexpected provider calls: schema=%d execute=%d", schemaCalls, execCalls)
			}
		})
	}
}

func TestGatewayFailsClosedWhenStartAuditCannotPersist(t *testing.T) {
	backend := validBackend()
	auditor := &fakeAuditor{failOutcome: "STARTED"}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

	_, err := gateway.Execute(context.Background(), testSpec())
	if err == nil {
		t.Fatal("want audit failure")
	}
	if _, execCalls := backend.calls(); execCalls != 0 {
		t.Fatalf("backend query ran despite failed start audit: %d", execCalls)
	}
}

func TestGatewayMarksProcessedByteOverflowIncomplete(t *testing.T) {
	backend := validBackend()
	backend.result.ProcessedBytes = 2048
	auditor := &fakeAuditor{}
	budget := testBudget()
	budget.MaxProcessedBytes = 1024
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, budget)

	result, err := gateway.Execute(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.IncompleteReason == "" {
		t.Fatalf("scan overflow was not gated: %#v", result)
	}
	if auditor.events[len(auditor.events)-1].Outcome != "INCOMPLETE" {
		t.Fatalf("unexpected terminal audit: %#v", auditor.events)
	}
}

func TestGatewayPreservesProviderCompletenessGates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.QueryResult)
	}{
		{name: "incomplete progress", mutate: func(result *domain.QueryResult) { result.Progress = "Incomplete" }},
		{name: "usage unknown", mutate: func(result *domain.QueryResult) { result.UsageKnown = false }},
		{name: "provider truncation", mutate: func(result *domain.QueryResult) { result.Truncated = true }},
		{name: "wrong API call count", mutate: func(result *domain.QueryResult) { result.APICalls = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := validBackend()
			test.mutate(&backend.result)
			auditor := &fakeAuditor{}
			gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

			result, err := gateway.Execute(context.Background(), testSpec())
			if err != nil {
				t.Fatal(err)
			}
			if result.Complete {
				t.Fatalf("quality signal was collapsed to complete: %#v", result)
			}
			if auditor.events[len(auditor.events)-1].Outcome != "INCOMPLETE" {
				t.Fatalf("unexpected terminal audit: %#v", auditor.events)
			}
		})
	}
}

func TestGatewayTreatsIsAccurateAsNanosecondOrderingMetadata(t *testing.T) {
	backend := validBackend()
	backend.result.NanosecondOrderedKnown = true
	backend.result.NanosecondOrdered = false
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, &fakeAuditor{}, testBudget())

	result, err := gateway.Execute(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.NanosecondOrdered {
		t.Fatalf("nanosecond ordering was treated as an aggregation quality gate: %#v", result)
	}
}

func TestGatewayRequiresFixedAnalysisBudget(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Budget)
	}{
		{name: "API calls", mutate: func(budget *Budget) { budget.MaxAPICalls = domain.ErrorAnalysisAPICalls - 1 }},
		{name: "result rows", mutate: func(budget *Budget) { budget.MaxRows = domain.ErrorAnalysisResultRows - 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			budget := testBudget()
			test.mutate(&budget)
			if _, err := NewGateway(fakeCatalog{}, validBackend(), &fakeAuditor{}, budget); err == nil {
				t.Fatal("want fixed-template budget error")
			}
		})
	}
}

func TestGatewayDoesNotReturnSuccessWhenTerminalAuditFails(t *testing.T) {
	backend := validBackend()
	gateway := newTestGateway(
		t,
		fakeCatalog{resource: testResource(), allowed: true},
		backend,
		&fakeAuditor{failOutcome: "SUCCEEDED"},
		testBudget(),
	)

	if _, err := gateway.Execute(context.Background(), testSpec()); err == nil {
		t.Fatal("want terminal audit failure")
	}
}

func TestGatewayAuditsBackendFailure(t *testing.T) {
	backend := validBackend()
	backend.executeErr = errors.New("provider unavailable with selector-secret")
	auditor := &fakeAuditor{}
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, testBudget())

	if _, err := gateway.Execute(context.Background(), testSpec()); err == nil {
		t.Fatal("want provider failure")
	}
	if len(auditor.events) != 2 || auditor.events[0].Outcome != "STARTED" || auditor.events[1].Outcome != "FAILED" {
		t.Fatalf("unexpected failure audit lifecycle: %#v", auditor.events)
	}
	if auditor.events[1].Reason != "provider_error" {
		t.Fatalf("raw provider error escaped into audit: %#v", auditor.events[1])
	}
}

func TestGatewayRejectsProviderSuccessThatArrivesAfterDeadline(t *testing.T) {
	backend := validBackend()
	backend.executeDelay = 30 * time.Millisecond
	auditor := &fakeAuditor{}
	budget := testBudget()
	budget.Timeout = 10 * time.Millisecond
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, budget)

	result, err := gateway.Execute(context.Background(), testSpec())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline failure, got result=%#v err=%v", result, err)
	}
	if result.Complete {
		t.Fatalf("late provider result became complete evidence: %#v", result)
	}
	if len(auditor.events) != 2 || auditor.events[1].Outcome != "FAILED" {
		t.Fatalf("unexpected late-result audit lifecycle: %#v", auditor.events)
	}
}

func TestGatewayLimitsConcurrentQueriesAndReleasesPermit(t *testing.T) {
	backend := validBackend()
	backend.started = make(chan struct{}, 1)
	backend.release = make(chan struct{})
	auditor := &fakeAuditor{}
	budget := testBudget()
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, auditor, budget)

	firstDone := make(chan error, 1)
	go func() {
		_, err := gateway.Execute(context.Background(), testSpec())
		firstDone <- err
	}()
	<-backend.started

	secondCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := gateway.Execute(secondCtx, testSpec())
	if !errors.Is(err, ports.ErrQueryBudgetExceeded) {
		t.Fatalf("want concurrency budget error, got %v", err)
	}
	close(backend.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first query did not finish: %v", err)
	}

	if _, err := gateway.Execute(context.Background(), testSpec()); err != nil {
		t.Fatalf("limiter permit leaked: %v", err)
	}
}

func TestGatewayCachesSchemaForItsTTL(t *testing.T) {
	backend := validBackend()
	gateway := newTestGateway(t, fakeCatalog{resource: testResource(), allowed: true}, backend, &fakeAuditor{}, testBudget())

	for range 2 {
		if _, err := gateway.Execute(context.Background(), testSpec()); err != nil {
			t.Fatal(err)
		}
	}
	if schemaCalls, execCalls := backend.calls(); schemaCalls != 1 || execCalls != 2 {
		t.Fatalf("unexpected cache behavior: schema=%d execute=%d", schemaCalls, execCalls)
	}
}

func newTestGateway(t *testing.T, catalog ports.ResourceCatalog, backend ports.SLSBackend, auditor ports.QueryAuditor, budget Budget) *Gateway {
	t.Helper()
	gateway, err := NewGateway(catalog, backend, auditor, budget)
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func validBackend() *fakeBackend {
	return &fakeBackend{
		schema: domain.IndexSchema{
			Fingerprint: "schema-v2",
			Fields: map[string]domain.IndexField{
				"service":     {Type: "text"},
				"environment": {Type: "text"},
				"level":       {Type: "text"},
				"error_type":  {Type: "text", DocValue: true},
				"pod_name":    {Type: "text", DocValue: true},
			},
		},
		result: domain.QueryResult{
			QueryID: "exec-count-before,exec-patterns,exec-instances,exec-count-after", ProviderRequestID: "req-count-before,req-patterns,req-instances,req-count-after", Progress: "Complete", NanosecondOrderedKnown: true, NanosecondOrdered: true,
			UsageKnown: true, ProcessedRows: 400, ProcessedBytes: 512, APICalls: domain.ErrorAnalysisAPICalls,
			ErrorCount: 100, TopError: "timeout", TopErrorCount: 80,
			ErrorPatterns:           []domain.CountBucket{{Label: "timeout", Count: 80}, {Label: "database", Count: 20}},
			Instances:               []domain.CountBucket{{Label: "order-pod-a", Count: 70}, {Label: "order-pod-b", Count: 30}},
			ErrorPatternsExhaustive: true, InstancesExhaustive: true,
			PatternLimit: domain.ErrorAnalysisPatternLimit, InstanceLimit: domain.ErrorAnalysisInstanceLimit,
		},
	}
}

func testResource() domain.LogResource {
	return domain.LogResource{
		ID: "order-prod", CatalogVersion: "catalog-v1", Service: "order-service", Environment: "prod",
		Endpoint: "https://cn-hangzhou.log.aliyuncs.com", Project: "project", LogStore: "logstore",
		TemplateVersion: domain.ErrorAnalysisTemplateVersion, ErrorField: "error_type", InstanceField: "pod_name",
		Selectors:     []domain.LogSelector{{Field: "service", Value: "order-service"}, {Field: "environment", Value: "prod"}},
		ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"},
	}
}

func testSpec() domain.QuerySpec {
	end := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	return domain.QuerySpec{
		InvestigationID: "inv_test", Name: "current", TemplateID: domain.ErrorAnalysisTemplateID,
		Service: "order-service", Environment: "prod", StartTime: end.Add(-30 * time.Minute), EndTime: end,
		Requester: domain.Principal{AppID: "cli_test", TenantKey: "tenant", UserID: "ou_allow"},
	}
}

func testBudget() Budget {
	return Budget{
		MaxWindow: 2 * time.Hour, IngestionGrace: domain.DefaultIngestionGrace, Timeout: time.Second, MaxRows: domain.ErrorAnalysisResultRows, MaxAPICalls: domain.ErrorAnalysisAPICalls,
		MaxProcessedBytes: 1024 * 1024, MaxConcurrent: 1, SchemaTTL: time.Minute,
	}
}
