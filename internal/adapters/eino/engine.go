// Package eino contains the only orchestration-framework dependency in the service.
package eino

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"

	"logagent/internal/domain"
	"logagent/internal/observability"
	"logagent/internal/ports"
)

// GraphVersion is emitted by the offline evaluation report so changes to the
// deterministic orchestration contract can be compared with the dataset and
// gate-policy versions that produced a result.
const GraphVersion = "error-spike-investigation-v1"

type graphInput struct {
	InvestigationID string
	Request         domain.InvestigationRequest
}

type queryPlan struct {
	InvestigationID string
	Current         domain.QuerySpec
	Baseline        domain.QuerySpec
}

type queryObservations struct {
	InvestigationID string
	Evidence        []domain.Evidence
}

type graphOutput struct {
	Evidence []domain.Evidence
	Report   domain.Report
}

type engineConfig struct {
	changeSource            ports.ChangeSource
	operationalSignalSource ports.OperationalSignalSource
	observer                ports.AgentObserver
}

// Option configures optional enrichment without changing the M2 constructor
// contract used by existing callers.
type Option func(*engineConfig)

// WithChangeSource enables governed release/configuration correlation. A nil
// source is treated as disabled so callers cannot accidentally create a graph
// that panics during optional enrichment.
func WithChangeSource(source ports.ChangeSource) Option {
	return func(config *engineConfig) {
		if source != nil {
			config.changeSource = source
		}
	}
}

// WithOperationalSignalSource enables an optional, bounded metric/Trace
// timeline. A nil source leaves existing reports unchanged.
func WithOperationalSignalSource(source ports.OperationalSignalSource) Option {
	return func(config *engineConfig) {
		if source != nil {
			config.operationalSignalSource = source
		}
	}
}

// WithObserver enables privacy-bounded Agent events when RunContext is also
// attached to Engine.Run's context. A nil observer preserves no-op behavior.
func WithObserver(observer ports.AgentObserver) Option {
	return func(config *engineConfig) {
		if observer != nil {
			config.observer = observer
		}
	}
}

// Engine runs a compiled, deterministic Eino graph.
type Engine struct {
	runner   compose.Runnable[graphInput, graphOutput]
	observer ports.AgentObserver
}

var graphNodeOrder = [...]domain.AgentSpanName{
	domain.AgentSpanPlanQueries,
	domain.AgentSpanExecuteQueries,
	domain.AgentSpanBuildReport,
	domain.AgentSpanCorrelateChanges,
}

// New compiles the graph once. The returned runner is safe to reuse concurrently.
func New(ctx context.Context, executor ports.SLSExecutor, now func() time.Time, options ...Option) (*Engine, error) {
	if executor == nil {
		return nil, errors.New("SLS executor is required")
	}
	if now == nil {
		now = time.Now
	}
	config := engineConfig{changeSource: domain.DisabledChangeSource{}}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}

	graph := compose.NewGraph[graphInput, graphOutput]()
	if err := graph.AddLambdaNode("plan_queries", compose.InvokableLambda(observeGraphNode(config.observer, domain.AgentSpanPlanQueries, planQueries))); err != nil {
		return nil, fmt.Errorf("add plan node: %w", err)
	}
	if err := graph.AddLambdaNode("execute_queries", compose.InvokableLambda(observeGraphNode(config.observer, domain.AgentSpanExecuteQueries, func(ctx context.Context, plan queryPlan) (queryObservations, error) {
		return executeQueries(ctx, executor, plan)
	}))); err != nil {
		return nil, fmt.Errorf("add execute node: %w", err)
	}
	if err := graph.AddLambdaNode("build_report", compose.InvokableLambda(observeGraphNode(config.observer, domain.AgentSpanBuildReport, func(_ context.Context, observations queryObservations) (graphOutput, error) {
		return buildReport(observations, now().UTC()), nil
	}))); err != nil {
		return nil, fmt.Errorf("add report node: %w", err)
	}
	if err := graph.AddLambdaNode("correlate_changes", compose.InvokableLambda(observeGraphNode(config.observer, domain.AgentSpanCorrelateChanges, func(ctx context.Context, output graphOutput) (graphOutput, error) {
		correlated, err := correlateChanges(ctx, output, config.changeSource)
		if err != nil {
			return graphOutput{}, err
		}
		return enrichIncidentTimeline(ctx, correlated, config.operationalSignalSource)
	}))); err != nil {
		return nil, fmt.Errorf("add change-correlation node: %w", err)
	}

	edges := [][2]string{
		{compose.START, "plan_queries"},
		{"plan_queries", "execute_queries"},
		{"execute_queries", "build_report"},
		{"build_report", "correlate_changes"},
		{"correlate_changes", compose.END},
	}
	for _, edge := range edges {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, fmt.Errorf("add graph edge %s -> %s: %w", edge[0], edge[1], err)
		}
	}

	runner, err := graph.Compile(ctx, compose.WithGraphName("error_spike_investigation"))
	if err != nil {
		return nil, fmt.Errorf("compile investigation graph: %w", err)
	}
	return &Engine{runner: runner, observer: config.observer}, nil
}

func (e *Engine) Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	state, observed := newTraceCallState(ctx)
	if observed {
		recordSpanStart(e.observer, ctx, domain.AgentLayerRun, domain.AgentSpanEngineRun, "")
		ctx = context.WithValue(ctx, traceCallStateKey{}, state)
	}

	startedAt := time.Now()
	output, err := e.runner.Invoke(ctx, graphInput{InvestigationID: investigationID, Request: request})
	if err != nil {
		if observed {
			failureClass := state.failureOr(classifyFailure(ctx, err, domain.FailureClassInternal))
			recordSkippedGraphNodes(e.observer, ctx, state)
			recordSpanTerminal(e.observer, ctx, domain.AgentLayerRun, domain.AgentSpanEngineRun, "", domain.AgentPhaseFailed, failureClass, startedAt, nil)
		}
		return nil, domain.Report{}, fmt.Errorf("invoke investigation graph: %w", err)
	}
	if observed {
		recordSpanTerminal(e.observer, ctx, domain.AgentLayerRun, domain.AgentSpanEngineRun, "", domain.AgentPhaseSucceeded, "", startedAt, nil)
	}
	return output.Evidence, output.Report, nil
}

