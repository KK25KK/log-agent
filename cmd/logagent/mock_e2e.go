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
	"logagent/internal/adapters/runbookmock"
	"logagent/internal/adapters/signalmock"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/adapters/summarymock"
	"logagent/internal/application"
	queryapp "logagent/internal/application/query"
	"logagent/internal/domain"
)

type mockE2EResult struct {
	Scenario           string                         `json:"scenario"`
	Safety             mockSafetySummary              `json:"safety"`
	Feishu             mockFeishuSummary              `json:"feishu"`
	AlibabaSLS         mockSLSSummary                 `json:"aliyun_sls"`
	TenantQuota        domain.TenantQuotaUsage        `json:"tenant_quota"`
	LLMQuota           domain.TenantSummaryQuotaUsage `json:"llm_quota"`
	ChangeSource       string                         `json:"change_source"`
	OperationalSignals mockOperationalSignalSummary   `json:"operational_signals"`
	RunbookKnowledge   mockRunbookKnowledgeSummary    `json:"runbook_knowledge"`
	LLMSummary         mockLLMSummary                 `json:"llm_summary"`
	Investigation      domain.Investigation           `json:"investigation"`
}

type mockOperationalSignalSummary struct {
	Mode           string                `json:"mode"`
	SourceCalls    int                   `json:"source_calls"`
	TimelineStatus domain.TimelineStatus `json:"timeline_status"`
	Signals        int                   `json:"signals"`
	TimelineItems  int                   `json:"timeline_items"`
}

type mockRunbookKnowledgeSummary struct {
	Mode        domain.RunbookGuidanceDataSource `json:"mode"`
	SourceCalls int                              `json:"source_calls"`
	Status      domain.RunbookGuidanceStatus     `json:"status"`
	Items       int                              `json:"items"`
	Steps       int                              `json:"steps"`
}

type mockLLMSummary struct {
	Mode              domain.SummaryMode   `json:"mode"`
	Status            domain.SummaryStatus `json:"status"`
	Provider          string               `json:"provider"`
	PromptVersion     string               `json:"prompt_version"`
	ExternalAPICalls  int                  `json:"external_api_calls"`
	CredentialsNeeded bool                 `json:"credentials_required"`
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
	Mode                 string `json:"mode"`
	LogicalObservations  int    `json:"logical_observations"`
	SchemaCalls          int    `json:"schema_calls"`
	BackendExecuteCalls  int    `json:"backend_execute_calls"`
	ProviderAPICalls     int    `json:"provider_api_calls"`
	QueryAuditEvents     int    `json:"query_audit_events"`
	QueryStepCheckpoints int    `json:"query_step_checkpoints"`
	RawLogRowsReturned   int    `json:"raw_log_rows_returned"`
	CurrentErrorCount    int64  `json:"current_error_count"`
	BaselineErrorCount   int64  `json:"baseline_error_count"`
}

