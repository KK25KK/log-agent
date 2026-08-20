package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

type summaryProviderFunc func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error)

func (function summaryProviderFunc) Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	return function(ctx, input)
}

func TestSummaryServiceAcceptsOnlyGroundedProviderDraft(t *testing.T) {
	evidence, report := summaryFixture()
	now := report.GeneratedAt.Add(time.Second)
	digest := sha256.Sum256([]byte("test prompt"))
	service, err := NewSummaryService(summaryProviderFunc(func(_ context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
		if len(input.Evidence) != 2 || len(input.Recommendations) != 1 {
			t.Fatalf("unexpected governed input: %#v", input)
		}
		return domain.SummaryProviderResult{
			Draft: domain.SummaryDraft{
				Phenomenon: "当前窗口错误量显著高于基线。", PhenomenonEvidenceIDs: []string{"ev-current", "ev-baseline"},
				EvidenceNotes:       []domain.SummaryEvidenceNote{{Statement: "当前窗口 120 条，基线 20 条。", EvidenceIDs: []string{"ev-current", "ev-baseline"}}},
				RecommendationCodes: []string{"inspect_dependency"},
			},
			Mode: domain.SummaryModeModel, Provider: "test_provider", Model: "test-model",
			PromptVersion: domain.EvidenceSummaryPromptVersion, PromptFingerprint: hex.EncodeToString(digest[:]),
			InputTokens: 100, OutputTokens: 30, TotalTokens: 130, LatencyMillis: 12,
		}, nil
	}), time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	result := service.Enrich(context.Background(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryGenerated || result.Summary.Mode != domain.SummaryModeModel {
		t.Fatalf("missing generated summary: %#v", result.Summary)
	}
	if len(result.Summary.NextSteps) != 1 || result.Summary.NextSteps[0].Statement != report.Recommendations[0].Statement {
		t.Fatalf("next step did not resolve from deterministic report: %#v", result.Summary.NextSteps)
	}
	if err := ValidateEngineOutput(report.InvestigationID, evidence, result); err != nil {
		t.Fatalf("enriched report rejected by production validator: %v", err)
	}
}

func TestSummaryServiceFallsBackWithoutFailingInvestigation(t *testing.T) {
	evidence, report := summaryFixture()
	original, _ := json.Marshal(report)
	service, err := NewSummaryService(summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return domain.SummaryProviderResult{}, errors.New("provider unavailable with secret Bearer must-not-leak")
	}), time.Second, func() time.Time { return report.GeneratedAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	result := service.Enrich(context.Background(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback || result.Summary.Mode != domain.SummaryModeFallback {
		t.Fatalf("want deterministic fallback, got %#v", result.Summary)
	}
	withoutSummary := result
	withoutSummary.Summary = nil
	got, _ := json.Marshal(withoutSummary)
	if string(got) != string(original) {
		t.Fatal("summary failure changed deterministic report fields")
	}
	encoded, _ := json.Marshal(result.Summary)
	if strings.Contains(string(encoded), "must-not-leak") || strings.Contains(string(encoded), "Bearer") {
		t.Fatalf("provider error leaked into fallback: %s", encoded)
	}
}

func TestSummaryServiceRejectsInventedOrUnsafeModelOutput(t *testing.T) {
	evidence, report := summaryFixture()
	digest := sha256.Sum256([]byte("test prompt"))
	service, err := NewSummaryService(summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		return domain.SummaryProviderResult{
			Draft: domain.SummaryDraft{
				Phenomenon: "立即执行 kubectl 删除生产实例", PhenomenonEvidenceIDs: []string{"ev-current"},
				EvidenceNotes:       []domain.SummaryEvidenceNote{{Statement: "伪造说明", EvidenceIDs: []string{"ev-unknown"}}},
				RecommendationCodes: []string{"invented_action"},
			},
			Mode: domain.SummaryModeModel, Provider: "test_provider", Model: "test-model",
			PromptVersion: domain.EvidenceSummaryPromptVersion, PromptFingerprint: hex.EncodeToString(digest[:]),
		}, nil
	}), time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Enrich(context.Background(), evidence, report)
	if result.Summary == nil || result.Summary.Status != domain.SummaryFallback {
		t.Fatalf("unsafe draft must become fallback: %#v", result.Summary)
	}
}

func TestSummaryServiceDoesNotCallProviderForSensitiveOutboundInput(t *testing.T) {
	evidence, report := summaryFixture()
	report.Findings[0].Statement = "Bearer abcdefghijklmnopqrstuvwxyz"
	called := false
	service, err := NewSummaryService(summaryProviderFunc(func(context.Context, domain.SummaryInput) (domain.SummaryProviderResult, error) {
		called = true
		return domain.SummaryProviderResult{}, nil
	}), time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Enrich(context.Background(), evidence, report)
	if called || result.Summary == nil || result.Summary.Status != domain.SummaryFallback {
		t.Fatalf("sensitive input crossed provider boundary: called=%v summary=%#v", called, result.Summary)
	}
}

func TestSummaryInputExcludesIdentityPhysicalQueriesAndRawLogs(t *testing.T) {
	_, report := summaryFixture()
	report.Evidence[0].ResourceID = "project/logstore/private"
	report.Evidence[0].QueryID = "query-secret"
	report.Evidence[0].QuerySpecHash = "hash-secret"
	payload, err := json.Marshal(BuildSummaryInput(report))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"project/logstore/private", "query-secret", "hash-secret", "raw_log", "requester"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("governed input leaked %q: %s", forbidden, payload)
		}
	}
}

func summaryFixture() ([]domain.Evidence, domain.Report) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	evidence := []domain.Evidence{
		{ID: "ev-current", Name: "current", QueryID: "query-current", QuerySpecHash: "hash-current", Complete: true, ErrorCount: 120, TopError: "payment_timeout", TopErrorCount: 90},
		{ID: "ev-baseline", Name: "baseline", QueryID: "query-baseline", QuerySpecHash: "hash-baseline", Complete: true, ErrorCount: 20, TopError: "payment_timeout", TopErrorCount: 5},
	}
	report := domain.Report{
		InvestigationID: "inv-summary", Outcome: "spike_detected", GeneratedAt: now,
		Findings:        []domain.Finding{{Code: "error_spike", Statement: "错误日志较基线显著增长。", Confidence: .95, Conclusive: true, EvidenceIDs: []string{"ev-current", "ev-baseline"}}},
		Recommendations: []domain.Recommendation{{Code: "inspect_dependency", Statement: "检查依赖超时指标。", EvidenceIDs: []string{"ev-current"}}},
		Evidence:        append([]domain.Evidence(nil), evidence...),
	}
	return evidence, report
}
