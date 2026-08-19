package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	testInputHash      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testInputHash2     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testResultHash     = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testGovernanceHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestQueryStepResultSurvivesRestartAndIsReusedByReclaimedAttempt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "query-step.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	first := acceptAndClaimQueryStepJob(t, store, "inv-reuse", "job-reuse", "worker-a", t0, 10*time.Second)
	decision, err := store.PrepareQueryStep(ctx, first, "sls.current", testInputHash, t0)
	if err != nil || decision.Action != domain.QueryStepExecute || decision.Result != nil {
		t.Fatalf("prepare first step: decision=%#v err=%v", decision, err)
	}
	want := validCheckpointResult()
	if err := store.CompleteQueryStep(ctx, first, "sls.current", testInputHash, want, t0.Add(time.Second)); err != nil {
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
	second, ok, err := reopened.ClaimNext(ctx, "worker-b", t0.Add(11*time.Second), time.Minute)
	if err != nil || !ok || second.Attempt != 2 {
		t.Fatalf("reclaim after restart: job=%#v ok=%v err=%v", second, ok, err)
	}
	decision, err = reopened.PrepareQueryStep(ctx, second, "sls.current", testInputHash, t0.Add(12*time.Second))
	if err != nil || decision.Action != domain.QueryStepReuse || decision.Result == nil {
		t.Fatalf("reuse completed step: decision=%#v err=%v", decision, err)
	}
	if decision.Result.QueryID != want.QueryID || decision.Result.ErrorCount != want.ErrorCount {
		t.Fatalf("reused result changed: got=%#v want=%#v", *decision.Result, want)
	}
	count, err := reopened.CountQuerySteps(ctx, second.InvestigationID)
	if err != nil || count != 1 {
		t.Fatalf("query-step count=%d err=%v", count, err)
	}
}

func TestPrepareQueryStepTurnsOlderStartedAttemptUnknownWithoutOverwritingFence(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	first := acceptAndClaimQueryStepJob(t, store, "inv-unknown", "job-unknown", "worker-a", t0, 10*time.Second)
	if _, err := store.PrepareQueryStep(ctx, first, "sls.current", testInputHash, t0); err != nil {
		t.Fatal(err)
	}
	second, ok, err := store.ClaimNext(ctx, "worker-b", t0.Add(11*time.Second), time.Minute)
	if err != nil || !ok || second.Attempt != 2 {
		t.Fatalf("reclaim: job=%#v ok=%v err=%v", second, ok, err)
	}
	// Governance may change while the process is down. The new input hash must
	// not hide the ambiguity of an older STARTED provider call.
	if _, err := store.PrepareQueryStep(ctx, second, "sls.current", testInputHash2, t0.Add(12*time.Second)); !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("old STARTED result was not made unknown: %v", err)
	}

	var status, inputHash, owner, resultJSON, outputHash, reason string
	var attempt int
	if err := store.db.QueryRowContext(ctx, `
SELECT status, input_hash, job_attempt, lease_owner, result_json, output_hash, reason_code
FROM query_steps WHERE investigation_id = ? AND step_key = ?`, first.InvestigationID, "sls.current").Scan(
		&status, &inputHash, &attempt, &owner, &resultJSON, &outputHash, &reason,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepUnknown) || inputHash != testInputHash || attempt != first.Attempt || owner != first.LeaseOwner {
		t.Fatalf("unknown transition overwrote origin: status=%s hash=%s attempt=%d owner=%s", status, inputHash, attempt, owner)
	}
	if resultJSON != "" || outputHash != "" || reason != domain.ReviewReasonExternalQueryOutcomeUnknown {
		t.Fatalf("unknown step contains unsafe payload: result=%q output=%q reason=%q", resultJSON, outputHash, reason)
	}
	if _, err := store.PrepareQueryStep(ctx, second, "sls.current", testInputHash2, t0.Add(13*time.Second)); !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("UNKNOWN did not remain fail closed: %v", err)
	}
}

