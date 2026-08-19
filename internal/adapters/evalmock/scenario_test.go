package evalmock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/observability"
)

func TestScenarioReturnsBoundedDeepCopiedFixturesAndStats(t *testing.T) {
	evaluationCase := fixtureCase()
	scenario, err := New(evaluationCase, "inv_eval_supported")
	if err != nil {
		t.Fatal(err)
	}

	// The scenario owns a copy; mutating the caller's decoded artifact cannot
	// alter what the graph observes.
	evaluationCase.Current.ErrorPatterns[0].Label = "mutated-outside-scenario"
	evaluationCase.ChangeSet.Events[0].AffectedInstances[0] = "mutated-outside-scenario"

	currentSpec := fixtureSpec("inv_eval_supported", scenario.Executor.request, "current")
	governance, err := scenario.Executor.ResolveQueryGovernance(context.Background(), currentSpec)
	if err != nil {
		t.Fatal(err)
	}
	current, err := scenario.Executor.Execute(context.Background(), currentSpec)
	if err != nil {
		t.Fatal(err)
	}
	if current.QuerySpecHash == "" || current.GovernanceFingerprint != governance {
		t.Fatalf("derived query identity is incomplete: %#v", current)
	}
	if current.ErrorPatterns[0].Label != "payment_timeout" {
		t.Fatalf("caller mutation reached scenario: %#v", current.ErrorPatterns)
	}
	current.ErrorPatterns[0].Label = "mutated-return-value"
	if scenario.Executor.current.ErrorPatterns[0].Label != "payment_timeout" {
		t.Fatal("returned query result aliases the scenario fixture")
	}

	baselineSpec := fixtureSpec("inv_eval_supported", scenario.Executor.request, "baseline")
	if _, err := scenario.Executor.Execute(context.Background(), baselineSpec); err != nil {
		t.Fatal(err)
	}
	changeQuery := expectedChangeQuery(scenario.ChangeSource.request, scenario.ChangeSource.resourceID)
	changeSet, err := scenario.ChangeSource.List(context.Background(), changeQuery)
	if err != nil {
		t.Fatal(err)
	}
	if changeSet.Events[0].AffectedInstances[0] != "order-pod-a" {
		t.Fatalf("caller mutation reached change source: %#v", changeSet.Events)
	}
	changeSet.Events[0].AffectedInstances[0] = "mutated-return-value"
	if scenario.ChangeSource.changeSet.Events[0].AffectedInstances[0] != "order-pod-a" {
		t.Fatal("returned change set aliases the scenario fixture")
	}

	stats := scenario.Stats()
	wantBytes := scenario.Executor.current.ProcessedBytes + scenario.Executor.baseline.ProcessedBytes
	if !stats.QueryContractValid || stats.LogicalSLSCalls != 2 || stats.GovernanceCalls != 1 || stats.ProviderAPICalls != evaluation.ExpectedProviderAPICalls || stats.ProcessedBytes != wantBytes || stats.ChangeSourceCalls != 1 {
		t.Fatalf("unexpected scenario stats: %#v", stats)
	}
	stats.QuerySpecs[0].Name = "mutated-stats"
	stats.ChangeQueries[0].ResourceID = "mutated-stats"
	again := scenario.Stats()
	if again.QuerySpecs[0].Name != "current" || again.ChangeQueries[0].ResourceID != scenario.ChangeSource.resourceID {
		t.Fatal("stats snapshot aliases scenario state")
	}
}

func TestExecutorRejectsQueryContractDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.QuerySpec)
	}{
		{name: "investigation", mutate: func(spec *domain.QuerySpec) { spec.InvestigationID = "inv_other" }},
		{name: "service", mutate: func(spec *domain.QuerySpec) { spec.Service = "other-service" }},
		{name: "environment", mutate: func(spec *domain.QuerySpec) { spec.Environment = "staging" }},
		{name: "requester", mutate: func(spec *domain.QuerySpec) { spec.Requester.UserID = "other-user" }},
		{name: "template", mutate: func(spec *domain.QuerySpec) { spec.TemplateID = "raw-sql" }},
		{name: "unknown name", mutate: func(spec *domain.QuerySpec) { spec.Name = "future" }},
		{name: "current start", mutate: func(spec *domain.QuerySpec) { spec.StartTime = spec.StartTime.Add(time.Second) }},
		{name: "current end", mutate: func(spec *domain.QuerySpec) { spec.EndTime = spec.EndTime.Add(time.Second) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluationCase := fixtureCase()
			scenario, err := New(evaluationCase, "inv_eval_supported")
			if err != nil {
				t.Fatal(err)
			}
			spec := fixtureSpec("inv_eval_supported", evaluationCase.Request, "current")
			test.mutate(&spec)
			if _, err := scenario.Executor.Execute(context.Background(), spec); err == nil {
				t.Fatalf("query contract drift was accepted: %#v", spec)
			}
		})
	}

	evaluationCase := fixtureCase()
	scenario, err := New(evaluationCase, "inv_eval_supported")
	if err != nil {
		t.Fatal(err)
	}
	baseline := fixtureSpec("inv_eval_supported", evaluationCase.Request, "baseline")
	baseline.StartTime = baseline.StartTime.Add(-time.Second)
	if _, err := scenario.Executor.ResolveQueryGovernance(context.Background(), baseline); err == nil {
		t.Fatal("governance resolver accepted a drifting baseline window")
	}

	scenario, err = New(evaluationCase, "inv_eval_supported")
	if err != nil {
		t.Fatal(err)
	}
	current := fixtureSpec("inv_eval_supported", evaluationCase.Request, "current")
	if _, err := scenario.Executor.Execute(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.Executor.Execute(context.Background(), current); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate logical query error=%v", err)
	}
	if stats := scenario.Stats(); stats.LogicalSLSCalls != 2 || stats.ProviderAPICalls != domain.ErrorAnalysisAPICalls {
		t.Fatalf("duplicate attempt was not visible in stats: %#v", stats)
	} else if stats.QueryContractValid {
		t.Fatalf("duplicate attempt did not invalidate the query contract: %#v", stats)
	}
}

func TestChangeSourceRejectsContractDriftAndDuplicateCalls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.ChangeQuery)
	}{
		{name: "resource", mutate: func(query *domain.ChangeQuery) { query.ResourceID = "mock/other/prod" }},
		{name: "start", mutate: func(query *domain.ChangeQuery) { query.StartTime = query.StartTime.Add(time.Second) }},
		{name: "end", mutate: func(query *domain.ChangeQuery) { query.EndTime = query.EndTime.Add(time.Second) }},
		{name: "limit", mutate: func(query *domain.ChangeQuery) { query.Limit-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluationCase := fixtureCase()
			scenario, err := New(evaluationCase, "inv_eval_supported")
			if err != nil {
				t.Fatal(err)
			}
			query := expectedChangeQuery(evaluationCase.Request, evaluationCase.Current.ResourceID)
			test.mutate(&query)
			if _, err := scenario.ChangeSource.List(context.Background(), query); err == nil {
				t.Fatalf("change-query contract drift was accepted: %#v", query)
			}
			if stats := scenario.Stats(); stats.ChangeSourceCalls != 1 {
				t.Fatalf("invalid call was not recorded: %#v", stats)
			} else if stats.QueryContractValid {
				t.Fatalf("invalid change query did not invalidate the contract: %#v", stats)
			}
		})
	}

	evaluationCase := fixtureCase()
	scenario, err := New(evaluationCase, "inv_eval_supported")
	if err != nil {
		t.Fatal(err)
	}
	query := expectedChangeQuery(evaluationCase.Request, evaluationCase.Current.ResourceID)
	if _, err := scenario.ChangeSource.List(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.ChangeSource.List(context.Background(), query); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate change query error=%v", err)
	}
}

func TestScenarioRejectsInvalidFixtureIdentity(t *testing.T) {
	evaluationCase := fixtureCase()
	evaluationCase.Baseline.GovernanceFingerprint = strings.Repeat("b", 64)
	if _, err := New(evaluationCase, "inv_eval_supported"); err == nil {
		t.Fatal("mismatched cross-window governance was accepted")
	}

	evaluationCase = fixtureCase()
	evaluationCase.Current.QuerySpecHash = "fixture-must-not-own-this"
	if _, err := New(evaluationCase, "inv_eval_supported"); err == nil {
		t.Fatal("fixture-owned query spec hash was accepted")
	}
}

