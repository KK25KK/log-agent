package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const schema = `
CREATE TABLE IF NOT EXISTS investigations (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    request_json TEXT NOT NULL,
    report_json TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS inbox (
    app_id TEXT NOT NULL,
    tenant_key TEXT NOT NULL,
    message_id TEXT NOT NULL,
    investigation_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    raw_text TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    PRIMARY KEY (app_id, tenant_key, message_id),
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    investigation_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS evidence (
    id TEXT PRIMARY KEY,
    investigation_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS evidence_ledger (
    entry_id TEXT PRIMARY KEY,
    investigation_id TEXT NOT NULL,
    hypothesis_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS query_audit (
    audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
    investigation_id TEXT NOT NULL,
    principal_app_id TEXT NOT NULL,
    principal_tenant_key TEXT NOT NULL,
    principal_user_id TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    template_id TEXT NOT NULL,
    template_version TEXT NOT NULL,
    query_spec_hash TEXT NOT NULL,
    schema_fingerprint TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL,
    provider_request_id TEXT NOT NULL,
    progress TEXT NOT NULL,
    complete INTEGER NOT NULL CHECK (complete IN (0, 1)),
    truncated INTEGER NOT NULL CHECK (truncated IN (0, 1)),
    processed_rows INTEGER NOT NULL,
    processed_bytes INTEGER NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS query_steps (
    investigation_id TEXT NOT NULL,
    step_key TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    job_attempt INTEGER NOT NULL,
    lease_owner TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '',
    output_hash TEXT NOT NULL DEFAULT '',
    reason_code TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    completed_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (investigation_id, step_key),
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS interaction_targets (
    investigation_id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL,
    tenant_key TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    source_message_id TEXT NOT NULL,
    card_message_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE TABLE IF NOT EXISTS delivery_events (
    id TEXT PRIMARY KEY,
    investigation_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until INTEGER NOT NULL DEFAULT 0,
    available_at INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (investigation_id, kind),
    FOREIGN KEY (investigation_id) REFERENCES investigations(id)
);

CREATE INDEX IF NOT EXISTS idx_jobs_claim
ON jobs(status, lease_until, created_at);

CREATE INDEX IF NOT EXISTS idx_query_audit_investigation
ON query_audit(investigation_id, audit_id);

CREATE INDEX IF NOT EXISTS idx_query_steps_status
ON query_steps(status, updated_at);

CREATE INDEX IF NOT EXISTS idx_evidence_ledger_investigation
ON evidence_ledger(investigation_id, hypothesis_id, entry_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_interaction_card
ON interaction_targets(app_id, tenant_key, card_message_id)
WHERE card_message_id <> '';

CREATE INDEX IF NOT EXISTS idx_delivery_claim
ON delivery_events(status, available_at, lease_until, created_at);
`

// Store is the SQLite technical-preview implementation of the durable contracts.
type Store struct {
	db *sql.DB
}

