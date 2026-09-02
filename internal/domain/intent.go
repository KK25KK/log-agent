package domain

import "time"

const IntentPromptVersion = "investigation-intent-v2"

type IntentKind string

const (
	IntentErrorSpike  IntentKind = "error_spike"
	IntentTraceSearch IntentKind = "trace_search"
	IntentUnknown     IntentKind = "unknown"
)

type IntentResolutionStatus string

const (
	IntentResolutionParsing        IntentResolutionStatus = "PARSING"
	IntentResolutionResolved       IntentResolutionStatus = "RESOLVED"
	IntentResolutionUnknown        IntentResolutionStatus = "UNKNOWN"
	IntentResolutionIncomplete     IntentResolutionStatus = "INCOMPLETE"
	IntentResolutionRejected       IntentResolutionStatus = "REJECTED"
	IntentResolutionFallback       IntentResolutionStatus = "FALLBACK"
	IntentResolutionOutcomeUnknown IntentResolutionStatus = "OUTCOME_UNKNOWN"
)

// ProblemStatement is the bounded, redacted user description kept for audit
// and display. It remains an untrusted claim and is never copied into a query.
type ProblemStatement struct {
	Text        string `json:"text"`
	Fingerprint string `json:"fingerprint"`
	Redacted    bool   `json:"redacted"`
}

// InvestigationCapability is an administrator-owned logical capability. It
// deliberately contains no endpoint, project, Logstore, field, or query text.
type InvestigationCapability struct {
	Service     string     `json:"service"`
	Environment string     `json:"environment"`
	Intent      IntentKind `json:"intent"`
	TemplateID  string     `json:"template_id"`
}

type IntentProviderInput struct {
	Problem      string                    `json:"problem"`
	Capabilities []InvestigationCapability `json:"capabilities"`
}

type IntentDraft struct {
	Intent          IntentKind `json:"intent"`
	Service         string     `json:"service,omitempty"`
	Environment     string     `json:"environment,omitempty"`
	DurationSeconds int64      `json:"duration_seconds,omitempty"`
	TraceID         string     `json:"trace_id,omitempty"`
	Confidence      float64    `json:"confidence"`
}

type IntentProviderResult struct {
	Draft             IntentDraft `json:"draft"`
	Provider          string      `json:"provider"`
	Model             string      `json:"model"`
	RequestID         string      `json:"request_id,omitempty"`
	PromptVersion     string      `json:"prompt_version"`
	PromptFingerprint string      `json:"prompt_fingerprint"`
	InputTokens       int64       `json:"input_tokens"`
	OutputTokens      int64       `json:"output_tokens"`
	TotalTokens       int64       `json:"total_tokens"`
	LatencyMillis     int64       `json:"latency_millis"`
}

// IntentResolution is the durable confirmation preview. Physical resources
// and provider diagnostics are intentionally absent.
type IntentResolution struct {
	ID                 string                 `json:"id"`
	Principal          Principal              `json:"-"`
	SourceMessageID    string                 `json:"-"`
	Problem            ProblemStatement       `json:"problem"`
	Status             IntentResolutionStatus `json:"status"`
	Intent             IntentKind             `json:"intent"`
	Service            string                 `json:"service,omitempty"`
	Environment        string                 `json:"environment,omitempty"`
	DurationSeconds    int64                  `json:"duration_seconds,omitempty"`
	TraceID            string                 `json:"-"`
	TraceIDFingerprint string                 `json:"trace_id_fingerprint,omitempty"`
	TraceIDHint        string                 `json:"trace_id_hint,omitempty"`
	TemplateID         string                 `json:"template_id,omitempty"`
	Confidence         float64                `json:"confidence"`
	Provider           string                 `json:"provider"`
	Model              string                 `json:"model"`
	RequestID          string                 `json:"request_id,omitempty"`
	PromptVersion      string                 `json:"prompt_version"`
	PromptFingerprint  string                 `json:"prompt_fingerprint"`
	InputTokens        int64                  `json:"input_tokens"`
	OutputTokens       int64                  `json:"output_tokens"`
	TotalTokens        int64                  `json:"total_tokens"`
	LatencyMillis      int64                  `json:"latency_millis"`
	ReasonCode         string                 `json:"reason_code,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	ExpiresAt          time.Time              `json:"expires_at"`
	ConfirmedAt        time.Time              `json:"confirmed_at,omitempty"`
	InvestigationID    string                 `json:"investigation_id,omitempty"`
}