type traceCallStateKey struct{}

type traceCallState struct {
	mu           sync.Mutex
	startedNodes map[domain.AgentSpanName]struct{}
	failureClass domain.FailureClass
}

func newTraceCallState(ctx context.Context) (*traceCallState, bool) {
	if _, ok := observability.RunContextFrom(ctx); !ok {
		return nil, false
	}
	return &traceCallState{startedNodes: make(map[domain.AgentSpanName]struct{}, len(graphNodeOrder))}, true
}

func traceCallStateFrom(ctx context.Context) *traceCallState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(traceCallStateKey{}).(*traceCallState)
	return state
}

func (state *traceCallState) start(name domain.AgentSpanName) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.startedNodes[name] = struct{}{}
	state.mu.Unlock()
}

func (state *traceCallState) fail(class domain.FailureClass) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.failureClass == "" {
		state.failureClass = class
	}
	state.mu.Unlock()
}

func (state *traceCallState) snapshot() map[domain.AgentSpanName]struct{} {
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make(map[domain.AgentSpanName]struct{}, len(state.startedNodes))
	for name := range state.startedNodes {
		result[name] = struct{}{}
	}
	return result
}

func (state *traceCallState) failureOr(fallback domain.FailureClass) domain.FailureClass {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failureClass != "" {
		return state.failureClass
	}
	return fallback
}

func observeGraphNode[Input, Output any](
	observer ports.AgentObserver,
	name domain.AgentSpanName,
	invoke func(context.Context, Input) (Output, error),
) func(context.Context, Input) (Output, error) {
	return func(ctx context.Context, input Input) (Output, error) {
		state := traceCallStateFrom(ctx)
		if state == nil {
			return invoke(ctx, input)
		}

		state.start(name)
		parentSpanID := engineRunSpanID(ctx)
		recordSpanStart(observer, ctx, domain.AgentLayerGraphNode, name, parentSpanID)
		startedAt := time.Now()
		output, err := invoke(ctx, input)
		if err != nil {
			failureClass := graphNodeFailureClass(ctx, name, err)
			state.fail(failureClass)
			recordSpanTerminal(observer, ctx, domain.AgentLayerGraphNode, name, parentSpanID, domain.AgentPhaseFailed, failureClass, startedAt, nil)
			return output, err
		}
		recordSpanTerminal(observer, ctx, domain.AgentLayerGraphNode, name, parentSpanID, domain.AgentPhaseSucceeded, "", startedAt, nil)
		return output, nil
	}
}

func recordSkippedGraphNodes(observer ports.AgentObserver, ctx context.Context, state *traceCallState) {
	started := state.snapshot()
	parentSpanID := engineRunSpanID(ctx)
	for _, name := range graphNodeOrder {
		if _, exists := started[name]; exists {
			continue
		}
		recordSpanStart(observer, ctx, domain.AgentLayerGraphNode, name, parentSpanID)
		recordSpanTerminal(observer, ctx, domain.AgentLayerGraphNode, name, parentSpanID, domain.AgentPhaseSkipped, "", time.Now(), nil)
	}
}

func graphNodeFailureClass(ctx context.Context, name domain.AgentSpanName, err error) domain.FailureClass {
	if class := classifyFailure(ctx, err, ""); class != "" {
		return class
	}
	var classified interface{ AgentFailureClass() domain.FailureClass }
	if errors.As(err, &classified) {
		return classified.AgentFailureClass()
	}
	switch name {
	case domain.AgentSpanPlanQueries:
		return domain.FailureClassValidation
	case domain.AgentSpanExecuteQueries, domain.AgentSpanCorrelateChanges:
		return domain.FailureClassDependency
	default:
		return domain.FailureClassInternal
	}
}

func classifyFailure(ctx context.Context, err error, fallback domain.FailureClass) domain.FailureClass {
	switch {
	case errors.Is(err, context.Canceled):
		return domain.FailureClassCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return domain.FailureClassTimeout
	}
	if ctx != nil {
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return domain.FailureClassCancelled
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return domain.FailureClassTimeout
		}
	}
	return fallback
}

func engineRunSpanID(ctx context.Context) string {
	run, ok := observability.RunContextFrom(ctx)
	if !ok {
		return ""
	}
	return observability.StableSpanID(run.TraceID, domain.AgentLayerRun, domain.AgentSpanEngineRun)
}

func recordSpanStart(observer ports.AgentObserver, ctx context.Context, layer domain.AgentLayer, name domain.AgentSpanName, parentSpanID string) {
	if observer == nil {
		return
	}
	run, ok := observability.RunContextFrom(ctx)
	if !ok {
		return
	}
	observer.Record(domain.AgentEvent{
		SpanID:       observability.StableSpanID(run.TraceID, layer, name),
		ParentSpanID: parentSpanID,
		Layer:        layer,
		Name:         name,
		Phase:        domain.AgentPhaseStarted,
		OccurredAt:   time.Now().UTC(),
	})
}

