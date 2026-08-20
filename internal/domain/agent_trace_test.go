package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAgentEventEnumsAndBoundaries(t *testing.T) {
	if AgentTraceSchemaVersion != "agent-trace-v1" || ReplaySchemaVersion != "evaluation-replay-v1" {
		t.Fatalf("schema constants drifted: trace=%q replay=%q", AgentTraceSchemaVersion, ReplaySchemaVersion)
	}
	wantFailureCodes := map[AgentFailureCode]string{
		AgentFailureCodeEngineRunFailed:   "engine_run_failed",
		AgentFailureCodeGraphNodeFailed:   "graph_node_failed",
		AgentFailureCodeToolFailed:        "tool_failed",
		AgentFailureCodeContextCancelled:  "context_cancelled",
		AgentFailureCodeDeadlineExceeded:  "deadline_exceeded",
		AgentFailureCodeContractViolation: "contract_violation",
	}
	for code, want := range wantFailureCodes {
		if string(code) != want {
			t.Fatalf("failure code drifted: got=%q want=%q", code, want)
		}
	}
	wantFailures := []FailureClass{
		FailureClassValidation, FailureClassPolicy, FailureClassDependency,
		FailureClassTimeout, FailureClassCancelled, FailureClassStateConflict,
		FailureClassExternalOutcomeUnknown, FailureClassContractViolation, FailureClassInternal,
	}
	for _, failure := range wantFailures {
		event := validDomainEvent()
		event.Phase = AgentPhaseFailed
		event.FailureClass = failure
		event.FailureCode = testFailureCode(failure, event.Layer)
		if err := ValidateAgentEvent(event); err != nil {
			t.Fatalf("failure class %q was rejected: %v", failure, err)
		}
	}

	tests := []struct {
		name   string
		mutate func(*AgentEvent)
	}{
		{"unknown layer", func(event *AgentEvent) { event.Layer = "MODEL" }},
		{"name outside layer", func(event *AgentEvent) { event.Name = AgentSpanPlanQueries }},
		{"unknown phase", func(event *AgentEvent) { event.Phase = "RUNNING" }},
		{"unknown failure class", func(event *AgentEvent) {
			event.Phase = AgentPhaseFailed
			event.FailureClass = "RAW_ERROR"
			event.FailureCode = AgentFailureCodeEngineRunFailed
		}},
		{"unknown failure code", func(event *AgentEvent) {
			event.Phase = AgentPhaseFailed
			event.FailureClass = FailureClassInternal
			event.FailureCode = "raw_error"
		}},
		{"missing failure code", func(event *AgentEvent) { event.Phase = AgentPhaseFailed; event.FailureClass = FailureClassInternal }},
		{"failure code layer mismatch", func(event *AgentEvent) {
			event.Phase = AgentPhaseFailed
			event.FailureClass = FailureClassDependency
			event.FailureCode = AgentFailureCodeToolFailed
		}},
		{"failure class code mismatch", func(event *AgentEvent) {
			event.Phase = AgentPhaseFailed
			event.FailureClass = FailureClassTimeout
			event.FailureCode = AgentFailureCodeEngineRunFailed
		}},
		{"failure on success", func(event *AgentEvent) { event.Phase = AgentPhaseSucceeded; event.FailureClass = FailureClassInternal }},
		{"failure code on success", func(event *AgentEvent) {
			event.Phase = AgentPhaseSucceeded
			event.FailureCode = AgentFailureCodeEngineRunFailed
		}},
		{"duration on start", func(event *AgentEvent) { event.DurationMilliseconds = 1 }},
		{"output hash on start", func(event *AgentEvent) { event.OutputFingerprint = strings.Repeat("a", 64) }},
		{"usage on non-tool", func(event *AgentEvent) { event.ToolUsage = &ToolUsage{} }},
		{"invalid hash", func(event *AgentEvent) { event.InputFingerprint = "not-sha256" }},
		{"negative duration", func(event *AgentEvent) { event.Phase = AgentPhaseSucceeded; event.DurationMilliseconds = -1 }},
		{"zero timestamp", func(event *AgentEvent) { event.OccurredAt = time.Time{} }},
		{"unsafe identity", func(event *AgentEvent) { event.CaseID = "case with spaces" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validDomainEvent()
			test.mutate(&event)
			if err := ValidateAgentEvent(event); err == nil {
				t.Fatal("invalid event was accepted")
			}
		})
	}

	terminalTool := validDomainEvent()
	terminalTool.Layer = AgentLayerTool
	terminalTool.Name = AgentSpanSLSCurrent
	terminalTool.Phase = AgentPhaseSucceeded
	terminalTool.DurationMilliseconds = 3
	if err := ValidateAgentEvent(terminalTool); err == nil {
		t.Fatal("terminal tool event without usage was accepted")
	}
	terminalTool.ToolUsage = &ToolUsage{LogicalCalls: 1, ProviderCalls: 4, ProcessedBytes: 10, Complete: true}
	if err := ValidateAgentEvent(terminalTool); err != nil {
		t.Fatalf("valid terminal tool event was rejected: %v", err)
	}
	terminalTool.ToolUsage.ProcessedBytes = -1
	if err := ValidateAgentEvent(terminalTool); err == nil {
		t.Fatal("negative tool usage was accepted")
	}
}

