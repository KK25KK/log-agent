package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type quotaDelegateStub struct {
	result       domain.QueryResult
	err          error
	executeCalls int
}

func (stub *quotaDelegateStub) ResolveQueryGovernance(context.Context, domain.QuerySpec) (string, error) {
	return strings.Repeat("a", 64), nil
}

func (stub *quotaDelegateStub) Execute(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
	stub.executeCalls++
	return stub.result, stub.err
}

type quotaStoreStub struct {
	reserveErr  error
	reservation domain.QueryQuotaReservation
	status      domain.QuotaReservationStatus
	calls       int64
	bytes       int64
	reason      string
}

func (stub *quotaStoreStub) ReserveQueryQuota(_ context.Context, reservation domain.QueryQuotaReservation, _ domain.TenantQuotaPolicy) error {
	stub.reservation = reservation
	return stub.reserveErr
}

func (stub *quotaStoreStub) SettleQueryQuota(_ context.Context, _ string, status domain.QuotaReservationStatus, calls, bytes int64, reason string, _ time.Time) error {
	stub.status, stub.calls, stub.bytes, stub.reason = status, calls, bytes, reason
	return nil
}

func (stub *quotaStoreStub) GetTenantQuotaUsage(context.Context, string, time.Time, time.Time, domain.TenantQuotaPolicy) (domain.TenantQuotaUsage, error) {
	return domain.TenantQuotaUsage{}, nil
}

func TestQuotaExecutorSettlesActualUsage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 20, 10, 17, 0, 0, time.UTC)
	delegate := &quotaDelegateStub{result: domain.QueryResult{APICalls: 4, ProcessedBytes: 2048}}
	store := &quotaStoreStub{}
	executor, err := NewQuotaExecutor(delegate, store, testQuotaPolicy(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), quotaSpec())
	if err != nil || result.ProcessedBytes != 2048 || delegate.executeCalls != 1 {
		t.Fatalf("unexpected quota execution: result=%+v calls=%d err=%v", result, delegate.executeCalls, err)
	}
	if store.status != domain.QuotaSettled || store.calls != 4 || store.bytes != 2048 || store.reason != "query_succeeded" {
		t.Fatalf("unexpected settlement: %+v", store)
	}
	if store.reservation.WindowStart != now.Truncate(time.Hour) || store.reservation.TenantID == "" || len(store.reservation.UsageKey) != 64 {
		t.Fatalf("unexpected reservation: %+v", store.reservation)
	}
}

func TestQuotaExecutorDoesNotCallDelegateWhenCircuitIsOpen(t *testing.T) {
	t.Parallel()
	delegate := &quotaDelegateStub{}
	store := &quotaStoreStub{reserveErr: ports.ErrTenantQuotaExceeded}
	executor, err := NewQuotaExecutor(delegate, store, testQuotaPolicy(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), quotaSpec())
	if !errors.Is(err, ports.ErrTenantQuotaExceeded) || delegate.executeCalls != 0 {
		t.Fatalf("quota denial reached provider: calls=%d err=%v", delegate.executeCalls, err)
	}
}

func TestQuotaExecutorReleasesOnlyDeterministicPreProviderFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		err    error
		status domain.QuotaReservationStatus
	}{
		{name: "policy denial", err: ports.ErrQueryDenied, status: domain.QuotaReleased},
		{name: "provider ambiguity", err: errors.New("provider transport failed"), status: domain.QuotaUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			delegate := &quotaDelegateStub{err: test.err}
			store := &quotaStoreStub{}
			executor, err := NewQuotaExecutor(delegate, store, testQuotaPolicy(), time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.Execute(context.Background(), quotaSpec()); err == nil {
				t.Fatal("expected execution failure")
			}
			if store.status != test.status {
				t.Fatalf("want %s, got %+v", test.status, store)
			}
		})
	}
}

func testQuotaPolicy() domain.TenantQuotaPolicy {
	return domain.TenantQuotaPolicy{
		Version: TenantQuotaPolicyVersion, Window: time.Hour,
		MaxObservations: 10, MaxAPICalls: 40, MaxProcessedBytes: 1024 * 1024,
		ReservedBytesPerObservation: 64 * 1024,
	}
}

func quotaSpec() domain.QuerySpec {
	return domain.QuerySpec{
		InvestigationID: "inv-quota", Name: "current", TemplateID: domain.ErrorAnalysisTemplateID,
		Service: "order-service", Environment: "prod",
		StartTime: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC),
		Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
	}
}
