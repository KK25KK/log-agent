package feishumock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type DeliveryOperation string

const (
	OperationReply DeliveryOperation = "REPLY"
	OperationPatch DeliveryOperation = "PATCH"
)

// DeliveryRecord is the observable state of one simulated Feishu API call.
// It intentionally contains no SDK request and no card JSON.
type DeliveryRecord struct {
	Operation           DeliveryOperation   `json:"operation"`
	Kind                domain.DeliveryKind `json:"kind"`
	InvestigationID     string              `json:"investigation_id"`
	InvestigationStatus domain.Status       `json:"investigation_status"`
	CardMessageID       string              `json:"card_message_id"`
	ReportOutcome       string              `json:"report_outcome,omitempty"`
	EvidenceCount       int                 `json:"evidence_count"`
}

// Sender simulates Feishu reply/patch calls and records their semantic payload.
// It is safe for concurrent inspection by tests and local runners.
type Sender struct {
	appID   string
	mu      sync.Mutex
	records []DeliveryRecord
}

func NewSender(appID string) (*Sender, error) {
	if appID == "" {
		return nil, errors.New("mock Feishu sender app ID is required")
	}
	return &Sender{appID: appID}, nil
}

func (s *Sender) Deliver(ctx context.Context, delivery domain.DeliveryJob) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if delivery.Investigation.ID == "" || delivery.Target.AppID != s.appID || delivery.Target.TenantKey == "" || delivery.Target.ChatID == "" {
		return "", errors.New("mock Feishu delivery is missing investigation or routing identity")
	}

	operation := OperationPatch
	cardMessageID := delivery.Target.CardMessageID
	if cardMessageID == "" {
		if delivery.Kind != domain.DeliveryQueued || delivery.Target.SourceMessageID == "" {
			return "", errors.New("mock Feishu initial delivery must be a queued reply with a source message")
		}
		operation = OperationReply
		cardMessageID = mockCardMessageID(delivery.Investigation.ID)
	}

	record := DeliveryRecord{
		Operation:           operation,
		Kind:                delivery.Kind,
		InvestigationID:     delivery.Investigation.ID,
		InvestigationStatus: delivery.Investigation.Status,
		CardMessageID:       cardMessageID,
	}
	if delivery.Investigation.Report != nil {
		record.ReportOutcome = delivery.Investigation.Report.Outcome
		record.EvidenceCount = len(delivery.Investigation.Report.Evidence)
	}

	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	return cardMessageID, nil
}

func (s *Sender) Records() []DeliveryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DeliveryRecord(nil), s.records...)
}

func mockCardMessageID(investigationID string) string {
	digest := sha256.Sum256([]byte("mock-feishu-card\x00" + investigationID))
	return "mock-card-" + hex.EncodeToString(digest[:8])
}

var _ ports.DeliverySender = (*Sender)(nil)