func TestBuiltInDatasetConstructsEveryScenario(t *testing.T) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	for _, evaluationCase := range dataset.Cases {
		t.Run(evaluationCase.ID, func(t *testing.T) {
			investigationID := "inv_" + evaluationCase.ID
			scenario, err := New(evaluationCase, investigationID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := scenario.Executor.Execute(context.Background(), fixtureSpec(investigationID, evaluationCase.Request, "current")); err != nil {
				t.Fatal(err)
			}
			if _, err := scenario.Executor.Execute(context.Background(), fixtureSpec(investigationID, evaluationCase.Request, "baseline")); err != nil {
				t.Fatal(err)
			}
			if evaluationCase.Expected.ChangeSourceCalls == 1 {
				if _, err := scenario.ChangeSource.List(context.Background(), expectedChangeQuery(evaluationCase.Request, evaluationCase.Current.ResourceID)); err != nil {
					t.Fatal(err)
				}
			}
			stats := scenario.ExecutionStats()
			if !stats.QueryContractValid || stats.LogicalSLSCalls != evaluationCase.Expected.LogicalSLSCalls || stats.ProviderAPICalls != evaluationCase.Expected.ProviderAPICalls || stats.ChangeSourceCalls != evaluationCase.Expected.ChangeSourceCalls {
				t.Fatalf("built-in fixture activity does not match its label: %#v", stats)
			}
			if stats.ProcessedBytes > evaluationCase.Expected.MaxProcessedBytes {
				t.Fatalf("processed bytes=%d exceed case ceiling=%d", stats.ProcessedBytes, evaluationCase.Expected.MaxProcessedBytes)
			}
		})
	}
}

