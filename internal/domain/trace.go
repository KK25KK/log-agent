package domain

import "time"

const (
	TraceSearchTemplateID      = "trace_search_v1"
	TraceSearchTemplateVersion = "trace-search-v1"
	TracePolicyVersion         = "trace-policy-v1"

	TraceDefaultMemberLimit = 50
	TraceDefaultGlobalLimit = 500
	TraceDefaultConcurrency = 2
	TraceMaximumMembers     = 16
)

type TraceQueryMode string

const (
	TraceQueryField    TraceQueryMode = "FIELD"
	TraceQueryFullText TraceQueryMode = "FULLTEXT"
)

type TraceEnvironmentMode string

const (
	TraceEnvironmentField    TraceEnvironmentMode = "FIELD"
	TraceEnvironmentFullText TraceEnvironmentMode = "FULLTEXT"
)

// TraceResourceGroup is administrator-owned configuration. Physical members
// never come from user text and are never copied into user-facing reports.
type TraceResourceGroup struct {
	ID              string                `json:"id"`
	CatalogVersion  string                `json:"catalog_version"`
	Service         string                `json:"service"`
	Environment     string                `json:"environment"`
	TemplateVersion string                `json:"template_version"`
	PrimaryMemberID string                `json:"primary_member_id"`
	Members         []TraceResourceMember `json:"members"`
}

type TraceResourceMember struct {
	ID                  string               `json:"id"`
	Endpoint            string               `json:"endpoint"`
	Project             string               `json:"project"`
	LogStore            string               `json:"logstore"`
	TraceMode           TraceQueryMode       `json:"trace_mode"`
	TraceField          string               `json:"trace_field,omitempty"`
	EnvironmentMode     TraceEnvironmentMode `json:"environment_mode"`
	EnvironmentField    string               `json:"environment_field,omitempty"`
	MessageField        string               `json:"message_field"`
	LevelField          string               `json:"level_field,omitempty"`
	OperationField      string               `json:"operation_field,omitempty"`
	EventTimeField      string               `json:"event_time_field"`
	ReceiveTimeField    string               `json:"receive_time_field,omitempty"`
	NanosecondTimeField string               `json:"nanosecond_time_field,omitempty"`
}

type TraceSearchSpec struct {
	InvestigationID string    `json:"investigation_id"`
	Service         string    `json:"service"`
	Environment     string    `json:"environment"`
	TraceID         string    `json:"-"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	Requester       Principal `json:"requester"`
}

type TracePlan struct {
	Spec                  TraceSearchSpec    `json:"spec"`
	Group                 TraceResourceGroup `json:"group"`
	GovernanceFingerprint string             `json:"governance_fingerprint"`
	TraceIDFingerprint    string             `json:"trace_id_fingerprint"`
	MemberLimit           int                `json:"member_limit"`
	GlobalLimit           int                `json:"global_limit"`
	MaxConcurrency        int                `json:"max_concurrency"`
	MaxProcessedBytes     int64              `json:"max_processed_bytes"`
	RetryIncomplete       int                `json:"retry_incomplete"`
}

type ApprovedTraceQuery struct {
	Spec                  TraceSearchSpec     `json:"spec"`
	GroupID               string              `json:"group_id"`
	Member                TraceResourceMember `json:"member"`
	GovernanceFingerprint string              `json:"governance_fingerprint"`
	TraceIDFingerprint    string              `json:"trace_id_fingerprint"`
	MemberLimit           int                 `json:"member_limit"`
	RetryIncomplete       int                 `json:"retry_incomplete"`
}

// TraceBackendEvent is a narrow provider projection. It contains no arbitrary
// map of log fields and is redacted before it can be persisted.
type TraceBackendEvent struct {
	EventTimeRaw   string `json:"event_time_raw"`
	ReceiveTimeRaw string `json:"receive_time_raw,omitempty"`
	NanosecondRaw  string `json:"nanosecond_raw,omitempty"`
	Level          string `json:"level,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Message        string `json:"message"`
}

type TraceBackendResult struct {
	ExecutionID          string              `json:"execution_id"`
	ProviderRequestID    string              `json:"provider_request_id,omitempty"`
	Progress             string              `json:"progress"`
	ProcessedRows        int64               `json:"processed_rows"`
	ProcessedBytes       int64               `json:"processed_bytes"`
	ElapsedMillisecond   int64               `json:"elapsed_millisecond"`
	UsageKnown           bool                `json:"usage_known"`
	NanosecondOrderKnown bool                `json:"nanosecond_order_known"`
	NanosecondOrdered    bool                `json:"nanosecond_ordered"`
	APICalls             int                 `json:"api_calls"`
	Events               []TraceBackendEvent `json:"events"`
}

type TraceMemberStatus string

const (
	TraceMemberComplete       TraceMemberStatus = "COMPLETE"
	TraceMemberZeroHit        TraceMemberStatus = "ZERO_HIT"
	TraceMemberIncomplete     TraceMemberStatus = "INCOMPLETE"
	TraceMemberTruncated      TraceMemberStatus = "TRUNCATED"
	TraceMemberInvalidSchema  TraceMemberStatus = "INVALID_SCHEMA"
	TraceMemberFailed         TraceMemberStatus = "FAILED"
	TraceMemberOutcomeUnknown TraceMemberStatus = "OUTCOME_UNKNOWN"
)

type TraceSortQuality string

const (
	TraceSortNanosecond TraceSortQuality = "NANOSECOND"
	TraceSortSecond     TraceSortQuality = "SECOND"
	TraceSortUnknown    TraceSortQuality = "UNKNOWN"
)