func recordSpanTerminal(
	observer ports.AgentObserver,
	ctx context.Context,
	layer domain.AgentLayer,
	name domain.AgentSpanName,
	parentSpanID string,
	phase domain.AgentPhase,
	failureClass domain.FailureClass,
	startedAt time.Time,
	usage *domain.ToolUsage,
) {
	if observer == nil {
		return
	}
	run, ok := observability.RunContextFrom(ctx)
	if !ok {
		return
	}
	duration := time.Since(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	failureCode := domain.AgentFailureCode("")
	if phase == domain.AgentPhaseFailed {
		failureCode = agentFailureCode(layer, failureClass)
	}
	observer.Record(domain.AgentEvent{
		SpanID:               observability.StableSpanID(run.TraceID, layer, name),
		ParentSpanID:         parentSpanID,
		Layer:                layer,
		Name:                 name,
		Phase:                phase,
		OccurredAt:           time.Now().UTC(),
		DurationMilliseconds: duration,
		FailureClass:         failureClass,
		FailureCode:          failureCode,
		ToolUsage:            usage,
	})
}

func agentFailureCode(layer domain.AgentLayer, class domain.FailureClass) domain.AgentFailureCode {
	switch class {
	case domain.FailureClassCancelled:
		return domain.AgentFailureCodeContextCancelled
	case domain.FailureClassTimeout:
		return domain.AgentFailureCodeDeadlineExceeded
	case domain.FailureClassContractViolation:
		return domain.AgentFailureCodeContractViolation
	}
	switch layer {
	case domain.AgentLayerRun:
		return domain.AgentFailureCodeEngineRunFailed
	case domain.AgentLayerGraphNode:
		return domain.AgentFailureCodeGraphNodeFailed
	default:
		return domain.AgentFailureCodeToolFailed
	}
}

type agentClassifiedError struct {
	class domain.FailureClass
	err   error
}

func (err agentClassifiedError) Error() string {
	return err.err.Error()
}

func (err agentClassifiedError) Unwrap() error {
	return err.err
}

func (err agentClassifiedError) AgentFailureClass() domain.FailureClass {
	return err.class
}

func classifyAgentError(class domain.FailureClass, err error) error {
	return agentClassifiedError{class: class, err: err}
}

func planQueries(_ context.Context, input graphInput) (queryPlan, error) {
	request := input.Request
	if input.InvestigationID == "" || request.Service == "" || request.Environment == "" {
		return queryPlan{}, errors.New("investigation ID, service, and environment are required")
	}
	if !request.EndTime.After(request.StartTime) {
		return queryPlan{}, errors.New("end time must be after start time")
	}
	duration := request.EndTime.Sub(request.StartTime)
	return queryPlan{
		InvestigationID: input.InvestigationID,
		Current: domain.QuerySpec{
			InvestigationID: input.InvestigationID,
			Name:            "current",
			TemplateID:      domain.ErrorAnalysisTemplateID,
			Service:         request.Service,
			Environment:     request.Environment,
			StartTime:       request.StartTime,
			EndTime:         request.EndTime,
			Requester:       request.Requester,
		},
		Baseline: domain.QuerySpec{
			InvestigationID: input.InvestigationID,
			Name:            "baseline",
			TemplateID:      domain.ErrorAnalysisTemplateID,
			Service:         request.Service,
			Environment:     request.Environment,
			StartTime:       request.StartTime.Add(-duration),
			EndTime:         request.StartTime,
			Requester:       request.Requester,
		},
	}, nil
}

func executeQueries(ctx context.Context, executor ports.SLSExecutor, plan queryPlan) (queryObservations, error) {
	specs := []domain.QuerySpec{plan.Current, plan.Baseline}
	evidence := make([]domain.Evidence, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return queryObservations{}, err
		}
		result, err := executor.Execute(ctx, spec)
		if err != nil {
			return queryObservations{}, classifyAgentError(
				domain.FailureClassDependency,
				fmt.Errorf("execute %s query: %w", spec.Name, err),
			)
		}
		if err := validateQueryResult(result); err != nil {
			return queryObservations{}, classifyAgentError(
				domain.FailureClassContractViolation,
				fmt.Errorf("validate %s query result: %w", spec.Name, err),
			)
		}
		hash := result.QuerySpecHash
		if hash == "" {
			var err error
			hash, err = hashSpec(spec)
			if err != nil {
				return queryObservations{}, classifyAgentError(domain.FailureClassInternal, err)
			}
		}
		evidence = append(evidence, domain.Evidence{
			ID:                      evidenceID(plan.InvestigationID, hash),
			QueryID:                 result.QueryID,
			QuerySpecHash:           hash,
			ResourceID:              result.ResourceID,
			TemplateID:              result.TemplateID,
			TemplateVersion:         result.TemplateVersion,
			SchemaFingerprint:       result.SchemaFingerprint,
			PolicyVersion:           result.PolicyVersion,
			GovernanceFingerprint:   result.GovernanceFingerprint,
			Name:                    spec.Name,
			StartTime:               spec.StartTime,
			EndTime:                 spec.EndTime,
			Progress:                result.Progress,
			Complete:                result.Complete,
			Truncated:               result.Truncated,
			NanosecondOrderedKnown:  result.NanosecondOrderedKnown,
			NanosecondOrdered:       result.NanosecondOrdered,
			UsageKnown:              result.UsageKnown,
			IncompleteReason:        result.IncompleteReason,
			ProcessedRows:           result.ProcessedRows,
			ProcessedBytes:          result.ProcessedBytes,
			ElapsedMillisecond:      result.ElapsedMillisecond,
			APICalls:                result.APICalls,
			Redacted:                result.Redacted,
			ErrorCount:              result.ErrorCount,
			TopError:                result.TopError,
			TopErrorCount:           result.TopErrorCount,
			ErrorPatterns:           append([]domain.CountBucket(nil), result.ErrorPatterns...),
			Instances:               append([]domain.CountBucket(nil), result.Instances...),
			ErrorPatternsExhaustive: result.ErrorPatternsExhaustive,
			InstancesExhaustive:     result.InstancesExhaustive,
			PatternLimit:            result.PatternLimit,
			InstanceLimit:           result.InstanceLimit,
		})
	}
	if err := validateCrossWindowGovernance(evidence); err != nil {
		return queryObservations{}, classifyAgentError(domain.FailureClassContractViolation, err)
	}
	return queryObservations{InvestigationID: plan.InvestigationID, Evidence: evidence}, nil
}

func validateCrossWindowGovernance(evidence []domain.Evidence) error {
	if len(evidence) != 2 {
		return errors.New("current and baseline governance evidence are required")
	}
	current, baseline := evidence[0], evidence[1]
	if !validSHA256Fingerprint(current.GovernanceFingerprint) || current.GovernanceFingerprint != baseline.GovernanceFingerprint {
		return errors.New("current and baseline query governance fingerprints differ")
	}
	if current.ResourceID == "" || current.ResourceID != baseline.ResourceID {
		return errors.New("current and baseline governed resources differ")
	}
	if current.TemplateID == "" || current.TemplateID != baseline.TemplateID ||
		current.TemplateVersion == "" || current.TemplateVersion != baseline.TemplateVersion {
		return errors.New("current and baseline governed templates differ")
	}
	if current.SchemaFingerprint == "" || current.SchemaFingerprint != baseline.SchemaFingerprint {
		return errors.New("current and baseline governed schemas differ")
	}
	if current.PolicyVersion == "" || current.PolicyVersion != baseline.PolicyVersion {
		return errors.New("current and baseline query policies differ")
	}
	return nil
}

