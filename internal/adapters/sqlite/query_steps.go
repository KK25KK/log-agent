package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const maxQueryStepResultBytes = 128 * 1024

type storedQueryStep struct {
	inputHash   string
	status      domain.QueryStepStatus
	jobAttempt  int
	leaseOwner  string
	resultJSON  []byte
	outputHash  string
	reasonCode  string
	startedAt   int64
	completedAt int64
}

func (s *Store) PrepareQueryStep(
	ctx context.Context,
	job domain.Job,
	stepKey, inputHash string,
	now time.Time,
) (domain.QueryStepDecision, error) {
	inputHash = strings.ToLower(inputHash)
	if err := validateQueryStepArguments(job, stepKey, inputHash); err != nil {
		return domain.QueryStepDecision{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.QueryStepDecision{}, fmt.Errorf("begin query-step preparation: %w", err)
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return domain.QueryStepDecision{}, err
	}

	stored, found, err := loadQueryStep(ctx, tx, job.InvestigationID, stepKey)
	if err != nil {
		return domain.QueryStepDecision{}, err
	}
	if !found {
		nowMillis := now.UTC().UnixMilli()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO query_steps(
    investigation_id, step_key, input_hash, status, job_attempt,
    lease_owner, started_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.InvestigationID, stepKey, inputHash, domain.QueryStepStarted,
			job.Attempt, job.LeaseOwner, nowMillis, nowMillis); err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("create query step: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("commit query-step preparation: %w", err)
		}
		return domain.QueryStepDecision{Action: domain.QueryStepExecute}, nil
	}

	switch stored.status {
	case domain.QueryStepSucceeded:
		if stored.inputHash != inputHash || !validSucceededStep(stored) {
			return domain.QueryStepDecision{}, ports.ErrStateConflict
		}
		result, err := decodeStoredQueryResult(stored.resultJSON, stored.outputHash)
		if err != nil {
			return domain.QueryStepDecision{}, ports.ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("commit query-step reuse: %w", err)
		}
		return domain.QueryStepDecision{Action: domain.QueryStepReuse, Result: &result}, nil
	case domain.QueryStepStarted:
		if !validStartedStep(stored) || job.Attempt <= stored.jobAttempt {
			return domain.QueryStepDecision{}, ports.ErrStateConflict
		}
		nowMillis := now.UTC().UnixMilli()
		result, err := tx.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, reason_code = ?, completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND step_key = ? AND input_hash = ?
  AND status = ? AND job_attempt = ? AND lease_owner = ?`,
			domain.QueryStepUnknown, domain.ReviewReasonExternalQueryOutcomeUnknown, nowMillis, nowMillis,
			job.InvestigationID, stepKey, stored.inputHash, domain.QueryStepStarted,
			stored.jobAttempt, stored.leaseOwner)
		if err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("mark query step unknown: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("read unknown query-step result: %w", err)
		}
		if updated != 1 {
			return domain.QueryStepDecision{}, ports.ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return domain.QueryStepDecision{}, fmt.Errorf("commit unknown query-step state: %w", err)
		}
		return domain.QueryStepDecision{}, ports.ErrExternalOutcomeUnknown
	case domain.QueryStepUnknown:
		return domain.QueryStepDecision{}, ports.ErrExternalOutcomeUnknown
	case domain.QueryStepFailed:
		if stored.inputHash != inputHash || !validTerminalFailureStep(stored) {
			return domain.QueryStepDecision{}, ports.ErrStateConflict
		}
		return domain.QueryStepDecision{}, ports.NewQueryStepFailure(stored.reasonCode, nil)
	default:
		return domain.QueryStepDecision{}, ports.ErrStateConflict
	}
}

func (s *Store) CompleteQueryStep(
	ctx context.Context,
	job domain.Job,
	stepKey, inputHash string,
	result domain.QueryResult,
	now time.Time,
) error {
	inputHash = strings.ToLower(inputHash)
	if err := validateQueryStepArguments(job, stepKey, inputHash); err != nil {
		return err
	}
	if err := validateCheckpointResult(result); err != nil {
		return errors.New("normalized query result is invalid")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return errors.New("encode normalized query result")
	}
	if len(payload) == 0 || len(payload) > maxQueryStepResultBytes {
		return errors.New("normalized query result exceeds checkpoint limit")
	}
	outputHash := hashBytes(payload)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin query-step completion: %w", err)
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return err
	}
	nowMillis := now.UTC().UnixMilli()
	update, err := tx.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, result_json = ?, output_hash = ?, reason_code = '',
    completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND step_key = ? AND input_hash = ?
  AND status = ? AND job_attempt = ? AND lease_owner = ?`,
		domain.QueryStepSucceeded, payload, outputHash, nowMillis, nowMillis,
		job.InvestigationID, stepKey, inputHash, domain.QueryStepStarted,
		job.Attempt, job.LeaseOwner)
	if err != nil {
		return fmt.Errorf("complete query step: %w", err)
	}
	updated, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("read query-step completion result: %w", err)
	}
	if updated != 1 {
		return ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit query-step completion: %w", err)
	}
	return nil
}

