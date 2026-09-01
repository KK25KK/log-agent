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

func (s *Store) ReserveQueryQuota(ctx context.Context, reservation domain.QueryQuotaReservation, policy domain.TenantQuotaPolicy) error {
	if err := validateQuotaPolicy(policy); err != nil {
		return err
	}
	if err := validateQuotaReservation(reservation, policy); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin query quota reservation: %w", err)
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRowContext(ctx, `SELECT status FROM query_quota_reservations WHERE usage_key = ?`, reservation.UsageKey).Scan(&existing)
	if err == nil {
		return ports.ErrQuotaUsageReplayed
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check query quota usage key: %w", err)
	}

	usage, err := quotaUsageTx(ctx, tx, reservation.TenantID, reservation.WindowStart, reservation.WindowEnd, policy)
	if err != nil {
		return err
	}
	if usage.Observations+1 > policy.MaxObservations ||
		usage.APICalls+reservation.ReservedAPICalls > policy.MaxAPICalls ||
		usage.ProcessedBytes+reservation.ReservedBytes > policy.MaxProcessedBytes {
		if _, auditErr := tx.ExecContext(ctx, `
INSERT INTO query_quota_events(
    usage_key, tenant_id, outcome, reason_code, api_calls, processed_bytes, occurred_at
) VALUES (?, ?, 'DENIED', 'tenant_cost_circuit_open', ?, ?, ?)`,
			reservation.UsageKey, reservation.TenantID,
			reservation.ReservedAPICalls, reservation.ReservedBytes,
			reservation.CreatedAt.UTC().UnixMilli()); auditErr != nil {
			return fmt.Errorf("audit query quota denial: %w", auditErr)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit query quota denial: %w", err)
		}
		return ports.ErrTenantQuotaExceeded
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO query_quota_reservations(
    usage_key, tenant_id, investigation_id, query_name,
    window_start, window_end, reserved_api_calls, reserved_bytes,
    status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reservation.UsageKey, reservation.TenantID, reservation.InvestigationID, reservation.QueryName,
		reservation.WindowStart.UTC().UnixMilli(), reservation.WindowEnd.UTC().UnixMilli(),
		reservation.ReservedAPICalls, reservation.ReservedBytes,
		domain.QuotaReserved, reservation.CreatedAt.UTC().UnixMilli(), reservation.UpdatedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("insert query quota reservation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO query_quota_events(
    usage_key, tenant_id, outcome, reason_code, api_calls, processed_bytes, occurred_at
) VALUES (?, ?, 'RESERVED', 'quota_reserved', ?, ?, ?)`,
		reservation.UsageKey, reservation.TenantID,
		reservation.ReservedAPICalls, reservation.ReservedBytes,
		reservation.CreatedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("audit query quota reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit query quota reservation: %w", err)
	}
	return nil
}

func (s *Store) SettleQueryQuota(
	ctx context.Context,
	usageKey string,
	status domain.QuotaReservationStatus,
	actualAPICalls, actualBytes int64,
	reasonCode string,
	now time.Time,
) error {
	if !boundedSafeCode(usageKey, 64, 64) || !boundedSafeCode(reasonCode, 1, 128) || actualAPICalls < 0 || actualBytes < 0 {
		return errors.New("query quota settlement is invalid")
	}
	switch status {
	case domain.QuotaSettled, domain.QuotaReleased, domain.QuotaUnknown:
	default:
		return errors.New("query quota terminal status is invalid")
	}
	if status == domain.QuotaReleased && (actualAPICalls != 0 || actualBytes != 0) {
		return errors.New("released query quota cannot carry actual usage")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin query quota settlement: %w", err)
	}
	defer tx.Rollback()

	var tenantID string
	var existingStatus string
	var existingCalls, existingBytes int64
	err = tx.QueryRowContext(ctx, `
SELECT tenant_id, status, actual_api_calls, actual_bytes
FROM query_quota_reservations WHERE usage_key = ?`, usageKey).Scan(
		&tenantID, &existingStatus, &existingCalls, &existingBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load query quota reservation: %w", err)
	}
	if domain.QuotaReservationStatus(existingStatus) != domain.QuotaReserved {
		if domain.QuotaReservationStatus(existingStatus) == status && existingCalls == actualAPICalls && existingBytes == actualBytes {
			return nil
		}
		return ports.ErrStateConflict
	}

	result, err := tx.ExecContext(ctx, `
UPDATE query_quota_reservations
SET status = ?, actual_api_calls = ?, actual_bytes = ?, reason_code = ?, updated_at = ?
WHERE usage_key = ? AND status = ?`,
		status, actualAPICalls, actualBytes, reasonCode, now.UTC().UnixMilli(), usageKey, domain.QuotaReserved)
	if err != nil {
		return fmt.Errorf("settle query quota reservation: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ports.ErrStateConflict
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO query_quota_events(
    usage_key, tenant_id, outcome, reason_code, api_calls, processed_bytes, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		usageKey, tenantID, status, reasonCode, actualAPICalls, actualBytes, now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("audit query quota settlement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit query quota settlement: %w", err)
	}
	return nil
}

func (s *Store) GetTenantQuotaUsage(
	ctx context.Context,
	tenantID string,
	windowStart, windowEnd time.Time,
	policy domain.TenantQuotaPolicy,
) (domain.TenantQuotaUsage, error) {
	if err := validateQuotaPolicy(policy); err != nil {
		return domain.TenantQuotaUsage{}, err
	}
	if !boundedSafeCode(tenantID, 64, 64) || !windowEnd.After(windowStart) {
		return domain.TenantQuotaUsage{}, errors.New("tenant quota window is invalid")
	}
	return quotaUsageDB(ctx, s.db, tenantID, windowStart, windowEnd, policy)
}

type quotaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func quotaUsageDB(ctx context.Context, queryer quotaQueryer, tenantID string, windowStart, windowEnd time.Time, policy domain.TenantQuotaPolicy) (domain.TenantQuotaUsage, error) {
	return queryQuotaUsage(ctx, queryer, tenantID, windowStart, windowEnd, policy)
}

func quotaUsageTx(ctx context.Context, tx *sql.Tx, tenantID string, windowStart, windowEnd time.Time, policy domain.TenantQuotaPolicy) (domain.TenantQuotaUsage, error) {
	return queryQuotaUsage(ctx, tx, tenantID, windowStart, windowEnd, policy)
}

func queryQuotaUsage(ctx context.Context, queryer quotaQueryer, tenantID string, windowStart, windowEnd time.Time, policy domain.TenantQuotaPolicy) (domain.TenantQuotaUsage, error) {
	usage := domain.TenantQuotaUsage{TenantID: tenantID, WindowStart: windowStart.UTC(), WindowEnd: windowEnd.UTC()}
	err := queryer.QueryRowContext(ctx, `
SELECT
    COALESCE(SUM(CASE WHEN status <> ? THEN 1 ELSE 0 END), 0),
    COALESCE(SUM(CASE
        WHEN status IN (?, ?) THEN actual_api_calls
        WHEN status = ? THEN reserved_api_calls
        ELSE 0 END), 0),
    COALESCE(SUM(CASE
        WHEN status IN (?, ?) THEN actual_bytes
        WHEN status = ? THEN reserved_bytes
        ELSE 0 END), 0)
FROM query_quota_reservations
WHERE tenant_id = ? AND window_start = ? AND window_end = ?`,
		domain.QuotaReleased,
		domain.QuotaSettled, domain.QuotaUnknown, domain.QuotaReserved,
		domain.QuotaSettled, domain.QuotaUnknown, domain.QuotaReserved,
		tenantID, windowStart.UTC().UnixMilli(), windowEnd.UTC().UnixMilli(),
	).Scan(&usage.Observations, &usage.APICalls, &usage.ProcessedBytes)
	if err != nil {
		return domain.TenantQuotaUsage{}, fmt.Errorf("read tenant query quota: %w", err)
	}
	usage.CircuitOpen = usage.Observations >= policy.MaxObservations || usage.APICalls >= policy.MaxAPICalls || usage.ProcessedBytes >= policy.MaxProcessedBytes
	return usage, nil
}

func validateQuotaPolicy(policy domain.TenantQuotaPolicy) error {
	if !boundedSafeCode(policy.Version, 1, 128) || policy.Window < time.Minute || policy.Window > 24*time.Hour ||
		policy.MaxObservations <= 0 || policy.MaxAPICalls <= 0 || policy.MaxProcessedBytes <= 0 || policy.ReservedBytesPerObservation <= 0 ||
		policy.ReservedBytesPerObservation > policy.MaxProcessedBytes {
		return errors.New("tenant quota policy is invalid")
	}
	return nil
}

func validateQuotaReservation(reservation domain.QueryQuotaReservation, policy domain.TenantQuotaPolicy) error {
	if !boundedSafeCode(reservation.UsageKey, 64, 64) || !boundedSafeCode(reservation.TenantID, 64, 64) ||
		!boundedSafeCode(reservation.InvestigationID, 1, 256) || !boundedSafeCode(reservation.QueryName, 1, 64) ||
		reservation.Status != domain.QuotaReserved || !reservation.WindowEnd.After(reservation.WindowStart) ||
		reservation.WindowEnd.Sub(reservation.WindowStart) != policy.Window || !registeredTemplateCallCount(reservation.ReservedAPICalls) ||
		reservation.ReservedBytes != policy.ReservedBytesPerObservation || reservation.CreatedAt.IsZero() || reservation.UpdatedAt.IsZero() {
		return errors.New("query quota reservation is invalid")
	}
	return nil
}

func registeredTemplateCallCount(calls int64) bool {
	return calls == int64(domain.ErrorAnalysisAPICalls) || calls == int64(domain.ErrorCountAPICalls)
}

var _ ports.QueryQuotaStore = (*Store)(nil)
