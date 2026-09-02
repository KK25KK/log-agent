package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const maxTraceStepResultBytes = 512 * 1024

func (s *Store) RecordTraceAudit(ctx context.Context, audit domain.TraceAudit) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO trace_audit(
    investigation_id, principal_app_id, principal_tenant_key, principal_user_id,
    group_id, member_id, template_id, trace_id_fingerprint, governance_fingerprint,
    query_spec_hash, schema_fingerprint, outcome, reason_code, provider_request_id,
    progress, returned_events, api_calls, processed_rows, processed_bytes,
    elapsed_millisecond, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		audit.InvestigationID, audit.Principal.AppID, audit.Principal.TenantKey, audit.Principal.UserID,
		audit.GroupID, audit.MemberID, audit.TemplateID, audit.TraceIDFingerprint, audit.GovernanceFingerprint,
		audit.QuerySpecHash, audit.SchemaFingerprint, audit.Outcome, audit.ReasonCode, audit.ProviderRequestID,
		audit.Progress, audit.ReturnedEvents, audit.APICalls, audit.ProcessedRows, audit.ProcessedBytes,
		audit.ElapsedMillisecond, audit.OccurredAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("record Trace audit: %w", err)
	}
	return nil
}

func (s *Store) ListTraceAudits(ctx context.Context, investigationID string) ([]domain.TraceAudit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT investigation_id, principal_app_id, principal_tenant_key, principal_user_id,
       group_id, member_id, template_id, trace_id_fingerprint, governance_fingerprint,
       query_spec_hash, schema_fingerprint, outcome, reason_code, provider_request_id,
       progress, returned_events, api_calls, processed_rows, processed_bytes,
       elapsed_millisecond, occurred_at
FROM trace_audit WHERE investigation_id = ? ORDER BY audit_id`, investigationID)
	if err != nil {
		return nil, fmt.Errorf("list Trace audits: %w", err)
	}
	defer rows.Close()
	result := make([]domain.TraceAudit, 0)
	for rows.Next() {
		var audit domain.TraceAudit
		var occurredAt string
		if err := rows.Scan(
			&audit.InvestigationID, &audit.Principal.AppID, &audit.Principal.TenantKey, &audit.Principal.UserID,
			&audit.GroupID, &audit.MemberID, &audit.TemplateID, &audit.TraceIDFingerprint, &audit.GovernanceFingerprint,
			&audit.QuerySpecHash, &audit.SchemaFingerprint, &audit.Outcome, &audit.ReasonCode, &audit.ProviderRequestID,
			&audit.Progress, &audit.ReturnedEvents, &audit.APICalls, &audit.ProcessedRows, &audit.ProcessedBytes,
			&audit.ElapsedMillisecond, &occurredAt,
		); err != nil {
			return nil, fmt.Errorf("scan Trace audit: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, errors.New("parse Trace audit occurrence time")
		}
		audit.OccurredAt = parsed
		result = append(result, audit)
	}
	return result, rows.Err()
}

func (s *Store) PrepareTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash string, now time.Time) (domain.TraceQueryStepDecision, error) {
	inputHash = strings.ToLower(inputHash)
	if err := validateTraceStepArguments(job, memberID, inputHash); err != nil {
		return domain.TraceQueryStepDecision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TraceQueryStepDecision{}, fmt.Errorf("begin Trace step preparation: %w", err)
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return domain.TraceQueryStepDecision{}, err
	}
	stored, found, err := loadTraceStep(ctx, tx, job.InvestigationID, memberID)
	if err != nil {
		return domain.TraceQueryStepDecision{}, err
	}
	if !found {
		nowMillis := now.UTC().UnixMilli()
		_, err := tx.ExecContext(ctx, `
INSERT INTO trace_query_steps(investigation_id, member_id, input_hash, status, job_attempt, lease_owner, started_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, job.InvestigationID, memberID, inputHash, domain.QueryStepStarted, job.Attempt, job.LeaseOwner, nowMillis, nowMillis)
		if err != nil {
			return domain.TraceQueryStepDecision{}, fmt.Errorf("create Trace query step: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.TraceQueryStepDecision{}, fmt.Errorf("commit Trace query-step preparation: %w", err)
		}
		return domain.TraceQueryStepDecision{Action: domain.QueryStepExecute}, nil
	}
	switch stored.status {
	case domain.QueryStepSucceeded:
		if stored.inputHash != inputHash || !validSucceededStep(stored) {
			return domain.TraceQueryStepDecision{}, ports.ErrStateConflict
		}
		result, err := decodeTraceResult(stored.resultJSON, stored.outputHash)
		if err != nil {
			return domain.TraceQueryStepDecision{}, ports.ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return domain.TraceQueryStepDecision{}, err
		}
		return domain.TraceQueryStepDecision{Action: domain.QueryStepReuse, Result: &result}, nil
	case domain.QueryStepStarted:
		if !validStartedStep(stored) || job.Attempt <= stored.jobAttempt {
			return domain.TraceQueryStepDecision{}, ports.ErrStateConflict
		}
		nowMillis := now.UTC().UnixMilli()
		update, err := tx.ExecContext(ctx, `
UPDATE trace_query_steps SET status = ?, reason_code = ?, completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND member_id = ? AND input_hash = ? AND status = ? AND job_attempt = ? AND lease_owner = ?`,
			domain.QueryStepUnknown, domain.ReviewReasonExternalQueryOutcomeUnknown, nowMillis, nowMillis,
			job.InvestigationID, memberID, stored.inputHash, domain.QueryStepStarted, stored.jobAttempt, stored.leaseOwner)
		if err != nil {
			return domain.TraceQueryStepDecision{}, err
		}
		if count, _ := update.RowsAffected(); count != 1 {
			return domain.TraceQueryStepDecision{}, ports.ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return domain.TraceQueryStepDecision{}, err
		}
		return domain.TraceQueryStepDecision{}, ports.ErrExternalOutcomeUnknown
	case domain.QueryStepUnknown:
		return domain.TraceQueryStepDecision{}, ports.ErrExternalOutcomeUnknown
	case domain.QueryStepFailed:
		return domain.TraceQueryStepDecision{}, ports.NewQueryStepFailure(stored.reasonCode, nil)
	default:
		return domain.TraceQueryStepDecision{}, ports.ErrStateConflict
	}
}