func (s *Store) FailQueryStep(
	ctx context.Context,
	job domain.Job,
	stepKey, inputHash, reasonCode string,
	now time.Time,
) error {
	inputHash = strings.ToLower(inputHash)
	if err := validateQueryStepArguments(job, stepKey, inputHash); err != nil {
		return err
	}
	if !validReasonCode(reasonCode) {
		return errors.New("stable query-step reason code is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin query-step failure: %w", err)
	}
	defer tx.Rollback()
	if err := fenceActiveJob(ctx, tx, job, now); err != nil {
		return err
	}
	nowMillis := now.UTC().UnixMilli()
	update, err := tx.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, result_json = '', output_hash = '', reason_code = ?,
    completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND step_key = ? AND input_hash = ?
  AND status = ? AND job_attempt = ? AND lease_owner = ?`,
		domain.QueryStepFailed, reasonCode, nowMillis, nowMillis,
		job.InvestigationID, stepKey, inputHash, domain.QueryStepStarted,
		job.Attempt, job.LeaseOwner)
	if err != nil {
		return fmt.Errorf("fail query step: %w", err)
	}
	updated, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("read query-step failure result: %w", err)
	}
	if updated != 1 {
		return ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit query-step failure: %w", err)
	}
	return nil
}

// CountQuerySteps exposes only an aggregate for local acceptance output. It
// intentionally does not return checkpoint payloads or provider metadata.
func (s *Store) CountQuerySteps(ctx context.Context, investigationID string) (int, error) {
	if investigationID == "" {
		return 0, errors.New("investigation ID is required")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM query_steps WHERE investigation_id = ?`, investigationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count query steps: %w", err)
	}
	return count, nil
}