type TraceEvent struct {
	ID                 string           `json:"id"`
	MemberID           string           `json:"member_id"`
	EventTime          time.Time        `json:"event_time,omitempty"`
	ReceiveTime        time.Time        `json:"receive_time,omitempty"`
	SortQuality        TraceSortQuality `json:"sort_quality"`
	Level              string           `json:"level,omitempty"`
	Operation          string           `json:"operation,omitempty"`
	Message            string           `json:"message"`
	MessageFingerprint string           `json:"message_fingerprint"`
	Redacted           bool             `json:"redacted"`
	Anchors            []RuntimeAnchor  `json:"anchors,omitempty"`
}

type TraceMemberResult struct {
	QueryID                string            `json:"query_id"`
	QuerySpecHash          string            `json:"query_spec_hash"`
	GroupID                string            `json:"group_id"`
	MemberID               string            `json:"member_id"`
	TemplateID             string            `json:"template_id"`
	TemplateVersion        string            `json:"template_version"`
	PolicyVersion          string            `json:"policy_version"`
	SchemaFingerprint      string            `json:"schema_fingerprint,omitempty"`
	GovernanceFingerprint  string            `json:"governance_fingerprint"`
	TraceIDFingerprint     string            `json:"trace_id_fingerprint"`
	StartTime              time.Time         `json:"start_time"`
	EndTime                time.Time         `json:"end_time"`
	Status                 TraceMemberStatus `json:"status"`
	Complete               bool              `json:"complete"`
	Truncated              bool              `json:"truncated"`
	ZeroHit                bool              `json:"zero_hit"`
	Progress               string            `json:"progress"`
	IncompleteReason       string            `json:"incomplete_reason,omitempty"`
	ProcessedRows          int64             `json:"processed_rows"`
	ProcessedBytes         int64             `json:"processed_bytes"`
	ElapsedMillisecond     int64             `json:"elapsed_millisecond"`
	UsageKnown             bool              `json:"usage_known"`
	NanosecondOrderedKnown bool              `json:"nanosecond_ordered_known"`
	NanosecondOrdered      bool              `json:"nanosecond_ordered"`
	APICalls               int               `json:"api_calls"`
	Events                 []TraceEvent      `json:"events"`
}

type TraceInvestigationStatus string

const (
	TraceInvestigationComplete TraceInvestigationStatus = "COMPLETE"
	TraceInvestigationZeroHit  TraceInvestigationStatus = "ZERO_HIT"
	TraceInvestigationPartial  TraceInvestigationStatus = "PARTIAL"
)

type TraceInvestigation struct {
	GroupID               string                   `json:"group_id"`
	TemplateID            string                   `json:"template_id"`
	TemplateVersion       string                   `json:"template_version"`
	PolicyVersion         string                   `json:"policy_version"`
	GovernanceFingerprint string                   `json:"governance_fingerprint"`
	TraceIDFingerprint    string                   `json:"trace_id_fingerprint"`
	Status                TraceInvestigationStatus `json:"status"`
	Complete              bool                     `json:"complete"`
	StartTime             time.Time                `json:"start_time"`
	EndTime               time.Time                `json:"end_time"`
	Members               []TraceMemberSummary     `json:"members"`
	Events                []TraceEvent             `json:"events"`
	AnchorSet             *RuntimeAnchorSet        `json:"anchor_set,omitempty"`
	TotalAPICalls         int                      `json:"total_api_calls"`
	TotalProcessedRows    int64                    `json:"total_processed_rows"`
	TotalProcessedBytes   int64                    `json:"total_processed_bytes"`
}

type TraceMemberSummary struct {
	MemberID         string            `json:"member_id"`
	EvidenceID       string            `json:"evidence_id"`
	Status           TraceMemberStatus `json:"status"`
	EventCount       int               `json:"event_count"`
	APICalls         int               `json:"api_calls"`
	ProcessedRows    int64             `json:"processed_rows"`
	ProcessedBytes   int64             `json:"processed_bytes"`
	IncompleteReason string            `json:"incomplete_reason,omitempty"`
}

type TraceAudit struct {
	InvestigationID       string    `json:"investigation_id"`
	Principal             Principal `json:"principal"`
	GroupID               string    `json:"group_id,omitempty"`
	MemberID              string    `json:"member_id,omitempty"`
	TemplateID            string    `json:"template_id"`
	TraceIDFingerprint    string    `json:"trace_id_fingerprint,omitempty"`
	GovernanceFingerprint string    `json:"governance_fingerprint,omitempty"`
	QuerySpecHash         string    `json:"query_spec_hash,omitempty"`
	SchemaFingerprint     string    `json:"schema_fingerprint,omitempty"`
	Outcome               string    `json:"outcome"`
	ReasonCode            string    `json:"reason_code,omitempty"`
	ProviderRequestID     string    `json:"provider_request_id,omitempty"`
	Progress              string    `json:"progress,omitempty"`
	ReturnedEvents        int       `json:"returned_events"`
	APICalls              int       `json:"api_calls"`
	ProcessedRows         int64     `json:"processed_rows"`
	ProcessedBytes        int64     `json:"processed_bytes"`
	ElapsedMillisecond    int64     `json:"elapsed_millisecond"`
	OccurredAt            time.Time `json:"occurred_at"`
}

type TraceQueryStepDecision struct {
	Action QueryStepAction    `json:"action"`
	Result *TraceMemberResult `json:"result,omitempty"`
}
