package domain

import "time"

// DeliveryKind is a durable projection of an investigation lifecycle event.
type DeliveryKind string

const (
	DeliveryQueued    DeliveryKind = "QUEUED"
	DeliveryRunning   DeliveryKind = "RUNNING"
	DeliverySucceeded DeliveryKind = "SUCCEEDED"
	DeliveryFailed    DeliveryKind = "FAILED"
	DeliveryCancelled DeliveryKind = "CANCELLED"
)

// DeliveryStatus is the local Feishu delivery lifecycle.
type DeliveryStatus string

const (
	DeliveryPending DeliveryStatus = "PENDING"
	DeliverySending DeliveryStatus = "RUNNING"
	DeliverySent    DeliveryStatus = "SENT"
	DeliveryDead    DeliveryStatus = "DEAD"
)

// InteractionTarget contains only routing identifiers persisted from a trusted
// Feishu envelope. CardMessageID is populated after the first reply succeeds.
type InteractionTarget struct {
	AppID           string `json:"app_id"`
	TenantKey       string `json:"tenant_key"`
	ChatID          string `json:"chat_id"`
	SourceMessageID string `json:"source_message_id"`
	CardMessageID   string `json:"card_message_id,omitempty"`
}

// DeliveryJob is a leased card update with the investigation snapshot needed
// by a protocol adapter. It never contains Feishu SDK types.
type DeliveryJob struct {
	ID            string            `json:"id"`
	Kind          DeliveryKind      `json:"kind"`
	Investigation Investigation     `json:"investigation"`
	Target        InteractionTarget `json:"target"`
	Attempt       int               `json:"attempt"`
	LeaseOwner    string            `json:"lease_owner"`
	LeaseUntil    time.Time         `json:"lease_until"`
}

type InvestigationAction string

const (
	ActionViewEvidence InvestigationAction = "view_evidence"
	ActionViewReport   InvestigationAction = "view_report"
	ActionCancel       InvestigationAction = "cancel"
	ActionExpandWindow InvestigationAction = "expand_window"
	ActionRerun        InvestigationAction = "rerun"
)

type ActionCommand struct {
	EventID         string              `json:"event_id"`
	Action          InvestigationAction `json:"action"`
	InvestigationID string              `json:"investigation_id"`
	Principal       Principal           `json:"principal"`
	ChatID          string              `json:"chat_id"`
	CardMessageID   string              `json:"card_message_id"`
	OccurredAt      time.Time           `json:"occurred_at"`
}

type ActionView string

const (
	ActionViewReportCard    ActionView = "REPORT"
	ActionViewEvidenceCard  ActionView = "EVIDENCE"
	ActionViewQueuedCard    ActionView = "QUEUED"
	ActionViewRunningCard   ActionView = "RUNNING"
	ActionViewCancelledCard ActionView = "CANCELLED"
)

type ActionResult struct {
	View          ActionView    `json:"view"`
	Investigation Investigation `json:"investigation"`
	Created       bool          `json:"created"`
}