var _ ports.QueryAuditor = (*Store)(nil)

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection keeps :memory: databases coherent and serializes the
	// small technical-preview write workload. File databases remain process safe.
	db.SetMaxOpenConns(1)

	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if path != ":memory:" {
		if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("enable sqlite WAL: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sqlite schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) AcceptOnce(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest, investigationID, jobID string) (string, bool, error) {
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return "", false, fmt.Errorf("encode request: %w", err)
	}
	now := inbound.ReceivedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowMillis := now.UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin intake transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
INSERT INTO investigations(id, status, request_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)`, investigationID, domain.StatusQueued, requestJSON, nowMillis, nowMillis)
	if err != nil {
		return "", false, fmt.Errorf("insert investigation: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO inbox(app_id, tenant_key, message_id, investigation_id, chat_id, user_id, raw_text, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, tenant_key, message_id) DO NOTHING`,
		inbound.AppID, inbound.TenantKey, inbound.MessageID, investigationID,
		inbound.ChatID, inbound.UserID, inbound.Text, nowMillis)
	if err != nil {
		return "", false, fmt.Errorf("insert inbox: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("read inbox insert result: %w", err)
	}
	if inserted == 0 {
		if err := tx.Rollback(); err != nil {
			return "", false, fmt.Errorf("rollback duplicate intake: %w", err)
		}
		var existingID string
		err := s.db.QueryRowContext(ctx, `
SELECT investigation_id FROM inbox
WHERE app_id = ? AND tenant_key = ? AND message_id = ?`,
			inbound.AppID, inbound.TenantKey, inbound.MessageID).Scan(&existingID)
		if err != nil {
			return "", false, fmt.Errorf("load duplicate investigation: %w", err)
		}
		return existingID, false, nil
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO jobs(id, investigation_id, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)`, jobID, investigationID, domain.StatusQueued, nowMillis, nowMillis)
	if err != nil {
		return "", false, fmt.Errorf("insert job: %w", err)
	}
	if err := prepareInteractionTarget(ctx, tx, inbound, investigationID, nowMillis); err != nil {
		return "", false, err
	}
	if err := enqueueDelivery(ctx, tx, investigationID, domain.DeliveryQueued, nowMillis); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit intake: %w", err)
	}
	return investigationID, true, nil
}

func (s *Store) ClaimNext(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (domain.Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	leaseUntil := now.UTC().Add(leaseDuration)
	var job domain.Job
	var leaseUntilMillis int64
	err = tx.QueryRowContext(ctx, `
WITH candidate AS (
    SELECT j.id
    FROM jobs j
    JOIN investigations i ON i.id = j.investigation_id
    WHERE (j.status = ? OR (j.status = ? AND j.lease_until <= ?))
      AND i.status NOT IN (?, ?, ?, ?)
    ORDER BY j.created_at, j.id
    LIMIT 1
)
UPDATE jobs
SET status = ?, attempts = attempts + 1, lease_owner = ?, lease_until = ?, updated_at = ?
WHERE id = (SELECT id FROM candidate)
RETURNING id, investigation_id, attempts, lease_until`,
		domain.StatusQueued, domain.StatusRunning, nowMillis,
		domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled, domain.StatusNeedsReview,
		domain.StatusRunning, workerID, leaseUntil.UnixMilli(), nowMillis,
	).Scan(&job.ID, &job.InvestigationID, &job.Attempt, &leaseUntilMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("claim job: %w", err)
	}

	var requestJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM investigations WHERE id = ?`, job.InvestigationID).Scan(&requestJSON); err != nil {
		return domain.Job{}, false, fmt.Errorf("load claimed request: %w", err)
	}
	if err := json.Unmarshal(requestJSON, &job.Request); err != nil {
		return domain.Job{}, false, fmt.Errorf("decode claimed request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE investigations SET status = ?, updated_at = ?
WHERE id = ? AND status IN (?, ?)`,
		domain.StatusRunning, nowMillis, job.InvestigationID, domain.StatusQueued, domain.StatusRunning); err != nil {
		return domain.Job{}, false, fmt.Errorf("mark investigation running: %w", err)
	}
	if err := enqueueDelivery(ctx, tx, job.InvestigationID, domain.DeliveryRunning, nowMillis); err != nil {
		return domain.Job{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Job{}, false, fmt.Errorf("commit claim: %w", err)
	}
	job.LeaseOwner = workerID
	job.LeaseUntil = time.UnixMilli(leaseUntilMillis).UTC()
	return job, true, nil
}

func (s *Store) RenewLease(ctx context.Context, job domain.Job, now time.Time, leaseDuration time.Duration) error {
	nowMillis := now.UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET lease_until = ?, updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ? AND lease_owner = ? AND attempts = ? AND lease_until >= ?
  AND EXISTS (
      SELECT 1 FROM investigations i
      WHERE i.id = jobs.investigation_id AND i.status = ?
  )`,
		now.UTC().Add(leaseDuration).UnixMilli(), nowMillis,
		job.ID, job.InvestigationID, domain.StatusRunning, job.LeaseOwner, job.Attempt, nowMillis,
		domain.StatusRunning)
	if err != nil {
		return fmt.Errorf("renew job lease: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read lease renewal result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}
	return nil
}

func (s *Store) FinishSuccess(ctx context.Context, job domain.Job, evidence []domain.Evidence, report domain.Report, now time.Time) error {
	report.Evidence = evidence
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin success transaction: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, lease_owner = '', lease_until = 0, updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ? AND lease_owner = ? AND attempts = ? AND lease_until >= ?`,
		domain.StatusSucceeded, nowMillis, job.ID, job.InvestigationID,
		domain.StatusRunning, job.LeaseOwner, job.Attempt, nowMillis)
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read finish result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}

	for _, item := range evidence {
		payload, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("encode evidence %q: %w", item.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence(id, investigation_id, payload_json, created_at)
VALUES (?, ?, ?, ?)`, item.ID, job.InvestigationID, payload, nowMillis); err != nil {
			return fmt.Errorf("insert evidence %q: %w", item.ID, err)
		}
	}
	if report.CauseAnalysis != nil {
		for _, entry := range report.CauseAnalysis.Ledger {
			payload, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encode evidence-ledger entry %q: %w", entry.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO evidence_ledger(entry_id, investigation_id, hypothesis_id, payload_json, created_at)
VALUES (?, ?, ?, ?, ?)`, entry.ID, job.InvestigationID, entry.HypothesisID, payload, nowMillis); err != nil {
				return fmt.Errorf("insert evidence-ledger entry %q: %w", entry.ID, err)
			}
		}
	}

	result, err = tx.ExecContext(ctx, `
UPDATE investigations
SET status = ?, report_json = ?, last_error = '', updated_at = ?
WHERE id = ? AND status = ?`,
		domain.StatusSucceeded, reportJSON, nowMillis, job.InvestigationID, domain.StatusRunning)
	if err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read investigation finish result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}
	if err := enqueueDelivery(ctx, tx, job.InvestigationID, domain.DeliverySucceeded, nowMillis); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit success: %w", err)
	}
	return nil
}

func (s *Store) FinishFailure(ctx context.Context, job domain.Job, cause string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure transaction: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, last_error = ?, lease_owner = '', lease_until = 0, updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ? AND lease_owner = ? AND attempts = ? AND lease_until >= ?`,
		domain.StatusFailed, cause, nowMillis, job.ID, job.InvestigationID,
		domain.StatusRunning, job.LeaseOwner, job.Attempt, nowMillis)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read fail result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE investigations SET status = ?, last_error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		domain.StatusFailed, cause, nowMillis, job.InvestigationID, domain.StatusRunning); err != nil {
		return fmt.Errorf("fail investigation: %w", err)
	}
	if err := enqueueDelivery(ctx, tx, job.InvestigationID, domain.DeliveryFailed, nowMillis); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failure: %w", err)
	}
	return nil
}

