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

type blockingSummaryProvider struct {
	started chan struct{}
	release chan struct{}
}

func (provider blockingSummaryProvider) Summarize(ctx context.Context, _ domain.SummaryInput) (domain.SummaryProviderResult, error) {
	close(provider.started)
	select {
	case <-ctx.Done():
		return domain.SummaryProviderResult{}, ctx.Err()
	case <-provider.release:
		return domain.SummaryProviderResult{}, errors.New("synthetic provider failure")
	}
}

func TestWorkerRenewsLeaseWhileSummaryProviderIsRunning(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	end := time.Now().UTC().Add(-domain.DefaultIngestionGrace)
	engine, err := eino.New(context.Background(), &slsmock.Executor{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	intake := application.NewIntake(store)
	id, _, err := intake.Accept(context.Background(), domain.InboundMessage{
		AppID: "app", TenantKey: "tenant", MessageID: "summary-lease", ReceivedAt: end,
	}, domain.InvestigationRequest{Service: "order-service", Environment: "prod", StartTime: end.Add(-30 * time.Minute), EndTime: end})
	if err != nil {
		t.Fatal(err)
	}
	provider := blockingSummaryProvider{started: make(chan struct{}), release: make(chan struct{})}
	summary, err := application.NewSummaryService(provider, 2*time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := application.NewWorker(store, engine, "summary-worker", 300*time.Millisecond, application.WithWorkerSummary(summary))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := worker.RunOne(context.Background())
		done <- runErr
	}()
	<-provider.started
	time.Sleep(650 * time.Millisecond)
	if _, claimable, claimErr := store.ClaimNext(context.Background(), "other-worker", time.Now().UTC(), time.Second); claimErr != nil || claimable {
		t.Fatalf("job was claimable while summary heartbeat was active: claimable=%v err=%v", claimable, claimErr)
	}
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	item, err := store.GetInvestigation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != domain.StatusSucceeded || item.Report == nil || item.Report.Summary == nil || item.Report.Summary.Status != domain.SummaryFallback {
		t.Fatalf("unexpected persisted summary result: %#v", item)
	}
}
