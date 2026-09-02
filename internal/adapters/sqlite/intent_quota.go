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

func (s *Store) ReserveIntentQuota(ctx context.Context, reservation domain.IntentQuotaReservation, policy domain.IntentQuotaPolicy) error {
	if err := validateIntentQuotaPolicy(policy); err != nil {
		return err
	}
	if err := validateIntentQuotaReservation(reservation, policy); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin intent quota reservation: %w", err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT status FROM intent_quota_reservations WHERE usage_key = ?`, reservation.UsageKey).Scan(&existing)
	if err == nil {
		return ports.ErrIntentQuotaUsageReplayed
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check intent quota usage key: %w", err)
	}
	usage, err := intentQuotaUsage(ctx, tx, reservation.TenantID, reservation.WindowStart, reservation.WindowEnd, policy)
	if err != nil {
		return err
	}
	if usage.Requests+1 > policy.MaxRequests || usage.Tokens+reservation.ReservedTokens > policy.MaxTokens {
		if _, auditErr := tx.ExecContext(ctx, `
INSERT INTO intent_quota_events(
    usage_key, tenant_id, outcome, reason_code, input_tokens, output_tokens, total_tokens, occurred_at
) VALUES (?, ?, 'DENIED', 'tenant_intent_cost_circuit_open', 0, 0, ?, ?)`,
			reservation.UsageKey, reservation.TenantID, reservation.ReservedTokens, reservation.CreatedAt.UTC().UnixMilli()); auditErr != nil {
			return fmt.Errorf("audit intent quota denial: %w", auditErr)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit intent quota denial: %w", err)
		}
		return ports.ErrTenantIntentQuotaExceeded
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO intent_quota_reservations(
    usage_key, tenant_id, resolution_id, prompt_version,
    window_start, window_end, reserved_tokens, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reservation.UsageKey, reservation.TenantID, reservation.ResolutionID, reservation.PromptVersion,
		reservation.WindowStart.UTC().UnixMilli(), reservation.WindowEnd.UTC().UnixMilli(), reservation.ReservedTokens,
		domain.QuotaReserved, reservation.CreatedAt.UTC().UnixMilli(), reservation.UpdatedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("insert intent quota reservation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO intent_quota_events(
    usage_key, tenant_id, outcome, reason_code, input_tokens, output_tokens, total_tokens, occurred_at
) VALUES (?, ?, 'RESERVED', 'intent_quota_reserved', 0, 0, ?, ?)`,
		reservation.UsageKey, reservation.TenantID, reservation.ReservedTokens, reservation.CreatedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("audit intent quota reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit intent quota reservation: %w", err)
	}
	return nil
}

func (s *Store) SettleIntentQuota(
	ctx context.Context,
	usageKey string,
	status domain.QuotaReservationStatus,
	inputTokens, outputTokens, totalTokens int64,
	reasonCode string,
	now time.Time,
) error {
	const maxInt64 = int64(^uint64(0) >> 1)
	if !boundedSafeCode(usageKey, 64, 64) || !boundedSafeCode(reasonCode, 1, 128) ||
		inputTokens < 0 || outputTokens < 0 || totalTokens < 0 || inputTokens > maxInt64-outputTokens || totalTokens < inputTokens+outputTokens {
		return errors.New("intent quota settlement is invalid")
	}
	if status != domain.QuotaSettled && status != domain.QuotaReleased && status != domain.QuotaUnknown {
		return errors.New("intent quota terminal status is invalid")
	}
	if status == domain.QuotaReleased && totalTokens != 0 {
		return errors.New("released intent quota cannot carry actual usage")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin intent quota settlement: %w", err)
	}
	defer tx.Rollback()
	var tenantID, existingStatus string
	var existingInput, existingOutput, existingTotal int64
	err = tx.QueryRowContext(ctx, `
SELECT tenant_id, status, actual_input_tokens, actual_output_tokens, actual_total_tokens
FROM intent_quota_reservations WHERE usage_key = ?`, usageKey).Scan(
		&tenantID, &existingStatus, &existingInput, &existingOutput, &existingTotal,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load intent quota reservation: %w", err)
	}
	if domain.QuotaReservationStatus(existingStatus) != domain.QuotaReserved {
		if domain.QuotaReservationStatus(existingStatus) == status && existingInput == inputTokens && existingOutput == outputTokens && existingTotal == totalTokens {
			return nil
		}
		return ports.ErrStateConflict
	}
	result, err := tx.ExecContext(ctx, `
UPDATE intent_quota_reservations
SET status = ?, actual_input_tokens = ?, actual_output_tokens = ?, actual_total_tokens = ?, reason_code = ?, updated_at = ?
WHERE usage_key = ? AND status = ?`,
		status, inputTokens, outputTokens, totalTokens, reasonCode, now.UTC().UnixMilli(), usageKey, domain.QuotaReserved)
	if err != nil {
		return fmt.Errorf("settle intent quota reservation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ports.ErrStateConflict
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO intent_quota_events(
    usage_key, tenant_id, outcome, reason_code, input_tokens, output_tokens, total_tokens, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		usageKey, tenantID, status, reasonCode, inputTokens, outputTokens, totalTokens, now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("audit intent quota settlement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit intent quota settlement: %w", err)
	}
	return nil
}

func (s *Store) GetTenantIntentQuotaUsage(
	ctx context.Context,
	tenantID string,
	windowStart, windowEnd time.Time,
	policy domain.IntentQuotaPolicy,
) (domain.TenantIntentQuotaUsage, error) {
	if err := validateIntentQuotaPolicy(policy); err != nil {
		return domain.TenantIntentQuotaUsage{}, err
	}
	if !boundedSafeCode(tenantID, 64, 64) || !windowEnd.After(windowStart) {
		return domain.TenantIntentQuotaUsage{}, errors.New("tenant intent quota window is invalid")
	}
	return intentQuotaUsage(ctx, s.db, tenantID, windowStart, windowEnd, policy)
}

func intentQuotaUsage(ctx context.Context, queryer quotaQueryer, tenantID string, windowStart, windowEnd time.Time, policy domain.IntentQuotaPolicy) (domain.TenantIntentQuotaUsage, error) {
	usage := domain.TenantIntentQuotaUsage{TenantID: tenantID, WindowStart: windowStart.UTC(), WindowEnd: windowEnd.UTC()}
	err := queryer.QueryRowContext(ctx, `
SELECT
    COALESCE(SUM(CASE WHEN status <> ? THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE
        WHEN status IN (?, ?) THEN actual_total_tokens
        WHEN status = ? THEN reserved_tokens
        ELSE 0 END), 0)
FROM intent_quota_reservations
WHERE tenant_id = ? AND window_start = ? AND window_end = ?`,
		domain.QuotaReleased, domain.QuotaSettled, domain.QuotaUnknown, domain.QuotaReserved,
		tenantID, windowStart.UTC().UnixMilli(), windowEnd.UTC().UnixMilli(),
	).Scan(&usage.Requests, &usage.Tokens)
	if err != nil {
		return domain.TenantIntentQuotaUsage{}, fmt.Errorf("read tenant intent quota: %w", err)
	}
	usage.CircuitOpen = usage.Requests >= policy.MaxRequests || usage.Tokens >= policy.MaxTokens
	return usage, nil
}

func validateIntentQuotaPolicy(policy domain.IntentQuotaPolicy) error {
	if !boundedSafeCode(policy.Version, 1, 128) || policy.Window < time.Minute || policy.Window > 24*time.Hour ||
		policy.MaxRequests <= 0 || policy.MaxTokens <= 0 || policy.ReservedTokensPerRequest <= 0 ||
		policy.ReservedTokensPerRequest > policy.MaxTokens {
		return errors.New("tenant intent quota policy is invalid")
	}
	return nil
}

func validateIntentQuotaReservation(reservation domain.IntentQuotaReservation, policy domain.IntentQuotaPolicy) error {
	if !boundedSafeCode(reservation.UsageKey, 64, 64) || !boundedSafeCode(reservation.TenantID, 64, 64) ||
		!boundedSafeCode(reservation.ResolutionID, 1, 256) || !boundedSafeCode(reservation.PromptVersion, 1, 128) ||
		reservation.Status != domain.QuotaReserved || !reservation.WindowEnd.After(reservation.WindowStart) ||
		reservation.WindowEnd.Sub(reservation.WindowStart) != policy.Window || reservation.ReservedTokens != policy.ReservedTokensPerRequest ||
		reservation.ActualInputTokens != 0 || reservation.ActualOutputTokens != 0 || reservation.ActualTotalTokens != 0 ||
		reservation.CreatedAt.IsZero() || reservation.UpdatedAt.IsZero() {
		return errors.New("intent quota reservation is invalid")
	}
	return nil
}

var _ ports.IntentQuotaStore = (*Store)(nil)
