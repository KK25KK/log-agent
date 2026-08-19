// Package evalmock exposes deterministic, fixture-backed provider boundaries
// for the M5-A offline evaluation gate. It performs no network access and does
// not depend on an orchestration framework.
package evalmock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/fingerprint"
	"logagent/internal/observability"
	"logagent/internal/ports"
)

// Stats is an immutable snapshot of one synthetic scenario. LogicalSLSCalls
// counts Execute invocations, while ProviderAPICalls and ProcessedBytes are the
// metered usage advertised by successfully returned fixture observations.
type Stats struct {
	QueryContractValid bool                 `json:"query_contract_valid"`
	LogicalSLSCalls    int                  `json:"logical_sls_calls"`
	GovernanceCalls    int                  `json:"governance_calls"`
	ProviderAPICalls   int                  `json:"provider_api_calls"`
	ProcessedBytes     int64                `json:"processed_bytes"`
	ChangeSourceCalls  int                  `json:"change_source_calls"`
	QuerySpecs         []domain.QuerySpec   `json:"query_specs"`
	ChangeQueries      []domain.ChangeQuery `json:"change_queries"`
}

// Scenario owns isolated SLS and change-source fixtures for exactly one
// evaluation case. A scenario must not be reused for another case.
type Scenario struct {
	Executor     *Executor
	ChangeSource *ChangeSource
}

type scenarioConfig struct {
	observer ports.AgentObserver
}

// Option configures synthetic-only adapter behavior without changing existing
// M5-A callers.
type Option func(*scenarioConfig)

// WithObserver enables bounded tool events when the execution context also
// carries an observability RunContext. Nil preserves no-op behavior.
func WithObserver(observer ports.AgentObserver) Option {
	return func(config *scenarioConfig) {
		if observer != nil {
			config.observer = observer
		}
	}
}

// New makes an immutable runtime copy of an already strictly decoded
// evaluation case. investigationID is bound into the query contract so the
// fixture cannot be consumed by another investigation accidentally.
func New(evaluationCase evaluation.EvaluationCase, investigationID string, options ...Option) (*Scenario, error) {
	if strings.TrimSpace(investigationID) == "" || strings.TrimSpace(investigationID) != investigationID {
		return nil, errors.New("evaluation investigation ID is required and cannot have surrounding whitespace")
	}
	if evaluationCase.ID == "" {
		return nil, errors.New("evaluation case ID is required")
	}
	if err := validateRequest(evaluationCase.Request); err != nil {
		return nil, err
	}
	if err := validateFixturePair(evaluationCase.Current, evaluationCase.Baseline); err != nil {
		return nil, err
	}
	if err := validateChangeSet(evaluationCase.ChangeSet, evaluationCase.Current.ResourceID); err != nil {
		return nil, err
	}
	var config scenarioConfig
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}

	request := evaluationCase.Request
	return &Scenario{
		Executor: &Executor{
			investigationID: investigationID,
			request:         request,
			current:         cloneQueryResult(evaluationCase.Current),
			baseline:        cloneQueryResult(evaluationCase.Baseline),
			seen:            make(map[string]struct{}, evaluation.ExpectedLogicalSLSCalls),
			contractValid:   true,
			observer:        config.observer,
		},
		ChangeSource: &ChangeSource{
			request:       request,
			resourceID:    evaluationCase.Current.ResourceID,
			changeSet:     cloneChangeSet(evaluationCase.ChangeSet),
			contractValid: true,
			observer:      config.observer,
		},
	}, nil
}

// Stats returns a defensive copy of all observed calls and usage.
func (scenario *Scenario) Stats() Stats {
	if scenario == nil {
		return Stats{}
	}
	var result Stats
	if scenario.Executor != nil {
		result = scenario.Executor.stats()
	}
	if scenario.ChangeSource != nil {
		valid, calls, queries := scenario.ChangeSource.stats()
		result.QueryContractValid = result.QueryContractValid && valid
		result.ChangeSourceCalls = calls
		result.ChangeQueries = queries
	}
	return result
}

