package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type recoveryExecutor struct {
	delegate      application.GovernedSLSExecutor
	beforeResolve func(context.Context, domain.QuerySpec) error
	beforeExecute func(context.Context, domain.QuerySpec) error

	mu           sync.Mutex
	executeCalls map[string]int
}

func newRecoveryExecutor() *recoveryExecutor {
	return &recoveryExecutor{
		delegate:     &slsmock.Executor{},
		executeCalls: make(map[string]int),
	}
}

func (e *recoveryExecutor) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	if e.beforeResolve != nil {
		if err := e.beforeResolve(ctx, spec); err != nil {
			return "", err
		}
	}
	return e.delegate.ResolveQueryGovernance(ctx, spec)
}

func (e *recoveryExecutor) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	e.mu.Lock()
	e.executeCalls[spec.Name]++
	e.mu.Unlock()
	if e.beforeExecute != nil {
		if err := e.beforeExecute(ctx, spec); err != nil {
			return domain.QueryResult{}, err
		}
	}
	return e.delegate.Execute(ctx, spec)
}

func (e *recoveryExecutor) calls(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls[name]
}

type runBarrierEngine struct {
	delegate ports.InvestigationEngine
	reached  chan struct{}
}

func (e runBarrierEngine) Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	evidence, report, err := e.delegate.Run(ctx, investigationID, request)
	if err != nil {
		return nil, domain.Report{}, err
	}
	close(e.reached)
	<-ctx.Done()
	return evidence, report, ctx.Err()
}

type recoveryWorkerResult struct {
	ran bool
	err error
}

func TestCheckpointRecoveryQueriesOnlyMissingBaselineAfterRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing-baseline.db")
	claimedAt := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	leaseDuration := time.Minute
	store := openRecoveryStore(t, databasePath)
	defer func() { _ = store.Close() }()
	investigationID := acceptRecoveryInvestigation(t, store, "missing-baseline", claimedAt)

	baselineResolutionStarted := make(chan struct{})
	firstExecutor := newRecoveryExecutor()
	firstExecutor.beforeResolve = func(ctx context.Context, spec domain.QuerySpec) error {
		if spec.Name != "baseline" {
			return nil
		}
		close(baselineResolutionStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	firstWorker := newRecoveryWorker(t, store, firstExecutor, "worker-before-crash", claimedAt, leaseDuration)
	workerContext, cancelWorker := context.WithCancel(context.Background())
	firstResult := runRecoveryWorker(firstWorker, workerContext)

	waitForRecoverySignal(t, baselineResolutionStarted, "baseline governance resolution")
	assertQueryStepCount(t, store, investigationID, 1)
	if got := firstExecutor.calls("current"); got != 1 {
		t.Fatalf("current execute calls before crash=%d, want 1", got)
	}
	if got := firstExecutor.calls("baseline"); got != 0 {
		t.Fatalf("baseline execute calls before crash=%d, want 0", got)
	}
	cancelWorker()
	assertCancelledWorker(t, firstResult)
	closeRecoveryStore(t, store)

	recoveredAt := claimedAt.Add(2 * leaseDuration)
	recoveredStore := openRecoveryStore(t, databasePath)
	defer closeRecoveryStore(t, recoveredStore)
	recoveryExecutor := newRecoveryExecutor()
	recoveryWorker := newRecoveryWorker(t, recoveredStore, recoveryExecutor, "worker-after-restart", recoveredAt, leaseDuration)
	if ran, err := recoveryWorker.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("run recovered worker: ran=%v err=%v", ran, err)
	}

	if got := recoveryExecutor.calls("current"); got != 0 {
		t.Fatalf("recovered worker repeated current query %d times", got)
	}
	if got := recoveryExecutor.calls("baseline"); got != 1 {
		t.Fatalf("recovered worker baseline execute calls=%d, want 1", got)
	}
	assertSucceededInvestigation(t, recoveredStore, investigationID)
	assertQueryStepCount(t, recoveredStore, investigationID, 2)
}

