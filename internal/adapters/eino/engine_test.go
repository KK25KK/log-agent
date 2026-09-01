package eino

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/observability"
)

type fakeExecutor struct {
	incomplete bool
	err        error
	got        []domain.QuerySpec
	results    map[string]domain.QueryResult
}

type fakeChangeSource struct {
	set domain.ChangeSet
	err error
	got []domain.ChangeQuery
}

type fakeOperationalSignalSource struct {
	set domain.OperationalSignalSet
	err error
	got []domain.OperationalSignalQuery
}

type cancellingChangeSource struct {
	cancel context.CancelFunc
}

type cancellingOperationalSignalSource struct {
	cancel context.CancelFunc
}

type countingObserver struct {
	events int
}

func (observer *countingObserver) Record(domain.AgentEvent) {
	observer.events++
}

func (s cancellingChangeSource) List(_ context.Context, _ domain.ChangeQuery) (domain.ChangeSet, error) {
	s.cancel()
	return domain.ChangeSet{}, context.Canceled
}

func (source cancellingOperationalSignalSource) List(_ context.Context, _ domain.OperationalSignalQuery) (domain.OperationalSignalSet, error) {
	source.cancel()
	return domain.OperationalSignalSet{}, context.Canceled
}

func (f *fakeChangeSource) List(_ context.Context, query domain.ChangeQuery) (domain.ChangeSet, error) {
	f.got = append(f.got, query)
	if f.err != nil {
		return domain.ChangeSet{}, f.err
	}
	return f.set, nil
}

func (source *fakeOperationalSignalSource) List(_ context.Context, query domain.OperationalSignalQuery) (domain.OperationalSignalSet, error) {
	source.got = append(source.got, query)
	if source.err != nil {
		return domain.OperationalSignalSet{}, source.err
	}
	return source.set, nil
}

func (f *fakeExecutor) Execute(_ context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	f.got = append(f.got, spec)
	if f.err != nil {
		return domain.QueryResult{}, f.err
	}
	if result, exists := f.results[spec.Name]; exists {
		return result, nil
	}
	result := fakeAnalysisResult(spec.Name)
	result.Complete = !f.incomplete
	if f.incomplete {
		result.Progress = "Incomplete"
		result.ErrorPatternsExhaustive = false
		result.InstancesExhaustive = false
	}
	return result, nil
}

