package domain

import "time"

const (
	ErrorAnalysisTemplateID      = "error_analysis_v2"
	ErrorAnalysisTemplateVersion = "error-analysis-v2"

	// ErrorSummaryTemplateID remains as a source-compatible alias while callers
	// migrate to the M2 name. Both constants select the same fixed template; no
	// legacy SQL path remains reachable.
	ErrorSummaryTemplateID = ErrorAnalysisTemplateID

	ErrorAnalysisPatternLimit  = 5
	ErrorAnalysisInstanceLimit = 5
	// Count is read before and after both Top-K queries. Matching boundary
	// counts are the M2 consistency gate for SLS, which has no snapshot token
	// spanning multiple GetLogsV2 calls.
	ErrorAnalysisAPICalls   = 4
	ErrorAnalysisResultRows = 2 + ErrorAnalysisPatternLimit + ErrorAnalysisInstanceLimit

	// The real-time SLS index is typically queryable after about three seconds.
	// M2 uses a larger configurable watermark and fails closed below this floor.
	MinimumIngestionGrace = 3 * time.Second
	DefaultIngestionGrace = 10 * time.Second
)

type LogSelector struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// LogResource is an administrator-owned mapping. It is never built from user text.
type LogResource struct {
	ID              string        `json:"id"`
	CatalogVersion  string        `json:"catalog_version"`
	Service         string        `json:"service"`
	Environment     string        `json:"environment"`
	Endpoint        string        `json:"endpoint"`
	Project         string        `json:"project"`
	LogStore        string        `json:"logstore"`
	TemplateVersion string        `json:"template_version"`
	Selectors       []LogSelector `json:"selectors"`
	ErrorSelector   LogSelector   `json:"error_selector"`
	ErrorField      string        `json:"error_field"`
	InstanceField   string        `json:"instance_field"`
}

type IndexField struct {
	Type     string `json:"type"`
	DocValue bool   `json:"doc_value"`
}

type IndexSchema struct {
	Fingerprint string                `json:"fingerprint"`
	Fields      map[string]IndexField `json:"fields"`
	FetchedAt   time.Time             `json:"fetched_at"`
}

// ApprovedQuery can only be produced after catalog, ACL, budget, and Schema checks.
type ApprovedQuery struct {
	SpecHash          string      `json:"spec_hash"`
	Resource          LogResource `json:"resource"`
	TemplateID        string      `json:"template_id"`
	PolicyVersion     string      `json:"policy_version"`
	SchemaFingerprint string      `json:"schema_fingerprint"`
	StartTime         time.Time   `json:"start_time"`
	EndTime           time.Time   `json:"end_time"`
	MaxRows           int64       `json:"max_rows"`
	MaxAPICalls       int         `json:"max_api_calls"`
	PatternLimit      int         `json:"pattern_limit"`
	InstanceLimit     int         `json:"instance_limit"`
	ExpectedAPICalls  int         `json:"expected_api_calls"`
}

type QueryAudit struct {
	InvestigationID   string    `json:"investigation_id"`
	Principal         Principal `json:"principal"`
	ResourceID        string    `json:"resource_id,omitempty"`
	TemplateID        string    `json:"template_id"`
	TemplateVersion   string    `json:"template_version,omitempty"`
	QuerySpecHash     string    `json:"query_spec_hash,omitempty"`
	SchemaFingerprint string    `json:"schema_fingerprint,omitempty"`
	PolicyVersion     string    `json:"policy_version"`
	Outcome           string    `json:"outcome"`
	Reason            string    `json:"reason,omitempty"`
	ProviderRequestID string    `json:"provider_request_id,omitempty"`
	Progress          string    `json:"progress,omitempty"`
	Complete          bool      `json:"complete"`
	Truncated         bool      `json:"truncated"`
	ProcessedRows     int64     `json:"processed_rows"`
	ProcessedBytes    int64     `json:"processed_bytes"`
	OccurredAt        time.Time `json:"occurred_at"`
}
