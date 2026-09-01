// Package localweb provides a loopback-only interaction adapter for validating
// the Agent application chain while Feishu application access is unavailable.
package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

// DeliverySnapshot is a safe local projection of the latest durable delivery.
// It intentionally excludes routing identities and rendered message content.
type DeliverySnapshot struct {
	Kind      domain.DeliveryKind `json:"kind"`
	CardReady bool                `json:"card_ready"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Sender simulates the remote card target while still exercising the real
// DeliveryWorker, leases, ordering, retries, audit, and card-rebinding rules.
type Sender struct {
	appID string
	now   func() time.Time
	mu    sync.RWMutex
	state map[string]DeliverySnapshot
}

func NewSender(appID string) (*Sender, error) {
	if appID == "" {
		return nil, errors.New("local Web sender app ID is required")
	}
	return &Sender{appID: appID, now: time.Now, state: make(map[string]DeliverySnapshot)}, nil
}

func (s *Sender) Deliver(ctx context.Context, delivery domain.DeliveryJob) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if delivery.Investigation.ID == "" || delivery.Target.AppID != s.appID ||
		delivery.Target.TenantKey == "" || delivery.Target.ChatID == "" {
		return "", errors.New("local Web delivery contract is invalid")
	}

	cardMessageID := delivery.Target.CardMessageID
	if cardMessageID == "" {
		if delivery.Kind != domain.DeliveryQueued || delivery.Target.SourceMessageID == "" {
			return "", errors.New("local Web initial delivery must be a queued receipt")
		}
		cardMessageID = localCardMessageID(delivery.Investigation.ID)
	}

	s.mu.Lock()
	s.state[delivery.Investigation.ID] = DeliverySnapshot{
		Kind: delivery.Kind, CardReady: true, UpdatedAt: s.now().UTC(),
	}
	s.mu.Unlock()
	return cardMessageID, nil
}

func (s *Sender) Snapshot(investigationID string) (DeliverySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.state[investigationID]
	return snapshot, ok
}

func localCardMessageID(investigationID string) string {
	digest := sha256.Sum256([]byte("local-web-card\x00" + investigationID))
	return "local-card-" + hex.EncodeToString(digest[:8])
}

var _ ports.DeliverySender = (*Sender)(nil)
