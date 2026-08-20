package observability

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestBoundedRecorderBuildsCompleteTraceAndStableEventIDs(t *testing.T) {
	run := testRunContext()
	first := recordNestedTrace(run)
	second := recordNestedTrace(run)

	firstTrace := first.Snapshot()
	secondTrace := second.Snapshot()
	if !firstTrace.Complete || firstTrace.DropCount != 0 || len(firstTrace.Events) != 6 {
		t.Fatalf("unexpected first trace: complete=%t drops=%d events=%d", firstTrace.Complete, firstTrace.DropCount, len(firstTrace.Events))
	}
	if err := domain.ValidateAgentTrace(firstTrace); err != nil {
		t.Fatalf("validate recorded trace: %v", err)
	}
	for index, event := range firstTrace.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("sequence[%d]=%d", index, event.Sequence)
		}
		if event.EventID != secondTrace.Events[index].EventID {
			t.Fatalf("event ID[%d] is not stable: first=%q second=%q", index, event.EventID, secondTrace.Events[index].EventID)
		}
		if event.TraceID != run.TraceID || event.CaseID != run.CaseID || event.VersionFingerprint != run.VersionFingerprint {
			t.Fatalf("recorder did not fill run metadata: %+v", event)
		}
	}
}

func TestBoundedRecorderOverflowAndInvalidEventsOnlyDegradeTrace(t *testing.T) {
	run := testRunContext()
	root := spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, "run-span", "", domain.AgentPhaseStarted, 0, nil)

	overflow := NewBoundedRecorder(1, run)
	overflow.Record(root)
	overflow.Record(spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, "run-span", "", domain.AgentPhaseSucceeded, 1, nil))
	overflowTrace := overflow.Snapshot()
	if overflowTrace.Complete || overflowTrace.DropCount != 1 || len(overflowTrace.Events) != 1 {
		t.Fatalf("overflow was not visible: %+v", overflowTrace)
	}

	invalid := NewBoundedRecorder(4, run)
	bad := root
	bad.Layer = domain.AgentLayerGraphNode
	bad.Name = domain.AgentSpanPlanQueries
	invalid.Record(bad)
	invalidTrace := invalid.Snapshot()
	if invalidTrace.Complete || invalidTrace.DropCount != 1 || len(invalidTrace.Events) != 0 {
		t.Fatalf("invalid event was not dropped: %+v", invalidTrace)
	}

	conflict := NewBoundedRecorder(4, run)
	bad = root
	bad.CaseID = "different-case"
	conflict.Record(bad)
	if trace := conflict.Snapshot(); trace.DropCount != 1 || trace.Complete {
		t.Fatalf("conflicting run metadata was not dropped: %+v", trace)
	}

	badConfig := NewBoundedRecorder(MaxRecorderEvents+1, run)
	badConfig.Record(root)
	if trace := badConfig.Snapshot(); trace.DropCount != 1 || trace.Complete {
		t.Fatalf("invalid capacity did not degrade trace: %+v", trace)
	}
}

func TestBoundedRecorderSnapshotIsDeepCopy(t *testing.T) {
	recorder := recordNestedTrace(testRunContext())
	first := recorder.Snapshot()
	if first.Events[3].ToolUsage == nil {
		t.Fatal("test trace is missing terminal tool usage")
	}
	first.Events[0].EventID = "mutated"
	first.Events[3].ToolUsage.ProviderCalls = 999
	first.Events = append(first.Events, domain.AgentEvent{})

	second := recorder.Snapshot()
	if second.Events[0].EventID == "mutated" || second.Events[3].ToolUsage.ProviderCalls != 4 || len(second.Events) != 6 {
		t.Fatalf("snapshot mutation leaked into recorder: %+v", second)
	}
}