func TestEngineDoesNotCalculateRatioFromZeroBaseline(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	executor := &fakeExecutor{results: map[string]domain.QueryResult{
		"current": analysisResult("current", 20,
			[]domain.CountBucket{{Label: "timeout", Count: 15}, {Label: "other", Count: 5}},
			[]domain.CountBucket{{Label: "pod-a", Count: 20}},
		),
		"baseline": analysisResult("baseline", 0, nil, nil),
	}}
	engine, err := New(context.Background(), executor, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "data_insufficient" || report.Findings[0].Conclusive {
		t.Fatalf("zero baseline produced a conclusive ratio: %#v", report)
	}
}

func TestEngineRejectsUntraceableQueryResult(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	invalid := fakeAnalysisResult("current")
	invalid.QueryID = ""
	executor := &fakeExecutor{results: map[string]domain.QueryResult{"current": invalid}}
	engine, err := New(context.Background(), executor, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err == nil {
		t.Fatal("want invalid query result error")
	}
}

func TestEngineRejectsCrossWindowGovernanceDriftBeforeReport(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	mutations := map[string]func(*domain.QueryResult){
		"governance fingerprint": func(result *domain.QueryResult) { result.GovernanceFingerprint = strings.Repeat("c", 64) },
		"resource":               func(result *domain.QueryResult) { result.ResourceID = "resource-other" },
		"template":               func(result *domain.QueryResult) { result.TemplateVersion = "template-v3" },
		"schema":                 func(result *domain.QueryResult) { result.SchemaFingerprint = "schema-v3" },
		"policy":                 func(result *domain.QueryResult) { result.PolicyVersion = "policy-v3" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			current := fakeAnalysisResult("current")
			baseline := fakeAnalysisResult("baseline")
			mutate(&baseline)
			engine, err := New(context.Background(), &fakeExecutor{results: map[string]domain.QueryResult{
				"current": current, "baseline": baseline,
			}}, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			evidence, report, err := engine.Run(context.Background(), "inv_governance_drift", domain.InvestigationRequest{
				Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
			})
			if err == nil {
				t.Fatalf("cross-window %s drift formed a report: %#v", name, report)
			}
			if evidence != nil || report.InvestigationID != "" {
				t.Fatalf("cross-window %s drift escaped fail-closed graph: evidence=%#v report=%#v", name, evidence, report)
			}
		})
	}
}

func TestEngineBuildsEvidenceBackedSpikeReport(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	generatedAt := start.Add(time.Hour)
	executor := &fakeExecutor{}
	engine, err := New(context.Background(), executor, func() time.Time { return generatedAt })
	if err != nil {
		t.Fatal(err)
	}

	evidence, report, err := engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service:     "order-service",
		Environment: "prod",
		StartTime:   start,
		EndTime:     start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.got) != 2 || executor.got[0].Name != "current" || executor.got[1].Name != "baseline" {
		t.Fatalf("unexpected queries: %#v", executor.got)
	}
	if len(evidence) != 2 || report.Outcome != "spike_detected" {
		t.Fatalf("unexpected output: evidence=%#v report=%#v", evidence, report)
	}
	if !report.Findings[0].Conclusive || len(report.Findings[0].EvidenceIDs) != 2 {
		t.Fatalf("finding is not evidence backed: %#v", report.Findings[0])
	}
	if len(report.Findings) < 4 || len(report.Recommendations) == 0 {
		t.Fatalf("M2 analysis was not included: %#v", report)
	}
	assertReportReferencesExistingEvidence(t, report)
	codes := findingCodes(report.Findings)
	for _, code := range []string{"error_spike", "error_pattern_share", "new_error_pattern", "instance_distribution"} {
		if !codes[code] {
			t.Fatalf("finding code %q missing from %#v", code, report.Findings)
		}
	}
	if !report.GeneratedAt.Equal(generatedAt) {
		t.Fatalf("unexpected generated time: %v", report.GeneratedAt)
	}
}

func TestEngineBuildsCountOnlyReportAndSkipsDimensionSources(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	changeSource := &fakeChangeSource{err: errors.New("must not be called")}
	signalSource := &fakeOperationalSignalSource{err: errors.New("must not be called")}
	executor := &fakeExecutor{results: map[string]domain.QueryResult{
		"current":  countResult("current", 120),
		"baseline": countResult("baseline", 20),
	}}
	engine, err := New(context.Background(), executor, time.Now, WithChangeSource(changeSource), WithOperationalSignalSource(signalSource))
	if err != nil {
		t.Fatal(err)
	}
	evidence, report, err := engine.Run(context.Background(), "inv_count", domain.InvestigationRequest{
		Service: "dam-server", Environment: "test", TemplateID: domain.ErrorCountTemplateID,
		StartTime: start, EndTime: start.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "spike_detected" || len(evidence) != 2 || len(executor.got) != 2 {
		t.Fatalf("unexpected count-only report: evidence=%#v report=%#v", evidence, report)
	}
	if len(changeSource.got) != 0 || len(signalSource.got) != 0 {
		t.Fatalf("count-only report called dimensional sources: changes=%d signals=%d", len(changeSource.got), len(signalSource.got))
	}
	if report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisInconclusive || report.IncidentTimeline == nil || report.IncidentTimeline.Status != domain.TimelineInconclusive {
		t.Fatalf("count-only limitations are not explicit: %#v", report)
	}
	for _, item := range evidence {
		if item.TemplateID != domain.ErrorCountTemplateID || item.APICalls != 2 || item.TopError != "" || len(item.ErrorPatterns) != 0 || len(item.Instances) != 0 {
			t.Fatalf("count-only evidence leaked dimensions: %#v", item)
		}
	}
	for _, finding := range report.Findings {
		if strings.Contains(finding.Statement, "错误模式") || strings.Contains(finding.Statement, "实例") || strings.Contains(finding.Statement, "根因") {
			t.Fatalf("count-only finding overclaimed: %#v", finding)
		}
	}
}

func TestEngineKeepsBoundedBaselineAbsenceAsCandidate(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := analysisResult("current", 100,
		[]domain.CountBucket{{Label: "new-pattern", Count: 60}, {Label: "known", Count: 40}},
		[]domain.CountBucket{{Label: "pod-a", Count: 100}},
	)
	baseline := analysisResult("baseline", 50,
		[]domain.CountBucket{{Label: "known", Count: 30}},
		[]domain.CountBucket{{Label: "pod-b", Count: 50}},
	)
	baseline.ErrorPatternsExhaustive = false
	executor := &fakeExecutor{results: map[string]domain.QueryResult{"current": current, "baseline": baseline}}
	engine, err := New(context.Background(), executor, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.Code == "new_error_pattern" {
			t.Fatalf("bounded baseline absence was promoted to confirmed newness: %#v", report.Findings)
		}
		if finding.Code == "new_error_pattern_candidate" && finding.Conclusive {
			t.Fatalf("candidate became conclusive: %#v", finding)
		}
	}
}

func TestEngineDoesNotConfirmNewnessForRedactedLabels(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := analysisResult("current", 100,
		[]domain.CountBucket{{Label: "[REDACTED_IP]", Count: 100, Redacted: true}},
		[]domain.CountBucket{{Label: "pod-a", Count: 100}},
	)
	baseline := analysisResult("baseline", 20,
		[]domain.CountBucket{{Label: "known", Count: 20}},
		[]domain.CountBucket{{Label: "pod-b", Count: 20}},
	)
	executor := &fakeExecutor{results: map[string]domain.QueryResult{"current": current, "baseline": baseline}}
	engine, err := New(context.Background(), executor, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if findingCodes(report.Findings)["new_error_pattern"] {
		t.Fatalf("redacted label was used to confirm newness: %#v", report.Findings)
	}
}

func TestEngineDoesNotConcludeFromIncompleteEvidence(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	engine, err := New(context.Background(), &fakeExecutor{incomplete: true}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	_, report, err := engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "data_insufficient" || report.Findings[0].Conclusive || report.Findings[0].Confidence != 0 {
		t.Fatalf("incomplete evidence produced a conclusion: %#v", report)
	}
}

func TestEnginePropagatesExecutorError(t *testing.T) {
	slsErr := errors.New("SLS unavailable")
	engine, err := New(context.Background(), &fakeExecutor{err: slsErr}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err = engine.Run(context.Background(), "inv_test", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if !errors.Is(err, slsErr) {
		t.Fatalf("want wrapped SLS error, got %v", err)
	}
}

func TestEngineRecordsClosedGraphTrace(t *testing.T) {
	ctx, recorder := traceTestContext(t, "case-success")
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	engine, err := New(context.Background(), &fakeExecutor{}, time.Now, WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := engine.Run(ctx, "inv_trace_success", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 {
		t.Fatalf("trace is incomplete: %#v", trace)
	}
	if err := domain.ValidateAgentTrace(trace); err != nil {
		t.Fatalf("validate trace: %v", err)
	}
	if len(trace.Events) != 2*(1+len(graphNodeOrder)) {
		t.Fatalf("event count=%d, want %d", len(trace.Events), 2*(1+len(graphNodeOrder)))
	}
	assertSpanTerminal(t, trace, domain.AgentSpanEngineRun, domain.AgentPhaseSucceeded, "")
	for _, name := range graphNodeOrder {
		assertSpanTerminal(t, trace, name, domain.AgentPhaseSucceeded, "")
	}
}

func TestEngineRecordsSafeFailureAndSkippedNodes(t *testing.T) {
	ctx, recorder := traceTestContext(t, "case-failure")
	secret := "SENSITIVE_PROVIDER_ERROR_BEARER_abc123"
	engine, err := New(context.Background(), &fakeExecutor{err: errors.New(secret)}, time.Now, WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	if _, _, err := engine.Run(ctx, "inv_trace_failure", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	}); err == nil {
		t.Fatal("want executor failure")
	}

	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 {
		t.Fatalf("failed run did not close its trace: %#v", trace)
	}
	if err := domain.ValidateAgentTrace(trace); err != nil {
		t.Fatalf("validate trace: %v", err)
	}
	assertSpanTerminal(t, trace, domain.AgentSpanEngineRun, domain.AgentPhaseFailed, domain.FailureClassDependency)
	assertSpanTerminal(t, trace, domain.AgentSpanPlanQueries, domain.AgentPhaseSucceeded, "")
	assertSpanTerminal(t, trace, domain.AgentSpanExecuteQueries, domain.AgentPhaseFailed, domain.FailureClassDependency)
	assertSpanTerminal(t, trace, domain.AgentSpanBuildReport, domain.AgentPhaseSkipped, "")
	assertSpanTerminal(t, trace, domain.AgentSpanCorrelateChanges, domain.AgentPhaseSkipped, "")

	payload, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) {
		t.Fatalf("serialized trace leaked dependency error: %s", payload)
	}
}

func TestEngineObserverRequiresRunContext(t *testing.T) {
	observer := &countingObserver{}
	engine, err := New(context.Background(), &fakeExecutor{}, time.Now, WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	if _, _, err := engine.Run(context.Background(), "inv_no_trace", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if observer.events != 0 {
		t.Fatalf("observer received %d events without RunContext", observer.events)
	}
}

func TestEngineBuildsSupportedChangeCorrelationWithoutMoreSLSQueries(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	executor := &fakeExecutor{results: causeAnalysisResults()}
	source := &fakeChangeSource{set: domain.ChangeSet{
		SourceVersion: "change-catalog-v1",
		Complete:      true,
		Events: []domain.ChangeEvent{{
			ID:                        "chg-release-42",
			ResourceID:                "res-order-prod",
			Kind:                      domain.ChangeKindRelease,
			StartedAt:                 start.Add(-10 * time.Minute),
			CompletedAt:               start.Add(-5 * time.Minute),
			FromVersion:               "v41",
			ToVersion:                 "v42",
			Owner:                     "order-team",
			Summary:                   "release v42",
			AffectedInstances:         []string{"order-pod-a"},
			AffectedInstancesComplete: true,
		}},
	}}
	engine, err := New(context.Background(), executor, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}

	evidence, report, err := engine.Run(context.Background(), "inv_supported", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.got) != 2 {
		t.Fatalf("cause enrichment issued extra logical SLS queries: got %d", len(executor.got))
	}
	providerCalls := 0
	for _, item := range evidence {
		providerCalls += item.APICalls
	}
	if providerCalls != 2*domain.ErrorAnalysisAPICalls {
		t.Fatalf("provider calls=%d, want %d", providerCalls, 2*domain.ErrorAnalysisAPICalls)
	}
	if len(source.got) != 1 {
		t.Fatalf("change source calls = %d, want 1", len(source.got))
	}
	query := source.got[0]
	if query.ResourceID != "res-order-prod" || !query.StartTime.Equal(start.Add(-30*time.Minute)) || !query.EndTime.Equal(start.Add(30*time.Minute)) || query.Limit != domain.MaxChangeEvents {
		t.Fatalf("change query was not derived from evidence: %#v", query)
	}
	analysis := report.CauseAnalysis
	if analysis == nil || analysis.Status != domain.CauseAnalysisComplete || len(analysis.Hypotheses) != 1 || len(analysis.Ledger) != 7 {
		t.Fatalf("unexpected cause analysis: %#v", analysis)
	}
	hypothesis := analysis.Hypotheses[0]
	if hypothesis.Verdict != domain.CauseVerdictSupportedCandidate || hypothesis.Confidence != domain.CauseConfidenceCap || hypothesis.ConfidenceMethod != domain.CauseConfidenceMethod {
		t.Fatalf("unexpected supported hypothesis: %#v", hypothesis)
	}
	if len(hypothesis.SupportEntryIDs) != 4 || len(hypothesis.CounterEntryIDs) != 3 || len(hypothesis.Limitations) < 2 {
		t.Fatalf("hypothesis is missing its ledger contract: %#v", hypothesis)
	}
	for _, code := range []string{"error_spike", "temporal_precedence", "affected_instance_concentration", "baseline_shift"} {
		entry := ledgerByCode(t, analysis.Ledger, code)
		if entry.Role != domain.EvidenceTestSupport || entry.Result != domain.EvidenceTestPass {
			t.Fatalf("support test %q did not pass: %#v", code, entry)
		}
	}
	for _, code := range []string{"no_instance_overlap", "preexisting_concentration", "confounding_changes"} {
		entry := ledgerByCode(t, analysis.Ledger, code)
		if entry.Role != domain.EvidenceTestCounter || entry.Result != domain.EvidenceTestFail {
			t.Fatalf("counter test %q was not tested and absent: %#v", code, entry)
		}
	}

	_, secondReport, err := engine.Run(context.Background(), "inv_supported", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondReport.CauseAnalysis.Hypotheses[0].ID != hypothesis.ID || secondReport.CauseAnalysis.Ledger[0].ID != analysis.Ledger[0].ID {
		t.Fatalf("cause IDs are not stable across equivalent runs")
	}
}

func TestEngineRefutesChangeWithCompleteZeroInstanceOverlap(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	source := &fakeChangeSource{set: domain.ChangeSet{
		SourceVersion: "change-catalog-v1",
		Complete:      true,
		Events: []domain.ChangeEvent{{
			ID:                        "chg-unrelated",
			ResourceID:                "res-order-prod",
			Kind:                      domain.ChangeKindConfig,
			StartedAt:                 start.Add(-10 * time.Minute),
			CompletedAt:               start.Add(-5 * time.Minute),
			Owner:                     "platform-team",
			Summary:                   "unrelated config",
			AffectedInstances:         []string{"other-pod"},
			AffectedInstancesComplete: true,
		}},
	}}
	engine, err := New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_refuted", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	analysis := report.CauseAnalysis
	if analysis == nil || analysis.Status != domain.CauseAnalysisComplete || analysis.Hypotheses[0].Verdict != domain.CauseVerdictRefuted {
		t.Fatalf("zero overlap was not a complete refutation: %#v", analysis)
	}
	if analysis.Hypotheses[0].Confidence != 0.05 {
		t.Fatalf("refuted confidence=%v, want deterministic 0.05", analysis.Hypotheses[0].Confidence)
	}
	entry := ledgerByCode(t, analysis.Ledger, "no_instance_overlap")
	if entry.Result != domain.EvidenceTestPass {
		t.Fatalf("zero-overlap counter-test = %#v", entry)
	}
}

func TestTemporalPrecedenceRequiresCompletionBeforeCurrentWindow(t *testing.T) {
	currentStart := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	baseline := domain.Evidence{StartTime: currentStart.Add(-30 * time.Minute)}
	current := domain.Evidence{StartTime: currentStart}

	tests := []struct {
		name      string
		completed time.Time
		want      domain.EvidenceTestResult
	}{
		{name: "inside baseline", completed: currentStart.Add(-time.Minute), want: domain.EvidenceTestPass},
		{name: "exact current boundary", completed: currentStart, want: domain.EvidenceTestFail},
		{name: "after current starts", completed: currentStart.Add(time.Second), want: domain.EvidenceTestFail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			change := domain.ChangeEvent{StartedAt: test.completed.Add(-time.Minute), CompletedAt: test.completed}
			got, _ := temporalPrecedence(change, current, baseline)
			if got != test.want {
				t.Fatalf("temporal precedence=%s, want %s", got, test.want)
			}
		})
	}
}

func TestEngineKeepsBoundedRedactedAndConfoundedChangeEvidenceInconclusive(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	baseEvent := domain.ChangeEvent{
		ID:                        "chg-one",
		ResourceID:                "res-order-prod",
		Kind:                      domain.ChangeKindRelease,
		StartedAt:                 start.Add(-10 * time.Minute),
		CompletedAt:               start.Add(-5 * time.Minute),
		FromVersion:               "v0",
		ToVersion:                 "v1",
		Owner:                     "order-team",
		Summary:                   "release",
		AffectedInstances:         []string{"order-pod-a"},
		AffectedInstancesComplete: true,
	}
	zeroOverlapEvent := baseEvent
	zeroOverlapEvent.ID = "chg-zero-overlap"
	zeroOverlapEvent.AffectedInstances = []string{"other-pod"}
	baselineUnknownResults := causeAnalysisResults()
	baselineUnknown := baselineUnknownResults["baseline"]
	baselineUnknown.Instances = baselineUnknown.Instances[:1]
	baselineUnknown.InstancesExhaustive = false
	baselineUnknownResults["baseline"] = baselineUnknown

	tests := []struct {
		name       string
		results    map[string]domain.QueryResult
		events     []domain.ChangeEvent
		unknown    string
		counter    string
		counterRes domain.EvidenceTestResult
	}{
		{
			name:    "non-exhaustive Top-K",
			results: nonExhaustiveCauseResults(),
			events:  []domain.ChangeEvent{baseEvent},
			unknown: "affected_instance_concentration",
		},
		{
			name:    "redacted instance",
			results: redactedCauseResults(),
			events:  []domain.ChangeEvent{baseEvent},
			unknown: "affected_instance_concentration",
		},
		{
			name:    "multiple candidate changes",
			results: causeAnalysisResults(),
			events: []domain.ChangeEvent{
				baseEvent,
				withChangeID(baseEvent, "chg-two", start.Add(-4*time.Minute)),
			},
			counter:    "confounding_changes",
			counterRes: domain.EvidenceTestPass,
		},
		{
			name:       "zero overlap with unknown baseline",
			results:    baselineUnknownResults,
			events:     []domain.ChangeEvent{zeroOverlapEvent},
			unknown:    "baseline_shift",
			counter:    "no_instance_overlap",
			counterRes: domain.EvidenceTestPass,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeChangeSource{set: domain.ChangeSet{
				SourceVersion: "change-catalog-v1",
				Complete:      true,
				Events:        test.events,
			}}
			engine, err := New(context.Background(), &fakeExecutor{results: test.results}, time.Now, WithChangeSource(source))
			if err != nil {
				t.Fatal(err)
			}
			_, report, err := engine.Run(context.Background(), "inv_inconclusive", domain.InvestigationRequest{
				Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			analysis := report.CauseAnalysis
			if analysis == nil || analysis.Status != domain.CauseAnalysisInconclusive {
				t.Fatalf("bounded or confounded input became conclusive: %#v", analysis)
			}
			for _, hypothesis := range analysis.Hypotheses {
				if hypothesis.Verdict != domain.CauseVerdictInconclusive {
					t.Fatalf("hypothesis unexpectedly conclusive: %#v", hypothesis)
				}
			}
			if test.unknown != "" && ledgerByCode(t, analysis.Ledger, test.unknown).Result != domain.EvidenceTestUnknown {
				t.Fatalf("bounded/redacted test was not UNKNOWN: %#v", analysis.Ledger)
			}
			if test.counter != "" && ledgerByCode(t, analysis.Ledger, test.counter).Result != test.counterRes {
				t.Fatalf("confounding counter-test mismatch: %#v", analysis.Ledger)
			}
		})
	}
}

func TestEnginePreservesM2ReportWhenChangeSourceReturnsInvalidData(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	source := &fakeChangeSource{set: domain.ChangeSet{
		SourceVersion: "change-catalog-v1",
		Complete:      true,
		Events: []domain.ChangeEvent{{
			ID:                        "chg-invalid-owner",
			ResourceID:                "res-order-prod",
			Kind:                      domain.ChangeKindRelease,
			StartedAt:                 start.Add(-10 * time.Minute),
			CompletedAt:               start.Add(-5 * time.Minute),
			ToVersion:                 "v42",
			Summary:                   "release v42",
			AffectedInstances:         []string{"order-pod-a"},
			AffectedInstancesComplete: true,
		}},
	}}
	engine, err := New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_invalid_change_source", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("invalid optional source failed the M2 investigation: %v", err)
	}
	if report.Outcome != "spike_detected" || report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisUnavailable {
		t.Fatalf("M2 result was not preserved after invalid change data: %#v", report)
	}
	if len(report.CauseAnalysis.MissingInputs) != 1 || report.CauseAnalysis.MissingInputs[0] != "valid_change_set" {
		t.Fatalf("invalid source reason was not explicit: %#v", report.CauseAnalysis)
	}

	source.set.SourceVersion = "invalid\nversion"
	source.set.Events[0].Owner = "order-team"
	engine, err = New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}
	_, report, err = engine.Run(context.Background(), "inv_invalid_source_version", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("invalid source version failed the M2 investigation: %v", err)
	}
	if report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisUnavailable || report.CauseAnalysis.SourceVersion != "" {
		t.Fatalf("invalid source version was not safely downgraded: %#v", report.CauseAnalysis)
	}
}

func TestEnginePreservesM2ReportWhenChangeSourceFails(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	source := &fakeChangeSource{err: errors.New("change backend unavailable")}
	engine, err := New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_source_error", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("optional source error failed the investigation: %v", err)
	}
	if report.Outcome != "spike_detected" || report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisUnavailable {
		t.Fatalf("M2 result was not preserved: %#v", report)
	}
}

func TestEngineDefaultsCauseEnrichmentToUnavailable(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	engine, err := New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_disabled_source", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "spike_detected" || report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisUnavailable {
		t.Fatalf("disabled optional source changed the M2 result: %#v", report)
	}
	if len(report.CauseAnalysis.MissingInputs) != 1 || report.CauseAnalysis.MissingInputs[0] != "change_source_disabled" {
		t.Fatalf("disabled source was not explicit: %#v", report.CauseAnalysis)
	}
}

func TestEnginePropagatesInvestigationCancellationFromChangeSource(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	engine, err := New(context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now, WithChangeSource(cancellingChangeSource{cancel: cancel}))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.Run(ctx, "inv_cancelled", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v, want context.Canceled", err)
	}
}

func TestEngineSkipsChangeSourceWithoutConclusiveSpike(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := analysisResult("current", 30,
		[]domain.CountBucket{{Label: "timeout", Count: 30}},
		[]domain.CountBucket{{Label: "pod-a", Count: 30}},
	)
	baseline := analysisResult("baseline", 20,
		[]domain.CountBucket{{Label: "timeout", Count: 20}},
		[]domain.CountBucket{{Label: "pod-a", Count: 20}},
	)
	current.ResourceID, baseline.ResourceID = "res-order-prod", "res-order-prod"
	source := &fakeChangeSource{err: errors.New("must not be called")}
	engine, err := New(context.Background(), &fakeExecutor{results: map[string]domain.QueryResult{"current": current, "baseline": baseline}}, time.Now, WithChangeSource(source))
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_no_spike", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.got) != 0 {
		t.Fatalf("change source called %d times without a spike", len(source.got))
	}
	if report.CauseAnalysis == nil || report.CauseAnalysis.Status != domain.CauseAnalysisSkippedNoSpike {
		t.Fatalf("unexpected skipped cause analysis: %#v", report.CauseAnalysis)
	}
}

func TestEngineBuildsGovernedCrossSignalTimelineWithoutMoreSLSQueries(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	change := domain.ChangeEvent{
		ID: "chg-timeline", ResourceID: "res-order-prod", Kind: domain.ChangeKindRelease,
		StartedAt: start.Add(-10 * time.Minute), CompletedAt: start.Add(-5 * time.Minute),
		FromVersion: "v1", ToVersion: "v2", Owner: "order-team", Summary: "release v2",
		AffectedInstances: []string{"order-pod-a"}, AffectedInstancesComplete: true,
	}
	changeSource := &fakeChangeSource{set: domain.ChangeSet{
		SourceVersion: "change-catalog-v1", Complete: true, Events: []domain.ChangeEvent{change},
	}}
	signalSource := &fakeOperationalSignalSource{set: domain.OperationalSignalSet{
		SourceVersion: "signals-v1", Complete: true,
		Observations: []domain.OperationalSignalObservation{
			{
				ID: "metric-errors", ResourceID: "res-order-prod", Kind: domain.OperationalSignalMetric,
				Code: domain.OperationalSignalErrorRate, StartedAt: start, CompletedAt: end,
				BaselineValue: 0.02, CurrentValue: 0.12, Unit: domain.OperationalSignalRatio,
			},
			{
				ID: "trace-latency", ResourceID: "res-order-prod", Kind: domain.OperationalSignalTrace,
				Code: domain.OperationalSignalLatencyP95, StartedAt: start, CompletedAt: end,
				BaselineValue: 120, CurrentValue: 420, Unit: domain.OperationalSignalMillisecond,
			},
		},
	}}
	executor := &fakeExecutor{results: causeAnalysisResults()}
	engine, err := New(
		context.Background(), executor, time.Now,
		WithChangeSource(changeSource),
		WithOperationalSignalSource(signalSource),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_timeline", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.got) != 2 {
		t.Fatalf("cross-signal enrichment changed SLS observations: %d", len(executor.got))
	}
	if len(signalSource.got) != 1 {
		t.Fatalf("operational source calls=%d, want 1", len(signalSource.got))
	}
	query := signalSource.got[0]
	if query.ResourceID != "res-order-prod" || !query.StartTime.Equal(start.Add(-30*time.Minute)) || !query.EndTime.Equal(end) || query.Limit != domain.MaxOperationalSignals {
		t.Fatalf("operational query was not derived from evidence: %#v", query)
	}
	timeline := report.IncidentTimeline
	if timeline == nil || timeline.Status != domain.TimelineComplete || len(timeline.Signals) != 2 || len(timeline.Items) != 3 {
		t.Fatalf("unexpected incident timeline: %#v", timeline)
	}
	if !timeline.Signals[0].Anomalous || !timeline.Signals[1].Anomalous {
		t.Fatalf("local anomaly rules were not applied: %#v", timeline.Signals)
	}
	for _, item := range timeline.Items {
		if !sameStringSet(item.EvidenceIDs, []string{report.Evidence[0].ID, report.Evidence[1].ID}) {
			t.Fatalf("timeline item is not grounded in both observations: %#v", item)
		}
	}
}

func TestEngineSkipsOperationalSignalsWithoutConclusiveSpike(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := analysisResult("current", 30,
		[]domain.CountBucket{{Label: "timeout", Count: 30}},
		[]domain.CountBucket{{Label: "pod-a", Count: 30}},
	)
	baseline := analysisResult("baseline", 20,
		[]domain.CountBucket{{Label: "timeout", Count: 20}},
		[]domain.CountBucket{{Label: "pod-a", Count: 20}},
	)
	current.ResourceID, baseline.ResourceID = "res-order-prod", "res-order-prod"
	source := &fakeOperationalSignalSource{err: errors.New("must not be called")}
	engine, err := New(
		context.Background(),
		&fakeExecutor{results: map[string]domain.QueryResult{"current": current, "baseline": baseline}},
		time.Now,
		WithOperationalSignalSource(source),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_no_signal_spike", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.got) != 0 || report.IncidentTimeline == nil || report.IncidentTimeline.Status != domain.TimelineSkippedNoSpike {
		t.Fatalf("operational source was not safely skipped: calls=%d timeline=%#v", len(source.got), report.IncidentTimeline)
	}
}

func TestEngineSkipsOperationalSignalsWhenEvidenceIsInsufficient(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	source := &fakeOperationalSignalSource{err: errors.New("must not be called")}
	engine, err := New(
		context.Background(),
		&fakeExecutor{incomplete: true},
		time.Now,
		WithOperationalSignalSource(source),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_insufficient_signal", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "data_insufficient" || len(source.got) != 0 || report.IncidentTimeline == nil || report.IncidentTimeline.Status != domain.TimelineInconclusive {
		t.Fatalf("operational source was not skipped for insufficient evidence: calls=%d report=%#v", len(source.got), report)
	}
}

func TestEngineDowngradesInvalidOrFailedOperationalSignalSource(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		source *fakeOperationalSignalSource
	}{
		{name: "provider failure", source: &fakeOperationalSignalSource{err: errors.New("provider details must not escape")}},
		{name: "invalid identity", source: &fakeOperationalSignalSource{set: domain.OperationalSignalSet{
			SourceVersion: "signals-v1", Complete: true,
			Observations: []domain.OperationalSignalObservation{{
				ID: "metric-errors", ResourceID: "wrong-resource", Kind: domain.OperationalSignalMetric,
				Code: domain.OperationalSignalErrorRate, StartedAt: start, CompletedAt: start.Add(30 * time.Minute),
				BaselineValue: 0.01, CurrentValue: 0.2, Unit: domain.OperationalSignalRatio,
			}},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, err := New(
				context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now,
				WithOperationalSignalSource(test.source),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, report, err := engine.Run(context.Background(), "inv_signal_downgrade", domain.InvestigationRequest{
				Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
			})
			if err != nil {
				t.Fatalf("optional operational source failed M2/M3 report: %v", err)
			}
			if report.Outcome != "spike_detected" || report.IncidentTimeline == nil || report.IncidentTimeline.Status != domain.TimelineUnavailable {
				t.Fatalf("operational source did not downgrade safely: %#v", report)
			}
		})
	}
}

func TestEngineKeepsIncompleteOperationalCoverageNonCausal(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	source := &fakeOperationalSignalSource{set: domain.OperationalSignalSet{
		SourceVersion: "signals-v1", Complete: false, ReasonCode: domain.OperationalSignalReasonIncomplete,
		Observations: []domain.OperationalSignalObservation{{
			ID: "metric-errors", ResourceID: "res-order-prod", Kind: domain.OperationalSignalMetric,
			Code: domain.OperationalSignalErrorRate, StartedAt: start, CompletedAt: start.Add(30 * time.Minute),
			BaselineValue: .02, CurrentValue: .12, Unit: domain.OperationalSignalRatio,
		}},
	}}
	engine, err := New(
		context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now,
		WithOperationalSignalSource(source),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, report, err := engine.Run(context.Background(), "inv_signal_incomplete", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "spike_detected" || report.IncidentTimeline == nil || report.IncidentTimeline.Status != domain.TimelineInconclusive {
		t.Fatalf("incomplete optional coverage changed the investigation: %#v", report)
	}
	if !sameStringSet(report.IncidentTimeline.MissingInputs, []string{"complete_operational_signal_set", "trace_signal_coverage"}) {
		t.Fatalf("incomplete coverage was not explicit: %#v", report.IncidentTimeline)
	}
}

func TestEnginePropagatesInvestigationCancellationFromOperationalSignalSource(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	engine, err := New(
		context.Background(), &fakeExecutor{results: causeAnalysisResults()}, time.Now,
		WithOperationalSignalSource(cancellingOperationalSignalSource{cancel: cancel}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.Run(ctx, "inv_signal_cancelled", domain.InvestigationRequest{
		Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v, want context.Canceled", err)
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := set[value]; !exists {
			return false
		}
	}
	return true
}

func fakeAnalysisResult(name string) domain.QueryResult {
	if name == "current" {
		return analysisResult(name, 120,
			[]domain.CountBucket{{Label: "payment_timeout", Count: 90}, {Label: "inventory_lock", Count: 20}, {Label: "signature_invalid", Count: 10}},
			[]domain.CountBucket{{Label: "order-pod-a", Count: 80}, {Label: "order-pod-b", Count: 30}, {Label: "order-pod-c", Count: 10}},
		)
	}
	return analysisResult(name, 20,
		[]domain.CountBucket{{Label: "inventory_lock", Count: 10}, {Label: "database_timeout", Count: 5}, {Label: "payment_timeout", Count: 5}},
		[]domain.CountBucket{{Label: "order-pod-b", Count: 10}, {Label: "order-pod-c", Count: 10}},
	)
}

func causeAnalysisResults() map[string]domain.QueryResult {
	current := fakeAnalysisResult("current")
	baseline := fakeAnalysisResult("baseline")
	current.ResourceID = "res-order-prod"
	baseline.ResourceID = "res-order-prod"
	return map[string]domain.QueryResult{"current": current, "baseline": baseline}
}

func nonExhaustiveCauseResults() map[string]domain.QueryResult {
	results := causeAnalysisResults()
	current := results["current"]
	current.Instances = current.Instances[:1]
	current.InstancesExhaustive = false
	results["current"] = current
	return results
}

func redactedCauseResults() map[string]domain.QueryResult {
	results := causeAnalysisResults()
	current := results["current"]
	current.Redacted = true
	current.Instances[0].Redacted = true
	results["current"] = current
	return results
}

func withChangeID(change domain.ChangeEvent, id string, completedAt time.Time) domain.ChangeEvent {
	change.ID = id
	change.StartedAt = completedAt.Add(-time.Minute)
	change.CompletedAt = completedAt
	change.AffectedInstances = append([]string(nil), change.AffectedInstances...)
	return change
}

func ledgerByCode(t *testing.T, ledger []domain.EvidenceLedgerEntry, code string) domain.EvidenceLedgerEntry {
	t.Helper()
	for _, entry := range ledger {
		if entry.Code == code {
			return entry
		}
	}
	t.Fatalf("ledger entry %q not found in %#v", code, ledger)
	return domain.EvidenceLedgerEntry{}
}

func analysisResult(name string, total int64, patterns, instances []domain.CountBucket) domain.QueryResult {
	result := domain.QueryResult{
		QueryID:                 "query-" + name,
		ResourceID:              "resource-order-prod",
		TemplateID:              domain.ErrorAnalysisTemplateID,
		TemplateVersion:         domain.ErrorAnalysisTemplateVersion,
		SchemaFingerprint:       "schema-v2",
		PolicyVersion:           "query-policy-v2",
		GovernanceFingerprint:   strings.Repeat("b", 64),
		Progress:                "Complete",
		Complete:                true,
		UsageKnown:              true,
		APICalls:                domain.ErrorAnalysisAPICalls,
		ErrorCount:              total,
		ErrorPatterns:           patterns,
		Instances:               instances,
		ErrorPatternsExhaustive: bucketTotal(patterns) == total,
		InstancesExhaustive:     bucketTotal(instances) == total,
		PatternLimit:            domain.ErrorAnalysisPatternLimit,
		InstanceLimit:           domain.ErrorAnalysisInstanceLimit,
		NanosecondOrderedKnown:  true,
		NanosecondOrdered:       true,
	}
	if len(patterns) > 0 {
		result.TopError = patterns[0].Label
		result.TopErrorCount = patterns[0].Count
	}
	return result
}

func countResult(name string, total int64) domain.QueryResult {
	return domain.QueryResult{
		QueryID: "count-query-" + name, ResourceID: "dam-server-test-count",
		TemplateID: domain.ErrorCountTemplateID, TemplateVersion: domain.ErrorCountTemplateVersion,
		SchemaFingerprint: "schema-count-v1", PolicyVersion: "query-policy-v2", GovernanceFingerprint: strings.Repeat("d", 64),
		Progress: "Complete", Complete: true, UsageKnown: true, APICalls: domain.ErrorCountAPICalls, ErrorCount: total,
		NanosecondOrderedKnown: true, NanosecondOrdered: true,
	}
}

func bucketTotal(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

func traceTestContext(t *testing.T, caseID string) (context.Context, *observability.BoundedRecorder) {
	t.Helper()
	run := observability.RunContext{
		EvaluationRunID:    "evaluation-run-1",
		TraceID:            "trace-" + caseID,
		RunID:              "engine-run-" + caseID,
		CaseID:             caseID,
		VersionFingerprint: strings.Repeat("a", 64),
	}
	recorder := observability.NewBoundedRecorder(64, run)
	return observability.WithRunContext(context.Background(), run), recorder
}

func assertSpanTerminal(
	t *testing.T,
	trace domain.AgentTrace,
	name domain.AgentSpanName,
	phase domain.AgentPhase,
	failureClass domain.FailureClass,
) {
	t.Helper()
	starts := 0
	terminals := 0
	for _, event := range trace.Events {
		if event.Name != name {
			continue
		}
		if event.Phase == domain.AgentPhaseStarted {
			starts++
			continue
		}
		terminals++
		if event.Phase != phase || event.FailureClass != failureClass {
			t.Fatalf("span %s terminal=%s/%s, want %s/%s", name, event.Phase, event.FailureClass, phase, failureClass)
		}
		if phase == domain.AgentPhaseFailed && event.FailureCode != agentFailureCode(event.Layer, failureClass) {
			t.Fatalf("span %s failure code=%s, want %s", name, event.FailureCode, agentFailureCode(event.Layer, failureClass))
		}
	}
	if starts != 1 || terminals != 1 {
		t.Fatalf("span %s starts=%d terminals=%d, want 1/1", name, starts, terminals)
	}
}

func findingCodes(findings []domain.Finding) map[string]bool {
	codes := make(map[string]bool, len(findings))
	for _, finding := range findings {
		codes[finding.Code] = true
	}
	return codes
}

func assertReportReferencesExistingEvidence(t *testing.T, report domain.Report) {
	t.Helper()
	existing := make(map[string]bool, len(report.Evidence))
	for _, evidence := range report.Evidence {
		existing[evidence.ID] = true
	}
	check := func(kind string, ids []string) {
		for _, id := range ids {
			if !existing[id] {
				t.Fatalf("%s references unknown evidence %q", kind, id)
			}
		}
	}
	for _, finding := range report.Findings {
		check("finding "+finding.Code, finding.EvidenceIDs)
	}
	for _, recommendation := range report.Recommendations {
		check("recommendation "+recommendation.Code, recommendation.EvidenceIDs)
	}
}
