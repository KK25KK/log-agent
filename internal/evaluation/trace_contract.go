package evaluation

import (
	"context"
	"reflect"

	"logagent/internal/domain"
	"logagent/internal/observability"
)

type expectedTraceSpan struct {
	layer       domain.AgentLayer
	name        domain.AgentSpanName
	parent      domain.AgentSpanName
	terminalUse *domain.ToolUsage
}

type observedTraceSpan struct {
	start    *domain.AgentEvent
	terminal *domain.AgentEvent
}

// validTraceContract is deliberately stricter than domain.ValidateAgentTrace:
// the domain validator proves a safe closed hierarchy, while this gate proves
// that the fixed synthetic evaluation executed exactly the expected graph and
// Mock tools and that their usage agrees with independently collected stats.
func validTraceContract(ctx context.Context, evaluationCase EvaluationCase, stats ExecutionStats, runErr error) (bool, int) {
	trace := stats.AgentTrace
	toolSpans := countTerminalToolSpans(trace.Events)
	if runErr != nil {
		return false, toolSpans
	}
	run, ok := observability.RunContextFrom(ctx)
	if !ok || !trace.Complete || trace.DropCount != 0 || domain.ValidateAgentTrace(trace) != nil {
		return false, toolSpans
	}
	if trace.EvaluationRunID != run.EvaluationRunID || trace.TraceID != run.TraceID || trace.RunID != run.RunID || trace.CaseID != evaluationCase.ID || trace.VersionFingerprint != run.VersionFingerprint {
		return false, toolSpans
	}

	expected := expectedTraceSpans(evaluationCase)
	if len(trace.Events) != len(expected)*2 {
		return false, toolSpans
	}
	observed := make(map[domain.AgentSpanName]observedTraceSpan, len(expected))
	for index := range trace.Events {
		event := trace.Events[index]
		span := observed[event.Name]
		copy := event
		if event.Phase == domain.AgentPhaseStarted {
			if span.start != nil {
				return false, toolSpans
			}
			span.start = &copy
		} else {
			if span.terminal != nil {
				return false, toolSpans
			}
			span.terminal = &copy
		}
		observed[event.Name] = span
	}
	if len(observed) != len(expected) {
		return false, toolSpans
	}

	var slsLogicalCalls, slsProviderCalls, changeCalls int64
	var slsProcessedBytes int64
	for _, want := range expected {
		got, exists := observed[want.name]
		if !exists || got.start == nil || got.terminal == nil || got.terminal.Phase != domain.AgentPhaseSucceeded {
			return false, toolSpans
		}
		spanID := observability.StableSpanID(run.TraceID, want.layer, want.name)
		parentSpanID := ""
		if want.parent != "" {
			parentLayer := domain.AgentLayerGraphNode
			if want.layer == domain.AgentLayerGraphNode {
				parentLayer = domain.AgentLayerRun
			}
			parentSpanID = observability.StableSpanID(run.TraceID, parentLayer, want.parent)
		}
		if got.start.SpanID != spanID || got.terminal.SpanID != spanID || got.start.ParentSpanID != parentSpanID || got.terminal.ParentSpanID != parentSpanID || got.start.Layer != want.layer || got.terminal.Layer != want.layer {
			return false, toolSpans
		}
		if !reflect.DeepEqual(got.terminal.ToolUsage, want.terminalUse) {
			return false, toolSpans
		}
		if want.terminalUse == nil {
			continue
		}
		if want.name == domain.AgentSpanChangeSourceList {
			changeCalls += want.terminalUse.LogicalCalls
			continue
		}
		slsLogicalCalls += want.terminalUse.LogicalCalls
		slsProviderCalls += want.terminalUse.ProviderCalls
		slsProcessedBytes += want.terminalUse.ProcessedBytes
	}
	if slsLogicalCalls != int64(stats.LogicalSLSCalls) || slsProviderCalls != int64(stats.ProviderAPICalls) || slsProcessedBytes != stats.ProcessedBytes || changeCalls != int64(stats.ChangeSourceCalls) {
		return false, toolSpans
	}
	return toolSpans == 2+evaluationCase.Expected.ChangeSourceCalls, toolSpans
}

func expectedTraceSpans(evaluationCase EvaluationCase) []expectedTraceSpan {
	result := []expectedTraceSpan{
		{layer: domain.AgentLayerRun, name: domain.AgentSpanEngineRun},
		{layer: domain.AgentLayerGraphNode, name: domain.AgentSpanPlanQueries, parent: domain.AgentSpanEngineRun},
		{layer: domain.AgentLayerGraphNode, name: domain.AgentSpanExecuteQueries, parent: domain.AgentSpanEngineRun},
		{layer: domain.AgentLayerTool, name: domain.AgentSpanSLSCurrent, parent: domain.AgentSpanExecuteQueries, terminalUse: &domain.ToolUsage{
			LogicalCalls: 1, ProviderCalls: int64(evaluationCase.Current.APICalls), ProcessedBytes: evaluationCase.Current.ProcessedBytes, Complete: true,
		}},
		{layer: domain.AgentLayerTool, name: domain.AgentSpanSLSBaseline, parent: domain.AgentSpanExecuteQueries, terminalUse: &domain.ToolUsage{
			LogicalCalls: 1, ProviderCalls: int64(evaluationCase.Baseline.APICalls), ProcessedBytes: evaluationCase.Baseline.ProcessedBytes, Complete: true,
		}},
		{layer: domain.AgentLayerGraphNode, name: domain.AgentSpanBuildReport, parent: domain.AgentSpanEngineRun},
		{layer: domain.AgentLayerGraphNode, name: domain.AgentSpanCorrelateChanges, parent: domain.AgentSpanEngineRun},
	}
	if evaluationCase.Expected.ChangeSourceCalls == 1 {
		result = append(result, expectedTraceSpan{
			layer: domain.AgentLayerTool, name: domain.AgentSpanChangeSourceList, parent: domain.AgentSpanCorrelateChanges,
			terminalUse: &domain.ToolUsage{LogicalCalls: 1, Complete: true},
		})
	}
	return result
}

func countTerminalToolSpans(events []domain.AgentEvent) int {
	count := 0
	for _, event := range events {
		if event.Layer == domain.AgentLayerTool && event.Phase != domain.AgentPhaseStarted {
			count++
		}
	}
	return count
}
