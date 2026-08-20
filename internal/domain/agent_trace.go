package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	AgentTraceSchemaVersion = "agent-trace-v1"
	ReplaySchemaVersion     = "evaluation-replay-v1"

	maxAgentIdentifierBytes = 128
	maxAgentVersionBytes    = 128
)

var (
	agentIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	agentVersionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)
)

// AgentLayer is deliberately closed so telemetry cannot grow into an
// ungoverned arbitrary-attribute channel.
type AgentLayer string

const (
	AgentLayerRun       AgentLayer = "RUN"
	AgentLayerGraphNode AgentLayer = "GRAPH_NODE"
	AgentLayerTool      AgentLayer = "TOOL"
)

// AgentSpanName is the fixed M5-B Engine observation surface.
type AgentSpanName string

const (
	AgentSpanEngineRun        AgentSpanName = "engine.run"
	AgentSpanPlanQueries      AgentSpanName = "plan_queries"
	AgentSpanExecuteQueries   AgentSpanName = "execute_queries"
	AgentSpanBuildReport      AgentSpanName = "build_report"
	AgentSpanCorrelateChanges AgentSpanName = "correlate_changes"
	AgentSpanSLSCurrent       AgentSpanName = "sls.current"
	AgentSpanSLSBaseline      AgentSpanName = "sls.baseline"
	AgentSpanChangeSourceList AgentSpanName = "change_source.list"
)

// AgentPhase has one start phase and three mutually exclusive terminal phases.
type AgentPhase string

const (
	AgentPhaseStarted   AgentPhase = "STARTED"
	AgentPhaseSucceeded AgentPhase = "SUCCEEDED"
	AgentPhaseFailed    AgentPhase = "FAILED"
	AgentPhaseSkipped   AgentPhase = "SKIPPED"
)

// FailureClass is safe to serialize. It never contains a provider error or
// other free-form diagnostic text and does not control retry behavior.
type FailureClass string

const (
	FailureClassValidation             FailureClass = "VALIDATION"
	FailureClassPolicy                 FailureClass = "POLICY"
	FailureClassDependency             FailureClass = "DEPENDENCY"
	FailureClassTimeout                FailureClass = "TIMEOUT"
	FailureClassCancelled              FailureClass = "CANCELLED"
	FailureClassStateConflict          FailureClass = "STATE_CONFLICT"
	FailureClassExternalOutcomeUnknown FailureClass = "EXTERNAL_OUTCOME_UNKNOWN"
	FailureClassContractViolation      FailureClass = "CONTRACT_VIOLATION"
	FailureClassInternal               FailureClass = "INTERNAL"
)

// AgentFailureCode is a closed, location-aware failure reason. FailureClass
// remains the orthogonal operational category used for aggregation.
type AgentFailureCode string

const (
	AgentFailureCodeEngineRunFailed   AgentFailureCode = "engine_run_failed"
	AgentFailureCodeGraphNodeFailed   AgentFailureCode = "graph_node_failed"
	AgentFailureCodeToolFailed        AgentFailureCode = "tool_failed"
	AgentFailureCodeContextCancelled  AgentFailureCode = "context_cancelled"
	AgentFailureCodeDeadlineExceeded  AgentFailureCode = "deadline_exceeded"
	AgentFailureCodeContractViolation AgentFailureCode = "contract_violation"
)

// ToolUsage is the complete permitted usage projection for an Agent event.
// It intentionally excludes token, resource, query, and provider-detail data.
type ToolUsage struct {
	LogicalCalls   int64 `json:"logical_calls"`
	ProviderCalls  int64 `json:"provider_calls"`
	ProcessedBytes int64 `json:"processed_bytes"`
	Complete       bool  `json:"complete"`
}

