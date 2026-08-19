// Package feishumock provides a credential-free Feishu boundary for local
// development and CI. It deliberately does not import the Feishu SDK.
package feishumock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/command"
	"logagent/internal/domain"
)

type accepter interface {
	Accept(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest) (string, bool, error)
}

// Message is the small, provider-neutral subset of a Feishu text message used
// by the local mock. App and tenant identity are owned by Receiver so a caller
// cannot put a forged principal in the investigation command.
type Message struct {
	MessageID string    `json:"message_id"`
	ChatID    string    `json:"chat_id"`
	UserID    string    `json:"user_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Receiver normalizes a local message through the same strict command parser
// and application intake contract as the real Feishu adapter.
type Receiver struct {
	appID          string
	tenantKey      string
	intake         accepter
	ingestionGrace time.Duration
}

func NewReceiver(appID, tenantKey string, intake accepter, ingestionGrace time.Duration) (*Receiver, error) {
	if appID == "" || tenantKey == "" {
		return nil, errors.New("mock Feishu app ID and tenant key are required")
	}
	if intake == nil {
		return nil, errors.New("mock Feishu intake is required")
	}
	if ingestionGrace < domain.MinimumIngestionGrace {
		return nil, fmt.Errorf("mock Feishu ingestion grace must be at least %s", domain.MinimumIngestionGrace)
	}
	return &Receiver{
		appID: appID, tenantKey: tenantKey, intake: intake, ingestionGrace: ingestionGrace,
	}, nil
}

// Receive simulates one successfully decoded im.message.receive_v1 event. It
// performs no network access and returns the durable intake idempotency result.
func (r *Receiver) Receive(ctx context.Context, message Message) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if message.MessageID == "" || message.ChatID == "" || message.UserID == "" || message.CreatedAt.IsZero() {
		return "", false, errors.New("mock Feishu message ID, chat ID, user ID, and creation time are required")
	}
	request, err := command.ParseInvestigationWithGrace(message.Text, message.CreatedAt, r.ingestionGrace)
	if err != nil {
		return "", false, fmt.Errorf("parse mock Feishu command: %w", err)
	}
	inbound := domain.InboundMessage{
		AppID:            r.appID,
		TenantKey:        r.tenantKey,
		MessageID:        message.MessageID,
		ReplyToMessageID: message.MessageID,
		ChatID:           message.ChatID,
		UserID:           message.UserID,
		Text:             message.Text,
		ReceivedAt:       message.CreatedAt.UTC(),
	}
	return r.intake.Accept(ctx, inbound, request)
}