// ExecutionStats projects adapter activity into the evaluator's public
// callback contract. Adapter-only governance and change-query diagnostics stay
// available through Stats without weakening the gate input.
func (scenario *Scenario) ExecutionStats() evaluation.ExecutionStats {
	stats := scenario.Stats()
	return evaluation.ExecutionStats{
		QuerySpecs:         stats.QuerySpecs,
		QueryContractValid: stats.QueryContractValid,
		LogicalSLSCalls:    stats.LogicalSLSCalls,
		ProviderAPICalls:   stats.ProviderAPICalls,
		ChangeSourceCalls:  stats.ChangeSourceCalls,
		ProcessedBytes:     stats.ProcessedBytes,
	}
}

// Executor returns only the two observations declared by one evaluation case.
// It rejects any drift in identity, scope, template, or time window.
type Executor struct {
	mu sync.Mutex

	investigationID string
	request         domain.InvestigationRequest
	current         domain.QueryResult
	baseline        domain.QueryResult
	seen            map[string]struct{}

	governanceCalls  int
	providerAPICalls int
	processedBytes   int64
	querySpecs       []domain.QuerySpec
	contractValid    bool
	observer         ports.AgentObserver
}

// ResolveQueryGovernance exposes the same immutable identity carried by both
// fixture results. It validates the complete logical QuerySpec without
// consuming the corresponding observation.
func (executor *Executor) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if executor == nil {
		return "", errors.New("evaluation executor is nil")
	}
	result, err := executor.fixtureFor(spec)
	executor.mu.Lock()
	executor.governanceCalls++
	if err != nil {
		executor.contractValid = false
	}
	executor.mu.Unlock()
	if err != nil {
		return "", err
	}
	return result.GovernanceFingerprint, nil
}

