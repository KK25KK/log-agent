package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

type summaryQuotaSettlement struct {
	status                                 domain.QuotaReservationStatus
	inputTokens, outputTokens, totalTokens int64
	reasonCode                             string
}

type recordingSummaryQuotaStore struct {
	reserveErr   error
	settleErr    error
	reservations []domain.SummaryQuotaReservation
	settlements  []summaryQuotaSettlement
}

func (store *recordingSummaryQuotaStore) ReserveSummaryQuota(_ context.Context, reservation domain.SummaryQuotaReservation, _ domain.SummaryQuotaPolicy) error {
	store.reservations = append(store.reservations, reservation)
	return store.reserveErr
}

func (store *recordingSummaryQuotaStore) SettleSummaryQuota(_ context.Context, _ string, status domain.QuotaReservationStatus, inputTokens, outputTokens, totalTokens int64, reasonCode string, _ time.Time) error {
	store.settlements = append(store.settlements, summaryQuotaSettlement{
		status: status, inputTokens: inputTokens, outputTokens: outputTokens, totalTokens: totalTokens, reasonCode: reasonCode,
	})
	return store.settleErr
}

func (*recordingSummaryQuotaStore) GetTenantSummaryQuotaUsage(context.Context, string, time.Time, time.Time, domain.SummaryQuotaPolicy) (domain.TenantSummaryQuotaUsage, error) {
	return domain.TenantSummaryQuotaUsage{}, nil
}

func TestSummaryQuotaSettlesSuccessfulProviderUsage(t *testing.T) {
	evidence, report := summaryFixture()
	store := &recordingSummaryQuotaStore{}
	providerCalls := 0
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		providerCalls++
		return groundedSummaryResult(100, 30, 130), nil
	}))

	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryGenerated || providerCalls != 1 {
		t.Fatalf("summary did not use the admitted provider call: summary=%+v calls=%d", result.Summary, providerCalls)
	}
	if len(store.reservations) != 1 || store.reservations[0].TenantID != domain.TrustedTenantID(summaryRequester()) || len(store.settlements) != 1 {
		t.Fatalf("unexpected quota lifecycle: reservations=%+v settlements=%+v", store.reservations, store.settlements)
	}
	settlement := store.settlements[0]
	if settlement.status != domain.QuotaSettled || settlement.inputTokens != 100 || settlement.outputTokens != 30 || settlement.totalTokens != 130 {
		t.Fatalf("unexpected successful settlement: %+v", settlement)
	}
}

func TestSummaryQuotaDenialAndReplayCallNoProvider(t *testing.T) {
	for _, denied := range []error{ports.ErrTenantSummaryQuotaExceeded, ports.ErrSummaryQuotaUsageReplayed, errors.New("ledger unavailable")} {
		t.Run(denied.Error(), func(t *testing.T) {
			evidence, report := summaryFixture()
			store := &recordingSummaryQuotaStore{reserveErr: denied}
			providerCalls := 0
			service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
				providerCalls++
				return groundedSummaryResult(0, 0, 0), nil
			}))
			result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
			if providerCalls != 0 || result.Summary == nil || result.Summary.Status != domain.SummaryFallback || len(store.settlements) != 0 {
				t.Fatalf("denied summary crossed provider boundary: calls=%d summary=%+v settlements=%+v", providerCalls, result.Summary, store.settlements)
			}
		})
	}
}

func TestSummaryQuotaReplayWithSQLiteDoesNotCallProviderAgain(t *testing.T) {
	evidence, report := summaryFixture()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	providerCalls := 0
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		providerCalls++
		return domain.SummaryProviderResult{}, errors.New("synthetic provider failure")
	}))
	first := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	second := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if providerCalls != 1 || first.Summary == nil || second.Summary == nil || first.Summary.Status != domain.SummaryFallback || second.Summary.Status != domain.SummaryFallback {
		t.Fatalf("summary replay repeated provider call: calls=%d first=%+v second=%+v", providerCalls, first.Summary, second.Summary)
	}
	windowStart := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	policy := domain.SummaryQuotaPolicy{
		Version: SummaryQuotaPolicyVersion, Window: time.Hour,
		MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: 256,
	}
	usage, err := store.GetTenantSummaryQuotaUsage(context.Background(), domain.TrustedTenantID(summaryRequester()), windowStart, windowStart.Add(time.Hour), policy)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Requests != 1 || usage.Tokens != 256 {
		t.Fatalf("summary replay changed durable cost: %+v", usage)
	}
}

