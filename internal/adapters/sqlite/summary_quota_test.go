package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func TestSummaryQuotaReservationSettlementAndCircuit(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	policy := testSummaryQuotaPolicy(2, 200)
	first := summaryQuotaReservation(strings.Repeat("a", 64), "inv-first", now, 100)
	if err := store.ReserveSummaryQuota(ctx, first, policy); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleSummaryQuota(ctx, first.UsageKey, domain.QuotaSettled, 40, 20, 60, "summary_succeeded", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second := summaryQuotaReservation(strings.Repeat("b", 64), "inv-second", now, 100)
	if err := store.ReserveSummaryQuota(ctx, second, policy); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetTenantSummaryQuotaUsage(ctx, first.TenantID, first.WindowStart, first.WindowEnd, policy)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Requests != 2 || usage.Tokens != 160 || !usage.CircuitOpen {
		t.Fatalf("unexpected summary quota usage: %+v", usage)
	}
	third := summaryQuotaReservation(strings.Repeat("c", 64), "inv-third", now, 100)
	if err := store.ReserveSummaryQuota(ctx, third, policy); !errors.Is(err, ports.ErrTenantSummaryQuotaExceeded) {
		t.Fatalf("want summary cost circuit denial, got %v", err)
	}
	if err := store.ReserveSummaryQuota(ctx, first, policy); !errors.Is(err, ports.ErrSummaryQuotaUsageReplayed) {
		t.Fatalf("want summary replay denial, got %v", err)
	}
}

func TestUnknownSummaryQuotaKeepsReservedTokens(t *testing.T) {
	t.Parallel()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	policy := testSummaryQuotaPolicy(1, 100)
	reservation := summaryQuotaReservation(strings.Repeat("d", 64), "inv-unknown", now, 100)
	if err := store.ReserveSummaryQuota(ctx, reservation, policy); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleSummaryQuota(ctx, reservation.UsageKey, domain.QuotaUnknown, 0, 0, 100, "summary_external_outcome_unknown", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetTenantSummaryQuotaUsage(ctx, reservation.TenantID, reservation.WindowStart, reservation.WindowEnd, policy)
	if err != nil || usage.Requests != 1 || usage.Tokens != 100 || !usage.CircuitOpen {
		t.Fatalf("unknown summary cost was not retained: usage=%+v err=%v", usage, err)
	}
}

func TestConcurrentSummaryQuotaReservationCannotExceedWindow(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	policy := testSummaryQuotaPolicy(1, 100)
	const attempts = 20
	var wait sync.WaitGroup
	results := make(chan error, attempts)
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			key := fmt.Sprintf("%064x", index+1)
			results <- store.ReserveSummaryQuota(ctx, summaryQuotaReservation(key, fmt.Sprintf("inv-%d", index), now, 100), policy)
		}(index)
	}
	wait.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ports.ErrTenantSummaryQuotaExceeded):
			denied++
		default:
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if succeeded != 1 || denied != attempts-1 {
		t.Fatalf("quota race exceeded policy: succeeded=%d denied=%d", succeeded, denied)
	}
}

func TestSummaryQuotaSchemaExcludesPromptAndProviderPayloads(t *testing.T) {
	store := openTestStore(t)
	expected := map[string][]string{
		"summary_quota_reservations": {"usage_key", "tenant_id", "investigation_id", "prompt_version", "window_start", "window_end", "reserved_tokens", "status", "actual_input_tokens", "actual_output_tokens", "actual_total_tokens", "reason_code", "created_at", "updated_at"},
		"summary_quota_events":       {"event_id", "usage_key", "tenant_id", "outcome", "reason_code", "input_tokens", "output_tokens", "total_tokens", "occurred_at"},
	}
	for table, wantColumns := range expected {
		rows, err := store.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var gotColumns []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			gotColumns = append(gotColumns, column)
			lower := strings.ToLower(column)
			for _, forbidden := range []string{"prompt_text", "evidence", "request_body", "response_body", "credential", "api_key", "provider_error"} {
				if strings.Contains(lower, forbidden) {
					rows.Close()
					t.Fatalf("unsafe summary quota column %q in %s", column, table)
				}
			}
		}
		rows.Close()
		if len(gotColumns) != len(wantColumns) {
			t.Fatalf("unexpected %s columns: got=%v want=%v", table, gotColumns, wantColumns)
		}
		for index := range wantColumns {
			if gotColumns[index] != wantColumns[index] {
				t.Fatalf("unexpected %s columns: got=%v want=%v", table, gotColumns, wantColumns)
			}
		}
	}
}

func testSummaryQuotaPolicy(maxRequests, maxTokens int64) domain.SummaryQuotaPolicy {
	return domain.SummaryQuotaPolicy{
		Version: "tenant-summary-quota-v1", Window: time.Hour,
		MaxRequests: maxRequests, MaxTokens: maxTokens, ReservedTokensPerRequest: 100,
	}
}

func summaryQuotaReservation(usageKey, investigationID string, now time.Time, reservedTokens int64) domain.SummaryQuotaReservation {
	return domain.SummaryQuotaReservation{
		UsageKey: usageKey, TenantID: strings.Repeat("e", 64), InvestigationID: investigationID,
		PromptVersion: "evidence-summary-zh-v1", WindowStart: now, WindowEnd: now.Add(time.Hour),
		ReservedTokens: reservedTokens, Status: domain.QuotaReserved, CreatedAt: now, UpdatedAt: now,
	}
}