func TestAgentEventHasNoArbitraryOrPrivatePayloadFields(t *testing.T) {
	wantJSONFields := []string{
		"schema_version", "event_id", "sequence", "evaluation_run_id", "trace_id", "run_id", "case_id",
		"version_fingerprint", "span_id", "parent_span_id", "layer", "name", "phase", "occurred_at",
		"duration_milliseconds", "failure_class", "failure_code", "input_fingerprint", "output_fingerprint", "tool_usage",
	}
	eventType := reflect.TypeOf(AgentEvent{})
	gotJSONFields := make([]string, 0, eventType.NumField())
	for index := 0; index < eventType.NumField(); index++ {
		field := eventType.Field(index)
		if field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Interface {
			t.Fatalf("AgentEvent field %s permits arbitrary payloads", field.Name)
		}
		gotJSONFields = append(gotJSONFields, strings.Split(field.Tag.Get("json"), ",")[0])
	}
	if !reflect.DeepEqual(gotJSONFields, wantJSONFields) {
		t.Fatalf("AgentEvent JSON surface changed:\n got=%v\nwant=%v", gotJSONFields, wantJSONFields)
	}

	usageType := reflect.TypeOf(ToolUsage{})
	if usageType.NumField() != 4 {
		t.Fatalf("ToolUsage gained fields: %d", usageType.NumField())
	}
	wantUsage := []string{"logical_calls", "provider_calls", "processed_bytes", "complete"}
	for index, want := range wantUsage {
		if got := strings.Split(usageType.Field(index).Tag.Get("json"), ",")[0]; got != want {
			t.Fatalf("ToolUsage field %d = %q, want %q", index, got, want)
		}
	}

	encoded, err := json.Marshal(validDomainEvent())
	if err != nil {
		t.Fatalf("marshal AgentEvent: %v", err)
	}
	for _, forbidden := range []string{"message", "principal", "resource", "query", "raw", "error_text", "attributes", "prompt", "model", "token", "callback"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("serialized AgentEvent contains forbidden surface %q: %s", forbidden, encoded)
		}
	}
}

func TestAgentVersionManifestValidationAndFingerprint(t *testing.T) {
	base := validVersionManifest()
	first, err := base.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint manifest: %v", err)
	}
	second, err := base.Fingerprint()
	if err != nil || first != second || len(first) != sha256.Size*2 {
		t.Fatalf("manifest fingerprint is unstable: first=%q second=%q err=%v", first, second, err)
	}

	drifts := []struct {
		name   string
		mutate func(*AgentVersionManifest)
	}{
		{"dataset schema", func(value *AgentVersionManifest) { value.DatasetSchemaVersion += ".2" }},
		{"dataset ID", func(value *AgentVersionManifest) { value.DatasetID += ".2" }},
		{"dataset fingerprint", func(value *AgentVersionManifest) { value.DatasetFingerprint = strings.Repeat("b", 64) }},
		{"graph", func(value *AgentVersionManifest) { value.GraphVersion += ".2" }},
		{"template ID", func(value *AgentVersionManifest) { value.TemplateID += ".2" }},
		{"template", func(value *AgentVersionManifest) { value.TemplateVersion += ".2" }},
		{"policy", func(value *AgentVersionManifest) { value.PolicyVersion += ".2" }},
		{"cause", func(value *AgentVersionManifest) { value.CauseVersion += ".2" }},
		{"evaluation", func(value *AgentVersionManifest) { value.EvaluationVersion += ".2" }},
		{"executor profile", func(value *AgentVersionManifest) { value.ExecutorProfile += ".2" }},
	}
	for _, drift := range drifts {
		t.Run(drift.name, func(t *testing.T) {
			changed := base
			drift.mutate(&changed)
			fingerprint, err := changed.Fingerprint()
			if err != nil {
				t.Fatalf("changed manifest rejected: %v", err)
			}
			if fingerprint == first {
				t.Fatal("manifest drift did not change fingerprint")
			}
		})
	}

	for _, mutate := range []func(*AgentVersionManifest){
		func(value *AgentVersionManifest) { value.PromptVersion = "prompt-v1" },
		func(value *AgentVersionManifest) { value.PromptFingerprint = strings.Repeat("c", 64) },
		func(value *AgentVersionManifest) { value.ModelProvider = "provider-v1" },
		func(value *AgentVersionManifest) { value.ModelName = "model-v1" },
	} {
		invalid := base
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatal("prompt_used=false accepted prompt/model metadata")
		}
	}

	model := base
	model.PromptUsed = true
	model.PromptVersion = "prompt-v1"
	model.PromptFingerprint = strings.Repeat("c", 64)
	model.ModelProvider = "provider-v1"
	model.ModelName = "model-v1"
	modelFingerprint, err := model.Fingerprint()
	if err != nil || modelFingerprint == first {
		t.Fatalf("valid model manifest did not drift fingerprint: fingerprint=%q err=%v", modelFingerprint, err)
	}

	leftJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var decoded AgentVersionManifest
	if err := json.Unmarshal(leftJSON, &decoded); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	decodedFingerprint, err := decoded.Fingerprint()
	if err != nil || decodedFingerprint != first {
		t.Fatalf("equivalent decoded manifest changed normalized fingerprint: got=%q want=%q err=%v", decodedFingerprint, first, err)
	}
}

