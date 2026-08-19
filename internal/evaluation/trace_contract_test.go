package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/observability"
)

func TestTraceContractFailsClosedOnMissingDroppedAndMismatchedUsage(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*ExecutionStats)
	}{
		{name: "missing", mutate: func(stats *ExecutionStats) { stats.AgentTrace = domain.AgentTrace{} }},
		{name: "dropped", mutate: func(stats *ExecutionStats) {
			stats.AgentTrace.Complete = false
			stats.AgentTrace.DropCount = 1
		}},
		{name: "provider usage mismatch", mutate: func(stats *ExecutionStats) {
			for index := range stats.AgentTrace.Events {
				event := &stats.AgentTrace.Events[index]
				if event.Name == domain.AgentSpanSLSCurrent && event.Phase == domain.AgentPhaseSucceeded {
					event.ToolUsage.ProviderCalls--
					return
				}
			}
			t.Fatal("current tool terminal event not found")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execute := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
				report, stats, err := successfulExecution(ctx, evaluationCase)
				if evaluationCase.ID == dataset.Cases[0].ID {
					test.mutate(&stats)
				}
				return report, stats, err
			}
			report, err := evaluateMetrics(context.Background(), dataset, execute)
			if !errors.Is(err, ErrGateFailed) {
				t.Fatalf("error=%v, want evaluation gate failure", err)
			}
			if report.Metrics.TraceContractAccuracy >= 1 || report.Metrics.TraceContractFailures != 1 || report.Cases[0].TraceContractPassed {
				t.Fatalf("trace drift was not visible: metrics=%#v case=%#v", report.Metrics, report.Cases[0])
			}
		})
	}
}

func successfulAgentTrace(ctx context.Context, evaluationCase EvaluationCase) domain.AgentTrace {
	run, ok := observability.RunContextFrom(ctx)
	if !ok {
		return domain.AgentTrace{}
	}
	recorder := observability.NewBoundedRecorder(32, run)
	clock := evaluationCase.Request.EndTime.UTC().Add(time.Second)
	advance := func() time.Time {
		clock = clock.Add(time.Millisecond)
		return clock
	}
	spanID := func(layer domain.AgentLayer, name domain.AgentSpanName) string {
		return observability.StableSpanID(run.TraceID, layer, name)
	}
	start := func(layer domain.AgentLayer, name domain.AgentSpanName, parent string) {
		recorder.Record(domain.AgentEvent{
			SpanID: spanID(layer, name), ParentSpanID: parent, Layer: layer, Name: name,
			Phase: domain.AgentPhaseStarted, OccurredAt: advance(),
		})
	}
	finish := func(layer domain.AgentLayer, name domain.AgentSpanName, parent string, usage *domain.ToolUsage) {
		recorder.Record(domain.AgentEvent{
			SpanID: spanID(layer, name), ParentSpanID: parent, Layer: layer, Name: name,
			Phase: domain.AgentPhaseSucceeded, OccurredAt: advance(), DurationMilliseconds: 1, ToolUsage: usage,
		})
	}

	root := spanID(domain.AgentLayerRun, domain.AgentSpanEngineRun)
	start(domain.AgentLayerRun, domain.AgentSpanEngineRun, "")
	start(domain.AgentLayerGraphNode, domain.AgentSpanPlanQueries, root)
	finish(domain.AgentLayerGraphNode, domain.AgentSpanPlanQueries, root, nil)

	start(domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries, root)
	executeNode := spanID(domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries)
	start(domain.AgentLayerTool, domain.AgentSpanSLSCurrent, executeNode)
	finish(domain.AgentLayerTool, domain.AgentSpanSLSCurrent, executeNode, &domain.ToolUsage{
		LogicalCalls: 1, ProviderCalls: int64(evaluationCase.Current.APICalls), ProcessedBytes: evaluationCase.Current.ProcessedBytes, Complete: true,
	})
	start(domain.AgentLayerTool, domain.AgentSpanSLSBaseline, executeNode)
	finish(domain.AgentLayerTool, domain.AgentSpanSLSBaseline, executeNode, &domain.ToolUsage{
		LogicalCalls: 1, ProviderCalls: int64(evaluationCase.Baseline.APICalls), ProcessedBytes: evaluationCase.Baseline.ProcessedBytes, Complete: true,
	})
	finish(domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries, root, nil)

	start(domain.AgentLayerGraphNode, domain.AgentSpanBuildReport, root)
	finish(domain.AgentLayerGraphNode, domain.AgentSpanBuildReport, root, nil)
	start(domain.AgentLayerGraphNode, domain.AgentSpanCorrelateChanges, root)
	if evaluationCase.Expected.ChangeSourceCalls == 1 {
		correlateNode := spanID(domain.AgentLayerGraphNode, domain.AgentSpanCorrelateChanges)
		start(domain.AgentLayerTool, domain.AgentSpanChangeSourceList, correlateNode)
		finish(domain.AgentLayerTool, domain.AgentSpanChangeSourceList, correlateNode, &domain.ToolUsage{LogicalCalls: 1, Complete: true})
	}
	finish(domain.AgentLayerGraphNode, domain.AgentSpanCorrelateChanges, root, nil)
	finish(domain.AgentLayerRun, domain.AgentSpanEngineRun, "", nil)
	return recorder.Snapshot()
}
