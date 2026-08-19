package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
)

type failingEngine struct {
	err error
}

func (f failingEngine) Run(context.Context, string, domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	return nil, domain.Report{}, f.err
}

type blockingEngine struct {
	started   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func (b blockingEngine) Run(ctx context.Context, investigationID string, _ domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	close(b.started)
	select {
	case <-b.release:
		evidence := []domain.Evidence{{
			ID: "ev_blocking", QueryID: "query_blocking", QuerySpecHash: "hash_blocking", Complete: true,
		}}
		return evidence, domain.Report{
			InvestigationID: investigationID,
			Outcome:         "completed",
			Findings: []domain.Finding{{
				Code: "completed", Statement: "completed", Confidence: 1, Conclusive: true, EvidenceIDs: []string{"ev_blocking"},
			}},
			GeneratedAt: time.Now().UTC(),
		}, nil
	case <-ctx.Done():
		close(b.cancelled)
		return nil, domain.Report{}, ctx.Err()
	}
}

func TestWorkerPersistsEvidenceAndReportBeforeSuccess(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	end := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	engine, err := eino.New(context.Background(), &slsmock.Executor{}, func() time.Time { return end.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	intake := application.NewIntake(store)
	id, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "message", ReceivedAt: end,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: end.Add(-30 * time.Minute), EndTime: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := application.NewWorker(
		store, engine, "worker", time.Minute,
		application.WithWorkerClock(func() time.Time { return end.Add(time.Second) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := worker.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("run worker: ran=%v err=%v", ran, err)
	}

	item, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusSucceeded || item.Report == nil {
		t.Fatalf("unexpected investigation: %#v", item)
	}
	if len(item.Report.Evidence) != 2 || len(item.Report.Findings[0].EvidenceIDs) != 2 {
		t.Fatalf("report is missing evidence: %#v", item.Report)
	}
	_, _, _, evidenceCount, err := store.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 2 {
		t.Fatalf("want 2 persisted evidence rows, got %d", evidenceCount)
	}
}

func TestWorkerPersistsTerminalFailure(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	intake := application.NewIntake(store)
	id, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "failed-message", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("query failed")
	worker, err := application.NewWorker(
		store, failingEngine{err: wantErr}, "worker", time.Minute,
		application.WithWorkerClock(func() time.Time { return now.Add(time.Second) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := worker.RunOne(context.Background()); !ran || !errors.Is(err, wantErr) {
		t.Fatalf("run failure: ran=%v err=%v", ran, err)
	}
	item, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusFailed || item.LastError != wantErr.Error() {
		t.Fatalf("unexpected failure state: %#v", item)
	}
}

func TestWorkerRenewsLeaseDuringLongExecution(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	intake := application.NewIntake(store)
	id, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "long-message", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := blockingEngine{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	worker, err := application.NewWorker(store, engine, "slow-worker", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := worker.RunOne(context.Background())
		result <- err
	}()
	<-engine.started
	time.Sleep(650 * time.Millisecond)
	if _, claimable, err := store.ClaimNext(context.Background(), "other-worker", time.Now().UTC(), time.Second); err != nil || claimable {
		t.Fatalf("actively renewed job was claimable: claimable=%v err=%v", claimable, err)
	}
	close(engine.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	item, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusSucceeded {
		t.Fatalf("unexpected long-job status: %s", item.Status)
	}
}

func TestWorkerPropagatesDurableCancellationToEngine(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	intake := application.NewIntake(store)
	id, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "cancel-message", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := blockingEngine{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	worker, err := application.NewWorker(store, engine, "worker", 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := worker.RunOne(context.Background())
		result <- err
	}()
	<-engine.started
	if err := store.RequestCancel(context.Background(), id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.cancelled:
	case <-time.After(time.Second):
		t.Fatal("running engine did not observe durable cancellation")
	}
	if err := <-result; err != nil {
		t.Fatalf("user cancellation should finish cleanly, got %v", err)
	}
	item, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusCancelled {
		t.Fatalf("unexpected cancellation status: %s", item.Status)
	}
}

func TestWorkerShutdownLeavesLeaseForRecovery(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	intake := application.NewIntake(store)
	_, _, err = intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "shutdown-message", ReceivedAt: now,
	}, domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: now.Add(-time.Minute), EndTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := blockingEngine{started: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	worker, err := application.NewWorker(store, engine, "worker", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := worker.RunOne(ctx)
		result <- err
	}()
	<-engine.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("want context cancellation, got %v", err)
	}
	if _, ok, err := store.ClaimNext(context.Background(), "recovery-worker", time.Now().UTC().Add(2*time.Second), time.Second); err != nil || !ok {
		t.Fatalf("shutdown job was not recoverable after lease expiry: ok=%v err=%v", ok, err)
	}
}