func buildReport(observations queryObservations, generatedAt time.Time) graphOutput {
	evidence := observations.Evidence
	evidenceIDs := make([]string, 0, len(evidence))
	complete := len(evidence) == 2
	for _, item := range evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
		complete = complete && item.Complete && !item.Truncated
	}

	report := domain.Report{
		InvestigationID: observations.InvestigationID,
		GeneratedAt:     generatedAt,
		Evidence:        evidence,
	}
	if !complete {
		report.Outcome = "data_insufficient"
		report.Findings = []domain.Finding{{
			Code:        "data_insufficient",
			Statement:   "查询结果不完整或被截断，当前证据不足以形成确定性结论。",
			Confidence:  0,
			Conclusive:  false,
			EvidenceIDs: evidenceIDs,
		}}
		report.Recommendations = []domain.Recommendation{{
			Code:        "retry_with_narrower_window",
			Statement:   "缩小时间窗口后重新运行调查，并检查查询预算与索引状态。",
			EvidenceIDs: evidenceIDs,
		}}
		return graphOutput{Evidence: evidence, Report: report}
	}

	current, baseline := evidence[0], evidence[1]
	if baseline.ErrorCount == 0 && current.ErrorCount > 0 {
		report.Outcome = "data_insufficient"
		report.Findings = []domain.Finding{{
			Code:        "insufficient_baseline",
			Statement:   fmt.Sprintf("基线窗口错误数为 0，无法计算可靠增长倍数；当前窗口发现 %d 条错误日志。", current.ErrorCount),
			Confidence:  0,
			Conclusive:  false,
			EvidenceIDs: evidenceIDs,
		}}
		report.Recommendations = []domain.Recommendation{{
			Code:        "expand_baseline",
			Statement:   "扩大基线覆盖范围或选择有代表性的历史时段后重新比较。",
			EvidenceIDs: evidenceIDs,
		}}
		return graphOutput{Evidence: evidence, Report: report}
	}
	if (current.ErrorCount > 0 && current.ErrorCount < 10) || (baseline.ErrorCount > 0 && baseline.ErrorCount < 5) {
		report.Outcome = "data_insufficient"
		report.Findings = []domain.Finding{{
			Code:        "sample_too_small",
			Statement:   fmt.Sprintf("样本量过小，暂不判断错误突增：当前窗口 %d 条，基线窗口 %d 条。", current.ErrorCount, baseline.ErrorCount),
			Confidence:  0,
			Conclusive:  false,
			EvidenceIDs: evidenceIDs,
		}}
		report.Recommendations = []domain.Recommendation{{
			Code:        "continue_observation",
			Statement:   "继续观察并在样本量达到门槛后重新运行调查。",
			EvidenceIDs: evidenceIDs,
		}}
		return graphOutput{Evidence: evidence, Report: report}
	}
	ratio := spikeRatio(current.ErrorCount, baseline.ErrorCount)
	if ratio >= 2 {
		report.Outcome = "spike_detected"
		statement := fmt.Sprintf("错误日志较基线增长 %.1f 倍，主要错误模式为 %s（%d 条，占当前错误 %.1f%%）。", ratio, current.TopError, current.TopErrorCount, percentage(current.TopErrorCount, current.ErrorCount))
		if current.TopError == "" {
			statement = fmt.Sprintf("错误日志较基线增长 %.1f 倍。", ratio)
		}
		report.Findings = []domain.Finding{{
			Code:        "error_spike",
			Statement:   statement,
			Confidence:  0.95,
			Conclusive:  true,
			EvidenceIDs: evidenceIDs,
		}}
		report.Recommendations = append(report.Recommendations, domain.Recommendation{
			Code:        "inspect_top_error_pattern",
			Statement:   "优先检查主要错误模式对应的近期发布、配置变更和下游依赖。",
			EvidenceIDs: evidenceIDs,
		})
	} else {
		report.Outcome = "no_significant_spike"
		report.Findings = []domain.Finding{{
			Code:        "no_significant_spike",
			Statement:   fmt.Sprintf("当前窗口错误数为 %d，基线为 %d，未发现显著增长。", current.ErrorCount, baseline.ErrorCount),
			Confidence:  0.9,
			Conclusive:  true,
			EvidenceIDs: evidenceIDs,
		}}
	}
	appendPatternFindings(&report, current, baseline)
	appendInstanceFinding(&report, current)
	return graphOutput{Evidence: evidence, Report: report}
}

