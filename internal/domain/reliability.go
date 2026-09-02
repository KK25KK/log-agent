package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// FailureDisposition is the closed application decision for an external
// operation. It is deliberately separate from provider-specific error types.
type FailureDisposition string

const (
	FailureRetryable      FailureDisposition = "RETRYABLE"
	FailurePermanent      FailureDisposition = "PERMANENT"
	FailureOutcomeUnknown FailureDisposition = "OUTCOME_UNKNOWN"
	FailureCancelled      FailureDisposition = "CANCELLED"
)

// DeliveryFailure contains only stable, operator-safe metadata.
type DeliveryFailure struct {
	Disposition FailureDisposition `json:"disposition"`
	ReasonCode  string             `json:"reason_code"`
}

type DeliveryAttemptOutcome string

const (
	DeliveryAttemptSent  DeliveryAttemptOutcome = "SENT"
	DeliveryAttemptRetry DeliveryAttemptOutcome = "RETRY_SCHEDULED"
	DeliveryAttemptDead  DeliveryAttemptOutcome = "DEAD"
)

type DeliveryAttempt struct {
	DeliveryID      string                 `json:"delivery_id"`
	InvestigationID string                 `json:"investigation_id"`
	Attempt         int                    `json:"attempt"`
	Outcome         DeliveryAttemptOutcome `json:"outcome"`
	Failure         *DeliveryFailure       `json:"failure,omitempty"`
	OccurredAt      time.Time              `json:"occurred_at"`
}