func TestStaleQueryStepCompletionIsRejectedByJobAndStepFences(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	stale := acceptAndClaimQueryStepJob(t, store, "inv-stale", "job-stale", "worker-a", t0, 10*time.Second)
	if _, err := store.PrepareQueryStep(ctx, stale, "sls.current", testInputHash, t0); err != nil {
		t.Fatal(err)
	}
	active, ok, err := store.ClaimNext(ctx, "worker-b", t0.Add(11*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim: ok=%v err=%v", ok, err)
	}
	if err := store.CompleteQueryStep(ctx, stale, "sls.current", testInputHash, validCheckpointResult(), t0.Add(12*time.Second)); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("stale completion was accepted: %v", err)
	}
	if _, err := store.PrepareQueryStep(ctx, active, "sls.current", testInputHash, t0.Add(12*time.Second)); !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("active worker did not observe unknown outcome: %v", err)
	}
}

func TestQueryStepRejectsHashChangesAndRepeatedStartedCall(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-hash", "job-hash", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now.Add(time.Second)); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("same-attempt duplicate STARTED did not fail closed: %v", err)
	}
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash2, now.Add(time.Second)); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("changed input hash did not fail closed: %v", err)
	}
	var storedHash, status string
	if err := store.db.QueryRowContext(ctx, `
SELECT input_hash, status FROM query_steps WHERE investigation_id = ? AND step_key = ?`,
		job.InvestigationID, "sls.current").Scan(&storedHash, &status); err != nil {
		t.Fatal(err)
	}
	if storedHash != testInputHash || status != string(domain.QueryStepStarted) {
		t.Fatalf("conflict changed stored step: hash=%s status=%s", storedHash, status)
	}
}

func TestCompleteQueryStepRejectsOversizedAndInvalidResults(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-invalid", "job-invalid", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}

	oversized := validCheckpointResult()
	oversized.QueryID = strings.Repeat("q", maxQueryStepResultBytes)
	if err := store.CompleteQueryStep(ctx, job, "sls.current", testInputHash, oversized, now.Add(time.Second)); err == nil {
		t.Fatal("oversized result was stored")
	}
	invalid := validCheckpointResult()
	invalid.TopErrorCount = invalid.ErrorCount + 1
	if err := store.CompleteQueryStep(ctx, job, "sls.current", testInputHash, invalid, now.Add(time.Second)); err == nil {
		t.Fatal("structurally invalid result was stored")
	}
	invalidGovernance := validCheckpointResult()
	invalidGovernance.GovernanceFingerprint = "not-a-sha256"
	if err := store.CompleteQueryStep(ctx, job, "sls.current", testInputHash, invalidGovernance, now.Add(time.Second)); err == nil {
		t.Fatal("invalid governance fingerprint was stored")
	}
	invalidUTF8 := validCheckpointResult()
	invalidUTF8.TopError = string([]byte{0xff})
	invalidUTF8.ErrorPatterns[0].Label = invalidUTF8.TopError
	if err := store.CompleteQueryStep(ctx, job, "sls.current", testInputHash, invalidUTF8, now.Add(time.Second)); err == nil {
		t.Fatal("invalid UTF-8 result was stored")
	}
	var status, resultJSON, outputHash string
	if err := store.db.QueryRowContext(ctx, `
SELECT status, result_json, output_hash FROM query_steps
WHERE investigation_id = ? AND step_key = ?`, job.InvestigationID, "sls.current").Scan(
		&status, &resultJSON, &outputHash,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepStarted) || resultJSON != "" || outputHash != "" {
		t.Fatalf("invalid completion modified checkpoint: status=%s result=%q output=%q", status, resultJSON, outputHash)
	}
}

func TestPrepareQueryStepRejectsCorruptSucceededPayload(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-corrupt", "job-corrupt", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"query_id":"q","processed_rows":NaN}`)
	if _, err := store.db.ExecContext(ctx, `
UPDATE query_steps
SET status = ?, result_json = ?, output_hash = ?, completed_at = ?, updated_at = ?
WHERE investigation_id = ? AND step_key = ?`,
		domain.QueryStepSucceeded, payload, hashBytes(payload), now.Add(time.Second).UnixMilli(),
		now.Add(time.Second).UnixMilli(), job.InvestigationID, "sls.current"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now.Add(2*time.Second)); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("corrupt result did not fail closed: %v", err)
	}
}