// AgentEvent is a privacy-bounded span event. The recorder owns EventID and
// Sequence; callers supply only typed observation data.
type AgentEvent struct {
	SchemaVersion        string           `json:"schema_version"`
	EventID              string           `json:"event_id"`
	Sequence             uint64           `json:"sequence"`
	EvaluationRunID      string           `json:"evaluation_run_id"`
	TraceID              string           `json:"trace_id"`
	RunID                string           `json:"run_id"`
	CaseID               string           `json:"case_id"`
	VersionFingerprint   string           `json:"version_fingerprint"`
	SpanID               string           `json:"span_id"`
	ParentSpanID         string           `json:"parent_span_id,omitempty"`
	Layer                AgentLayer       `json:"layer"`
	Name                 AgentSpanName    `json:"name"`
	Phase                AgentPhase       `json:"phase"`
	OccurredAt           time.Time        `json:"occurred_at"`
	DurationMilliseconds int64            `json:"duration_milliseconds"`
	FailureClass         FailureClass     `json:"failure_class,omitempty"`
	FailureCode          AgentFailureCode `json:"failure_code,omitempty"`
	InputFingerprint     string           `json:"input_fingerprint,omitempty"`
	OutputFingerprint    string           `json:"output_fingerprint,omitempty"`
	ToolUsage            *ToolUsage       `json:"tool_usage,omitempty"`
}

// AgentTrace is the bounded recorder projection for one synthetic evaluation
// Case. Complete=false is an explicit signal that it must not pass replay gates.
type AgentTrace struct {
	SchemaVersion      string       `json:"schema_version"`
	EvaluationRunID    string       `json:"evaluation_run_id"`
	TraceID            string       `json:"trace_id"`
	RunID              string       `json:"run_id"`
	CaseID             string       `json:"case_id"`
	VersionFingerprint string       `json:"version_fingerprint"`
	Complete           bool         `json:"complete"`
	DropCount          uint64       `json:"drop_count"`
	Events             []AgentEvent `json:"events"`
}

// AgentVersionManifest binds every deterministic input that may change replay
// behavior. Prompt and model values are metadata identifiers, never raw text.
type AgentVersionManifest struct {
	DatasetSchemaVersion string `json:"dataset_schema_version"`
	DatasetID            string `json:"dataset_id"`
	DatasetFingerprint   string `json:"dataset_fingerprint"`
	GraphVersion         string `json:"graph_version"`
	TemplateID           string `json:"template_id"`
	TemplateVersion      string `json:"template_version"`
	PolicyVersion        string `json:"policy_version"`
	CauseVersion         string `json:"cause_version"`
	EvaluationVersion    string `json:"evaluation_version"`
	TraceSchemaVersion   string `json:"trace_schema_version"`
	ReplaySchemaVersion  string `json:"replay_schema_version"`
	ExecutorProfile      string `json:"executor_profile"`
	PromptUsed           bool   `json:"prompt_used"`
	PromptVersion        string `json:"prompt_version,omitempty"`
	PromptFingerprint    string `json:"prompt_fingerprint,omitempty"`
	ModelProvider        string `json:"model_provider,omitempty"`
	ModelName            string `json:"model_name,omitempty"`
}

type agentSpanState struct {
	start    AgentEvent
	terminal *AgentEvent
}

// ValidateAgentEvent enforces the serialized event contract after recorder
// identity and ordering fields have been assigned.
func ValidateAgentEvent(event AgentEvent) error {
	if event.SchemaVersion != AgentTraceSchemaVersion {
		return errors.New("agent event schema version is invalid")
	}
	if !validSHA256(event.EventID) || event.Sequence == 0 {
		return errors.New("agent event identity is invalid")
	}
	if err := validateAgentRunIdentity(event.EvaluationRunID, event.TraceID, event.RunID, event.CaseID, event.VersionFingerprint); err != nil {
		return err
	}
	if !validAgentIdentifier(event.SpanID) || (event.ParentSpanID != "" && !validAgentIdentifier(event.ParentSpanID)) {
		return errors.New("agent span identity is invalid")
	}
	if !validLayerAndName(event.Layer, event.Name) {
		return errors.New("agent layer and span name are invalid")
	}
	if event.OccurredAt.IsZero() {
		return errors.New("agent event timestamp is required")
	}
	if event.DurationMilliseconds < 0 {
		return errors.New("agent event duration cannot be negative")
	}
	if err := validateAgentPhase(event); err != nil {
		return err
	}
	if event.InputFingerprint != "" && !validSHA256(event.InputFingerprint) {
		return errors.New("agent event input fingerprint is invalid")
	}
	if event.OutputFingerprint != "" && !validSHA256(event.OutputFingerprint) {
		return errors.New("agent event output fingerprint is invalid")
	}
	return validateToolUsage(event)
}

