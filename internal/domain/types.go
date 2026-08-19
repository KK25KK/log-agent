package domain

import "time"

// Status is the durable lifecycle of an investigation and its job.
type Status string

const (
	StatusQueued    Status = "QUEUED"
	StatusRunning   Status = "RUNNING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusCancelled Status = "CANCELLED"
)

// InboundMessage is the framework-independent envelope produced by an entry adapter.
type InboundMessage struct {
	AppID            string `json:"app_id"`
	TenantKey        string `json:"tenant_key"`
	MessageID        string `json:"message_id"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	// ExpectedInvestigationID is set only for a card-derived request. SQLite
	// uses it as a compare-and-swap guard so two stale buttons cannot both move
	// the same card to different investigations.
	ExpectedInvestigationID string    `json:"expected_investigation_id,omitempty"`
	ChatID                  string    `json:"chat_id"`
	UserID                  string    `json:"user_id"`
	Text                    string    `json:"text"`
	ReceivedAt              time.Time `json:"received_at"`
}

// Principal is the trusted caller identity derived from an inbound adapter.
type Principal struct {
	AppID     string `json:"app_id"`
	TenantKey string `json:"tenant_key"`
	UserID    string `json:"user_id"`
}

func (p Principal) Complete() bool {
	return p.AppID != "" && p.TenantKey != "" && p.UserID != ""
}

func (p Principal) Key() string {
	return p.AppID + "/" + p.TenantKey + "/" + p.UserID
}

// InvestigationRequest is the normalized scope accepted by the application.
type InvestigationRequest struct {
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Requester   Principal `json:"requester"`
}

// QuerySpec is the auditable, typed request sent to an SLS executor.
type QuerySpec struct {
	InvestigationID string    `json:"investigation_id"`
	Name            string    `json:"name"`
	TemplateID      string    `json:"template_id"`
	Service         string    `json:"service"`
	Environment     string    `json:"environment"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	Requester       Principal `json:"requester"`
}

// CountBucket is one bounded aggregate row. Labels are untrusted provider
// output and are redacted by the query gateway before they become evidence.
type CountBucket struct {
	Label    string `json:"label"`
	Count    int64  `json:"count"`
	Redacted bool   `json:"redacted"`
}

// QueryResult is the provider-neutral observation returned by an SLS executor.
type QueryResult struct {
	QueryID                 string        `json:"query_id"`
	QuerySpecHash           string        `json:"query_spec_hash"`
	ResourceID              string        `json:"resource_id"`
	TemplateID              string        `json:"template_id"`
	TemplateVersion         string        `json:"template_version"`
	SchemaFingerprint       string        `json:"schema_fingerprint"`
	PolicyVersion           string        `json:"policy_version"`
	Progress                string        `json:"progress"`
	Complete                bool          `json:"complete"`
	Truncated               bool          `json:"truncated"`
	NanosecondOrderedKnown  bool          `json:"nanosecond_ordered_known"`
	NanosecondOrdered       bool          `json:"nanosecond_ordered"`
	UsageKnown              bool          `json:"usage_known"`
	IncompleteReason        string        `json:"incomplete_reason,omitempty"`
	ProcessedRows           int64         `json:"processed_rows"`
	ProcessedBytes          int64         `json:"processed_bytes"`
	ElapsedMillisecond      int64         `json:"elapsed_millisecond"`
	APICalls                int           `json:"api_calls"`
	Redacted                bool          `json:"redacted"`
	ErrorCount              int64         `json:"error_count"`
	TopError                string        `json:"top_error,omitempty"`
	TopErrorCount           int64         `json:"top_error_count,omitempty"`
	ErrorPatterns           []CountBucket `json:"error_patterns,omitempty"`
	Instances               []CountBucket `json:"instances,omitempty"`
	ErrorPatternsExhaustive bool          `json:"error_patterns_exhaustive"`
	InstancesExhaustive     bool          `json:"instances_exhaustive"`
	PatternLimit            int           `json:"pattern_limit"`
	InstanceLimit           int           `json:"instance_limit"`
}

// Evidence preserves what was queried and whether the result was safe to use.
type Evidence struct {
	ID                      string        `json:"id"`
	QueryID                 string        `json:"query_id"`
	QuerySpecHash           string        `json:"query_spec_hash"`
	ResourceID              string        `json:"resource_id"`
	TemplateID              string        `json:"template_id"`
	TemplateVersion         string        `json:"template_version"`
	SchemaFingerprint       string        `json:"schema_fingerprint"`
	PolicyVersion           string        `json:"policy_version"`
	Name                    string        `json:"name"`
	StartTime               time.Time     `json:"start_time"`
	EndTime                 time.Time     `json:"end_time"`
	Progress                string        `json:"progress"`
	Complete                bool          `json:"complete"`
	Truncated               bool          `json:"truncated"`
	NanosecondOrderedKnown  bool          `json:"nanosecond_ordered_known"`
	NanosecondOrdered       bool          `json:"nanosecond_ordered"`
	UsageKnown              bool          `json:"usage_known"`
	IncompleteReason        string        `json:"incomplete_reason,omitempty"`
	ProcessedRows           int64         `json:"processed_rows"`
	ProcessedBytes          int64         `json:"processed_bytes"`
	ElapsedMillisecond      int64         `json:"elapsed_millisecond"`
	APICalls                int           `json:"api_calls"`
	Redacted                bool          `json:"redacted"`
	ErrorCount              int64         `json:"error_count"`
	TopError                string        `json:"top_error,omitempty"`
	TopErrorCount           int64         `json:"top_error_count,omitempty"`
	ErrorPatterns           []CountBucket `json:"error_patterns,omitempty"`
	Instances               []CountBucket `json:"instances,omitempty"`
	ErrorPatternsExhaustive bool          `json:"error_patterns_exhaustive"`
	InstancesExhaustive     bool          `json:"instances_exhaustive"`
	PatternLimit            int           `json:"pattern_limit"`
	InstanceLimit           int           `json:"instance_limit"`
}

// Finding is a conclusion with explicit evidence references.
type Finding struct {
	Code        string   `json:"code"`
	Statement   string   `json:"statement"`
	Confidence  float64  `json:"confidence"`
	Conclusive  bool     `json:"conclusive"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// Recommendation is a deterministic next step justified by report evidence.
// It is not an automatic remediation action.
type Recommendation struct {
	Code        string   `json:"code"`
	Statement   string   `json:"statement"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// Report is the durable user-facing result of an investigation.
type Report struct {
	InvestigationID string           `json:"investigation_id"`
	Outcome         string           `json:"outcome"`
	Findings        []Finding        `json:"findings"`
	Recommendations []Recommendation `json:"recommendations,omitempty"`
	Evidence        []Evidence       `json:"evidence"`
	CauseAnalysis   *CauseAnalysis   `json:"cause_analysis,omitempty"`
	GeneratedAt     time.Time        `json:"generated_at"`
}

// Job is a claimed unit of work. Request is already decoded for the worker.
type Job struct {
	ID              string               `json:"id"`
	InvestigationID string               `json:"investigation_id"`
	Request         InvestigationRequest `json:"request"`
	Attempt         int                  `json:"attempt"`
	LeaseOwner      string               `json:"lease_owner"`
	LeaseUntil      time.Time            `json:"lease_until"`
}

// Investigation is the persisted state returned to callers.
type Investigation struct {
	ID        string               `json:"id"`
	Status    Status               `json:"status"`
	Request   InvestigationRequest `json:"request"`
	Report    *Report              `json:"report,omitempty"`
	LastError string               `json:"last_error,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
}