func TestFailQueryStepPersistsOnlyStableReasonAndBecomesTerminal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 15, 30, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-step-failed", "job-step-failed", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.baseline", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FailQueryStep(ctx, job, "sls.baseline", testInputHash, "query_denied", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var status, reason, resultJSON, outputHash string
	if err := store.db.QueryRowContext(ctx, `
SELECT status, reason_code, result_json, output_hash FROM query_steps
WHERE investigation_id = ? AND step_key = ?`, job.InvestigationID, "sls.baseline").Scan(
		&status, &reason, &resultJSON, &outputHash,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepFailed) || reason != "query_denied" || resultJSON != "" || outputHash != "" {
		t.Fatalf("unexpected failed checkpoint: status=%s reason=%q result=%q output=%q", status, reason, resultJSON, outputHash)
	}
	if _, err := store.PrepareQueryStep(ctx, job, "sls.baseline", testInputHash, now.Add(2*time.Second)); err == nil {
		t.Fatal("FAILED checkpoint did not return its stable failure")
	} else if code, ok := ports.QueryStepFailureCode(err); !ok || code != "query_denied" {
		t.Fatalf("FAILED checkpoint lost its stable failure code: code=%q ok=%v err=%v", code, ok, err)
	}
}

func TestFailedQueryStepSurvivesLeaseRecoveryWithStableCode(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 8, 19, 15, 45, 0, 0, time.UTC)
	first := acceptAndClaimQueryStepJob(t, store, "inv-failed-recovery", "job-failed-recovery", "worker-a", t0, 10*time.Second)
	if _, err := store.PrepareQueryStep(ctx, first, "sls.current", testInputHash, t0); err != nil {
		t.Fatal(err)
	}
	if err := store.FailQueryStep(ctx, first, "sls.current", testInputHash, "invalid_query_schema", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, ok, err := store.ClaimNext(ctx, "worker-b", t0.Add(11*time.Second), time.Minute)
	if err != nil || !ok || second.Attempt != first.Attempt+1 {
		t.Fatalf("reclaim failed query step: job=%#v ok=%v err=%v", second, ok, err)
	}
	if _, err := store.PrepareQueryStep(ctx, second, "sls.current", testInputHash, t0.Add(12*time.Second)); err == nil {
		t.Fatal("recovered FAILED checkpoint did not return its stable failure")
	} else if code, ok := ports.QueryStepFailureCode(err); !ok || code != "invalid_query_schema" {
		t.Fatalf("recovered failure code changed: code=%q ok=%v err=%v", code, ok, err)
	}
}

func TestCompleteQueryStepAcceptsGatewaySizedUnicodeLabels(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 15, 50, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-unicode", "job-unicode", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	label := strings.Repeat("错", 100) + strings.Repeat("🔎", 100)
	result := validCheckpointResult()
	result.TopError = label
	result.ErrorPatterns[0].Label = label
	result.Instances[0].Label = label
	if err := store.CompleteQueryStep(ctx, job, "sls.current", testInputHash, result, now.Add(time.Second)); err != nil {
		t.Fatalf("200-rune Unicode label was rejected: %v", err)
	}
	decision, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now.Add(2*time.Second))
	if err != nil || decision.Action != domain.QueryStepReuse || decision.Result == nil {
		t.Fatalf("reuse Unicode checkpoint: decision=%#v err=%v", decision, err)
	}
	if decision.Result.TopError != label {
		t.Fatal("Unicode checkpoint label changed during persistence")
	}
}