// ValidateAgentTrace validates accepted events and, when marked complete, the
// closed parent/child lifecycle for every span.
func ValidateAgentTrace(trace AgentTrace) error {
	if trace.SchemaVersion != AgentTraceSchemaVersion {
		return errors.New("agent trace schema version is invalid")
	}
	if err := validateAgentRunIdentity(trace.EvaluationRunID, trace.TraceID, trace.RunID, trace.CaseID, trace.VersionFingerprint); err != nil {
		return err
	}
	if trace.Complete && trace.DropCount != 0 {
		return errors.New("complete agent trace cannot contain dropped events")
	}
	if trace.Complete && len(trace.Events) == 0 {
		return errors.New("complete agent trace requires events")
	}
	return validateAgentTraceEvents(trace)
}

// Validate checks that a runtime manifest is bounded and internally coherent.
func (manifest AgentVersionManifest) Validate() error {
	versionFields := []struct {
		name  string
		value string
	}{
		{"dataset schema version", manifest.DatasetSchemaVersion},
		{"dataset ID", manifest.DatasetID},
		{"graph version", manifest.GraphVersion},
		{"template ID", manifest.TemplateID},
		{"template version", manifest.TemplateVersion},
		{"policy version", manifest.PolicyVersion},
		{"cause version", manifest.CauseVersion},
		{"evaluation version", manifest.EvaluationVersion},
		{"executor profile", manifest.ExecutorProfile},
	}
	for _, field := range versionFields {
		if err := validateAgentVersion(field.name, field.value); err != nil {
			return err
		}
	}
	if !validSHA256(manifest.DatasetFingerprint) {
		return errors.New("dataset fingerprint is invalid")
	}
	if manifest.TraceSchemaVersion != AgentTraceSchemaVersion {
		return errors.New("trace schema version is invalid")
	}
	if manifest.ReplaySchemaVersion != ReplaySchemaVersion {
		return errors.New("replay schema version is invalid")
	}
	return validatePromptManifest(manifest)
}