func TestScenarioRecordsToolUsageMatchingStatsWithoutSensitivePayloads(t *testing.T) {
	evaluationCase := fixtureCase()
	secret := "SENSITIVE_BEARER_abc123"
	evaluationCase.Request.Service = secret
	evaluationCase.Current.TopError = secret
	evaluationCase.Current.ErrorPatterns[0].Label = secret
	evaluationCase.ChangeSet.Events[0].Summary = secret

	ctx, recorder := scenarioTraceContext(t, "case-tool-success")
	scenario, err := New(evaluationCase, "inv_tool_success", WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := eino.New(
		context.Background(),
		scenario.Executor,
		time.Now,
		eino.WithChangeSource(scenario.ChangeSource),
		eino.WithObserver(recorder),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Run(ctx, "inv_tool_success", evaluationCase.Request); err != nil {
		t.Fatal(err)
	}

	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 {
		t.Fatalf("tool trace is incomplete: %#v", trace)
	}
	if err := domain.ValidateAgentTrace(trace); err != nil {
		t.Fatalf("validate tool trace: %v", err)
	}
	stats := scenario.Stats()
	var slsLogical, providerCalls, processedBytes, changeCalls int64
	seen := make(map[domain.AgentSpanName]int)
	for _, event := range trace.Events {
		if event.Layer != domain.AgentLayerTool || event.Phase == domain.AgentPhaseStarted {
			continue
		}
		if event.Phase != domain.AgentPhaseSucceeded || event.ToolUsage == nil || !event.ToolUsage.Complete {
			t.Fatalf("unexpected tool terminal: %#v", event)
		}
		seen[event.Name]++
		switch event.Name {
		case domain.AgentSpanSLSCurrent, domain.AgentSpanSLSBaseline:
			slsLogical += event.ToolUsage.LogicalCalls
			providerCalls += event.ToolUsage.ProviderCalls
			processedBytes += event.ToolUsage.ProcessedBytes
		case domain.AgentSpanChangeSourceList:
			changeCalls += event.ToolUsage.LogicalCalls
		}
	}
	if seen[domain.AgentSpanSLSCurrent] != 1 || seen[domain.AgentSpanSLSBaseline] != 1 || seen[domain.AgentSpanChangeSourceList] != 1 {
		t.Fatalf("unexpected tool terminals: %#v", seen)
	}
	if slsLogical != int64(stats.LogicalSLSCalls) || providerCalls != int64(stats.ProviderAPICalls) || processedBytes != stats.ProcessedBytes || changeCalls != int64(stats.ChangeSourceCalls) {
		t.Fatalf("trace usage does not match stats: trace=%d/%d/%d/%d stats=%#v", slsLogical, providerCalls, processedBytes, changeCalls, stats)
	}
	payload, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) {
		t.Fatalf("serialized trace leaked fixture payload: %s", payload)
	}
}

func TestScenarioRecordsFailedToolUsageWithoutChangingStats(t *testing.T) {
	evaluationCase := fixtureCase()
	ctx, recorder := scenarioTraceContext(t, "case-tool-failure")
	scenario, err := New(evaluationCase, "inv_tool_failure", WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	// Force the fixture boundary to reject the otherwise valid graph query.
	// This exercises a tool-contract failure without adding a second mock API.
	scenario.Executor.request.Service = "different-service"
	engine, err := eino.New(context.Background(), scenario.Executor, time.Now, eino.WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Run(ctx, "inv_tool_failure", evaluationCase.Request); err == nil {
		t.Fatal("want fixture contract failure")
	}

	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 {
		t.Fatalf("failed tool did not close trace: %#v", trace)
	}
	stats := scenario.Stats()
	if stats.LogicalSLSCalls != 1 || stats.ProviderAPICalls != 0 || stats.ProcessedBytes != 0 {
		t.Fatalf("failed usage changed stats contract: %#v", stats)
	}
	terminal := findToolTerminal(t, trace, domain.AgentSpanSLSCurrent)
	if terminal.Phase != domain.AgentPhaseFailed || terminal.FailureClass != domain.FailureClassContractViolation || terminal.FailureCode != domain.AgentFailureCodeContractViolation || terminal.ToolUsage == nil {
		t.Fatalf("unexpected failed tool terminal: %#v", terminal)
	}
	if terminal.ToolUsage.LogicalCalls != int64(stats.LogicalSLSCalls) || terminal.ToolUsage.ProviderCalls != 0 || terminal.ToolUsage.ProcessedBytes != 0 || terminal.ToolUsage.Complete {
		t.Fatalf("failed tool usage does not match stats: event=%#v stats=%#v", terminal.ToolUsage, stats)
	}
}

func TestScenarioOmitsChangeToolWhenGraphSkipsChangeLookup(t *testing.T) {
	evaluationCase := fixtureCase()
	evaluationCase.Current = cloneQueryResult(evaluationCase.Baseline)
	evaluationCase.Current.QueryID = "eval-current-no-spike"
	evaluationCase.Current.ProcessedBytes = 3072
	ctx, recorder := scenarioTraceContext(t, "case-no-change-tool")
	scenario, err := New(evaluationCase, "inv_no_change_tool", WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := eino.New(
		context.Background(),
		scenario.Executor,
		time.Now,
		eino.WithChangeSource(scenario.ChangeSource),
		eino.WithObserver(recorder),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, report, err := engine.Run(ctx, "inv_no_change_tool", evaluationCase.Request); err != nil {
		t.Fatal(err)
	} else if report.Outcome != "no_significant_spike" {
		t.Fatalf("unexpected outcome: %s", report.Outcome)
	}
	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 {
		t.Fatalf("conditional trace is incomplete: %#v", trace)
	}
	for _, event := range trace.Events {
		if event.Name == domain.AgentSpanChangeSourceList {
			t.Fatalf("change tool was recorded without a source lookup: %#v", event)
		}
	}
	if stats := scenario.Stats(); stats.ChangeSourceCalls != 0 || stats.LogicalSLSCalls != 2 {
		t.Fatalf("conditional tool activity drifted: %#v", stats)
	}
}

func TestScenarioObserverRequiresRunContext(t *testing.T) {
	observer := &scenarioCountingObserver{}
	scenario, err := New(fixtureCase(), "inv_no_run_context", WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	request := scenario.Executor.request
	if _, err := scenario.Executor.Execute(context.Background(), fixtureSpec("inv_no_run_context", request, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.Executor.Execute(context.Background(), fixtureSpec("inv_no_run_context", request, "baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.ChangeSource.List(context.Background(), expectedChangeQuery(request, scenario.ChangeSource.resourceID)); err != nil {
		t.Fatal(err)
	}
	if observer.events != 0 {
		t.Fatalf("observer received %d events without RunContext", observer.events)
	}
	stats := scenario.Stats()
	if stats.LogicalSLSCalls != 2 || stats.ProviderAPICalls != evaluation.ExpectedProviderAPICalls || stats.ChangeSourceCalls != 1 {
		t.Fatalf("no-context observation changed adapter stats: %#v", stats)
	}
}

func fixtureCase() evaluation.EvaluationCase {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	request := domain.InvestigationRequest{
		Service:     "order-service",
		Environment: "prod",
		StartTime:   start,
		EndTime:     start.Add(30 * time.Minute),
		Requester: domain.Principal{
			AppID:     "mock-app",
			TenantKey: "mock-tenant",
			UserID:    "mock-user",
		},
	}
	current := fixtureResult(
		"eval-current",
		120,
		15360,
		[]domain.CountBucket{
			{Label: "payment_timeout", Count: 90},
			{Label: "inventory_lock", Count: 20},
			{Label: "signature_invalid", Count: 10},
		},
		[]domain.CountBucket{
			{Label: "order-pod-a", Count: 80},
			{Label: "order-pod-b", Count: 30},
			{Label: "order-pod-c", Count: 10},
		},
	)
	baseline := fixtureResult(
		"eval-baseline",
		20,
		2560,
		[]domain.CountBucket{
			{Label: "inventory_lock", Count: 10},
			{Label: "database_timeout", Count: 5},
			{Label: "payment_timeout", Count: 5},
		},
		[]domain.CountBucket{
			{Label: "order-pod-b", Count: 10},
			{Label: "order-pod-c", Count: 10},
		},
	)
	return evaluation.EvaluationCase{
		ID:          "spike-supported-change",
		Description: "synthetic supported candidate",
		Request:     request,
		Current:     current,
		Baseline:    baseline,
		ChangeSet: domain.ChangeSet{
			SourceVersion: "eval-change-v1",
			Complete:      true,
			Events: []domain.ChangeEvent{{
				ID:                        "chg-supported",
				ResourceID:                current.ResourceID,
				Kind:                      domain.ChangeKindRelease,
				StartedAt:                 start.Add(-10 * time.Minute),
				CompletedAt:               start.Add(-5 * time.Minute),
				FromVersion:               "v1",
				ToVersion:                 "v2",
				Owner:                     "order-team",
				Summary:                   "synthetic release",
				AffectedInstances:         []string{"order-pod-a"},
				AffectedInstancesComplete: true,
			}},
		},
	}
}

func fixtureResult(queryID string, errorCount, processedBytes int64, patterns, instances []domain.CountBucket) domain.QueryResult {
	result := domain.QueryResult{
		QueryID:                 queryID,
		ResourceID:              "mock/order-service/prod",
		TemplateID:              domain.ErrorAnalysisTemplateID,
		TemplateVersion:         domain.ErrorAnalysisTemplateVersion,
		SchemaFingerprint:       "eval-schema-v1",
		PolicyVersion:           "query-policy-v2",
		GovernanceFingerprint:   strings.Repeat("a", 64),
		Progress:                "Complete",
		Complete:                true,
		NanosecondOrderedKnown:  true,
		NanosecondOrdered:       true,
		UsageKnown:              true,
		ProcessedRows:           errorCount * domain.ErrorAnalysisAPICalls,
		ProcessedBytes:          processedBytes,
		ElapsedMillisecond:      15,
		APICalls:                domain.ErrorAnalysisAPICalls,
		ErrorCount:              errorCount,
		ErrorPatterns:           patterns,
		Instances:               instances,
		ErrorPatternsExhaustive: bucketTotal(patterns) == errorCount,
		InstancesExhaustive:     bucketTotal(instances) == errorCount,
		PatternLimit:            domain.ErrorAnalysisPatternLimit,
		InstanceLimit:           domain.ErrorAnalysisInstanceLimit,
	}
	if len(patterns) > 0 {
		result.TopError = patterns[0].Label
		result.TopErrorCount = patterns[0].Count
	}
	return result
}

func fixtureSpec(investigationID string, request domain.InvestigationRequest, name string) domain.QuerySpec {
	start, end := request.StartTime, request.EndTime
	if name == "baseline" {
		duration := request.EndTime.Sub(request.StartTime)
		start, end = request.StartTime.Add(-duration), request.StartTime
	}
	return domain.QuerySpec{
		InvestigationID: investigationID,
		Name:            name,
		TemplateID:      domain.ErrorAnalysisTemplateID,
		Service:         request.Service,
		Environment:     request.Environment,
		StartTime:       start,
		EndTime:         end,
		Requester:       request.Requester,
	}
}

func expectedChangeQuery(request domain.InvestigationRequest, resourceID string) domain.ChangeQuery {
	duration := request.EndTime.Sub(request.StartTime)
	return domain.ChangeQuery{
		ResourceID: resourceID,
		StartTime:  request.StartTime.Add(-duration),
		EndTime:    request.EndTime,
		Limit:      domain.MaxChangeEvents,
	}
}

func bucketTotal(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

type scenarioCountingObserver struct {
	events int
}

func (observer *scenarioCountingObserver) Record(domain.AgentEvent) {
	observer.events++
}

func scenarioTraceContext(t *testing.T, caseID string) (context.Context, *observability.BoundedRecorder) {
	t.Helper()
	run := observability.RunContext{
		EvaluationRunID:    "evaluation-run-1",
		TraceID:            "trace-" + caseID,
		RunID:              "engine-run-" + caseID,
		CaseID:             caseID,
		VersionFingerprint: strings.Repeat("b", 64),
	}
	recorder := observability.NewBoundedRecorder(64, run)
	return observability.WithRunContext(context.Background(), run), recorder
}

func findToolTerminal(t *testing.T, trace domain.AgentTrace, name domain.AgentSpanName) domain.AgentEvent {
	t.Helper()
	for _, event := range trace.Events {
		if event.Layer == domain.AgentLayerTool && event.Name == name && event.Phase != domain.AgentPhaseStarted {
			return event
		}
	}
	t.Fatalf("tool terminal %s was not recorded", name)
	return domain.AgentEvent{}
}