func fenceActiveJob(ctx context.Context, tx *sql.Tx, job domain.Job, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET updated_at = updated_at
WHERE id = ? AND investigation_id = ? AND status = ?
  AND lease_owner = ? AND attempts = ? AND lease_until >= ?
  AND EXISTS (
      SELECT 1 FROM investigations i
      WHERE i.id = jobs.investigation_id AND i.status = ?
  )`,
		job.ID, job.InvestigationID, domain.StatusRunning,
		job.LeaseOwner, job.Attempt, now.UTC().UnixMilli(), domain.StatusRunning)
	if err != nil {
		return fmt.Errorf("fence active query-step job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read active query-step fence: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}
	return nil
}

func loadQueryStep(ctx context.Context, tx *sql.Tx, investigationID, stepKey string) (storedQueryStep, bool, error) {
	var stored storedQueryStep
	var status string
	err := tx.QueryRowContext(ctx, `
SELECT input_hash, status, job_attempt, lease_owner, result_json,
       output_hash, reason_code, started_at, completed_at
FROM query_steps
WHERE investigation_id = ? AND step_key = ?`, investigationID, stepKey).Scan(
		&stored.inputHash, &status, &stored.jobAttempt, &stored.leaseOwner,
		&stored.resultJSON, &stored.outputHash, &stored.reasonCode,
		&stored.startedAt, &stored.completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedQueryStep{}, false, nil
	}
	if err != nil {
		return storedQueryStep{}, false, fmt.Errorf("load query step: %w", err)
	}
	stored.status = domain.QueryStepStatus(status)
	return stored, true, nil
}

func validateQueryStepArguments(job domain.Job, stepKey, inputHash string) error {
	if job.ID == "" || job.InvestigationID == "" || job.LeaseOwner == "" || job.Attempt <= 0 {
		return errors.New("complete claimed job is required")
	}
	if stepKey != "sls.current" && stepKey != "sls.baseline" {
		return errors.New("unsupported query-step key")
	}
	if !validHash(inputHash) {
		return errors.New("query-step input hash must be 64 hexadecimal characters")
	}
	return nil
}

func validStartedStep(stored storedQueryStep) bool {
	return stored.jobAttempt > 0 && stored.leaseOwner != "" && len(stored.resultJSON) == 0 &&
		stored.outputHash == "" && stored.reasonCode == "" && stored.startedAt > 0 && stored.completedAt == 0
}

func validSucceededStep(stored storedQueryStep) bool {
	return stored.jobAttempt > 0 && stored.leaseOwner != "" && stored.startedAt > 0 &&
		stored.completedAt >= stored.startedAt && stored.reasonCode == "" &&
		len(stored.resultJSON) > 0 && validHash(stored.outputHash)
}

func validTerminalFailureStep(stored storedQueryStep) bool {
	return stored.jobAttempt > 0 && stored.leaseOwner != "" && stored.startedAt > 0 &&
		stored.completedAt >= stored.startedAt && validReasonCode(stored.reasonCode) &&
		len(stored.resultJSON) == 0 && stored.outputHash == ""
}

func decodeStoredQueryResult(payload []byte, outputHash string) (domain.QueryResult, error) {
	if len(payload) == 0 || len(payload) > maxQueryStepResultBytes || !validHash(outputHash) || hashBytes(payload) != strings.ToLower(outputHash) {
		return domain.QueryResult{}, errors.New("stored query result integrity check failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var result domain.QueryResult
	if err := decoder.Decode(&result); err != nil {
		return domain.QueryResult{}, errors.New("stored query result cannot be decoded")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.QueryResult{}, errors.New("stored query result has trailing data")
	}
	if err := validateCheckpointResult(result); err != nil {
		return domain.QueryResult{}, errors.New("stored query result is invalid")
	}
	return result, nil
}

func validateCheckpointResult(result domain.QueryResult) error {
	if !boundedString(result.QueryID, 1, 4096) || !validHash(result.QuerySpecHash) ||
		!boundedString(result.ResourceID, 1, 256) ||
		!boundedString(result.TemplateVersion, 1, 256) || !boundedString(result.SchemaFingerprint, 1, 256) ||
		!boundedString(result.PolicyVersion, 1, 256) || !validHash(result.GovernanceFingerprint) ||
		!boundedString(result.Progress, 1, 128) || !boundedString(result.IncompleteReason, 0, 2048) {
		return errors.New("query result metadata is invalid")
	}
	if result.ProcessedRows < 0 || result.ProcessedBytes < 0 || result.ElapsedMillisecond < 0 ||
		result.ErrorCount < 0 || result.TopErrorCount < 0 || result.TopErrorCount > result.ErrorCount {
		return errors.New("query result counters are invalid")
	}
	contract, ok := domain.QueryTemplateByID(result.TemplateID)
	if !ok || result.APICalls != contract.APICalls ||
		result.PatternLimit != contract.PatternLimit ||
		result.InstanceLimit != contract.InstanceLimit {
		return errors.New("query result fixed limits are invalid")
	}
	if !contract.Dimensional {
		if result.TopError != "" || result.TopErrorCount != 0 || len(result.ErrorPatterns) != 0 || len(result.Instances) != 0 || result.ErrorPatternsExhaustive || result.InstancesExhaustive {
			return errors.New("count-only query result contains dimensional evidence")
		}
		return validateStoredCheckpointCompletion(result)
	}
	patternTotal, err := validateCheckpointBuckets(result.ErrorPatterns, result.PatternLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	instanceTotal, err := validateCheckpointBuckets(result.Instances, result.InstanceLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	if result.ErrorCount > 0 && (len(result.ErrorPatterns) == 0 || len(result.Instances) == 0) {
		return errors.New("non-zero error count requires aggregate buckets")
	}
	if (result.ErrorPatternsExhaustive && patternTotal != result.ErrorCount) ||
		(result.InstancesExhaustive && instanceTotal != result.ErrorCount) {
		return errors.New("aggregate exhaustiveness is invalid")
	}
	if result.Complete && !result.Truncated &&
		(result.ErrorPatternsExhaustive != (patternTotal == result.ErrorCount) ||
			result.InstancesExhaustive != (instanceTotal == result.ErrorCount)) {
		return errors.New("complete aggregate exhaustiveness is invalid")
	}
	if len(result.ErrorPatterns) == 0 {
		if result.TopError != "" || result.TopErrorCount != 0 {
			return errors.New("top error is present without a bucket")
		}
	} else if result.TopError != result.ErrorPatterns[0].Label || result.TopErrorCount != result.ErrorPatterns[0].Count {
		return errors.New("top error does not match the first bucket")
	}
	return validateStoredCheckpointCompletion(result)
}

func validateStoredCheckpointCompletion(result domain.QueryResult) error {
	if result.Complete {
		if result.Truncated || !result.UsageKnown || !strings.EqualFold(result.Progress, "complete") || result.IncompleteReason != "" {
			return errors.New("complete result markers are inconsistent")
		}
	} else if result.IncompleteReason == "" {
		return errors.New("incomplete result needs a stable reason")
	}
	return nil
}

func validateCheckpointBuckets(buckets []domain.CountBucket, limit int, total int64) (int64, error) {
	if len(buckets) > limit {
		return 0, errors.New("aggregate bucket limit exceeded")
	}
	labels := make(map[string]struct{}, len(buckets))
	var sum int64
	for _, bucket := range buckets {
		if !boundedString(bucket.Label, 1, 512) || bucket.Count <= 0 || bucket.Count > total-sum {
			return 0, errors.New("aggregate bucket is invalid")
		}
		if _, duplicate := labels[bucket.Label]; duplicate {
			return 0, errors.New("aggregate bucket labels are duplicated")
		}
		labels[bucket.Label] = struct{}{}
		sum += bucket.Count
	}
	return sum, nil
}

func boundedString(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validReasonCode(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

var _ ports.QueryStepStore = (*Store)(nil)
