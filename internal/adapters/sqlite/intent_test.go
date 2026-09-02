package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/domain"
)

func TestOpenMigratesLegacyDatabaseAndRecordsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_marker (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version=%d, want 2", version)
	}
	var table string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='intent_resolutions'`).Scan(&table); err != nil || table != "intent_resolutions" {
		t.Fatalf("intent table missing: table=%q err=%v", table, err)
	}
}

func TestOpenUpgradesVersionOneIntentTableWithTraceColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-one.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE intent_resolutions (
 id TEXT PRIMARY KEY, app_id TEXT NOT NULL, tenant_key TEXT NOT NULL, user_id TEXT NOT NULL,
 source_message_id TEXT NOT NULL, problem_text TEXT NOT NULL, problem_fingerprint TEXT NOT NULL,
 problem_redacted INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, intent TEXT NOT NULL DEFAULT '',
 service TEXT NOT NULL DEFAULT '', environment TEXT NOT NULL DEFAULT '', duration_seconds INTEGER NOT NULL DEFAULT 0,
 template_id TEXT NOT NULL DEFAULT '', confidence REAL NOT NULL DEFAULT 0, provider TEXT NOT NULL, model TEXT NOT NULL,
 request_id TEXT NOT NULL DEFAULT '', prompt_version TEXT NOT NULL, prompt_fingerprint TEXT NOT NULL,
 input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
 latency_millis INTEGER NOT NULL DEFAULT 0, reason_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL, confirmed_at INTEGER NOT NULL DEFAULT 0, investigation_id TEXT,
 UNIQUE(app_id, tenant_key, source_message_id)
);
PRAGMA user_version = 1;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`PRAGMA table_info(intent_resolutions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range []string{"trace_id", "trace_id_fingerprint", "trace_id_hint"} {
		if !found[name] {
			t.Fatalf("version-one migration did not add %s", name)
		}
	}
}

func TestIntentResolutionRoundTripsProtectedTraceFields(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	resolution := domain.IntentResolution{
		ID: "intent-trace", Principal: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
		SourceMessageID: "message-trace", Problem: domain.ProblemStatement{Text: "查 Trace", Fingerprint: strings.Repeat("a", 64)},
		Status: domain.IntentResolutionParsing, Provider: "mock", Model: "mock", PromptVersion: domain.IntentPromptVersion,
		PromptFingerprint: strings.Repeat("b", 64), CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	if _, created, err := store.BeginIntentResolution(context.Background(), resolution); err != nil || !created {
		t.Fatalf("begin Trace intent: created=%v err=%v", created, err)
	}
	resolution.Status = domain.IntentResolutionResolved
	resolution.Intent = domain.IntentTraceSearch
	resolution.Service = "dam-server"
	resolution.Environment = "test"
	resolution.DurationSeconds = 600
	resolution.TraceID = "trace-12345678"
	resolution.TraceIDFingerprint = strings.Repeat("c", 64)
	resolution.TraceIDHint = "trac…5678"
	resolution.TemplateID = domain.TraceSearchTemplateID
	resolution.Confidence = .99
	if err := store.CompleteIntentResolution(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetIntentResolution(context.Background(), resolution.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TraceID != resolution.TraceID || loaded.TraceIDFingerprint != resolution.TraceIDFingerprint || loaded.TraceIDHint != resolution.TraceIDHint {
		t.Fatalf("Trace intent fields did not round trip: %#v", loaded)
	}
}