func TestFinishNeedsReviewIsAtomicAndEnqueuesTerminalDelivery(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-review", "job-review", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareQueryStep(ctx, job, "sls.baseline", testInputHash2, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishNeedsReview(ctx, job, "external_query_outcome_unknown", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	item, err := store.GetInvestigation(ctx, job.InvestigationID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusNeedsReview || item.LastError != "external_query_outcome_unknown" {
		t.Fatalf("unexpected investigation review state: %#v", item)
	}
	var jobStatus, jobOwner, jobError string
	var leaseUntil int64
	if err := store.db.QueryRowContext(ctx, `
SELECT status, lease_owner, lease_until, last_error FROM jobs WHERE id = ?`, job.ID).Scan(
		&jobStatus, &jobOwner, &leaseUntil, &jobError,
	); err != nil {
		t.Fatal(err)
	}
	if jobStatus != string(domain.StatusNeedsReview) || jobOwner != "" || leaseUntil != 0 || jobError != "external_query_outcome_unknown" {
		t.Fatalf("unexpected job review state: status=%s owner=%q lease=%d error=%q", jobStatus, jobOwner, leaseUntil, jobError)
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT status, reason_code, result_json, output_hash FROM query_steps
WHERE investigation_id = ? ORDER BY step_key`, job.InvestigationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stepCount := 0
	for rows.Next() {
		var status, reason, resultJSON, outputHash string
		if err := rows.Scan(&status, &reason, &resultJSON, &outputHash); err != nil {
			t.Fatal(err)
		}
		if status != string(domain.QueryStepUnknown) || reason != "external_query_outcome_unknown" || resultJSON != "" || outputHash != "" {
			t.Fatalf("unsafe review checkpoint: status=%s reason=%q result=%q output=%q", status, reason, resultJSON, outputHash)
		}
		stepCount++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if stepCount != 2 {
		t.Fatalf("unknown checkpoint count=%d", stepCount)
	}
	var deliveryKind, deliveryStatus string
	var sequence int
	if err := store.db.QueryRowContext(ctx, `
SELECT kind, sequence, status FROM delivery_events
WHERE investigation_id = ? AND kind = ?`, job.InvestigationID, domain.DeliveryNeedsReview).Scan(
		&deliveryKind, &sequence, &deliveryStatus,
	); err != nil {
		t.Fatal(err)
	}
	if deliveryKind != string(domain.DeliveryNeedsReview) || sequence != 30 || deliveryStatus != string(domain.DeliveryPending) {
		t.Fatalf("unexpected review delivery: kind=%s sequence=%d status=%s", deliveryKind, sequence, deliveryStatus)
	}
	if _, ok, err := store.ClaimNext(ctx, "other-worker", now.Add(2*time.Second), time.Minute); err != nil || ok {
		t.Fatalf("NEEDS_REVIEW job was claimable: ok=%v err=%v", ok, err)
	}
}

func TestFinishNeedsReviewRejectsUnstableReasonWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 17, 0, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-review-invalid", "job-review-invalid", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishNeedsReview(ctx, job, "raw SQL: select * from logs", now.Add(time.Second)); err == nil {
		t.Fatal("unstable reason was accepted")
	}
	item, err := store.GetInvestigation(ctx, job.InvestigationID)
	if err != nil || item.Status != domain.StatusRunning || item.LastError != "" {
		t.Fatalf("invalid reason changed investigation: item=%#v err=%v", item, err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `
SELECT status FROM query_steps WHERE investigation_id = ? AND step_key = ?`,
		job.InvestigationID, "sls.current").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepStarted) {
		t.Fatalf("invalid reason changed query step to %s", status)
	}
}

func TestRequestCancelMarksInFlightQueryStepsUnknownAtomically(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)
	job := acceptAndClaimQueryStepJob(t, store, "inv-cancel-step", "job-cancel-step", "worker", now, time.Minute)
	if _, err := store.PrepareQueryStep(ctx, job, "sls.current", testInputHash, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(ctx, job.InvestigationID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	item, err := store.GetInvestigation(ctx, job.InvestigationID)
	if err != nil || item.Status != domain.StatusCancelled || item.LastError != domain.CancelReasonExternalQueryOutcomeUnknown {
		t.Fatalf("cancelled investigation state: item=%#v err=%v", item, err)
	}
	var status, reason, resultJSON, outputHash string
	var completedAt int64
	if err := store.db.QueryRowContext(ctx, `
SELECT status, reason_code, result_json, output_hash, completed_at
FROM query_steps WHERE investigation_id = ? AND step_key = ?`,
		job.InvestigationID, "sls.current").Scan(
		&status, &reason, &resultJSON, &outputHash, &completedAt,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepUnknown) || reason != domain.CancelReasonExternalQueryOutcomeUnknown {
		t.Fatalf("in-flight query step was not made unknown: status=%s reason=%q", status, reason)
	}
	if resultJSON != "" || outputHash != "" || completedAt != now.Add(time.Second).UnixMilli() {
		t.Fatalf("cancelled query step retained unsafe state: result=%q output=%q completed=%d", resultJSON, outputHash, completedAt)
	}
	var started int
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM query_steps WHERE investigation_id = ? AND status = ?`,
		job.InvestigationID, domain.QueryStepStarted).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("cancelled investigation retained %d STARTED query steps", started)
	}
}