func TestCheckpointRecoveryReusesBothWindowsAfterEngineCompleted(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "both-windows.db")
	claimedAt := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	leaseDuration := time.Minute
	store := openRecoveryStore(t, databasePath)
	defer func() { _ = store.Close() }()
	investigationID := acceptRecoveryInvestigation(t, store, "both-windows", claimedAt)

	firstExecutor := newRecoveryExecutor()
	checkpointEngine := newRecoveryEngine(t, store, firstExecutor, claimedAt)
	engineCompleted := make(chan struct{})
	firstWorker := newRecoveryWorkerWithEngine(
		t,
		store,
		runBarrierEngine{delegate: checkpointEngine, reached: engineCompleted},
		"worker-before-finish",
		claimedAt,
		leaseDuration,
	)
	workerContext, cancelWorker := context.WithCancel(context.Background())
	firstResult := runRecoveryWorker(firstWorker, workerContext)

	waitForRecoverySignal(t, engineCompleted, "Eino graph completion")
	assertQueryStepCount(t, store, investigationID, 2)
	if got := firstExecutor.calls("current"); got != 1 {
		t.Fatalf("current execute calls before crash=%d, want 1", got)
	}
	if got := firstExecutor.calls("baseline"); got != 1 {
		t.Fatalf("baseline execute calls before crash=%d, want 1", got)
	}
	cancelWorker()
	assertCancelledWorker(t, firstResult)
	closeRecoveryStore(t, store)

	recoveredAt := claimedAt.Add(2 * leaseDuration)
	recoveredStore := openRecoveryStore(t, databasePath)
	defer closeRecoveryStore(t, recoveredStore)
	recoveryExecutor := newRecoveryExecutor()
	recoveryWorker := newRecoveryWorker(t, recoveredStore, recoveryExecutor, "worker-after-restart", recoveredAt, leaseDuration)
	if ran, err := recoveryWorker.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("run recovered worker: ran=%v err=%v", ran, err)
	}

	if got := recoveryExecutor.calls("current"); got != 0 {
		t.Fatalf("recovered worker repeated current query %d times", got)
	}
	if got := recoveryExecutor.calls("baseline"); got != 0 {
		t.Fatalf("recovered worker repeated baseline query %d times", got)
	}
	assertSucceededInvestigation(t, recoveredStore, investigationID)
	assertQueryStepCount(t, recoveredStore, investigationID, 2)
}

func TestCheckpointRecoveryMarksStartedQueryNeedsReviewWithoutSecondExecution(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "started-query.db")
	claimedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	leaseDuration := time.Minute
	store := openRecoveryStore(t, databasePath)
	defer func() { _ = store.Close() }()
	investigationID := acceptRecoveryInvestigation(t, store, "started-query", claimedAt)

	queryStarted := make(chan struct{})
	firstExecutor := newRecoveryExecutor()
	firstExecutor.beforeExecute = func(ctx context.Context, spec domain.QuerySpec) error {
		if spec.Name != "current" {
			return nil
		}
		close(queryStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	firstWorker := newRecoveryWorker(t, store, firstExecutor, "worker-before-crash", claimedAt, leaseDuration)
	workerContext, cancelWorker := context.WithCancel(context.Background())
	firstResult := runRecoveryWorker(firstWorker, workerContext)

	waitForRecoverySignal(t, queryStarted, "current query start")
	assertQueryStepCount(t, store, investigationID, 1)
	cancelWorker()
	assertCancelledWorker(t, firstResult)
	closeRecoveryStore(t, store)

	recoveredAt := claimedAt.Add(2 * leaseDuration)
	recoveredStore := openRecoveryStore(t, databasePath)
	defer closeRecoveryStore(t, recoveredStore)
	recoveryExecutor := newRecoveryExecutor()
	recoveryWorker := newRecoveryWorker(t, recoveredStore, recoveryExecutor, "worker-after-restart", recoveredAt, leaseDuration)
	ran, err := recoveryWorker.RunOne(context.Background())
	if !ran || !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("recovered ambiguous query: ran=%v err=%v", ran, err)
	}

	if got := firstExecutor.calls("current"); got != 1 {
		t.Fatalf("first attempt current execute calls=%d, want 1", got)
	}
	if got := recoveryExecutor.calls("current"); got != 0 {
		t.Fatalf("recovered worker touched current execute delegate %d times", got)
	}
	if got := recoveryExecutor.calls("baseline"); got != 0 {
		t.Fatalf("recovered worker touched baseline execute delegate %d times", got)
	}
	item, getErr := recoveredStore.GetInvestigation(context.Background(), investigationID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if item.Status != domain.StatusNeedsReview || item.LastError != "external_query_outcome_unknown" || item.Report != nil {
		t.Fatalf("ambiguous recovered investigation=%#v", item)
	}
	assertQueryStepCount(t, recoveredStore, investigationID, 1)
}

func openRecoveryStore(t *testing.T, databasePath string) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func closeRecoveryStore(t *testing.T, store *sqlite.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func acceptRecoveryInvestigation(t *testing.T, store *sqlite.Store, messageID string, now time.Time) string {
	t.Helper()
	intake := application.NewIntake(store)
	investigationID, created, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID:      "app-recovery",
		TenantKey:  "tenant-recovery",
		MessageID:  messageID,
		ChatID:     "chat-recovery",
		UserID:     "user-recovery",
		ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service:     "order-service",
		Environment: "prod",
		StartTime:   now.Add(-30 * time.Minute),
		EndTime:     now,
	})
	if err != nil || !created {
		t.Fatalf("accept recovery investigation: created=%v err=%v", created, err)
	}
	return investigationID
}

