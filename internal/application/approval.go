package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

// ApprovalService defines the high-risk authorization boundary only. The
// current runtime registers no high-risk tool executor.
type ApprovalService struct {
	store ports.ApprovalStore
	now   func() time.Time
}

func NewApprovalService(store ports.ApprovalStore, now func() time.Time) (*ApprovalService, error) {
	if store == nil || now == nil {
		return nil, errors.New("approval store and clock are required")
	}
	return &ApprovalService{store: store, now: now}, nil
}

func (service *ApprovalService) Request(
	ctx context.Context,
	approvalID, investigationID string,
	action domain.HighRiskAction,
	payloadHash string,
	requester domain.Principal,
	ttl time.Duration,
) (domain.ApprovalRequest, error) {
	if approvalID == "" || investigationID == "" || !requester.Complete() || !validApprovalHash(payloadHash) || ttl < time.Minute || ttl > 24*time.Hour {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	if action != domain.HighRiskReadRawSample && action != domain.HighRiskRemediation {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	now := service.now().UTC()
	request := domain.ApprovalRequest{
		ID: approvalID, InvestigationID: investigationID, TenantID: TenantQuotaID(requester),
		Action: action, PayloadHash: payloadHash, RequestedBy: requester,
		Status: domain.ApprovalPending, CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	if err := service.store.CreateApproval(ctx, request); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return request, nil
}

func (service *ApprovalService) Decide(ctx context.Context, approvalID string, approve bool, actor domain.Principal) (domain.ApprovalRequest, error) {
	if approvalID == "" || !actor.Complete() {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	request, err := service.store.GetApproval(ctx, approvalID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if request.RequestedBy.AppID != actor.AppID || request.RequestedBy.TenantKey != actor.TenantKey || request.RequestedBy.UserID == actor.UserID {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	decision := domain.ApprovalRejected
	if approve {
		decision = domain.ApprovalApproved
	}
	return service.store.DecideApproval(ctx, approvalID, decision, actor, service.now().UTC())
}

func (service *ApprovalService) Consume(ctx context.Context, approvalID, payloadHash string) (domain.ApprovalRequest, error) {
	if approvalID == "" || !validApprovalHash(payloadHash) {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	return service.store.ConsumeApproval(ctx, approvalID, payloadHash, service.now().UTC())
}

func ApprovalPayloadHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validApprovalHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