// Execute consumes one unique current or baseline fixture. Duplicate logical
// observations fail closed instead of silently understating evaluation cost.
func (executor *Executor) Execute(ctx context.Context, spec domain.QuerySpec) (result domain.QueryResult, err error) {
	var observer ports.AgentObserver
	if executor != nil {
		observer = executor.observer
	}
	observation := beginToolObservation(observer, ctx, queryToolName(spec.Name), domain.AgentSpanExecuteQueries)
	usage := domain.ToolUsage{}
	failureClass := domain.FailureClassContractViolation
	defer func() {
		observation.finish(ctx, err, failureClass, usage)
	}()

	if err = ctx.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	if executor == nil {
		failureClass = domain.FailureClassInternal
		return domain.QueryResult{}, errors.New("evaluation executor is nil")
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.querySpecs = append(executor.querySpecs, spec)
	usage.LogicalCalls = 1
	result, err = executor.fixtureFor(spec)
	if err != nil {
		executor.contractValid = false
		return domain.QueryResult{}, err
	}
	if _, duplicate := executor.seen[spec.Name]; duplicate {
		executor.contractValid = false
		return domain.QueryResult{}, fmt.Errorf("evaluation query %q was executed more than once", spec.Name)
	}
	executor.seen[spec.Name] = struct{}{}

	result = cloneQueryResult(result)
	result.QuerySpecHash, err = fingerprint.JSON(spec)
	if err != nil {
		failureClass = domain.FailureClassInternal
		return domain.QueryResult{}, fmt.Errorf("fingerprint evaluation query %q: %w", spec.Name, err)
	}
	executor.providerAPICalls += result.APICalls
	executor.processedBytes += result.ProcessedBytes
	usage.ProviderCalls = int64(result.APICalls)
	usage.ProcessedBytes = result.ProcessedBytes
	return result, nil
}

func (executor *Executor) fixtureFor(spec domain.QuerySpec) (domain.QueryResult, error) {
	if spec.InvestigationID != executor.investigationID {
		return domain.QueryResult{}, errors.New("evaluation query investigation identity does not match the scenario")
	}
	if spec.Service != executor.request.Service || spec.Environment != executor.request.Environment || spec.Requester != executor.request.Requester {
		return domain.QueryResult{}, errors.New("evaluation query scope or requester does not match the trusted request")
	}
	if spec.TemplateID != domain.ErrorAnalysisTemplateID {
		return domain.QueryResult{}, fmt.Errorf("evaluation query template %q is not allowed", spec.TemplateID)
	}

	duration := executor.request.EndTime.Sub(executor.request.StartTime)
	var expectedStart, expectedEnd time.Time
	var result domain.QueryResult
	switch spec.Name {
	case "current":
		expectedStart, expectedEnd = executor.request.StartTime, executor.request.EndTime
		result = executor.current
	case "baseline":
		expectedStart, expectedEnd = executor.request.StartTime.Add(-duration), executor.request.StartTime
		result = executor.baseline
	default:
		return domain.QueryResult{}, fmt.Errorf("unsupported evaluation query %q", spec.Name)
	}
	if !spec.StartTime.Equal(expectedStart) || !spec.EndTime.Equal(expectedEnd) {
		return domain.QueryResult{}, fmt.Errorf(
			"evaluation %s window [%s,%s) does not match [%s,%s)",
			spec.Name,
			spec.StartTime.UTC().Format(time.RFC3339Nano),
			spec.EndTime.UTC().Format(time.RFC3339Nano),
			expectedStart.UTC().Format(time.RFC3339Nano),
			expectedEnd.UTC().Format(time.RFC3339Nano),
		)
	}
	return result, nil
}

func (executor *Executor) stats() Stats {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return Stats{
		QueryContractValid: executor.contractValid,
		LogicalSLSCalls:    len(executor.querySpecs),
		GovernanceCalls:    executor.governanceCalls,
		ProviderAPICalls:   executor.providerAPICalls,
		ProcessedBytes:     executor.processedBytes,
		QuerySpecs:         append([]domain.QuerySpec(nil), executor.querySpecs...),
	}
}

// ChangeSource exposes only the case-owned, bounded change set. Its expected
// query is derived from the same governed resource and current/baseline range
// as the SLS fixtures.
type ChangeSource struct {
	mu sync.Mutex

	request       domain.InvestigationRequest
	resourceID    string
	changeSet     domain.ChangeSet
	called        bool
	queries       []domain.ChangeQuery
	contractValid bool
	observer      ports.AgentObserver
}

// List accepts exactly one full baseline-plus-current lookup with the domain
// hard limit. A duplicate call fails so an orchestration regression cannot
// hide behind a deterministic fixture.
func (source *ChangeSource) List(ctx context.Context, query domain.ChangeQuery) (result domain.ChangeSet, err error) {
	var observer ports.AgentObserver
	if source != nil {
		observer = source.observer
	}
	observation := beginToolObservation(observer, ctx, domain.AgentSpanChangeSourceList, domain.AgentSpanCorrelateChanges)
	usage := domain.ToolUsage{}
	failureClass := domain.FailureClassContractViolation
	defer func() {
		observation.finish(ctx, err, failureClass, usage)
	}()

	if err = ctx.Err(); err != nil {
		return domain.ChangeSet{}, err
	}
	if source == nil {
		failureClass = domain.FailureClassInternal
		return domain.ChangeSet{}, errors.New("evaluation change source is nil")
	}

	source.mu.Lock()
	defer source.mu.Unlock()
	source.queries = append(source.queries, query)
	usage.LogicalCalls = 1
	if source.called {
		source.contractValid = false
		return domain.ChangeSet{}, errors.New("evaluation change source was queried more than once")
	}
	source.called = true

	duration := source.request.EndTime.Sub(source.request.StartTime)
	expectedStart := source.request.StartTime.Add(-duration)
	expectedEnd := source.request.EndTime
	if query.ResourceID != source.resourceID {
		source.contractValid = false
		return domain.ChangeSet{}, errors.New("evaluation change query resource does not match governed evidence")
	}
	if !query.StartTime.Equal(expectedStart) || !query.EndTime.Equal(expectedEnd) {
		source.contractValid = false
		return domain.ChangeSet{}, errors.New("evaluation change query window does not cover the fixed baseline and current windows")
	}
	if query.Limit != domain.MaxChangeEvents {
		source.contractValid = false
		return domain.ChangeSet{}, fmt.Errorf("evaluation change query limit=%d, want %d", query.Limit, domain.MaxChangeEvents)
	}
	return cloneChangeSet(source.changeSet), nil
}

func (source *ChangeSource) stats() (bool, int, []domain.ChangeQuery) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.contractValid, len(source.queries), append([]domain.ChangeQuery(nil), source.queries...)
}

