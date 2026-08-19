package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestFileStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logagent.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil {
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
	item, err := reopened.GetInvestigation(context.Background(), "inv_one")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusQueued {
		t.Fatalf("unexpected status after restart: %s", item.Status)
	}
	if _, ok, err := reopened.ClaimNext(context.Background(), "worker", time.Now(), time.Minute); err != nil || !ok {
		t.Fatalf("claim after restart: ok=%v err=%v", ok, err)
	}
}

func TestAcceptOnceDeduplicatesConcurrentMessages(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()

	const submissions = 20
	ids := make(chan string, submissions)
	errorsCh := make(chan error, submissions)
	var wait sync.WaitGroup
	for index := 0; index < submissions; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			id, _, err := store.AcceptOnce(
				context.Background(), inbound, request,
				fmt.Sprintf("inv_%02d", index), fmt.Sprintf("job_%02d", index),
			)
			if err != nil {
				errorsCh <- err
				return
			}
			ids <- id
		}(index)
	}
	wait.Wait()
	close(ids)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}

	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate submissions returned different investigations: %q and %q", first, id)
		}
	}
	inbox, investigations, jobs, evidence, err := store.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inbox != 1 || investigations != 1 || jobs != 1 || evidence != 0 {
		t.Fatalf("unexpected row counts: inbox=%d investigations=%d jobs=%d evidence=%d", inbox, investigations, jobs, evidence)
	}
}

func TestAcceptOnceDeduplicatesAcrossStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	firstStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	inbound, request := testInput()

	type result struct {
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for index, store := range []*Store{firstStore, secondStore} {
		go func(index int, store *Store) {
			<-start
			id, _, err := store.AcceptOnce(
				context.Background(), inbound, request,
				fmt.Sprintf("inv_connection_%d", index), fmt.Sprintf("job_connection_%d", index),
			)
			results <- result{id: id, err: err}
		}(index, store)
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("cross-connection accepts failed: first=%v second=%v", first.err, second.err)
	}
	if first.id != second.id {
		t.Fatalf("cross-connection duplicate returned %q and %q", first.id, second.id)
	}
	inbox, investigations, jobs, _, err := firstStore.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inbox != 1 || investigations != 1 || jobs != 1 {
		t.Fatalf("unexpected shared row counts: inbox=%d investigations=%d jobs=%d", inbox, investigations, jobs)
	}
}

func TestAcceptOnceScopesMessageIDByAppAndTenant(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()
	if _, created, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil || !created {
		t.Fatalf("first accept: created=%v err=%v", created, err)
	}
	inbound.TenantKey = "tenant-b"
	if _, created, err := store.AcceptOnce(context.Background(), inbound, request, "inv_two", "job_two"); err != nil || !created {
		t.Fatalf("second tenant accept: created=%v err=%v", created, err)
	}
}

func TestExpiredLeaseCanBeReclaimed(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	first, ok, err := store.ClaimNext(context.Background(), "worker-a", t0, 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ClaimNext(context.Background(), "worker-b", t0.Add(9*time.Second), 10*time.Second); err != nil || ok {
		t.Fatalf("claim before expiry: ok=%v err=%v", ok, err)
	}
	second, ok, err := store.ClaimNext(context.Background(), "worker-b", t0.Add(11*time.Second), 10*time.Second)
	if err != nil || !ok || second.Attempt != 2 {
		t.Fatalf("reclaim: job=%#v ok=%v err=%v", second, ok, err)
	}
	if err := store.FinishFailure(context.Background(), first, "stale", t0.Add(12*time.Second)); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("stale owner should lose lease, got %v", err)
	}
}

func TestExpiredLeaseOwnerCannotFinish(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	job, ok, err := store.ClaimNext(context.Background(), "worker-a", t0, 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := store.FinishFailure(context.Background(), job, "too late", t0.Add(11*time.Second)); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("expired lease should not finish, got %v", err)
	}
}

func TestAttemptFencesStaleClaimWithSameWorkerID(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	stale, ok, err := store.ClaimNext(context.Background(), "worker-local", t0, 10*time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	active, ok, err := store.ClaimNext(context.Background(), "worker-local", t0.Add(11*time.Second), 10*time.Second)
	if err != nil || !ok || active.Attempt != stale.Attempt+1 {
		t.Fatalf("same-owner reclaim: job=%#v ok=%v err=%v", active, ok, err)
	}
	if err := store.RenewLease(context.Background(), stale, t0.Add(12*time.Second), 10*time.Second); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("stale attempt renewed active lease: %v", err)
	}
	if err := store.FinishFailure(context.Background(), stale, "stale", t0.Add(12*time.Second)); !errors.Is(err, ports.ErrLeaseLost) {
		t.Fatalf("stale attempt finished active claim: %v", err)
	}
	if err := store.RenewLease(context.Background(), active, t0.Add(12*time.Second), 10*time.Second); err != nil {
		t.Fatalf("active attempt could not renew: %v", err)
	}
}

