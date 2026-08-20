package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func (s *Store) CreateApproval(ctx context.Context, request domain.ApprovalRequest) error {
	if err := validateApprovalRequest(request); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO approval_requests(
    id, investigation_id, tenant_id, action, payload_hash,
    requester_app_id, requester_tenant_key, requester_user_id,
    status, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.ID, request.InvestigationID, request.TenantID, request.Action, request.PayloadHash,
		request.RequestedBy.AppID, request.RequestedBy.TenantKey, request.RequestedBy.UserID,
		request.Status, request.CreatedAt.UTC().UnixMilli(), request.ExpiresAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("create approval request: %w", err)
	}
	return nil
}

func (s *Store) DecideApproval(
	ctx context.Context,
	approvalID string,
	decision domain.ApprovalStatus,
	actor domain.Principal,
	now time.Time,
) (domain.ApprovalRequest, error) {
	if decision != domain.ApprovalApproved && decision != domain.ApprovalRejected || !actor.Complete() {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("begin approval decision: %w", err)
	}
	defer tx.Rollback()
	request, err := loadApproval(ctx, tx, approvalID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if request.Status != domain.ApprovalPending || request.RequestedBy.AppID != actor.AppID || request.RequestedBy.TenantKey != actor.TenantKey || request.RequestedBy.UserID == actor.UserID {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	if !now.UTC().Before(request.ExpiresAt) {
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = ? WHERE id = ? AND status = ?`, domain.ApprovalExpired, approvalID, domain.ApprovalPending); err != nil {
			return domain.ApprovalRequest{}, fmt.Errorf("expire approval request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.ApprovalRequest{}, fmt.Errorf("commit approval expiry: %w", err)
		}
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	result, err := tx.ExecContext(ctx, `
UPDATE approval_requests
SET status = ?, decider_app_id = ?, decider_tenant_key = ?, decider_user_id = ?, decided_at = ?
WHERE id = ? AND status = ?`,
		decision, actor.AppID, actor.TenantKey, actor.UserID, now.UTC().UnixMilli(), approvalID, domain.ApprovalPending)
	if err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("decide approval request: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return domain.ApprovalRequest{}, ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("commit approval decision: %w", err)
	}
	request.Status = decision
	request.DecidedBy = actor
	request.DecidedAt = now.UTC()
	return request, nil
}

func (s *Store) ConsumeApproval(ctx context.Context, approvalID, payloadHash string, now time.Time) (domain.ApprovalRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("begin approval consumption: %w", err)
	}
	defer tx.Rollback()
	request, err := loadApproval(ctx, tx, approvalID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if request.Status != domain.ApprovalApproved || request.PayloadHash != payloadHash || !now.UTC().Before(request.ExpiresAt) {
		return domain.ApprovalRequest{}, ports.ErrApprovalInvalid
	}
	result, err := tx.ExecContext(ctx, `
UPDATE approval_requests SET status = ?, consumed_at = ?
WHERE id = ? AND status = ? AND payload_hash = ?`,
		domain.ApprovalConsumed, now.UTC().UnixMilli(), approvalID, domain.ApprovalApproved, payloadHash)
	if err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("consume approval request: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return domain.ApprovalRequest{}, ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("commit approval consumption: %w", err)
	}
	request.Status = domain.ApprovalConsumed
	request.ConsumedAt = now.UTC()
	return request, nil
}

func (s *Store) GetApproval(ctx context.Context, approvalID string) (domain.ApprovalRequest, error) {
	return loadApproval(ctx, s.db, approvalID)
}

type approvalQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadApproval(ctx context.Context, queryer approvalQueryer, approvalID string) (domain.ApprovalRequest, error) {
	var request domain.ApprovalRequest
	var action, status string
	var createdMillis, expiresMillis, decidedMillis, consumedMillis int64
	err := queryer.QueryRowContext(ctx, `
SELECT id, investigation_id, tenant_id, action, payload_hash,
       requester_app_id, requester_tenant_key, requester_user_id,
       status, decider_app_id, decider_tenant_key, decider_user_id,
       created_at, expires_at, decided_at, consumed_at
FROM approval_requests WHERE id = ?`, approvalID).Scan(
		&request.ID, &request.InvestigationID, &request.TenantID, &action, &request.PayloadHash,
		&request.RequestedBy.AppID, &request.RequestedBy.TenantKey, &request.RequestedBy.UserID,
		&status, &request.DecidedBy.AppID, &request.DecidedBy.TenantKey, &request.DecidedBy.UserID,
		&createdMillis, &expiresMillis, &decidedMillis, &consumedMillis,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ApprovalRequest{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.ApprovalRequest{}, fmt.Errorf("load approval request: %w", err)
	}
	request.Action = domain.HighRiskAction(action)
	request.Status = domain.ApprovalStatus(status)
	request.CreatedAt = time.UnixMilli(createdMillis).UTC()
	request.ExpiresAt = time.UnixMilli(expiresMillis).UTC()
	if decidedMillis > 0 {
		request.DecidedAt = time.UnixMilli(decidedMillis).UTC()
	}
	if consumedMillis > 0 {
		request.ConsumedAt = time.UnixMilli(consumedMillis).UTC()
	}
	return request, nil
}

func validateApprovalRequest(request domain.ApprovalRequest) error {
	if !boundedSafeCode(request.ID, 1, 256) || !boundedSafeCode(request.InvestigationID, 1, 256) || !boundedSafeCode(request.TenantID, 64, 64) ||
		!boundedSafeCode(request.PayloadHash, 64, 64) || !request.RequestedBy.Complete() || request.Status != domain.ApprovalPending ||
		!request.ExpiresAt.After(request.CreatedAt) || request.ExpiresAt.Sub(request.CreatedAt) > 24*time.Hour {
		return ports.ErrApprovalInvalid
	}
	if request.TenantID != domain.TrustedTenantID(request.RequestedBy) ||
		!boundedSafeCode(request.RequestedBy.AppID, 1, 256) || !boundedSafeCode(request.RequestedBy.TenantKey, 1, 256) ||
		!boundedSafeCode(request.RequestedBy.UserID, 1, 256) {
		return ports.ErrApprovalInvalid
	}
	if request.Action != domain.HighRiskReadRawSample && request.Action != domain.HighRiskRemediation {
		return ports.ErrApprovalInvalid
	}
	return nil
}

var _ ports.ApprovalStore = (*Store)(nil)
