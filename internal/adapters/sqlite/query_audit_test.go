package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestRecordQueryAuditAppendsAndPreservesFields(t *testing.T) {
	store := openTestStore(t)
	first := testQueryAudit("inv_one", time.Date(2026, 8, 18, 11, 0, 0, 123456789, time.UTC))
	second := first
	second.Outcome = "INCOMPLETE"
	second.Reason = "provider result was limited"
	second.Complete = false
	second.Truncated = true
	second.ProcessedRows = 84
	second.ProcessedBytes = 8192
	second.OccurredAt = first.OccurredAt.Add(time.Second)
	otherInvestigation := first
	otherInvestigation.InvestigationID = "inv_two"

	for _, audit := range []domain.QueryAudit{first, otherInvestigation, first, second} {
		if err := store.RecordQueryAudit(context.Background(), audit); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListQueryAudits(context.Background(), "inv_one")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.QueryAudit{first, first, second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query audit round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestQueryAuditSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query-audit.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	audit := testQueryAudit("inv_restart", time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err := store.RecordQueryAudit(context.Background(), audit); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ListQueryAudits(context.Background(), audit.InvestigationID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []domain.QueryAudit{audit}) {
		t.Fatalf("query audit after restart mismatch: got %#v want %#v", got, audit)
	}
}

func TestQueryAuditSchemaContainsOnlyApprovedFields(t *testing.T) {
	store := openTestStore(t)
	rows, err := store.db.QueryContext(context.Background(), `
SELECT name
FROM pragma_table_info('query_audit')
ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"audit_id",
		"investigation_id",
		"principal_app_id",
		"principal_tenant_key",
		"principal_user_id",
		"resource_id",
		"template_id",
		"template_version",
		"query_spec_hash",
		"schema_fingerprint",
		"policy_version",
		"outcome",
		"reason",
		"provider_request_id",
		"progress",
		"complete",
		"truncated",
		"processed_rows",
		"processed_bytes",
		"occurred_at",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected query audit columns: got %v want %v", got, want)
	}
}

func testQueryAudit(investigationID string, occurredAt time.Time) domain.QueryAudit {
	return domain.QueryAudit{
		InvestigationID: investigationID,
		Principal: domain.Principal{
			AppID:     "app",
			TenantKey: "tenant",
			UserID:    "user",
		},
		ResourceID:        "resource-order-prod",
		TemplateID:        domain.ErrorSummaryTemplateID,
		TemplateVersion:   "1",
		QuerySpecHash:     "spec-sha256",
		SchemaFingerprint: "schema-sha256",
		PolicyVersion:     "policy-v1",
		Outcome:           "SUCCEEDED",
		Reason:            "bounded aggregate completed",
		ProviderRequestID: "provider-request-id",
		Progress:          "Complete",
		Complete:          true,
		Truncated:         false,
		ProcessedRows:     42,
		ProcessedBytes:    4096,
		OccurredAt:        occurredAt,
	}
}
