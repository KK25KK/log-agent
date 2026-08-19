package evalmock

import (
	"context"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
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
