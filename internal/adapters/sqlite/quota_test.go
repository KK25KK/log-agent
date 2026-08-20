package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestQueryQuotaReservationSettlementAndCircuit(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	policy := domain.TenantQuotaPolicy{
		Version: "tenant-query-quota-v1", Window: time.Hour,
		MaxObservations: 2, MaxAPICalls: 8, MaxProcessedBytes: 2000,
		ReservedBytesPerObservation: 1000,
	}
	first := quotaReservation(strings.Repeat("a", 64), "current", now)
	if err := store.ReserveQueryQuota(ctx, first, policy); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleQueryQuota(ctx, first.UsageKey, domain.QuotaSettled, 4, 600, "query_succeeded", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second := quotaReservation(strings.Repeat("b", 64), "baseline", now)
	if err := store.ReserveQueryQuota(ctx, second, policy); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetTenantQuotaUsage(ctx, first.TenantID, first.WindowStart, first.WindowEnd, policy)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Observations != 2 || usage.APICalls != 8 || usage.ProcessedBytes != 1600 || !usage.CircuitOpen {
		t.Fatalf("unexpected quota usage: %+v", usage)
	}
	third := quotaReservation(strings.Repeat("c", 64), "current", now)
	if err := store.ReserveQueryQuota(ctx, third, policy); !errors.Is(err, ports.ErrTenantQuotaExceeded) {
		t.Fatalf("want cost circuit denial, got %v", err)
	}
	if err := store.ReserveQueryQuota(ctx, first, policy); !errors.Is(err, ports.ErrQuotaUsageReplayed) {
		t.Fatalf("want replay denial, got %v", err)
	}
}

func TestUnknownQueryQuotaKeepsReservedCost(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	policy := domain.TenantQuotaPolicy{
		Version: "tenant-query-quota-v1", Window: time.Hour,
		MaxObservations: 1, MaxAPICalls: 4, MaxProcessedBytes: 1000,
		ReservedBytesPerObservation: 1000,
	}
	reservation := quotaReservation(strings.Repeat("d", 64), "current", now)
	if err := store.ReserveQueryQuota(ctx, reservation, policy); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleQueryQuota(ctx, reservation.UsageKey, domain.QuotaUnknown, 4, 1000, "external_outcome_unknown", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetTenantQuotaUsage(ctx, reservation.TenantID, reservation.WindowStart, reservation.WindowEnd, policy)
	if err != nil || !usage.CircuitOpen || usage.ProcessedBytes != 1000 {
		t.Fatalf("unknown cost was not retained: usage=%+v err=%v", usage, err)
	}
}

func quotaReservation(usageKey, name string, now time.Time) domain.QueryQuotaReservation {
	return domain.QueryQuotaReservation{
		UsageKey: usageKey, TenantID: strings.Repeat("e", 64), InvestigationID: "inv-quota", QueryName: name,
		WindowStart: now, WindowEnd: now.Add(time.Hour), ReservedAPICalls: 4, ReservedBytes: 1000,
		Status: domain.QuotaReserved, CreatedAt: now, UpdatedAt: now,
	}
}