func (s *Store) FinishNeedsReview(ctx context.Context, job domain.Job, reasonCode string, now time.Time) error {
	if !validReasonCode(reasonCode) {
		return errors.New("stable review reason code is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin needs-review transaction: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET status = ?, last_error = ?, lease_owner = '', lease_until = 0, updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ? AND lease_owner = ? AND attempts = ? AND lease_until >= ?`,
		domain.StatusNeedsReview, reasonCode, nowMillis, job.ID, job.InvestigationID,
		domain.StatusRunning, job.LeaseOwner, job.Attempt, nowMillis)
	if err != nil {
		return fmt.Errorf("mark job for review: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read needs-review job result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, reason_code = ?, completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND status = ? AND job_attempt = ? AND lease_owner = ?`,
		domain.QueryStepUnknown, reasonCode, nowMillis, nowMillis,
		job.InvestigationID, domain.QueryStepStarted, job.Attempt, job.LeaseOwner); err != nil {
		return fmt.Errorf("mark active query steps unknown: %w", err)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE investigations
SET status = ?, last_error = ?, updated_at = ?
WHERE id = ? AND status = ?`,
		domain.StatusNeedsReview, reasonCode, nowMillis, job.InvestigationID, domain.StatusRunning)
	if err != nil {
		return fmt.Errorf("mark investigation for review: %w", err)
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read needs-review investigation result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}
	if err := enqueueDelivery(ctx, tx, job.InvestigationID, domain.DeliveryNeedsReview, nowMillis); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit needs-review state: %w", err)
	}
	return nil
}

func (s *Store) RequestCancel(ctx context.Context, investigationID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE investigations SET status = ?, updated_at = ?
WHERE id = ? AND status IN (?, ?)`,
		domain.StatusCancelled, nowMillis, investigationID, domain.StatusQueued, domain.StatusRunning)
	if err != nil {
		return fmt.Errorf("cancel investigation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read cancel result: %w", err)
	}
	if updated == 0 {
		var currentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM investigations WHERE id = ?`, investigationID).Scan(&currentStatus); errors.Is(err, sql.ErrNoRows) {
			return ports.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("check investigation: %w", err)
		}
		if domain.Status(currentStatus) != domain.StatusCancelled {
			return ports.ErrStateConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET status = ?, lease_owner = '', lease_until = 0, updated_at = ?
WHERE investigation_id = ? AND status IN (?, ?)`,
		domain.StatusCancelled, nowMillis, investigationID, domain.StatusQueued, domain.StatusRunning); err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	if updated == 1 {
		unknownResult, err := tx.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, result_json = '', output_hash = '',
    reason_code = ?,
    completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND status = ?`,
			domain.QueryStepUnknown, domain.CancelReasonExternalQueryOutcomeUnknown,
			nowMillis, nowMillis, investigationID, domain.QueryStepStarted)
		if err != nil {
			return fmt.Errorf("mark cancelled query steps unknown: %w", err)
		}
		if _, err := unknownResult.RowsAffected(); err != nil {
			return fmt.Errorf("read cancelled unknown query-step result: %w", err)
		}
		// A reclaimed worker may already have changed an abandoned STARTED step
		// to UNKNOWN but not yet committed NEEDS_REVIEW on the investigation.
		// Count both that state and rows converted above inside this same
		// cancellation transaction so callback ordering cannot bypass the cost
		// acknowledgement gate.
		var unknownCount int
		err = tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM query_steps
WHERE investigation_id = ? AND status = ?`,
			investigationID, domain.QueryStepUnknown).Scan(&unknownCount)
		if err != nil {
			return fmt.Errorf("count cancelled unknown query steps: %w", err)
		}
		if unknownCount > 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE investigations SET last_error = ?
WHERE id = ? AND status = ?`,
				domain.CancelReasonExternalQueryOutcomeUnknown,
				investigationID, domain.StatusCancelled); err != nil {
				return fmt.Errorf("mark cancelled investigation outcome unknown: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET last_error = ?
WHERE investigation_id = ? AND status = ?`,
				domain.CancelReasonExternalQueryOutcomeUnknown,
				investigationID, domain.StatusCancelled); err != nil {
				return fmt.Errorf("mark cancelled job outcome unknown: %w", err)
			}
		}
		if err := enqueueDelivery(ctx, tx, investigationID, domain.DeliveryCancelled, nowMillis); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cancellation: %w", err)
	}
	return nil
}