func (s *Store) CompleteTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash string, result domain.TraceMemberResult, now time.Time) error {
	inputHash = strings.ToLower(inputHash)
	if err := validateTraceStepArguments(job, memberID, inputHash); err != nil {
		return err
	}
	if err := validateStoredTraceResult(result); err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil || len(payload) == 0 || len(payload) > maxTraceStepResultBytes {
		return errors.New("normalized Trace result exceeds checkpoint limit")
	}
	outputHash := hashBytes(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return err
	}
	nowMillis := now.UTC().UnixMilli()
	update, err := tx.ExecContext(ctx, `
UPDATE trace_query_steps SET status = ?, result_json = ?, output_hash = ?, reason_code = '', completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND member_id = ? AND input_hash = ? AND status = ? AND job_attempt = ? AND lease_owner = ?`,
		domain.QueryStepSucceeded, payload, outputHash, nowMillis, nowMillis,
		job.InvestigationID, memberID, inputHash, domain.QueryStepStarted, job.Attempt, job.LeaseOwner)
	if err != nil {
		return err
	}
	if count, _ := update.RowsAffected(); count != 1 {
		return ports.ErrStateConflict
	}
	return tx.Commit()
}

func (s *Store) FailTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash, reasonCode string, now time.Time) error {
	inputHash = strings.ToLower(inputHash)
	if err := validateTraceStepArguments(job, memberID, inputHash); err != nil || !validReasonCode(reasonCode) {
		return errors.New("invalid Trace query-step failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return err
	}
	nowMillis := now.UTC().UnixMilli()
	update, err := tx.ExecContext(ctx, `
UPDATE trace_query_steps SET status = ?, reason_code = ?, completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND member_id = ? AND input_hash = ? AND status = ? AND job_attempt = ? AND lease_owner = ?`,
		domain.QueryStepFailed, reasonCode, nowMillis, nowMillis,
		job.InvestigationID, memberID, inputHash, domain.QueryStepStarted, job.Attempt, job.LeaseOwner)
	if err != nil {
		return err
	}
	if count, _ := update.RowsAffected(); count != 1 {
		return ports.ErrStateConflict
	}
	return tx.Commit()
}

func (s *Store) CountTraceQuerySteps(ctx context.Context, investigationID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trace_query_steps WHERE investigation_id = ?`, investigationID).Scan(&count)
	return count, err
}

func validateTraceStepArguments(job domain.Job, memberID, inputHash string) error {
	if job.ID == "" || job.InvestigationID == "" || job.LeaseOwner == "" || job.Attempt <= 0 ||
		memberID == "" || len(memberID) > 128 || !validHash(inputHash) {
		return errors.New("Trace query-step arguments are invalid")
	}
	return nil
}

func loadTraceStep(ctx context.Context, tx *sql.Tx, investigationID, memberID string) (storedQueryStep, bool, error) {
	var stored storedQueryStep
	var status string
	err := tx.QueryRowContext(ctx, `
SELECT input_hash, status, job_attempt, lease_owner, result_json, output_hash, reason_code, started_at, completed_at
FROM trace_query_steps WHERE investigation_id = ? AND member_id = ?`, investigationID, memberID).Scan(
		&stored.inputHash, &status, &stored.jobAttempt, &stored.leaseOwner, &stored.resultJSON,
		&stored.outputHash, &stored.reasonCode, &stored.startedAt, &stored.completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedQueryStep{}, false, nil
	}
	if err != nil {
		return storedQueryStep{}, false, err
	}
	stored.status = domain.QueryStepStatus(status)
	return stored, true, nil
}

func decodeTraceResult(payload []byte, expectedHash string) (domain.TraceMemberResult, error) {
	if len(payload) == 0 || len(payload) > maxTraceStepResultBytes || !validHash(expectedHash) || hashBytes(payload) != expectedHash {
		return domain.TraceMemberResult{}, errors.New("stored Trace result integrity check failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var result domain.TraceMemberResult
	if err := decoder.Decode(&result); err != nil {
		return domain.TraceMemberResult{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.TraceMemberResult{}, errors.New("stored Trace result contains trailing JSON")
	}
	if err := validateStoredTraceResult(result); err != nil {
		return domain.TraceMemberResult{}, err
	}
	return result, nil
}

func validateStoredTraceResult(result domain.TraceMemberResult) error {
	if result.QueryID == "" || !validHash(result.QuerySpecHash) || result.GroupID == "" || result.MemberID == "" ||
		result.TemplateID != domain.TraceSearchTemplateID || result.TemplateVersion != domain.TraceSearchTemplateVersion ||
		result.PolicyVersion != domain.TracePolicyVersion || !validHash(result.GovernanceFingerprint) || !validHash(result.TraceIDFingerprint) ||
		result.StartTime.IsZero() || !result.StartTime.Before(result.EndTime) || result.ProcessedRows < 0 ||
		result.ProcessedBytes < 0 || result.ElapsedMillisecond < 0 || result.APICalls < 0 || len(result.Events) > domain.TraceDefaultMemberLimit {
		return errors.New("normalized Trace result is invalid")
	}
	return nil
}

var _ ports.TraceAuditor = (*Store)(nil)
var _ ports.TraceQueryStepStore = (*Store)(nil)