type toolObservation struct {
	observer     ports.AgentObserver
	spanID       string
	parentSpanID string
	name         domain.AgentSpanName
	startedAt    time.Time
}

func beginToolObservation(
	observer ports.AgentObserver,
	ctx context.Context,
	name domain.AgentSpanName,
	parentName domain.AgentSpanName,
) toolObservation {
	if observer == nil || name == "" {
		return toolObservation{}
	}
	run, ok := observability.RunContextFrom(ctx)
	if !ok {
		return toolObservation{}
	}
	startedAt := time.Now()
	observation := toolObservation{
		observer:     observer,
		spanID:       observability.StableSpanID(run.TraceID, domain.AgentLayerTool, name),
		parentSpanID: observability.StableSpanID(run.TraceID, domain.AgentLayerGraphNode, parentName),
		name:         name,
		startedAt:    startedAt,
	}
	observer.Record(domain.AgentEvent{
		SpanID:       observation.spanID,
		ParentSpanID: observation.parentSpanID,
		Layer:        domain.AgentLayerTool,
		Name:         name,
		Phase:        domain.AgentPhaseStarted,
		OccurredAt:   startedAt.UTC(),
	})
	return observation
}

func (observation toolObservation) finish(
	ctx context.Context,
	err error,
	fallback domain.FailureClass,
	usage domain.ToolUsage,
) {
	if observation.observer == nil {
		return
	}
	phase := domain.AgentPhaseSucceeded
	failureClass := domain.FailureClass("")
	failureCode := domain.AgentFailureCode("")
	usage.Complete = err == nil
	if err != nil {
		phase = domain.AgentPhaseFailed
		failureClass = toolFailureClass(ctx, err, fallback)
		failureCode = toolFailureCode(failureClass)
	}
	duration := time.Since(observation.startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	observation.observer.Record(domain.AgentEvent{
		SpanID:               observation.spanID,
		ParentSpanID:         observation.parentSpanID,
		Layer:                domain.AgentLayerTool,
		Name:                 observation.name,
		Phase:                phase,
		OccurredAt:           time.Now().UTC(),
		DurationMilliseconds: duration,
		FailureClass:         failureClass,
		FailureCode:          failureCode,
		ToolUsage:            &usage,
	})
}

func toolFailureCode(class domain.FailureClass) domain.AgentFailureCode {
	switch class {
	case domain.FailureClassCancelled:
		return domain.AgentFailureCodeContextCancelled
	case domain.FailureClassTimeout:
		return domain.AgentFailureCodeDeadlineExceeded
	case domain.FailureClassContractViolation:
		return domain.AgentFailureCodeContractViolation
	default:
		return domain.AgentFailureCodeToolFailed
	}
}

func toolFailureClass(ctx context.Context, err error, fallback domain.FailureClass) domain.FailureClass {
	switch {
	case errors.Is(err, context.Canceled), ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		return domain.FailureClassCancelled
	case errors.Is(err, context.DeadlineExceeded), ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return domain.FailureClassTimeout
	default:
		return fallback
	}
}

func queryToolName(name string) domain.AgentSpanName {
	switch name {
	case "current":
		return domain.AgentSpanSLSCurrent
	case "baseline":
		return domain.AgentSpanSLSBaseline
	default:
		return ""
	}
}

func validateRequest(request domain.InvestigationRequest) error {
	if request.Service == "" || request.Environment == "" {
		return errors.New("evaluation request service and environment are required")
	}
	if !request.EndTime.After(request.StartTime) {
		return errors.New("evaluation request end time must be after start time")
	}
	if !request.Requester.Complete() {
		return errors.New("evaluation request must carry a complete trusted requester")
	}
	return nil
}

func validateFixturePair(current, baseline domain.QueryResult) error {
	if err := validateFixture("current", current); err != nil {
		return err
	}
	if err := validateFixture("baseline", baseline); err != nil {
		return err
	}
	if current.ResourceID != baseline.ResourceID ||
		current.TemplateID != baseline.TemplateID ||
		current.TemplateVersion != baseline.TemplateVersion ||
		current.SchemaFingerprint != baseline.SchemaFingerprint ||
		current.PolicyVersion != baseline.PolicyVersion ||
		current.GovernanceFingerprint != baseline.GovernanceFingerprint {
		return errors.New("evaluation current and baseline fixtures do not share one governance identity")
	}
	return nil
}

func validateFixture(name string, result domain.QueryResult) error {
	if result.QueryID == "" {
		return fmt.Errorf("evaluation %s query ID is required", name)
	}
	if result.QuerySpecHash != "" {
		return fmt.Errorf("evaluation %s query spec hash must be derived at execution", name)
	}
	if err := domain.ValidateResourceID(result.ResourceID); err != nil {
		return fmt.Errorf("evaluation %s resource: %w", name, err)
	}
	if result.TemplateID != domain.ErrorAnalysisTemplateID || result.TemplateVersion != domain.ErrorAnalysisTemplateVersion {
		return fmt.Errorf("evaluation %s fixture does not use the fixed template", name)
	}
	if result.SchemaFingerprint == "" || result.PolicyVersion == "" || result.GovernanceFingerprint == "" {
		return fmt.Errorf("evaluation %s fixture governance identity is incomplete", name)
	}
	if result.APICalls != domain.ErrorAnalysisAPICalls || result.PatternLimit != domain.ErrorAnalysisPatternLimit || result.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
		return fmt.Errorf("evaluation %s fixture does not use the fixed query budget", name)
	}
	if result.ProcessedBytes < 0 {
		return fmt.Errorf("evaluation %s processed bytes cannot be negative", name)
	}
	return nil
}