func correlateChanges(ctx context.Context, output graphOutput, source ports.ChangeSource) (graphOutput, error) {
	if !hasConclusiveSpike(output.Report) {
		status := domain.CauseAnalysisSkippedNoSpike
		missing := []string(nil)
		if output.Report.Outcome == "data_insufficient" {
			status = domain.CauseAnalysisInconclusive
			missing = []string{"conclusive_error_spike"}
		}
		output.Report.CauseAnalysis = &domain.CauseAnalysis{Status: status, MissingInputs: missing}
		return output, nil
	}

	current, baseline, ok := currentAndBaseline(output.Evidence)
	if !ok || !evidenceComplete(current) || !evidenceComplete(baseline) {
		output.Report.CauseAnalysis = &domain.CauseAnalysis{
			Status:        domain.CauseAnalysisInconclusive,
			MissingInputs: []string{"complete_current_baseline_evidence"},
		}
		return output, nil
	}
	if current.ResourceID == "" || current.ResourceID != baseline.ResourceID {
		output.Report.CauseAnalysis = &domain.CauseAnalysis{
			Status:        domain.CauseAnalysisUnavailable,
			MissingInputs: []string{"governed_resource_identity"},
		}
		return output, nil
	}

	changeQuery := domain.ChangeQuery{
		ResourceID: current.ResourceID,
		StartTime:  baseline.StartTime,
		EndTime:    current.EndTime,
		Limit:      domain.MaxChangeEvents,
	}
	changeSet, err := source.List(ctx, changeQuery)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return output, contextErr
		}
		output.Report.CauseAnalysis = &domain.CauseAnalysis{
			Status:        domain.CauseAnalysisUnavailable,
			MissingInputs: []string{"change_source_unavailable"},
		}
		return output, nil
	}
	if changeSet.ReasonCode == "change_source_disabled" {
		output.Report.CauseAnalysis = &domain.CauseAnalysis{
			Status:        domain.CauseAnalysisUnavailable,
			SourceVersion: safeChangeSourceVersion(changeSet.SourceVersion),
			MissingInputs: []string{"change_source_disabled"},
		}
		return output, nil
	}
	if err := validateChangeSet(changeSet, changeQuery); err != nil {
		output.Report.CauseAnalysis = &domain.CauseAnalysis{
			Status:        domain.CauseAnalysisUnavailable,
			SourceVersion: safeChangeSourceVersion(changeSet.SourceVersion),
			MissingInputs: []string{"valid_change_set"},
		}
		return output, nil
	}

	changes := boundedChanges(changeSet.Events)
	if len(changeSet.Events) > domain.MaxChangeEvents {
		changeSet.Complete = false
		changeSet.Truncated = true
	}
	analysis := domain.CauseAnalysis{
		Status:        domain.CauseAnalysisComplete,
		SourceVersion: changeSet.SourceVersion,
		Changes:       changes,
	}
	if !changeSet.Complete {
		analysis.Status = domain.CauseAnalysisInconclusive
		analysis.MissingInputs = append(analysis.MissingInputs, "complete_change_set")
	}
	if changeSet.Truncated {
		analysis.Status = domain.CauseAnalysisInconclusive
		analysis.MissingInputs = appendUnique(analysis.MissingInputs, "untruncated_change_set")
	}
	if len(changes) == 0 {
		analysis.Status = domain.CauseAnalysisInconclusive
		analysis.MissingInputs = appendUnique(analysis.MissingInputs, "change_candidates")
		output.Report.CauseAnalysis = &analysis
		return output, nil
	}

	for _, change := range changes {
		hypothesis, ledger := evaluateChange(
			output.Report.InvestigationID,
			current,
			baseline,
			change,
			changes,
			changeSet.Complete && !changeSet.Truncated,
		)
		analysis.Hypotheses = append(analysis.Hypotheses, hypothesis)
		analysis.Ledger = append(analysis.Ledger, ledger...)
		if hypothesis.Verdict == domain.CauseVerdictInconclusive {
			analysis.Status = domain.CauseAnalysisInconclusive
		}
	}
	output.Report.CauseAnalysis = &analysis
	return output, nil
}

func safeChangeSourceVersion(version string) string {
	if err := domain.ValidateChangeSourceVersion(version); err != nil {
		return ""
	}
	return version
}

func hasConclusiveSpike(report domain.Report) bool {
	if report.Outcome != "spike_detected" {
		return false
	}
	for _, finding := range report.Findings {
		if finding.Code == "error_spike" && finding.Conclusive {
			return true
		}
	}
	return false
}

func currentAndBaseline(evidence []domain.Evidence) (domain.Evidence, domain.Evidence, bool) {
	var current, baseline domain.Evidence
	currentFound, baselineFound := false, false
	for _, item := range evidence {
		switch item.Name {
		case "current":
			current, currentFound = item, true
		case "baseline":
			baseline, baselineFound = item, true
		}
	}
	return current, baseline, currentFound && baselineFound
}

func evidenceComplete(evidence domain.Evidence) bool {
	return evidence.Complete && !evidence.Truncated
}

func validateChangeSet(changeSet domain.ChangeSet, query domain.ChangeQuery) error {
	if changeSet.Complete && changeSet.Truncated {
		return errors.New("complete change set cannot be truncated")
	}
	if len(changeSet.Events) > domain.MaxChangeEvents {
		return fmt.Errorf("change set contains %d events, limit is %d", len(changeSet.Events), domain.MaxChangeEvents)
	}
	if err := domain.ValidateChangeSourceVersion(changeSet.SourceVersion); err != nil {
		return err
	}
	seenEvents := make(map[string]struct{}, len(changeSet.Events))
	for _, change := range changeSet.Events {
		if err := domain.ValidateChangeEvent(change); err != nil {
			return fmt.Errorf("change %q is invalid: %w", change.ID, err)
		}
		if change.ResourceID != query.ResourceID {
			return errors.New("change identity does not match the governed resource")
		}
		if _, duplicate := seenEvents[change.ID]; duplicate {
			return fmt.Errorf("duplicate change event ID %q", change.ID)
		}
		seenEvents[change.ID] = struct{}{}
		if !change.StartedAt.Before(query.EndTime) || !change.CompletedAt.After(query.StartTime) {
			return fmt.Errorf("change %q has an invalid or unrelated time range", change.ID)
		}
	}
	return nil
}

func boundedChanges(events []domain.ChangeEvent) []domain.ChangeEvent {
	changes := append([]domain.ChangeEvent(nil), events...)
	for index := range changes {
		changes[index].AffectedInstances = append([]string(nil), changes[index].AffectedInstances...)
	}
	sort.SliceStable(changes, func(left, right int) bool {
		if changes[left].StartedAt.Equal(changes[right].StartedAt) {
			if changes[left].CompletedAt.Equal(changes[right].CompletedAt) {
				return changes[left].ID < changes[right].ID
			}
			return changes[left].CompletedAt.Before(changes[right].CompletedAt)
		}
		return changes[left].StartedAt.Before(changes[right].StartedAt)
	})
	if len(changes) > domain.MaxChangeEvents {
		changes = changes[:domain.MaxChangeEvents]
	}
	return changes
}