func TestValidateAgentTraceRequiresClosedNestedSpans(t *testing.T) {
	start := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	runStart := validDomainEvent()
	runStart.OccurredAt = start
	runStart.SpanID = "run-span"
	runStart.EventID = domainTestHash("event-1")

	nodeStart := runStart
	nodeStart.Sequence = 2
	nodeStart.EventID = domainTestHash("event-2")
	nodeStart.SpanID = "node-span"
	nodeStart.ParentSpanID = runStart.SpanID
	nodeStart.Layer = AgentLayerGraphNode
	nodeStart.Name = AgentSpanPlanQueries

	nodeEnd := nodeStart
	nodeEnd.Sequence = 3
	nodeEnd.EventID = domainTestHash("event-3")
	nodeEnd.Phase = AgentPhaseSucceeded
	nodeEnd.OccurredAt = start.Add(time.Millisecond)
	nodeEnd.DurationMilliseconds = 1

	runEnd := runStart
	runEnd.Sequence = 4
	runEnd.EventID = domainTestHash("event-4")
	runEnd.Phase = AgentPhaseSucceeded
	runEnd.OccurredAt = start.Add(2 * time.Millisecond)
	runEnd.DurationMilliseconds = 2

	trace := AgentTrace{
		SchemaVersion: AgentTraceSchemaVersion, EvaluationRunID: runStart.EvaluationRunID,
		TraceID: runStart.TraceID, RunID: runStart.RunID, CaseID: runStart.CaseID,
		VersionFingerprint: runStart.VersionFingerprint, Complete: true,
		Events: []AgentEvent{runStart, nodeStart, nodeEnd, runEnd},
	}
	if err := ValidateAgentTrace(trace); err != nil {
		t.Fatalf("valid trace was rejected: %v", err)
	}

	open := trace
	open.Events = open.Events[:3]
	if err := ValidateAgentTrace(open); err == nil {
		t.Fatal("complete trace with open root was accepted")
	}
	outOfOrder := trace
	outOfOrder.Events = append([]AgentEvent(nil), trace.Events...)
	outOfOrder.Events[2].Sequence = 5
	if err := ValidateAgentTrace(outOfOrder); err == nil {
		t.Fatal("non-contiguous event sequence was accepted")
	}
}

func validDomainEvent() AgentEvent {
	return AgentEvent{
		SchemaVersion: AgentTraceSchemaVersion, EventID: strings.Repeat("a", 64), Sequence: 1,
		EvaluationRunID: "evaluation-run-1", TraceID: "trace-1", RunID: "run-1", CaseID: "case-1",
		VersionFingerprint: strings.Repeat("f", 64), SpanID: "span-1", Layer: AgentLayerRun,
		Name: AgentSpanEngineRun, Phase: AgentPhaseStarted,
		OccurredAt: time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC),
	}
}

func validVersionManifest() AgentVersionManifest {
	return AgentVersionManifest{
		DatasetSchemaVersion: "evaluation-dataset-v1",
		DatasetID:            "synthetic-m5a-v1",
		DatasetFingerprint:   strings.Repeat("a", 64),
		GraphVersion:         "error-spike-investigation-v1",
		TemplateID:           ErrorAnalysisTemplateID,
		TemplateVersion:      "error-analysis-v2",
		PolicyVersion:        "evaluation-policy-v1",
		CauseVersion:         "change-correlation-v1",
		EvaluationVersion:    "evaluation-gate-v1",
		TraceSchemaVersion:   AgentTraceSchemaVersion,
		ReplaySchemaVersion:  ReplaySchemaVersion,
		ExecutorProfile:      "synthetic_mock",
		PromptUsed:           false,
	}
}

func testFailureCode(class FailureClass, layer AgentLayer) AgentFailureCode {
	switch class {
	case FailureClassCancelled:
		return AgentFailureCodeContextCancelled
	case FailureClassTimeout:
		return AgentFailureCodeDeadlineExceeded
	case FailureClassContractViolation:
		return AgentFailureCodeContractViolation
	}
	switch layer {
	case AgentLayerRun:
		return AgentFailureCodeEngineRunFailed
	case AgentLayerGraphNode:
		return AgentFailureCodeGraphNodeFailed
	case AgentLayerTool:
		return AgentFailureCodeToolFailed
	default:
		return ""
	}
}

func domainTestHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