func TestSummaryQuotaRetainsUnknownProviderCost(t *testing.T) {
	evidence, report := summaryFixture()
	store := &recordingSummaryQuotaStore{}
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return domain.SummaryProviderResult{}, errors.New("synthetic provider failure")
	}))
	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback || len(store.settlements) != 1 {
		t.Fatalf("provider failure did not fall back with settlement: summary=%+v settlements=%+v", result.Summary, store.settlements)
	}
	settlement := store.settlements[0]
	if settlement.status != domain.QuotaUnknown || settlement.totalTokens != 256 || settlement.reasonCode != "summary_external_outcome_unknown" {
		t.Fatalf("unknown provider cost was not retained: %+v", settlement)
	}
}

func TestSummaryQuotaTreatsMissingModelTokenUsageAsUnknown(t *testing.T) {
	evidence, report := summaryFixture()
	store := &recordingSummaryQuotaStore{}
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return groundedSummaryResult(0, 0, 0), nil
	}))
	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback || len(store.settlements) != 1 {
		t.Fatalf("missing model usage did not fail closed: summary=%+v settlements=%+v", result.Summary, store.settlements)
	}
	if store.settlements[0].status != domain.QuotaUnknown || store.settlements[0].totalTokens != 256 || store.settlements[0].reasonCode != "summary_token_usage_invalid" {
		t.Fatalf("missing model usage was not retained as unknown: %+v", store.settlements[0])
	}
}

func TestSummaryQuotaRecordsOverReservationButRejectsOutput(t *testing.T) {
	evidence, report := summaryFixture()
	store := &recordingSummaryQuotaStore{}
	service := newQuotaSummaryService(t, store, 100, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return groundedSummaryResult(100, 30, 130), nil
	}))
	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback || len(store.settlements) != 1 {
		t.Fatalf("over-reservation output was not rejected: summary=%+v settlements=%+v", result.Summary, store.settlements)
	}
	if store.settlements[0].status != domain.QuotaSettled || store.settlements[0].totalTokens != 130 {
		t.Fatalf("actual overage was not durably settled: %+v", store.settlements[0])
	}
}

func TestSummaryQuotaSettlementFailureRejectsProviderOutput(t *testing.T) {
	evidence, report := summaryFixture()
	store := &recordingSummaryQuotaStore{settleErr: errors.New("synthetic ledger failure")}
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return groundedSummaryResult(100, 30, 130), nil
	}))
	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback || len(store.settlements) != 1 {
		t.Fatalf("unsettled provider output was accepted: summary=%+v settlements=%+v", result.Summary, store.settlements)
	}
}

func TestUnsafeSummaryInputDoesNotReserveQuota(t *testing.T) {
	evidence, report := summaryFixture()
	report.Findings[0].Statement = "Bearer abcdefghijklmnopqrstuvwxyz"
	store := &recordingSummaryQuotaStore{}
	providerCalls := 0
	service := newQuotaSummaryService(t, store, 256, summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		providerCalls++
		return groundedSummaryResult(0, 0, 0), nil
	}))
	result := service.Enrich(context.Background(), summaryRequester(), evidence, report)
	if providerCalls != 0 || len(store.reservations) != 0 || result.Summary == nil || result.Summary.Status != domain.SummaryFallback {
		t.Fatalf("unsafe input reached quota/provider: calls=%d reservations=%+v summary=%+v", providerCalls, store.reservations, result.Summary)
	}
}

func newQuotaSummaryService(t *testing.T, store ports.SummaryQuotaStore, reservedTokens int64, provider ports.ReportSummarizer) *SummaryService {
	t.Helper()
	service, err := NewSummaryService(provider, time.Second, func() time.Time {
		return time.Date(2026, 8, 20, 8, 0, 1, 0, time.UTC)
	}, WithSummaryQuota(store, domain.SummaryQuotaPolicy{
		Version: SummaryQuotaPolicyVersion, Window: time.Hour,
		MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: reservedTokens,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func groundedSummaryResult(inputTokens, outputTokens, totalTokens int64) domain.SummaryProviderResult {
	return domain.SummaryProviderResult{
		Draft: domain.SummaryDraft{
			Phenomenon: "当前窗口错误量显著高于基线。", PhenomenonEvidenceIDs: []string{"ev-current", "ev-baseline"},
			EvidenceNotes:       []domain.SummaryEvidenceNote{{Statement: "当前窗口 120 条，基线 20 条。", EvidenceIDs: []string{"ev-current", "ev-baseline"}}},
			RecommendationCodes: []string{"inspect_dependency"},
		},
		Mode: domain.SummaryModeModel, Provider: "test_provider", Model: "test-model",
		PromptVersion:     domain.EvidenceSummaryPromptVersion,
		PromptFingerprint: "4e3c70e2e9175733c5c41819f6c8c7f0af8c3c2b4c9f50f3df438b7517c2bb68",
		InputTokens:       inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens,
	}
}