// DeadDelivery is the bounded operator projection of one dead card event.
// It intentionally excludes card content and provider error text.
type DeadDelivery struct {
	ID              string       `json:"id"`
	InvestigationID string       `json:"investigation_id"`
	Kind            DeliveryKind `json:"kind"`
	Attempt         int          `json:"attempt"`
	ReasonCode      string       `json:"reason_code"`
	Replayable      bool         `json:"replayable"`
	ReplayReason    string       `json:"replay_reason"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type DeliveryReplayResult struct {
	DeliveryID string    `json:"delivery_id"`
	Replayed   bool      `json:"replayed"`
	ReasonCode string    `json:"reason_code"`
	OccurredAt time.Time `json:"occurred_at"`
}

type QuotaReservationStatus string

const (
	QuotaReserved QuotaReservationStatus = "RESERVED"
	QuotaSettled  QuotaReservationStatus = "SETTLED"
	QuotaReleased QuotaReservationStatus = "RELEASED"
	QuotaUnknown  QuotaReservationStatus = "UNKNOWN"
)

// TenantQuotaPolicy is a local fixed-window cost-proxy policy. Processed
// bytes are not an Alibaba Cloud bill.
type TenantQuotaPolicy struct {
	Version                     string        `json:"version"`
	Window                      time.Duration `json:"window"`
	MaxObservations             int64         `json:"max_observations"`
	MaxAPICalls                 int64         `json:"max_api_calls"`
	MaxProcessedBytes           int64         `json:"max_processed_bytes"`
	ReservedBytesPerObservation int64         `json:"reserved_bytes_per_observation"`
}

type QueryQuotaReservation struct {
	UsageKey         string                 `json:"usage_key"`
	TenantID         string                 `json:"tenant_id"`
	InvestigationID  string                 `json:"investigation_id"`
	QueryName        string                 `json:"query_name"`
	WindowStart      time.Time              `json:"window_start"`
	WindowEnd        time.Time              `json:"window_end"`
	ReservedAPICalls int64                  `json:"reserved_api_calls"`
	ReservedBytes    int64                  `json:"reserved_bytes"`
	Status           QuotaReservationStatus `json:"status"`
	ActualAPICalls   int64                  `json:"actual_api_calls"`
	ActualBytes      int64                  `json:"actual_bytes"`
	ReasonCode       string                 `json:"reason_code,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

type TenantQuotaUsage struct {
	TenantID       string    `json:"tenant_id"`
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`
	Observations   int64     `json:"observations"`
	APICalls       int64     `json:"api_calls"`
	ProcessedBytes int64     `json:"processed_bytes"`
	CircuitOpen    bool      `json:"circuit_open"`
}

// SummaryQuotaPolicy is a local fixed-window request and Token policy for the
// evidence-only summary boundary. Tokens are provider-reported usage, not a
// currency bill.
type SummaryQuotaPolicy struct {
	Version                  string        `json:"version"`
	Window                   time.Duration `json:"window"`
	MaxRequests              int64         `json:"max_requests"`
	MaxTokens                int64         `json:"max_tokens"`
	ReservedTokensPerRequest int64         `json:"reserved_tokens_per_request"`
}

type SummaryQuotaReservation struct {
	UsageKey           string                 `json:"usage_key"`
	TenantID           string                 `json:"tenant_id"`
	InvestigationID    string                 `json:"investigation_id"`
	PromptVersion      string                 `json:"prompt_version"`
	WindowStart        time.Time              `json:"window_start"`
	WindowEnd          time.Time              `json:"window_end"`
	ReservedTokens     int64                  `json:"reserved_tokens"`
	Status             QuotaReservationStatus `json:"status"`
	ActualInputTokens  int64                  `json:"actual_input_tokens"`
	ActualOutputTokens int64                  `json:"actual_output_tokens"`
	ActualTotalTokens  int64                  `json:"actual_total_tokens"`
	ReasonCode         string                 `json:"reason_code,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type TenantSummaryQuotaUsage struct {
	TenantID    string    `json:"tenant_id"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Requests    int64     `json:"requests"`
	Tokens      int64     `json:"tokens"`
	CircuitOpen bool      `json:"circuit_open"`
}

// IntentQuotaPolicy is independent from report-summary quota so natural
// language parsing cannot consume or hide the budget reserved for reports.
type IntentQuotaPolicy struct {
	Version                  string        `json:"version"`
	Window                   time.Duration `json:"window"`
	MaxRequests              int64         `json:"max_requests"`
	MaxTokens                int64         `json:"max_tokens"`
	ReservedTokensPerRequest int64         `json:"reserved_tokens_per_request"`
}

type IntentQuotaReservation struct {
	UsageKey           string                 `json:"usage_key"`
	TenantID           string                 `json:"tenant_id"`
	ResolutionID       string                 `json:"resolution_id"`
	PromptVersion      string                 `json:"prompt_version"`
	WindowStart        time.Time              `json:"window_start"`
	WindowEnd          time.Time              `json:"window_end"`
	ReservedTokens     int64                  `json:"reserved_tokens"`
	Status             QuotaReservationStatus `json:"status"`
	ActualInputTokens  int64                  `json:"actual_input_tokens"`
	ActualOutputTokens int64                  `json:"actual_output_tokens"`
	ActualTotalTokens  int64                  `json:"actual_total_tokens"`
	ReasonCode         string                 `json:"reason_code,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type TenantIntentQuotaUsage struct {
	TenantID    string    `json:"tenant_id"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Requests    int64     `json:"requests"`
	Tokens      int64     `json:"tokens"`
	CircuitOpen bool      `json:"circuit_open"`
}

func TrustedTenantID(principal Principal) string {
	digest := sha256.Sum256([]byte(principal.AppID + "\x00" + principal.TenantKey))
	return hex.EncodeToString(digest[:])
}

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "PENDING"
	ApprovalApproved ApprovalStatus = "APPROVED"
	ApprovalRejected ApprovalStatus = "REJECTED"
	ApprovalExpired  ApprovalStatus = "EXPIRED"
	ApprovalConsumed ApprovalStatus = "CONSUMED"
)

type HighRiskAction string

const (
	HighRiskReadRawSample HighRiskAction = "READ_RAW_LOG_SAMPLE"
	HighRiskRemediation   HighRiskAction = "EXECUTE_REMEDIATION"
)

// ApprovalRequest carries an immutable payload hash only. No tool parameters,
// credentials or raw logs are persisted in the approval ledger.
type ApprovalRequest struct {
	ID              string         `json:"id"`
	InvestigationID string         `json:"investigation_id"`
	TenantID        string         `json:"tenant_id"`
	Action          HighRiskAction `json:"action"`
	PayloadHash     string         `json:"payload_hash"`
	RequestedBy     Principal      `json:"requested_by"`
	Status          ApprovalStatus `json:"status"`
	DecidedBy       Principal      `json:"decided_by,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	ExpiresAt       time.Time      `json:"expires_at"`
	DecidedAt       time.Time      `json:"decided_at,omitempty"`
	ConsumedAt      time.Time      `json:"consumed_at,omitempty"`
}