// Fingerprint returns the SHA-256 of the validated canonical manifest shape.
func (manifest AgentVersionManifest) Fingerprint() (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode agent version manifest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validateAgentTraceEvents(trace AgentTrace) error {
	spans := make(map[string]*agentSpanState, len(trace.Events)/2)
	eventIDs := make(map[string]struct{}, len(trace.Events))
	for index := range trace.Events {
		event := trace.Events[index]
		if err := ValidateAgentEvent(event); err != nil {
			return fmt.Errorf("validate agent event %d: %w", index+1, err)
		}
		if event.Sequence != uint64(index+1) {
			return errors.New("agent event sequence is not contiguous and increasing")
		}
		if !sameAgentRun(trace, event) {
			return errors.New("agent event belongs to a different trace context")
		}
		if _, duplicate := eventIDs[event.EventID]; duplicate {
			return errors.New("agent trace contains a duplicate event ID")
		}
		eventIDs[event.EventID] = struct{}{}
		if err := addAgentSpanEvent(spans, event); err != nil {
			return err
		}
	}
	if !trace.Complete {
		return nil
	}
	return validateClosedAgentSpans(spans)
}

func addAgentSpanEvent(spans map[string]*agentSpanState, event AgentEvent) error {
	state, exists := spans[event.SpanID]
	if event.Phase == AgentPhaseStarted {
		if exists {
			return errors.New("agent span has more than one start event")
		}
		spans[event.SpanID] = &agentSpanState{start: event}
		return nil
	}
	if !exists {
		return errors.New("agent span terminal event precedes its start")
	}
	if state.terminal != nil {
		return errors.New("agent span has more than one terminal event")
	}
	if state.start.Layer != event.Layer || state.start.Name != event.Name || state.start.ParentSpanID != event.ParentSpanID {
		return errors.New("agent span terminal event does not match its start")
	}
	if event.OccurredAt.Before(state.start.OccurredAt) {
		return errors.New("agent span terminal timestamp precedes its start")
	}
	copy := event
	state.terminal = &copy
	return nil
}

func validateClosedAgentSpans(spans map[string]*agentSpanState) error {
	rootCount := 0
	for spanID, state := range spans {
		if state.terminal == nil {
			return errors.New("complete agent trace contains an open span")
		}
		if state.start.ParentSpanID == "" {
			if state.start.Layer != AgentLayerRun || state.start.Name != AgentSpanEngineRun {
				return errors.New("agent trace root must be the engine run")
			}
			rootCount++
			continue
		}
		parent, ok := spans[state.start.ParentSpanID]
		if !ok || parent.terminal == nil {
			return errors.New("agent span parent is missing or open")
		}
		if !validAgentParentLayer(state.start.Layer, parent.start.Layer) {
			return errors.New("agent span parent layer is invalid")
		}
		if parent.start.Sequence >= state.start.Sequence || parent.terminal.Sequence <= state.terminal.Sequence {
			return fmt.Errorf("agent span %s is outside its parent lifecycle", spanID)
		}
	}
	if rootCount != 1 {
		return errors.New("complete agent trace requires exactly one root span")
	}
	return nil
}

func validAgentParentLayer(layer, parentLayer AgentLayer) bool {
	switch layer {
	case AgentLayerGraphNode:
		return parentLayer == AgentLayerRun
	case AgentLayerTool:
		return parentLayer == AgentLayerGraphNode
	default:
		return false
	}
}

func sameAgentRun(trace AgentTrace, event AgentEvent) bool {
	return event.SchemaVersion == trace.SchemaVersion &&
		event.EvaluationRunID == trace.EvaluationRunID &&
		event.TraceID == trace.TraceID &&
		event.RunID == trace.RunID &&
		event.CaseID == trace.CaseID &&
		event.VersionFingerprint == trace.VersionFingerprint
}

func validateAgentPhase(event AgentEvent) error {
	switch event.Phase {
	case AgentPhaseStarted:
		if event.DurationMilliseconds != 0 || event.FailureClass != "" || event.FailureCode != "" || event.OutputFingerprint != "" {
			return errors.New("started agent event contains terminal-only fields")
		}
	case AgentPhaseSucceeded, AgentPhaseSkipped:
		if event.FailureClass != "" || event.FailureCode != "" {
			return errors.New("non-failed agent event contains failure metadata")
		}
	case AgentPhaseFailed:
		if !validFailureClass(event.FailureClass) || !validAgentFailureCode(event) {
			return errors.New("failed agent event requires a valid failure class and code")
		}
	default:
		return errors.New("agent event phase is invalid")
	}
	return nil
}

func validAgentFailureCode(event AgentEvent) bool {
	switch event.FailureCode {
	case AgentFailureCodeContextCancelled:
		return event.FailureClass == FailureClassCancelled
	case AgentFailureCodeDeadlineExceeded:
		return event.FailureClass == FailureClassTimeout
	case AgentFailureCodeContractViolation:
		return event.FailureClass == FailureClassContractViolation
	case AgentFailureCodeEngineRunFailed:
		return event.Layer == AgentLayerRun && !specialFailureClass(event.FailureClass)
	case AgentFailureCodeGraphNodeFailed:
		return event.Layer == AgentLayerGraphNode && !specialFailureClass(event.FailureClass)
	case AgentFailureCodeToolFailed:
		return event.Layer == AgentLayerTool && !specialFailureClass(event.FailureClass)
	default:
		return false
	}
}

func specialFailureClass(class FailureClass) bool {
	return class == FailureClassCancelled || class == FailureClassTimeout || class == FailureClassContractViolation
}

func validateToolUsage(event AgentEvent) error {
	if event.Layer != AgentLayerTool {
		if event.ToolUsage != nil {
			return errors.New("non-tool agent event contains tool usage")
		}
		return nil
	}
	if event.Phase == AgentPhaseStarted {
		if event.ToolUsage != nil {
			return errors.New("started tool event contains terminal usage")
		}
		return nil
	}
	if event.ToolUsage == nil {
		return errors.New("terminal tool event requires usage")
	}
	if event.ToolUsage.LogicalCalls < 0 || event.ToolUsage.ProviderCalls < 0 || event.ToolUsage.ProcessedBytes < 0 {
		return errors.New("tool usage counters cannot be negative")
	}
	return nil
}

func validLayerAndName(layer AgentLayer, name AgentSpanName) bool {
	switch layer {
	case AgentLayerRun:
		return name == AgentSpanEngineRun
	case AgentLayerGraphNode:
		return name == AgentSpanPlanQueries || name == AgentSpanExecuteQueries || name == AgentSpanBuildReport || name == AgentSpanCorrelateChanges
	case AgentLayerTool:
		return name == AgentSpanSLSCurrent || name == AgentSpanSLSBaseline || name == AgentSpanChangeSourceList
	default:
		return false
	}
}

func validFailureClass(class FailureClass) bool {
	switch class {
	case FailureClassValidation, FailureClassPolicy, FailureClassDependency, FailureClassTimeout,
		FailureClassCancelled, FailureClassStateConflict, FailureClassExternalOutcomeUnknown,
		FailureClassContractViolation, FailureClassInternal:
		return true
	default:
		return false
	}
}

func validateAgentRunIdentity(evaluationRunID, traceID, runID, caseID, versionFingerprint string) error {
	if !validAgentIdentifier(evaluationRunID) || !validAgentIdentifier(traceID) || !validAgentIdentifier(runID) || !validAgentIdentifier(caseID) {
		return errors.New("agent run identity is invalid")
	}
	if !validSHA256(versionFingerprint) {
		return errors.New("agent version fingerprint is invalid")
	}
	return nil
}

func validatePromptManifest(manifest AgentVersionManifest) error {
	if !manifest.PromptUsed {
		if manifest.PromptVersion != "" || manifest.PromptFingerprint != "" || manifest.ModelProvider != "" || manifest.ModelName != "" {
			return errors.New("unused prompt cannot contain prompt or model metadata")
		}
		return nil
	}
	if err := validateAgentVersion("prompt version", manifest.PromptVersion); err != nil {
		return err
	}
	if !validSHA256(manifest.PromptFingerprint) {
		return errors.New("prompt fingerprint is invalid")
	}
	if err := validateAgentVersion("model provider", manifest.ModelProvider); err != nil {
		return err
	}
	return validateAgentVersion("model name", manifest.ModelName)
}

func validateAgentVersion(name, value string) error {
	if value == "" || len(value) > maxAgentVersionBytes || !agentVersionPattern.MatchString(value) {
		return fmt.Errorf("%s is missing or invalid", name)
	}
	if strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s is missing or invalid", name)
	}
	return nil
}

func validAgentIdentifier(value string) bool {
	return value != "" && len(value) <= maxAgentIdentifierBytes && agentIdentifierPattern.MatchString(value)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
