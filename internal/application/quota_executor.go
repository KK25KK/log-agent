package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const TenantQuotaPolicyVersion = "tenant-query-quota-v1"

type QuotaExecutor struct {
	delegate GovernedSLSExecutor
	store    ports.QueryQuotaStore
	policy   domain.TenantQuotaPolicy
	now      func() time.Time
}

type quotaUsageIdentity struct {
	InvestigationID       string `json:"investigation_id"`
	QueryName             string `json:"query_name"`
	GovernanceFingerprint string `json:"governance_fingerprint"`
}

func NewQuotaExecutor(
	delegate GovernedSLSExecutor,
	store ports.QueryQuotaStore,
	policy domain.TenantQuotaPolicy,
	now func() time.Time,
) (*QuotaExecutor, error) {
	if delegate == nil || store == nil || now == nil {
		return nil, errors.New("governed executor, quota store, and clock are required")
	}
	if err := validateTenantQuotaPolicy(policy); err != nil {
		return nil, err
	}
	return &QuotaExecutor{delegate: delegate, store: store, policy: policy, now: now}, nil
}

func (e *QuotaExecutor) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	return e.delegate.ResolveQueryGovernance(ctx, spec)
}

func (e *QuotaExecutor) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	contract, ok := domain.QueryTemplateByID(spec.TemplateID)
	if !ok {
		return domain.QueryResult{}, ports.ErrQueryDenied
	}
	governance, err := e.delegate.ResolveQueryGovernance(ctx, spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	usageKey, err := fingerprint.JSON(quotaUsageIdentity{
		InvestigationID: spec.InvestigationID, QueryName: spec.Name, GovernanceFingerprint: governance,
	})
	if err != nil {
		return domain.QueryResult{}, fmt.Errorf("fingerprint tenant quota usage: %w", err)
	}
	now := e.now().UTC()
	windowStart := fixedQuotaWindowStart(now, e.policy.Window)
	reservation := domain.QueryQuotaReservation{
		UsageKey: usageKey, TenantID: TenantQuotaID(spec.Requester),
		InvestigationID: spec.InvestigationID, QueryName: spec.Name,
		WindowStart: windowStart, WindowEnd: windowStart.Add(e.policy.Window),
		ReservedAPICalls: int64(contract.APICalls),
		ReservedBytes:    e.policy.ReservedBytesPerObservation,
		Status:           domain.QuotaReserved, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.ReserveQueryQuota(ctx, reservation, e.policy); err != nil {
		return domain.QueryResult{}, err
	}

	result, executeErr := e.delegate.Execute(ctx, spec)
	if executeErr == nil {
		if err := e.settle(ctx, reservation, domain.QuotaSettled, int64(result.APICalls), result.ProcessedBytes, "query_succeeded"); err != nil {
			return domain.QueryResult{}, fmt.Errorf("%w: persist query quota settlement", ports.ErrExternalOutcomeUnknown)
		}
		return result, nil
	}

	if deterministicQuotaRelease(executeErr) {
		if err := e.settle(ctx, reservation, domain.QuotaReleased, 0, 0, "pre_provider_failure"); err != nil {
			return domain.QueryResult{}, fmt.Errorf("%w: persist released query quota", ports.ErrExternalOutcomeUnknown)
		}
		return domain.QueryResult{}, executeErr
	}
	if err := e.settle(ctx, reservation, domain.QuotaUnknown, reservation.ReservedAPICalls, reservation.ReservedBytes, "external_outcome_unknown"); err != nil {
		return domain.QueryResult{}, fmt.Errorf("%w: persist unknown query quota", ports.ErrExternalOutcomeUnknown)
	}
	return domain.QueryResult{}, executeErr
}

func (e *QuotaExecutor) settle(
	ctx context.Context,
	reservation domain.QueryQuotaReservation,
	status domain.QuotaReservationStatus,
	apiCalls, processedBytes int64,
	reasonCode string,
) error {
	settleCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		settleCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	}
	defer cancel()
	return e.store.SettleQueryQuota(settleCtx, reservation.UsageKey, status, apiCalls, processedBytes, reasonCode, e.now().UTC())
}

func deterministicQuotaRelease(err error) bool {
	return errors.Is(err, ports.ErrQueryDenied) ||
		errors.Is(err, ports.ErrQueryBudgetExceeded) ||
		errors.Is(err, ports.ErrInvalidQuerySchema) ||
		errors.Is(err, ports.ErrTenantQuotaExceeded) ||
		errors.Is(err, ports.ErrQuotaUsageReplayed)
}

func TenantQuotaID(principal domain.Principal) string {
	return domain.TrustedTenantID(principal)
}

func fixedQuotaWindowStart(now time.Time, window time.Duration) time.Time {
	nanoseconds := now.UTC().UnixNano()
	start := nanoseconds - nanoseconds%int64(window)
	return time.Unix(0, start).UTC()
}

func validateTenantQuotaPolicy(policy domain.TenantQuotaPolicy) error {
	if policy.Version != TenantQuotaPolicyVersion || policy.Window < time.Minute || policy.Window > 24*time.Hour ||
		policy.MaxObservations <= 0 || policy.MaxAPICalls <= 0 || policy.MaxProcessedBytes <= 0 ||
		policy.ReservedBytesPerObservation <= 0 || policy.ReservedBytesPerObservation > policy.MaxProcessedBytes {
		return errors.New("tenant query quota policy is invalid")
	}
	return nil
}

var _ GovernedSLSExecutor = (*QuotaExecutor)(nil)
