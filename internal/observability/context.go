package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"logagent/internal/domain"
)

type runContextKey struct{}

// RunContext carries only synthetic execution identity. Business principals,
// physical resources, queries, and message content do not belong here.
type RunContext struct {
	EvaluationRunID    string
	TraceID            string
	RunID              string
	CaseID             string
	VersionFingerprint string
}

// Validate applies the same identity rules used by serialized Agent traces.
func (run RunContext) Validate() error {
	return domain.ValidateAgentTrace(domain.AgentTrace{
		SchemaVersion:      domain.AgentTraceSchemaVersion,
		EvaluationRunID:    run.EvaluationRunID,
		TraceID:            run.TraceID,
		RunID:              run.RunID,
		CaseID:             run.CaseID,
		VersionFingerprint: run.VersionFingerprint,
		Complete:           false,
		Events:             []domain.AgentEvent{},
	})
}

// WithRunContext returns a child context containing a value copy of run.
func WithRunContext(ctx context.Context, run RunContext) context.Context {
	return context.WithValue(ctx, runContextKey{}, run)
}

// RunContextFrom returns the typed run identity when one was attached.
func RunContextFrom(ctx context.Context) (RunContext, bool) {
	if ctx == nil {
		return RunContext{}, false
	}
	run, ok := ctx.Value(runContextKey{}).(RunContext)
	return run, ok
}

// StableSpanID derives a repeatable opaque ID without embedding run or span
// text in the serialized identifier.
func StableSpanID(traceID string, layer domain.AgentLayer, name domain.AgentSpanName) string {
	digest := sha256.Sum256([]byte(traceID + "\x00" + string(layer) + "\x00" + string(name)))
	return hex.EncodeToString(digest[:])
}