func TestRequestCancelPreservesCostWarningAfterRecoveryAlreadyMarkedUnknown(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 8, 19, 18, 0, 0, 0, time.UTC)
	first := acceptAndClaimQueryStepJob(t, store, "inv-cancel-recovered-unknown", "job-cancel-recovered-unknown", "worker-a", t0, 10*time.Second)
	if _, err := store.PrepareQueryStep(ctx, first, "sls.current", testInputHash, t0); err != nil {
		t.Fatal(err)
	}
	second, ok, err := store.ClaimNext(ctx, "worker-b", t0.Add(11*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim: ok=%v err=%v", ok, err)
	}
	if _, err := store.PrepareQueryStep(ctx, second, "sls.current", testInputHash2, t0.Add(12*time.Second)); !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("recovery did not mark old query unknown: %v", err)
	}

	// Model the callback landing after PrepareQueryStep committed UNKNOWN but
	// before the worker could commit NEEDS_REVIEW.
	if err := store.RequestCancel(ctx, second.InvestigationID, t0.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetInvestigation(ctx, second.InvestigationID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusCancelled || item.LastError != domain.CancelReasonExternalQueryOutcomeUnknown {
		t.Fatalf("cancelled recovered UNKNOWN lost its cost warning: %#v", item)
	}
	var status, reason string
	if err := store.db.QueryRowContext(ctx, `
SELECT status, reason_code FROM query_steps
WHERE investigation_id = ? AND step_key = ?`,
		second.InvestigationID, "sls.current").Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.QueryStepUnknown) || reason != domain.ReviewReasonExternalQueryOutcomeUnknown {
		t.Fatalf("cancellation overwrote recovered UNKNOWN provenance: status=%s reason=%s", status, reason)
	}
}

func acceptAndClaimQueryStepJob(
	t *testing.T,
	store *Store,
	investigationID, jobID, workerID string,
	now time.Time,
	lease time.Duration,
) domain.Job {
	t.Helper()
	inbound, request := testInput()
	inbound.MessageID = "message-" + investigationID
	inbound.ReceivedAt = now
	if _, created, err := store.AcceptOnce(context.Background(), inbound, request, investigationID, jobID); err != nil || !created {
		t.Fatalf("accept: created=%v err=%v", created, err)
	}
	job, ok, err := store.ClaimNext(context.Background(), workerID, now, lease)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	return job
}

func validCheckpointResult() domain.QueryResult {
	return domain.QueryResult{
		QueryID:                "provider-request-1,provider-request-2,provider-request-3,provider-request-4",
		QuerySpecHash:          testResultHash,
		ResourceID:             "order-service-prod",
		TemplateID:             domain.ErrorAnalysisTemplateID,
		TemplateVersion:        domain.ErrorAnalysisTemplateVersion,
		SchemaFingerprint:      "schema-v1",
		PolicyVersion:          "policy-v1",
		GovernanceFingerprint:  testGovernanceHash,
		Progress:               "Complete",
		Complete:               true,
		NanosecondOrderedKnown: true,
		NanosecondOrdered:      true,
		UsageKnown:             true,
		ProcessedRows:          40,
		ProcessedBytes:         4096,
		ElapsedMillisecond:     50,
		APICalls:               domain.ErrorAnalysisAPICalls,
		ErrorCount:             10,
		TopError:               "payment_timeout",
		TopErrorCount:          10,
		ErrorPatterns: []domain.CountBucket{
			{Label: "payment_timeout", Count: 10},
		},
		Instances: []domain.CountBucket{
			{Label: "order-pod-a", Count: 10},
		},
		ErrorPatternsExhaustive: true,
		InstancesExhaustive:     true,
		PatternLimit:            domain.ErrorAnalysisPatternLimit,
		InstanceLimit:           domain.ErrorAnalysisInstanceLimit,
	}
}