func evaluateChange(
	investigationID string,
	current, baseline domain.Evidence,
	change domain.ChangeEvent,
	allChanges []domain.ChangeEvent,
	completeChangeSet bool,
) (domain.CauseHypothesis, []domain.EvidenceLedgerEntry) {
	hypothesisID := stableID("hyp", investigationID, change.ID)
	evidenceIDs := []string{current.ID, baseline.ID}
	changeIDs := []string{change.ID}
	ledger := make([]domain.EvidenceLedgerEntry, 0, 7)
	appendEntry := func(code string, role domain.EvidenceTestRole, result domain.EvidenceTestResult, weight float64, statement string, refs, changes []string) {
		ledger = append(ledger, domain.EvidenceLedgerEntry{
			ID:             stableID("led", hypothesisID, code),
			HypothesisID:   hypothesisID,
			Code:           code,
			Role:           role,
			Result:         result,
			Weight:         weight,
			Statement:      statement,
			EvidenceIDs:    append([]string(nil), refs...),
			ChangeEventIDs: append([]string(nil), changes...),
		})
	}

	appendEntry(
		"error_spike",
		domain.EvidenceTestSupport,
		domain.EvidenceTestPass,
		0.25,
		"完整的当前/基线证据已确认错误日志显著突增。",
		evidenceIDs,
		nil,
	)

	temporalResult, temporalStatement := temporalPrecedence(change, current, baseline)
	appendEntry("temporal_precedence", domain.EvidenceTestSupport, temporalResult, 0.20, temporalStatement, evidenceIDs, changeIDs)

	currentShare, currentOverlap, currentKnown, currentReason := affectedInstanceShare(current, change)
	concentrationResult := domain.EvidenceTestUnknown
	concentrationStatement := currentReason
	if currentKnown {
		concentrationResult = domain.EvidenceTestFail
		concentrationStatement = fmt.Sprintf("受变更影响实例承载当前错误的 %.1f%%，未达到 50%% 支持阈值。", currentShare)
		if currentShare >= 50 {
			concentrationResult = domain.EvidenceTestPass
			concentrationStatement = fmt.Sprintf("受变更影响实例承载当前错误的 %.1f%%，达到 50%% 支持阈值。", currentShare)
		}
	}
	appendEntry("affected_instance_concentration", domain.EvidenceTestSupport, concentrationResult, 0.30, concentrationStatement, []string{current.ID}, changeIDs)

	baselineShare, _, baselineKnown, baselineReason := affectedInstanceShare(baseline, change)
	shiftResult := domain.EvidenceTestUnknown
	shiftStatement := firstUnknownReason(currentKnown, currentReason, baselineKnown, baselineReason)
	if currentKnown && baselineKnown {
		shift := currentShare - baselineShare
		shiftResult = domain.EvidenceTestFail
		shiftStatement = fmt.Sprintf("受影响实例错误占比较基线变化 %.1f 个百分点，未达到 20 个百分点支持阈值。", shift)
		if shift >= 20 {
			shiftResult = domain.EvidenceTestPass
			shiftStatement = fmt.Sprintf("受影响实例错误占比较基线上升 %.1f 个百分点，达到 20 个百分点支持阈值。", shift)
		}
	}
	appendEntry("baseline_shift", domain.EvidenceTestSupport, shiftResult, 0.10, shiftStatement, evidenceIDs, changeIDs)

	noOverlapResult := domain.EvidenceTestUnknown
	noOverlapStatement := currentReason
	if currentKnown {
		noOverlapResult = domain.EvidenceTestFail
		noOverlapStatement = "完整当前实例分布与变更影响范围存在交集，未发现零交集反证。"
		if currentOverlap == 0 {
			noOverlapResult = domain.EvidenceTestPass
			noOverlapStatement = "完整当前实例分布与完整变更影响范围零交集，形成硬反证。"
		}
	}
	appendEntry("no_instance_overlap", domain.EvidenceTestCounter, noOverlapResult, 0.40, noOverlapStatement, []string{current.ID}, changeIDs)

	preexistingResult := domain.EvidenceTestUnknown
	preexistingStatement := baselineReason
	if baselineKnown {
		preexistingResult = domain.EvidenceTestFail
		preexistingStatement = fmt.Sprintf("基线窗口受影响实例错误占比为 %.1f%%，未达到 50%% 既有集中阈值。", baselineShare)
		if baselineShare >= 50 {
			preexistingResult = domain.EvidenceTestPass
			preexistingStatement = fmt.Sprintf("基线窗口受影响实例已承载 %.1f%% 错误，形成既有集中反证。", baselineShare)
		}
	}
	appendEntry("preexisting_concentration", domain.EvidenceTestCounter, preexistingResult, 0.15, preexistingStatement, []string{baseline.ID}, changeIDs)

	confoundingResult := domain.EvidenceTestUnknown
	confoundingStatement := "变更源未证明候选集合完整，无法排除其他混杂变更。"
	allChangeIDs := make([]string, 0, len(allChanges))
	for _, item := range allChanges {
		allChangeIDs = append(allChangeIDs, item.ID)
	}
	if completeChangeSet {
		confoundingResult = domain.EvidenceTestFail
		confoundingStatement = "完整变更集合中仅有一个候选，未发现并行混杂变更。"
		if len(allChanges) > 1 {
			confoundingResult = domain.EvidenceTestPass
			confoundingStatement = fmt.Sprintf("同一调查窗口内存在 %d 个候选变更，形成混杂反证。", len(allChanges))
		}
	}
	appendEntry("confounding_changes", domain.EvidenceTestCounter, confoundingResult, 0.10, confoundingStatement, evidenceIDs, allChangeIDs)

	hypothesis := domain.CauseHypothesis{
		ID:               hypothesisID,
		Code:             "change_error_spike_correlation",
		Statement:        fmt.Sprintf("%s 变更 %s 是当前错误突增的可反驳关联候选。", change.Kind, change.ID),
		Verdict:          domain.CauseVerdictInconclusive,
		Confidence:       correlationScore(ledger),
		ConfidenceMethod: domain.CauseConfidenceMethod,
		Limitations: []string{
			"变更关联仅表示时间与实例分布相关，不构成因果证明。",
			"置信度是版本化确定性启发式分数，不是概率。",
		},
	}
	for _, entry := range ledger {
		if entry.Role == domain.EvidenceTestSupport {
			hypothesis.SupportEntryIDs = append(hypothesis.SupportEntryIDs, entry.ID)
		} else {
			hypothesis.CounterEntryIDs = append(hypothesis.CounterEntryIDs, entry.ID)
		}
		if entry.Result == domain.EvidenceTestUnknown {
			hypothesis.Limitations = appendUnique(hypothesis.Limitations, "存在未知测试输入，不能把未观察到的数据解释为不存在。")
		}
	}
	hasUnknown := false
	for _, entry := range ledger {
		if entry.Result == domain.EvidenceTestUnknown {
			hasUnknown = true
			break
		}
	}
	if !hasUnknown && confoundingResult != domain.EvidenceTestPass && noOverlapResult == domain.EvidenceTestPass {
		hypothesis.Verdict = domain.CauseVerdictRefuted
	} else if completeChangeSet && allRoleResults(ledger, domain.EvidenceTestSupport, domain.EvidenceTestPass) && allRoleResults(ledger, domain.EvidenceTestCounter, domain.EvidenceTestFail) {
		hypothesis.Verdict = domain.CauseVerdictSupportedCandidate
	}
	if confoundingResult == domain.EvidenceTestPass {
		hypothesis.Limitations = appendUnique(hypothesis.Limitations, "同一窗口存在多个变更，当前证据不能区分各变更的独立贡献。")
	}
	return hypothesis, ledger
}