func (s *Store) GetInvestigation(ctx context.Context, investigationID string) (domain.Investigation, error) {
	var item domain.Investigation
	var requestJSON []byte
	var reportJSON []byte
	var createdMillis, updatedMillis int64
	err := s.db.QueryRowContext(ctx, `
SELECT id, status, request_json, COALESCE(report_json, ''), last_error, created_at, updated_at
FROM investigations WHERE id = ?`, investigationID).Scan(
		&item.ID, &item.Status, &requestJSON, &reportJSON, &item.LastError, &createdMillis, &updatedMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Investigation{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.Investigation{}, fmt.Errorf("get investigation: %w", err)
	}
	if err := json.Unmarshal(requestJSON, &item.Request); err != nil {
		return domain.Investigation{}, fmt.Errorf("decode request: %w", err)
	}
	if len(reportJSON) > 0 {
		var report domain.Report
		if err := json.Unmarshal(reportJSON, &report); err != nil {
			return domain.Investigation{}, fmt.Errorf("decode report: %w", err)
		}
		item.Report = &report
	}
	item.CreatedAt = time.UnixMilli(createdMillis).UTC()
	item.UpdatedAt = time.UnixMilli(updatedMillis).UTC()
	return item, nil
}

// Counts supports black-box persistence assertions in the technical preview.
func (s *Store) Counts(ctx context.Context) (inbox, investigations, jobs, evidence int, err error) {
	values := []*int{&inbox, &investigations, &jobs, &evidence}
	tables := []string{"inbox", "investigations", "jobs", "evidence"}
	for index, table := range tables {
		query := "SELECT COUNT(*) FROM " + table // table names are fixed above, never external input.
		if scanErr := s.db.QueryRowContext(ctx, query).Scan(values[index]); scanErr != nil {
			return 0, 0, 0, 0, fmt.Errorf("count %s: %w", table, scanErr)
		}
	}
	return inbox, investigations, jobs, evidence, nil
}

var _ ports.Store = (*Store)(nil)
