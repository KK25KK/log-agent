package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/feishumock"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	queryapp "logagent/internal/application/query"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type mockE2EResult struct {
	Scenario      string               `json:"scenario"`
	Safety        mockSafetySummary    `json:"safety"`
	Feishu        mockFeishuSummary    `json:"feishu"`
	AlibabaSLS    mockSLSSummary       `json:"aliyun_sls"`
	ChangeSource  string               `json:"change_source"`
	Investigation domain.Investigation `json:"investigation"`
}

type mockSafetySummary struct {
	ExternalNetworkCalls int    `json:"external_network_calls"`
	CredentialsRequired  bool   `json:"credentials_required"`
	DataNotice           string `json:"data_notice"`
}

type mockFeishuSummary struct {
	Mode                        string                      `json:"mode"`
	InboundMessageID            string                      `json:"inbound_message_id"`
	FirstMessageCreated         bool                        `json:"first_message_created"`
	DuplicateReplayDeduplicated bool                        `json:"duplicate_replay_deduplicated"`
	CardMessageID               string                      `json:"card_message_id"`
	Deliveries                  []feishumock.DeliveryRecord `json:"deliveries"`
}

type mockSLSSummary struct {
	Mode                string `json:"mode"`
	LogicalObservations int    `json:"logical_observations"`
	SchemaCalls         int    `json:"schema_calls"`
	BackendExecuteCalls int    `json:"backend_execute_calls"`
	ProviderAPICalls    int    `json:"provider_api_calls"`
	QueryAuditEvents    int    `json:"query_audit_events"`
	RawLogRowsReturned  int    `json:"raw_log_rows_returned"`
	CurrentErrorCount   int64  `json:"current_error_count"`
	BaselineErrorCount  int64  `json:"baseline_error_count"`
}

type gatedMockExecutor struct {
	delegate ports.SLSExecutor
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

type localMockChangeSource struct {
	event domain.ChangeEvent
}

func (source localMockChangeSource) List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.ChangeSet{}, err
	}
	event := source.event
	if event.ResourceID != query.ResourceID || !event.StartedAt.Before(query.EndTime) || !event.CompletedAt.After(query.StartTime) {
		return domain.ChangeSet{SourceVersion: "mock-change-v1", Complete: true}, nil
	}
	event.AffectedInstances = append([]string(nil), event.AffectedInstances...)
	return domain.ChangeSet{SourceVersion: "mock-change-v1", Events: []domain.ChangeEvent{event}, Complete: true}, nil
}

func (executor *gatedMockExecutor) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	executor.once.Do(func() { close(executor.started) })
	select {
	case <-ctx.Done():
		return domain.QueryResult{}, ctx.Err()
	case <-executor.release:
		return executor.delegate.Execute(ctx, spec)
	}
}

func runMockE2E() error {
	result, err := executeMockE2E(context.Background())
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mock E2E output: %w", err)
	}
	fmt.Println(string(payload))
	return nil
}

