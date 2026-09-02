package trace_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/traceresourcecatalog"
	traceapp "logagent/internal/application/trace"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type backendStub struct {
	schema      domain.IndexSchema
	result      domain.TraceBackendResult
	searchCalls int
}

func (b *backendStub) GetTraceSchema(context.Context, domain.TraceResourceMember) (domain.IndexSchema, error) {
	return b.schema, nil
}

func (b *backendStub) SearchTrace(_ context.Context, _ domain.ApprovedTraceQuery) (domain.TraceBackendResult, error) {
	b.searchCalls++
	return b.result, nil
}

type auditStub struct{ entries []domain.TraceAudit }

func (a *auditStub) RecordTraceAudit(_ context.Context, value domain.TraceAudit) error {
	a.entries = append(a.entries, value)
	return nil
}

func TestGatewayRedactsTraceIDAndSensitiveLogTextBeforeEvidence(t *testing.T) {
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"}
	catalog := newCatalog(t, principal)
	backend := &backendStub{
		schema: domain.IndexSchema{Fingerprint: strings.Repeat("a", 64), FullText: true, Fields: map[string]domain.IndexField{
			"env": {Type: "text"}, "msg": {Type: "text"}, "level": {Type: "text"},
		}},
		result: domain.TraceBackendResult{
			ExecutionID: "exec-1", Progress: "Complete", UsageKnown: true, APICalls: 1,
			ProcessedRows: 1, ProcessedBytes: 128, ElapsedMillisecond: 2,
			Events: []domain.TraceBackendEvent{{
				EventTimeRaw: "1788335990", Level: "error",
				Message: "trace-12345678 Bearer abcdefghijklmnop user@example.com 10.0.0.1 https://example.com/path?token=secret",
			}},
		},
	}
	auditor := &auditStub{}
	gateway := newGateway(t, catalog, backend, auditor, now)
	plan, err := gateway.ResolveTraceGovernance(context.Background(), domain.TraceSearchSpec{
		InvestigationID: "inv-1", Service: "dam-server", Environment: "test", TraceID: "trace-12345678",
		StartTime: now.Add(-10 * time.Minute), EndTime: now.Add(-10 * time.Second), Requester: principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := gateway.ExecuteTraceMember(context.Background(), plan, "dam-server")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.TraceMemberComplete || len(result.Events) != 1 || !result.Events[0].Redacted {
		t.Fatalf("unexpected normalized result: %#v", result)
	}
	message := result.Events[0].Message
	for _, secret := range []string{"trace-12345678", "abcdefghijklmnop", "user@example.com", "10.0.0.1", "token=secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("persistable event leaked %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "[TRACE_ID]") || !strings.Contains(message, "https://example.com/path") {
		t.Fatalf("redacted message lost useful safe context: %q", message)
	}
	if len(auditor.entries) != 2 || auditor.entries[0].Outcome != "STARTED" || auditor.entries[1].Outcome != "SUCCEEDED" {
		t.Fatalf("unexpected audit sequence: %#v", auditor.entries)
	}
}

func TestGatewayRejectsUnsafeTraceBeforeBackend(t *testing.T) {
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"}
	backend := &backendStub{}
	auditor := &auditStub{}
	gateway := newGateway(t, newCatalog(t, principal), backend, auditor, now)
	_, err := gateway.ResolveTraceGovernance(context.Background(), domain.TraceSearchSpec{
		InvestigationID: "inv-1", Service: "dam-server", Environment: "test", TraceID: `* | SELECT secret`,
		StartTime: now.Add(-time.Minute), EndTime: now.Add(-10 * time.Second), Requester: principal,
	})
	if !errors.Is(err, ports.ErrQueryDenied) || backend.searchCalls != 0 {
		t.Fatalf("unsafe Trace was not rejected before backend: err=%v calls=%d", err, backend.searchCalls)
	}
	if len(auditor.entries) != 1 || auditor.entries[0].Outcome != "DENIED" || auditor.entries[0].TraceIDFingerprint == "" {
		t.Fatalf("missing safe deny audit: %#v", auditor.entries)
	}
}

func newGateway(t *testing.T, catalog *traceresourcecatalog.Catalog, backend *backendStub, auditor *auditStub, now time.Time) *traceapp.Gateway {
	t.Helper()
	gateway, err := traceapp.NewGateway(catalog, backend, auditor, traceapp.Budget{
		MaxWindow: 30 * time.Minute, IngestionGrace: 10 * time.Second, Timeout: time.Second,
		MemberLimit: 50, GlobalLimit: 500, MaxProcessedBytes: 1024 * 1024, MaxConcurrency: 2, RetryIncomplete: 1,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func newCatalog(t *testing.T, principal domain.Principal) *traceresourcecatalog.Catalog {
	t.Helper()
	group := domain.TraceResourceGroup{
		ID: "dam-trace-test", CatalogVersion: "test-v1", Service: "dam-server", Environment: "test",
		TemplateVersion: domain.TraceSearchTemplateVersion, PrimaryMemberID: "dam-server",
		Members: []domain.TraceResourceMember{{
			ID: "dam-server", Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha",
			LogStore: "2016-hyper-dam-file", TraceMode: domain.TraceQueryFullText,
			EnvironmentMode: domain.TraceEnvironmentField, EnvironmentField: "env", MessageField: "msg",
			LevelField: "level", EventTimeField: "__time__",
		}},
	}
	catalog, err := traceresourcecatalog.New([]domain.TraceResourceGroup{group}, map[domain.Principal][]string{principal: {group.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