type gatedMockExecutor struct {
	delegate application.GovernedSLSExecutor
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (executor *gatedMockExecutor) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	return executor.delegate.ResolveQueryGovernance(ctx, spec)
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
	mockCatalog := slsmock.NewCatalog(mockPrincipal)
	queryGateway, err := queryapp.NewGateway(
		mockCatalog,
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
	quotaPolicy := domain.TenantQuotaPolicy{
		Version: application.TenantQuotaPolicyVersion, Window: time.Hour,
		MaxObservations: 10, MaxAPICalls: 40, MaxProcessedBytes: 10 << 20,
		ReservedBytesPerObservation: 1 << 20,
	}
	quotaExecutor, err := application.NewQuotaExecutor(
		queryGateway, store, quotaPolicy, func() time.Time { return messageAt.Add(time.Second) },
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	gatedExecutor := &gatedMockExecutor{
		delegate: quotaExecutor,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	checkpointedExecutor, err := application.NewCheckpointExecutor(
		gatedExecutor,
		store,
		func() time.Time { return messageAt.Add(time.Second) },
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	released := false
	operationalSource, err := signalmock.NewIncident("mock/order-service/prod", requestEnd.Add(-30*time.Minute), requestEnd)
	if err != nil {
		return mockE2EResult{}, err
	}
	engine, err := eino.New(
		ctx,
		checkpointedExecutor,
		func() time.Time { return messageAt.Add(time.Second) },
		eino.WithChangeSource(localMockChangeSource{event: domain.ChangeEvent{
			ID: "chg_mock_release_v2", ResourceID: "mock/order-service/prod", Kind: domain.ChangeKindRelease,
			StartedAt: requestEnd.Add(-35 * time.Minute), CompletedAt: requestEnd.Add(-32 * time.Minute),
			FromVersion: "v1", ToVersion: "v2", Owner: "order-team", Summary: "mock release v2",
			AffectedInstances: []string{"order-pod-a"}, AffectedInstancesComplete: true,
		}}),
		eino.WithOperationalSignalSource(operationalSource),
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	summaryQuotaPolicy := domain.SummaryQuotaPolicy{
		Version: application.SummaryQuotaPolicyVersion, Window: time.Hour,
		MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: 4096,
	}
	summary, err := application.NewSummaryService(
		summarymock.New(), 5*time.Second, func() time.Time { return messageAt.Add(time.Second) },
		application.WithSummaryQuota(store, summaryQuotaPolicy),
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	runbookSource, err := runbookmock.NewIncident("mock/order-service/prod")
	if err != nil {
		return mockE2EResult{}, err
	}
	runbook, err := application.NewRunbookService(
		runbookSource, mockCatalog, domain.RunbookGuidanceSourceSyntheticMock,
		application.WithRunbookClock(func() time.Time { return messageAt.Add(time.Second) }),
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	worker, err := application.NewWorker(
		store, engine, "mock-investigation-worker", time.Minute,
		application.WithWorkerClock(func() time.Time { return messageAt.Add(time.Second) }),
		application.WithWorkerRunbook(runbook),
		application.WithWorkerSummary(summary),
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
	if investigation.Report.Summary == nil || investigation.Report.Summary.Mode != domain.SummaryModeMock || investigation.Report.Summary.Status != domain.SummaryGenerated {
		return mockE2EResult{}, fmt.Errorf("mock investigation is missing its governed summary: %#v", investigation.Report.Summary)
	}
	if investigation.Report.IncidentTimeline == nil || investigation.Report.IncidentTimeline.Status != domain.TimelineComplete {
		return mockE2EResult{}, fmt.Errorf("mock investigation is missing its incident timeline: %#v", investigation.Report.IncidentTimeline)
	}
	if investigation.Report.RunbookGuidance == nil || investigation.Report.RunbookGuidance.Status != domain.RunbookGuidanceComplete {
		return mockE2EResult{}, fmt.Errorf("mock investigation is missing governed runbook guidance: %#v", investigation.Report.RunbookGuidance)
	}
	runbookStats := runbookSource.Stats()
	if runbookStats.LookupCalls != 1 {
		return mockE2EResult{}, fmt.Errorf("unexpected mock runbook activity %#v", runbookStats)
	}
	operationalStats := operationalSource.Stats()
	if operationalStats.ListCalls != 1 {
		return mockE2EResult{}, fmt.Errorf("unexpected mock operational signal activity %#v", operationalStats)
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
	queryStepCheckpoints, err := store.CountQuerySteps(ctx, investigationID)
	if err != nil {
		return mockE2EResult{}, err
	}
	if queryStepCheckpoints != 2 {
		return mockE2EResult{}, fmt.Errorf("unexpected mock query checkpoint count %d", queryStepCheckpoints)
	}
	quotaStart := messageAt.Add(time.Second).Truncate(time.Hour)
	quotaUsage, err := store.GetTenantQuotaUsage(
		ctx, application.TenantQuotaID(mockPrincipal), quotaStart, quotaStart.Add(time.Hour), quotaPolicy,
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	if quotaUsage.Observations != 2 || quotaUsage.APICalls != int64(providerCalls) || quotaUsage.CircuitOpen {
		return mockE2EResult{}, fmt.Errorf("unexpected mock tenant quota usage %#v", quotaUsage)
	}
	summaryQuotaUsage, err := store.GetTenantSummaryQuotaUsage(
		ctx, domain.TrustedTenantID(mockPrincipal), quotaStart, quotaStart.Add(time.Hour), summaryQuotaPolicy,
	)
	if err != nil {
		return mockE2EResult{}, err
	}
	if summaryQuotaUsage.Requests != 1 || summaryQuotaUsage.Tokens != 0 || summaryQuotaUsage.CircuitOpen {
		return mockE2EResult{}, fmt.Errorf("unexpected mock LLM quota usage %#v", summaryQuotaUsage)
	}

	return mockE2EResult{
		Scenario: "feishu_to_sls_investigation_full_mock",
		Safety: mockSafetySummary{
			ExternalNetworkCalls: 0,
			CredentialsRequired:  false,
			DataNotice:           "All Feishu messages, SLS aggregates, change events, operational signals, runbook guidance, and report summaries are deterministic test data.",
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
			Mode:                 "mock",
			LogicalObservations:  len(investigation.Report.Evidence),
			SchemaCalls:          backendStats.SchemaCalls,
			BackendExecuteCalls:  backendStats.ExecuteCalls,
			ProviderAPICalls:     backendStats.ProviderAPICalls,
			QueryAuditEvents:     len(audits),
			QueryStepCheckpoints: queryStepCheckpoints,
			RawLogRowsReturned:   0,
			CurrentErrorCount:    currentErrors,
			BaselineErrorCount:   baselineErrors,
		},
		TenantQuota:  quotaUsage,
		LLMQuota:     summaryQuotaUsage,
		ChangeSource: "mock",
		OperationalSignals: mockOperationalSignalSummary{
			Mode: "mock", SourceCalls: operationalStats.ListCalls,
			TimelineStatus: investigation.Report.IncidentTimeline.Status,
			Signals:        len(investigation.Report.IncidentTimeline.Signals),
			TimelineItems:  len(investigation.Report.IncidentTimeline.Items),
		},
		RunbookKnowledge: mockRunbookKnowledgeSummary{
			Mode: investigation.Report.RunbookGuidance.DataSource, SourceCalls: runbookStats.LookupCalls,
			Status: investigation.Report.RunbookGuidance.Status,
			Items:  len(investigation.Report.RunbookGuidance.Items),
			Steps:  countRunbookSteps(investigation.Report.RunbookGuidance.Items),
		},
		LLMSummary: mockLLMSummary{
			Mode: investigation.Report.Summary.Mode, Status: investigation.Report.Summary.Status,
			Provider: investigation.Report.Summary.Provider, PromptVersion: investigation.Report.Summary.PromptVersion,
			ExternalAPICalls: 0, CredentialsNeeded: false,
		},
		Investigation: investigation,
	}, nil
}

func countRunbookSteps(items []domain.RunbookGuidanceItem) int {
	total := 0
	for _, item := range items {
		total += len(item.Steps)
	}
	return total
}
