package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"sync"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const MaxRecorderEvents = 1024

// NoopObserver is the safe application default when trace collection is not
// requested.
type NoopObserver struct{}

func (NoopObserver) Record(domain.AgentEvent) {}

// BoundedRecorder retains a finite, concurrency-safe Agent Trace. Record has
// no error result by design: bad telemetry degrades Trace completeness, never
// the business run.
type BoundedRecorder struct {
	mu        sync.RWMutex
	capacity  int
	run       RunContext
	ready     bool
	events    []domain.AgentEvent
	dropCount uint64
}

var (
	_ ports.AgentObserver = NoopObserver{}
	_ ports.AgentObserver = (*BoundedRecorder)(nil)
)

// NewBoundedRecorder constructs a recorder without allocating outside the
// hard event bound. Invalid configuration produces an incomplete recorder.
func NewBoundedRecorder(capacity int, run RunContext) *BoundedRecorder {
	recorder := &BoundedRecorder{run: run}
	if capacity <= 0 || capacity > MaxRecorderEvents || run.Validate() != nil {
		return recorder
	}
	recorder.capacity = capacity
	recorder.ready = true
	recorder.events = make([]domain.AgentEvent, 0, capacity)
	return recorder
}

// Record assigns ordering and identity, then retains only contract-valid
// events that preserve the current span lifecycle.
func (recorder *BoundedRecorder) Record(event domain.AgentEvent) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if !recorder.ready || len(recorder.events) >= recorder.capacity {
		recorder.recordDrop()
		return
	}
	event = recorder.prepareEvent(event)
	if domain.ValidateAgentEvent(event) != nil || !recorder.matchesRun(event) || !recorder.canAppend(event) {
		recorder.recordDrop()
		return
	}
	recorder.events = append(recorder.events, cloneEvent(event))
}

// Snapshot returns an isolated copy. Complete is true only when no event was
// dropped and the retained span hierarchy is fully closed.
func (recorder *BoundedRecorder) Snapshot() domain.AgentTrace {
	if recorder == nil {
		return domain.AgentTrace{SchemaVersion: domain.AgentTraceSchemaVersion, Complete: false, Events: []domain.AgentEvent{}}
	}
	recorder.mu.RLock()
	trace := domain.AgentTrace{
		SchemaVersion:      domain.AgentTraceSchemaVersion,
		EvaluationRunID:    recorder.run.EvaluationRunID,
		TraceID:            recorder.run.TraceID,
		RunID:              recorder.run.RunID,
		CaseID:             recorder.run.CaseID,
		VersionFingerprint: recorder.run.VersionFingerprint,
		Complete:           recorder.ready && recorder.dropCount == 0,
		DropCount:          recorder.dropCount,
		Events:             cloneEvents(recorder.events),
	}
	recorder.mu.RUnlock()

	if trace.Complete && domain.ValidateAgentTrace(trace) != nil {
		trace.Complete = false
	}
	return trace
}

func (recorder *BoundedRecorder) prepareEvent(event domain.AgentEvent) domain.AgentEvent {
	if event.SchemaVersion == "" {
		event.SchemaVersion = domain.AgentTraceSchemaVersion
	}
	fillIfEmpty(&event.EvaluationRunID, recorder.run.EvaluationRunID)
	fillIfEmpty(&event.TraceID, recorder.run.TraceID)
	fillIfEmpty(&event.RunID, recorder.run.RunID)
	fillIfEmpty(&event.CaseID, recorder.run.CaseID)
	fillIfEmpty(&event.VersionFingerprint, recorder.run.VersionFingerprint)
	event.Sequence = uint64(len(recorder.events) + 1)
	event.EventID = stableEventID(event.TraceID, event.Sequence)
	return event
}

func (recorder *BoundedRecorder) matchesRun(event domain.AgentEvent) bool {
	return event.EvaluationRunID == recorder.run.EvaluationRunID &&
		event.TraceID == recorder.run.TraceID &&
		event.RunID == recorder.run.RunID &&
		event.CaseID == recorder.run.CaseID &&
		event.VersionFingerprint == recorder.run.VersionFingerprint
}

func (recorder *BoundedRecorder) canAppend(event domain.AgentEvent) bool {
	start, terminal, exists := recorder.spanState(event.SpanID)
	if event.Phase == domain.AgentPhaseStarted {
		return !exists && recorder.validParentForStart(event)
	}
	if !exists || terminal != nil || !matchingSpan(start, event) || event.OccurredAt.Before(start.OccurredAt) {
		return false
	}
	return !recorder.hasOpenChild(event.SpanID)
}

func (recorder *BoundedRecorder) validParentForStart(event domain.AgentEvent) bool {
	if event.ParentSpanID == "" {
		if event.Layer != domain.AgentLayerRun || event.Name != domain.AgentSpanEngineRun {
			return false
		}
		return !recorder.hasRoot()
	}
	parent, terminal, exists := recorder.spanState(event.ParentSpanID)
	return exists && terminal == nil && validParentLayer(event.Layer, parent.Layer)
}

func (recorder *BoundedRecorder) spanState(spanID string) (domain.AgentEvent, *domain.AgentEvent, bool) {
	var start domain.AgentEvent
	var terminal *domain.AgentEvent
	found := false
	for index := range recorder.events {
		event := recorder.events[index]
		if event.SpanID != spanID {
			continue
		}
		if event.Phase == domain.AgentPhaseStarted {
			start = event
			found = true
			continue
		}
		copy := event
		terminal = &copy
	}
	return start, terminal, found
}

func (recorder *BoundedRecorder) hasRoot() bool {
	for _, event := range recorder.events {
		if event.Phase == domain.AgentPhaseStarted && event.ParentSpanID == "" {
			return true
		}
	}
	return false
}

func (recorder *BoundedRecorder) hasOpenChild(parentSpanID string) bool {
	for _, event := range recorder.events {
		if event.Phase != domain.AgentPhaseStarted || event.ParentSpanID != parentSpanID {
			continue
		}
		_, terminal, _ := recorder.spanState(event.SpanID)
		if terminal == nil {
			return true
		}
	}
	return false
}

func (recorder *BoundedRecorder) recordDrop() {
	if recorder.dropCount < math.MaxUint64 {
		recorder.dropCount++
	}
}

func matchingSpan(start, terminal domain.AgentEvent) bool {
	return start.Layer == terminal.Layer && start.Name == terminal.Name && start.ParentSpanID == terminal.ParentSpanID
}

func validParentLayer(layer, parentLayer domain.AgentLayer) bool {
	switch layer {
	case domain.AgentLayerGraphNode:
		return parentLayer == domain.AgentLayerRun
	case domain.AgentLayerTool:
		return parentLayer == domain.AgentLayerGraphNode
	default:
		return false
	}
}

func stableEventID(traceID string, sequence uint64) string {
	digest := sha256.Sum256([]byte(traceID + "\x00" + strconv.FormatUint(sequence, 10)))
	return hex.EncodeToString(digest[:])
}

func fillIfEmpty(target *string, value string) {
	if *target == "" {
		*target = value
	}
}

func cloneEvents(events []domain.AgentEvent) []domain.AgentEvent {
	cloned := make([]domain.AgentEvent, len(events))
	for index := range events {
		cloned[index] = cloneEvent(events[index])
	}
	return cloned
}

func cloneEvent(event domain.AgentEvent) domain.AgentEvent {
	if event.ToolUsage != nil {
		usage := *event.ToolUsage
		event.ToolUsage = &usage
	}
	return event
}