func temporalPrecedence(change domain.ChangeEvent, current, baseline domain.Evidence) (domain.EvidenceTestResult, string) {
	if change.StartedAt.IsZero() || change.CompletedAt.IsZero() || change.CompletedAt.Before(change.StartedAt) {
		return domain.EvidenceTestUnknown, "变更时间范围无效，无法验证时序先后。"
	}
	if !change.CompletedAt.Before(baseline.StartTime) && change.CompletedAt.Before(current.StartTime) {
		return domain.EvidenceTestPass, "变更在基线开始后、当前窗口开始前完成，满足时序支持条件。"
	}
	return domain.EvidenceTestFail, "变更未在基线开始至当前窗口开始之间完成，不满足时序支持条件。"
}

func affectedInstanceShare(evidence domain.Evidence, change domain.ChangeEvent) (share float64, overlap int, known bool, reason string) {
	if !evidenceComplete(evidence) {
		return 0, 0, false, "实例证据不完整，无法比较变更影响范围。"
	}
	if !change.AffectedInstancesComplete || len(change.AffectedInstances) == 0 {
		return 0, 0, false, "变更影响实例集合不完整，无法比较实例分布。"
	}
	if !evidence.InstancesExhaustive {
		return 0, 0, false, "实例分布仅为未穷尽 Top-K，不能把未返回实例解释为不存在。"
	}
	if bucketsRedacted(evidence.Instances) {
		return 0, 0, false, "实例标签已脱敏，无法与变更影响实例安全比较。"
	}
	affected := make(map[string]struct{}, len(change.AffectedInstances))
	for _, instance := range change.AffectedInstances {
		if instance == "" {
			return 0, 0, false, "变更影响实例包含空标识，无法比较实例分布。"
		}
		affected[instance] = struct{}{}
	}
	var count int64
	for _, bucket := range evidence.Instances {
		if _, exists := affected[bucket.Label]; exists {
			count += bucket.Count
			overlap++
		}
	}
	return percentage(count, evidence.ErrorCount), overlap, true, ""
}

func bucketsRedacted(buckets []domain.CountBucket) bool {
	for _, bucket := range buckets {
		if bucket.Redacted {
			return true
		}
	}
	return false
}

func firstUnknownReason(currentKnown bool, currentReason string, baselineKnown bool, baselineReason string) string {
	if !currentKnown {
		return currentReason
	}
	if !baselineKnown {
		return baselineReason
	}
	return ""
}

func correlationScore(entries []domain.EvidenceLedgerEntry) float64 {
	var score float64
	for _, entry := range entries {
		if entry.Result != domain.EvidenceTestPass {
			continue
		}
		if entry.Role == domain.EvidenceTestSupport {
			score += entry.Weight
		} else {
			score -= entry.Weight
		}
	}
	if score < 0 {
		return 0
	}
	if score > domain.CauseConfidenceCap {
		return domain.CauseConfidenceCap
	}
	return math.Round(score*100) / 100
}

func allRoleResults(entries []domain.EvidenceLedgerEntry, role domain.EvidenceTestRole, result domain.EvidenceTestResult) bool {
	found := false
	for _, entry := range entries {
		if entry.Role != role {
			continue
		}
		found = true
		if entry.Result != result {
			return false
		}
	}
	return found
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stableID(prefix string, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil)[:12])
}

func validateQueryResult(result domain.QueryResult) error {
	if result.QueryID == "" {
		return errors.New("query ID is required")
	}
	if !validSHA256Fingerprint(result.GovernanceFingerprint) {
		return errors.New("query governance fingerprint is required")
	}
	if result.ErrorCount < 0 || result.TopErrorCount < 0 || result.TopErrorCount > result.ErrorCount {
		return errors.New("error counts are inconsistent")
	}
	if result.APICalls != domain.ErrorAnalysisAPICalls {
		return fmt.Errorf("analysis result used %d API calls, want %d", result.APICalls, domain.ErrorAnalysisAPICalls)
	}
	if result.PatternLimit != domain.ErrorAnalysisPatternLimit || result.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
		return errors.New("analysis result limits do not match the fixed template")
	}
	patternTotal, err := validateBuckets("error patterns", result.ErrorPatterns, result.PatternLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	instanceTotal, err := validateBuckets("instances", result.Instances, result.InstanceLimit, result.ErrorCount)
	if err != nil {
		return err
	}
	if result.ErrorCount > 0 && (len(result.ErrorPatterns) == 0 || len(result.Instances) == 0) {
		return errors.New("non-zero error count requires pattern and instance buckets")
	}
	if (result.ErrorPatternsExhaustive && patternTotal != result.ErrorCount) || (result.InstancesExhaustive && instanceTotal != result.ErrorCount) {
		return errors.New("aggregate exhaustiveness markers are inconsistent")
	}
	if result.Complete && !result.Truncated && (result.ErrorPatternsExhaustive != (patternTotal == result.ErrorCount) || result.InstancesExhaustive != (instanceTotal == result.ErrorCount)) {
		return errors.New("complete aggregate exhaustiveness markers are inconsistent")
	}
	if len(result.ErrorPatterns) == 0 {
		if result.TopError != "" || result.TopErrorCount != 0 {
			return errors.New("top error is present without an error-pattern bucket")
		}
	} else if result.TopError != result.ErrorPatterns[0].Label || result.TopErrorCount != result.ErrorPatterns[0].Count {
		return errors.New("top error does not match the first error-pattern bucket")
	}
	return nil
}