func executeMockE2E(ctx context.Context) (mockE2EResult, error) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		return mockE2EResult{}, err
	}
	defer store.Close()

	messageAt := time.Date(2026, 1, 2, 10, 30, 10, 0, time.UTC)
	requestEnd := messageAt.Add(-domain.DefaultIngestionGrace)
	mockPrincipal := domain.Principal{AppID: "mock-feishu-app", TenantKey: "mock-tenant", UserID: "mock-feishu-user"}
	intake := application.NewIntake(store)
	receiver, err := feishumock.NewReceiver("mock-feishu-app", "mock-tenant", intake, domain.DefaultIngestionGrace)
	if err != nil {
		return mockE2EResult{}, err
	}
	message := feishumock.Message{
		MessageID: "mock-feishu-message-001",
		ChatID:    "mock-feishu-chat",
		UserID:    "mock-feishu-user",
		Text:      "/investigate order-service prod 30m",
		CreatedAt: messageAt,
	}
	investigationID, firstCreated, err := receiver.Receive(ctx, message)
	if err != nil {
		return mockE2EResult{}, fmt.Errorf("receive mock Feishu message: %w", err)
	}
	replayedID, replayCreated, err := receiver.Receive(ctx, message)
	if err != nil {
		return mockE2EResult{}, fmt.Errorf("replay mock Feishu message: %w", err)
	}
	if !firstCreated || replayCreated || replayedID != investigationID {
		return mockE2EResult{}, errors.New("mock Feishu inbox idempotency check failed")
	}

	sender, err := feishumock.NewSender("mock-feishu-app")
	if err != nil {
		return mockE2EResult{}, err
	}
	deliveryWorker, err := application.NewDeliveryWorker(
		store, sender, "mock-feishu-delivery", time.Minute, 5*time.Second, 3, time.Second,
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	if run, err := deliveryWorker.RunOne(ctx); err != nil {
		return mockE2EResult{}, fmt.Errorf("deliver mock Feishu receipt: %w", err)
	} else if !run {
		return mockE2EResult{}, errors.New("deliver mock Feishu receipt: no runnable delivery")
	}

	mockBackend, err := slsmock.NewBackend(requestEnd.Add(-30*time.Minute), requestEnd)
	if err != nil {
		return mockE2EResult{}, err
	}
	queryGateway, err := queryapp.NewGateway(
		slsmock.NewCatalog(mockPrincipal),
		mockBackend,
		store,
		queryapp.Budget{
			MaxWindow:         2 * time.Hour,
			IngestionGrace:    domain.DefaultIngestionGrace,
			Timeout:           5 * time.Second,
			MaxRows:           domain.ErrorAnalysisResultRows,
			MaxAPICalls:       domain.ErrorAnalysisAPICalls,
			MaxProcessedBytes: 1 << 20,
			MaxConcurrent:     1,
			SchemaTTL:         time.Minute,
		},
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	gatedExecutor := &gatedMockExecutor{
		delegate: queryGateway,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	released := false
	engine, err := eino.New(
		ctx,
		gatedExecutor,
		func() time.Time { return messageAt.Add(time.Second) },
		eino.WithChangeSource(localMockChangeSource{event: domain.ChangeEvent{
			ID: "chg_mock_release_v2", ResourceID: "mock/order-service/prod", Kind: domain.ChangeKindRelease,
			StartedAt: requestEnd.Add(-35 * time.Minute), CompletedAt: requestEnd.Add(-32 * time.Minute),
			FromVersion: "v1", ToVersion: "v2", Owner: "order-team", Summary: "mock release v2",
			AffectedInstances: []string{"order-pod-a"}, AffectedInstancesComplete: true,
		}}),
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	worker, err := application.NewWorker(
		store,
		engine,
		"mock-investigation-worker",
		time.Minute,
		application.WithWorkerClock(func() time.Time { return messageAt.Add(time.Second) }),
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	type runResult struct {
		run bool
		err error
	}
	workerDone := make(chan runResult, 1)
	runCtx, cancelRun := context.WithCancel(ctx)
	go func() {
		run, runErr := worker.RunOne(runCtx)
		workerDone <- runResult{run: run, err: runErr}
	}()
	workerFinished := false
	defer func() {
		if !released {
			close(gatedExecutor.release)
		}
		cancelRun()
		if !workerFinished {
			<-workerDone
		}
	}()
	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	select {
	case <-gatedExecutor.started:
	case <-waitCtx.Done():
		return mockE2EResult{}, fmt.Errorf("wait for mock SLS query: %w", waitCtx.Err())
	}
	if run, err := deliveryWorker.RunOne(ctx); err != nil {
		return mockE2EResult{}, fmt.Errorf("deliver mock Feishu running update: %w", err)
	} else if !run {
		return mockE2EResult{}, errors.New("deliver mock Feishu running update: no runnable delivery")
	}
	close(gatedExecutor.release)
	released = true
	select {
	case result := <-workerDone:
		workerFinished = true
		if result.err != nil {
			return mockE2EResult{}, fmt.Errorf("run mock investigation: %w", result.err)
		}
		if !result.run {
			return mockE2EResult{}, errors.New("run mock investigation: no runnable job")
		}
	case <-waitCtx.Done():
		return mockE2EResult{}, fmt.Errorf("wait for mock investigation: %w", waitCtx.Err())
	}
	if run, err := deliveryWorker.RunOne(ctx); err != nil {
		return mockE2EResult{}, fmt.Errorf("deliver mock Feishu report: %w", err)
	} else if !run {
		return mockE2EResult{}, errors.New("deliver mock Feishu report: no runnable delivery")
	}
	if run, err := deliveryWorker.RunOne(ctx); err != nil {
		return mockE2EResult{}, fmt.Errorf("check mock Feishu delivery drain: %w", err)
	} else if run {
		return mockE2EResult{}, errors.New("mock Feishu delivery queue did not drain")
	}

	investigation, err := store.GetInvestigation(ctx, investigationID)
	if err != nil {
		return mockE2EResult{}, err
	}
	if investigation.Status != domain.StatusSucceeded || investigation.Report == nil {
		return mockE2EResult{}, fmt.Errorf("mock investigation ended in %s without a report", investigation.Status)
	}

	providerCalls := 0
	currentErrors := int64(0)
	baselineErrors := int64(0)
	for _, evidence := range investigation.Report.Evidence {
		providerCalls += evidence.APICalls
		switch evidence.Name {
		case "current":
			currentErrors = evidence.ErrorCount
		case "baseline":
			baselineErrors = evidence.ErrorCount
		}
	}
	deliveries := sender.Records()
	if len(deliveries) != 3 || deliveries[0].Kind != domain.DeliveryQueued || deliveries[1].Kind != domain.DeliveryRunning || deliveries[2].Kind != domain.DeliverySucceeded {
		return mockE2EResult{}, fmt.Errorf("unexpected mock Feishu lifecycle: %#v", deliveries)
	}
	backendStats := mockBackend.Stats()
	if backendStats.SchemaCalls != 1 || backendStats.ExecuteCalls != 2 || backendStats.ProviderAPICalls != 2*domain.ErrorAnalysisAPICalls {
		return mockE2EResult{}, fmt.Errorf("unexpected mock SLS backend activity %#v", backendStats)
	}
	if providerCalls != backendStats.ProviderAPICalls {
		return mockE2EResult{}, fmt.Errorf("mock SLS evidence calls %d do not match backend activity %d", providerCalls, backendStats.ProviderAPICalls)
	}
	audits, err := store.ListQueryAudits(ctx, investigationID)
	if err != nil {
		return mockE2EResult{}, err
	}
	if len(audits) != 4 {
		return mockE2EResult{}, fmt.Errorf("unexpected mock query audit count %d", len(audits))
	}

	return mockE2EResult{
		Scenario: "feishu_to_sls_investigation_full_mock",
		Safety: mockSafetySummary{
			ExternalNetworkCalls: 0,
			CredentialsRequired:  false,
			DataNotice:           "All Feishu messages, SLS aggregates, and change events are deterministic test data.",
		},
		Feishu: mockFeishuSummary{
			Mode:                        "mock",
			InboundMessageID:            message.MessageID,
			FirstMessageCreated:         firstCreated,
			DuplicateReplayDeduplicated: !replayCreated && replayedID == investigationID,
			CardMessageID:               deliveries[0].CardMessageID,
			Deliveries:                  deliveries,
		},
		AlibabaSLS: mockSLSSummary{
			Mode:                "mock",
			LogicalObservations: len(investigation.Report.Evidence),
			SchemaCalls:         backendStats.SchemaCalls,
			BackendExecuteCalls: backendStats.ExecuteCalls,
			ProviderAPICalls:    backendStats.ProviderAPICalls,
			QueryAuditEvents:    len(audits),
			RawLogRowsReturned:  0,
			CurrentErrorCount:   currentErrors,
			BaselineErrorCount:  baselineErrors,
		},
		ChangeSource:  "mock",
		Investigation: investigation,
	}, nil
}