func TestCancelPreventsClaim(t *testing.T) {
	store := openTestStore(t)
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(context.Background(), inbound, request, "inv_one", "job_one"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(context.Background(), "inv_one", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext(context.Background(), "worker", time.Now(), time.Minute); err != nil || ok {
		t.Fatalf("cancelled job was claimable: ok=%v err=%v", ok, err)
	}
	item, err := store.GetInvestigation(context.Background(), "inv_one")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusCancelled {
		t.Fatalf("unexpected status: %s", item.Status)
	}
}

func TestRequestCancelRejectsAConcurrentTerminalState(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(ctx, inbound, request, "inv_terminal_cancel", "job_terminal_cancel"); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimNext(ctx, "worker", time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim job: ok=%v err=%v", ok, err)
	}
	if err := store.FinishFailure(ctx, job, "finished first", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(ctx, "inv_terminal_cancel", time.Now().UTC()); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("terminal investigation was reported as cancelled: %v", err)
	}
	item, err := store.GetInvestigation(ctx, "inv_terminal_cancel")
	if err != nil || item.Status != domain.StatusFailed {
		t.Fatalf("terminal state changed: item=%+v err=%v", item, err)
	}
}

func TestFinishSuccessPersistsCauseLedgerAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cause-ledger.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(ctx, inbound, request, "inv_cause", "job_cause"); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	job, ok, err := store.ClaimNext(ctx, "worker", claimedAt, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	evidence := []domain.Evidence{{ID: "ev-cause", QueryID: "query", QuerySpecHash: "hash", Complete: true}}
	entry := domain.EvidenceLedgerEntry{
		ID: "ledger-cause", HypothesisID: "hyp-cause", Code: "spike", Role: domain.EvidenceTestSupport,
		Result: domain.EvidenceTestPass, Weight: .25, Statement: "error spike is present", EvidenceIDs: []string{"ev-cause"},
	}
	report := domain.Report{
		InvestigationID: job.InvestigationID, Outcome: "spike_detected", GeneratedAt: claimedAt.Add(time.Second),
		Findings:      []domain.Finding{{Code: "spike", Statement: "spike", Confidence: .9, Conclusive: true, EvidenceIDs: []string{"ev-cause"}}},
		CauseAnalysis: &domain.CauseAnalysis{Status: domain.CauseAnalysisInconclusive, Ledger: []domain.EvidenceLedgerEntry{entry}},
	}
	if err := store.FinishSuccess(ctx, job, evidence, report, claimedAt.Add(time.Second)); err != nil {
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
	var payload []byte
	if err := reopened.db.QueryRowContext(ctx, `SELECT payload_json FROM evidence_ledger WHERE entry_id = ?`, entry.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored domain.EvidenceLedgerEntry
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != entry.ID || stored.HypothesisID != entry.HypothesisID {
		t.Fatalf("unexpected ledger row: %#v", stored)
	}
	item, err := reopened.GetInvestigation(ctx, job.InvestigationID)
	if err != nil || item.Report == nil || item.Report.CauseAnalysis == nil || len(item.Report.CauseAnalysis.Ledger) != 1 {
		t.Fatalf("cause report did not survive restart: item=%#v err=%v", item, err)
	}
}

func TestFinishSuccessRollsBackDuplicateCauseLedger(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	inbound, request := testInput()
	if _, _, err := store.AcceptOnce(ctx, inbound, request, "inv_duplicate_ledger", "job_duplicate_ledger"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	job, ok, err := store.ClaimNext(ctx, "worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	entry := domain.EvidenceLedgerEntry{ID: "duplicate", HypothesisID: "hyp", Code: "test", Role: domain.EvidenceTestSupport, Result: domain.EvidenceTestPass, Weight: .1, Statement: "test", EvidenceIDs: []string{"ev"}}
	report := domain.Report{InvestigationID: job.InvestigationID, Outcome: "spike_detected", GeneratedAt: now, CauseAnalysis: &domain.CauseAnalysis{Status: domain.CauseAnalysisInconclusive, Ledger: []domain.EvidenceLedgerEntry{entry, entry}}}
	if err := store.FinishSuccess(ctx, job, []domain.Evidence{{ID: "ev", QueryID: "query", QuerySpecHash: "hash"}}, report, now.Add(time.Second)); err == nil {
		t.Fatal("duplicate ledger insert unexpectedly succeeded")
	}
	item, err := store.GetInvestigation(ctx, job.InvestigationID)
	if err != nil || item.Status != domain.StatusRunning || item.Report != nil {
		t.Fatalf("failed transaction partially committed: item=%#v err=%v", item, err)
	}
	var evidenceCount, ledgerCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence`).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_ledger`).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 || ledgerCount != 0 {
		t.Fatalf("failed transaction left rows: evidence=%d ledger=%d", evidenceCount, ledgerCount)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testInput() (domain.InboundMessage, domain.InvestigationRequest) {
	end := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	return domain.InboundMessage{
			AppID: "app", TenantKey: "tenant", MessageID: "message", ChatID: "chat", UserID: "user",
			Text: "/investigate order-service prod 30m", ReceivedAt: end,
		}, domain.InvestigationRequest{
			Service: "order-service", Environment: "prod", StartTime: end.Add(-30 * time.Minute), EndTime: end,
		}
}
