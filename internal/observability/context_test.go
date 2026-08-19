package observability

import (
	"context"
	"strings"
	"testing"

	"logagent/internal/domain"
)

func TestRunContextRoundTripAndValidation(t *testing.T) {
	run := testRunContext()
	if err := run.Validate(); err != nil {
		t.Fatalf("validate run context: %v", err)
	}
	ctx := WithRunContext(context.Background(), run)
	run.CaseID = "mutated-after-attach"
	got, ok := RunContextFrom(ctx)
	if !ok || got.CaseID != "case-1" {
		t.Fatalf("run context was not retained by value: got=%+v ok=%t", got, ok)
	}
	if _, ok := RunContextFrom(context.Background()); ok {
		t.Fatal("empty context unexpectedly contained a run context")
	}
	if _, ok := RunContextFrom(nil); ok {
		t.Fatal("nil context unexpectedly contained a run context")
	}

	invalid := testRunContext()
	invalid.VersionFingerprint = "not-sha256"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid run context was accepted")
	}
}

func TestStableSpanIDIsDeterministicAndBoundToInputs(t *testing.T) {
	first := StableSpanID("trace-1", domain.AgentLayerRun, domain.AgentSpanEngineRun)
	second := StableSpanID("trace-1", domain.AgentLayerRun, domain.AgentSpanEngineRun)
	if first != second || len(first) != 64 {
		t.Fatalf("span ID is unstable: first=%q second=%q", first, second)
	}
	if first == StableSpanID("trace-2", domain.AgentLayerRun, domain.AgentSpanEngineRun) {
		t.Fatal("trace drift did not change span ID")
	}
	if first == StableSpanID("trace-1", domain.AgentLayerGraphNode, domain.AgentSpanPlanQueries) {
		t.Fatal("layer/name drift did not change span ID")
	}
}

func testRunContext() RunContext {
	return RunContext{
		EvaluationRunID:    "evaluation-run-1",
		TraceID:            "trace-1",
		RunID:              "run-1",
		CaseID:             "case-1",
		VersionFingerprint: strings.Repeat("f", 64),
	}
}