func validSHA256Fingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateBuckets(name string, buckets []domain.CountBucket, limit int, total int64) (int64, error) {
	if len(buckets) > limit {
		return 0, fmt.Errorf("%s returned %d buckets, limit is %d", name, len(buckets), limit)
	}
	var sum int64
	for index, bucket := range buckets {
		if bucket.Label == "" || bucket.Count <= 0 {
			return 0, fmt.Errorf("%s bucket %d is invalid", name, index)
		}
		if sum > total-bucket.Count {
			return 0, fmt.Errorf("%s counts exceed total error count", name)
		}
		sum += bucket.Count
	}
	return sum, nil
}

func appendPatternFindings(report *domain.Report, current, baseline domain.Evidence) {
	if current.ErrorCount == 0 || len(current.ErrorPatterns) == 0 {
		return
	}
	currentEvidenceIDs := []string{current.ID}
	top := current.ErrorPatterns[0]
	report.Findings = append(report.Findings, domain.Finding{
		Code:        "error_pattern_share",
		Statement:   fmt.Sprintf("当前主要错误模式为 %s（%d 条，占当前错误 %.1f%%）。", top.Label, top.Count, percentage(top.Count, current.ErrorCount)),
		Confidence:  0.95,
		Conclusive:  true,
		EvidenceIDs: currentEvidenceIDs,
	})

	baselineLabels := make(map[string]struct{}, len(baseline.ErrorPatterns))
	baselineComparable := true
	for _, bucket := range baseline.ErrorPatterns {
		baselineLabels[bucket.Label] = struct{}{}
		baselineComparable = baselineComparable && !bucket.Redacted
	}
	comparisonEvidenceIDs := []string{current.ID, baseline.ID}
	recommendationAdded := false
	for _, bucket := range current.ErrorPatterns {
		if _, exists := baselineLabels[bucket.Label]; exists {
			continue
		}
		confirmed := baseline.ErrorPatternsExhaustive && baselineComparable && !bucket.Redacted
		finding := domain.Finding{
			Code:        "new_error_pattern_candidate",
			Statement:   fmt.Sprintf("新增候选模式 %s 在基线 Top%d 中未见，当前 %d 条，占 %.1f%%；基线分布未穷尽或标签不可比较，不能确认首次出现。", bucket.Label, baseline.PatternLimit, bucket.Count, percentage(bucket.Count, current.ErrorCount)),
			Confidence:  0,
			Conclusive:  false,
			EvidenceIDs: comparisonEvidenceIDs,
		}
		if confirmed {
			finding.Code = "new_error_pattern"
			finding.Statement = fmt.Sprintf("错误模式 %s 在等长基线窗口未出现，当前 %d 条，占 %.1f%%。", bucket.Label, bucket.Count, percentage(bucket.Count, current.ErrorCount))
			finding.Confidence = 0.95
			finding.Conclusive = true
		}
		report.Findings = append(report.Findings, finding)
		if !recommendationAdded {
			report.Recommendations = append(report.Recommendations, domain.Recommendation{
				Code:        "compare_recent_changes",
				Statement:   "对照该错误在当前窗口的出现时段检查发布、配置和依赖变更；如需判断是否为历史首次出现，应扩大基线后再确认。",
				EvidenceIDs: comparisonEvidenceIDs,
			})
			recommendationAdded = true
		}
	}
}

func appendInstanceFinding(report *domain.Report, current domain.Evidence) {
	if current.ErrorCount == 0 || len(current.Instances) == 0 {
		return
	}
	top := current.Instances[0]
	coverage := bucketCount(current.Instances)
	evidenceIDs := []string{current.ID}
	report.Findings = append(report.Findings, domain.Finding{
		Code:        "instance_distribution",
		Statement:   fmt.Sprintf("错误最多的实例为 %s（%d 条，占 %.1f%%）；实例 Top%d 覆盖当前错误的 %.1f%%。", top.Label, top.Count, percentage(top.Count, current.ErrorCount), current.InstanceLimit, percentage(coverage, current.ErrorCount)),
		Confidence:  0.95,
		Conclusive:  true,
		EvidenceIDs: evidenceIDs,
	})
	if percentage(top.Count, current.ErrorCount) >= 50 {
		report.Recommendations = append(report.Recommendations, domain.Recommendation{
			Code:        "inspect_hot_instance",
			Statement:   "优先检查错误最集中的实例状态、版本、重启记录和节点资源。",
			EvidenceIDs: evidenceIDs,
		})
	}
}

func percentage(part, total int64) float64 {
	if part <= 0 || total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func bucketCount(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

func spikeRatio(current, baseline int64) float64 {
	if baseline == 0 {
		if current == 0 {
			return 1
		}
		return float64(current)
	}
	return float64(current) / float64(baseline)
}

func hashSpec(spec domain.QuerySpec) (string, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode query spec: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func evidenceID(investigationID, specHash string) string {
	digest := sha256.Sum256([]byte(investigationID + ":" + specHash))
	return "ev_" + hex.EncodeToString(digest[:12])
}

var _ ports.InvestigationEngine = (*Engine)(nil)