func newRecoveryWorker(
	t *testing.T,
	store *sqlite.Store,
	executor application.GovernedSLSExecutor,
	workerID string,
	now time.Time,
	leaseDuration time.Duration,
) *application.Worker {
	t.Helper()
	return newRecoveryWorkerWithEngine(t, store, newRecoveryEngine(t, store, executor, now), workerID, now, leaseDuration)
}

func newRecoveryEngine(
	t *testing.T,
	store *sqlite.Store,
	executor application.GovernedSLSExecutor,
	now time.Time,
) ports.InvestigationEngine {
	t.Helper()
	checkpointExecutor, err := application.NewCheckpointExecutor(executor, store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	engine, err := eino.New(context.Background(), checkpointExecutor, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func newRecoveryWorkerWithEngine(
	t *testing.T,
	store *sqlite.Store,
	engine ports.InvestigationEngine,
	workerID string,
	now time.Time,
	leaseDuration time.Duration,
) *application.Worker {
	t.Helper()
	worker, err := application.NewWorker(
		store,
		engine,
		workerID,
		leaseDuration,
		application.WithWorkerClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func runRecoveryWorker(worker *application.Worker, ctx context.Context) <-chan recoveryWorkerResult {
	result := make(chan recoveryWorkerResult, 1)
	go func() {
		ran, err := worker.RunOne(ctx)
		result <- recoveryWorkerResult{ran: ran, err: err}
	}()
	return result
}

func waitForRecoverySignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertCancelledWorker(t *testing.T, result <-chan recoveryWorkerResult) {
	t.Helper()
	select {
	case outcome := <-result:
		if !outcome.ran || !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("cancelled worker: ran=%v err=%v", outcome.ran, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled worker")
	}
}

func assertQueryStepCount(t *testing.T, store *sqlite.Store, investigationID string, want int) {
	t.Helper()
	got, err := store.CountQuerySteps(context.Background(), investigationID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query-step count=%d, want %d", got, want)
	}
}

func assertSucceededInvestigation(t *testing.T, store *sqlite.Store, investigationID string) {
	t.Helper()
	item, err := store.GetInvestigation(context.Background(), investigationID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusSucceeded || item.Report == nil || len(item.Report.Evidence) != 2 {
		t.Fatalf("recovered investigation=%#v", item)
	}
}

var (
	_ ports.SLSExecutor             = (*recoveryExecutor)(nil)
	_ ports.QueryGovernanceResolver = (*recoveryExecutor)(nil)
)