func TestBoundedRecorderConcurrentRecordingKeepsStrictSequence(t *testing.T) {
	run := testRunContext()
	const children = 100
	recorder := NewBoundedRecorder(2+children*2, run)
	rootID := StableSpanID(run.TraceID, domain.AgentLayerRun, domain.AgentSpanEngineRun)
	recorder.Record(spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, rootID, "", domain.AgentPhaseStarted, 0, nil))

	start := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	var group sync.WaitGroup
	for index := 0; index < children; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			event := spanEvent(run, domain.AgentLayerGraphNode, domain.AgentSpanPlanQueries, fmt.Sprintf("node-%03d", index), rootID, domain.AgentPhaseStarted, 0, nil)
			event.OccurredAt = start
			recorder.Record(event)
		}(index)
	}
	group.Wait()

	for index := 0; index < children; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			event := spanEvent(run, domain.AgentLayerGraphNode, domain.AgentSpanPlanQueries, fmt.Sprintf("node-%03d", index), rootID, domain.AgentPhaseSucceeded, 1, nil)
			event.OccurredAt = start.Add(time.Millisecond)
			recorder.Record(event)
		}(index)
	}
	group.Wait()
	rootEnd := spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, rootID, "", domain.AgentPhaseSucceeded, 2, nil)
	rootEnd.OccurredAt = start.Add(2 * time.Millisecond)
	recorder.Record(rootEnd)

	trace := recorder.Snapshot()
	if !trace.Complete || trace.DropCount != 0 || len(trace.Events) != 2+children*2 {
		t.Fatalf("concurrent trace is incomplete: complete=%t drops=%d events=%d", trace.Complete, trace.DropCount, len(trace.Events))
	}
	for index, event := range trace.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("sequence[%d]=%d", index, event.Sequence)
		}
	}
	if err := domain.ValidateAgentTrace(trace); err != nil {
		t.Fatalf("validate concurrent trace: %v", err)
	}
}

func TestNoopObserverAcceptsEvents(t *testing.T) {
	NoopObserver{}.Record(domain.AgentEvent{})
}

func recordNestedTrace(run RunContext) *BoundedRecorder {
	recorder := NewBoundedRecorder(6, run)
	runID := StableSpanID(run.TraceID, domain.AgentLayerRun, domain.AgentSpanEngineRun)
	nodeID := StableSpanID(run.TraceID, domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries)
	toolID := StableSpanID(run.TraceID, domain.AgentLayerTool, domain.AgentSpanSLSCurrent)
	recorder.Record(spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, runID, "", domain.AgentPhaseStarted, 0, nil))
	recorder.Record(spanEvent(run, domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries, nodeID, runID, domain.AgentPhaseStarted, 0, nil))
	recorder.Record(spanEvent(run, domain.AgentLayerTool, domain.AgentSpanSLSCurrent, toolID, nodeID, domain.AgentPhaseStarted, 0, nil))
	recorder.Record(spanEvent(run, domain.AgentLayerTool, domain.AgentSpanSLSCurrent, toolID, nodeID, domain.AgentPhaseSucceeded, 2, &domain.ToolUsage{LogicalCalls: 1, ProviderCalls: 4, ProcessedBytes: 1024, Complete: true}))
	recorder.Record(spanEvent(run, domain.AgentLayerGraphNode, domain.AgentSpanExecuteQueries, nodeID, runID, domain.AgentPhaseSucceeded, 3, nil))
	recorder.Record(spanEvent(run, domain.AgentLayerRun, domain.AgentSpanEngineRun, runID, "", domain.AgentPhaseSucceeded, 4, nil))
	return recorder
}

func spanEvent(run RunContext, layer domain.AgentLayer, name domain.AgentSpanName, spanID, parentID string, phase domain.AgentPhase, duration int64, usage *domain.ToolUsage) domain.AgentEvent {
	return domain.AgentEvent{
		SpanID: spanID, ParentSpanID: parentID, Layer: layer, Name: name, Phase: phase,
		OccurredAt:           time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC).Add(time.Duration(duration) * time.Millisecond),
		DurationMilliseconds: duration, ToolUsage: usage,
	}
}