func validateChangeSet(changeSet domain.ChangeSet, resourceID string) error {
	if changeSet.ReasonCode == "change_source_disabled" {
		if changeSet.Complete || changeSet.Truncated || changeSet.SourceVersion != "" || len(changeSet.Events) != 0 {
			return errors.New("evaluation disabled change source fixture contains source data")
		}
		return nil
	}
	if err := domain.ValidateChangeSourceVersion(changeSet.SourceVersion); err != nil {
		return fmt.Errorf("evaluation change source version: %w", err)
	}
	if changeSet.Complete && changeSet.Truncated {
		return errors.New("evaluation change set cannot be complete and truncated")
	}
	if len(changeSet.Events) > domain.MaxChangeEvents {
		return fmt.Errorf("evaluation change set exceeds %d events", domain.MaxChangeEvents)
	}
	seen := make(map[string]struct{}, len(changeSet.Events))
	for _, event := range changeSet.Events {
		if err := domain.ValidateChangeEvent(event); err != nil {
			return fmt.Errorf("evaluation change %q: %w", event.ID, err)
		}
		if event.ResourceID != resourceID {
			return fmt.Errorf("evaluation change %q uses a different resource", event.ID)
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return fmt.Errorf("evaluation change %q is duplicated", event.ID)
		}
		seen[event.ID] = struct{}{}
	}
	return nil
}

func cloneQueryResult(result domain.QueryResult) domain.QueryResult {
	result.ErrorPatterns = append([]domain.CountBucket(nil), result.ErrorPatterns...)
	result.Instances = append([]domain.CountBucket(nil), result.Instances...)
	return result
}

func cloneChangeSet(changeSet domain.ChangeSet) domain.ChangeSet {
	changeSet.Events = append([]domain.ChangeEvent(nil), changeSet.Events...)
	for index := range changeSet.Events {
		changeSet.Events[index].AffectedInstances = append([]string(nil), changeSet.Events[index].AffectedInstances...)
	}
	return changeSet
}

var (
	_ ports.SLSExecutor             = (*Executor)(nil)
	_ ports.QueryGovernanceResolver = (*Executor)(nil)
	_ ports.ChangeSource            = (*ChangeSource)(nil)
)
