package application

import (
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestValidateEngineOutputRejectsForgedRunbookGuidance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.RunbookGuidance)
		want   string
	}{
		{
			name: "unknown evidence reference",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].EvidenceIDs = []string{"ev-forged"}
			},
			want: "fabricated evidence references",
		},
		{
			name: "unknown recommendation reference",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].RecommendationCodes = []string{"unknown_recommendation"}
			},
			want: "outside the query",
		},
		{
			name: "fabricated fingerprint",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].Fingerprint = strings.Repeat("0", 64)
			},
			want: "fabricated fingerprint",
		},
		{
			name: "automatic execution mode",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].ExecutionMode = "AUTO_EXECUTE"
			},
			want: "HUMAN_REVIEW_ONLY",
		},
		{
			name: "dangerous step",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].Steps[0].Instruction = "执行命令并重启实例。"
			},
			want: "requires its canonical instruction",
		},
		{
			name: "unknown step code",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].Steps[0].Code = "FUTURE_ACTION"
			},
			want: "unsupported runbook step code",
		},
		{
			name: "future update time",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items[0].UpdatedAt = guidance.Items[0].UpdatedAt.Add(maxRunbookFutureSkew + time.Second)
			},
			want: "future update time",
		},
		{
			name: "duplicate item",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Items = append(guidance.Items, guidance.Items[0])
			},
			want: "duplicate runbook guidance entry ID",
		},
		{
			name: "same entry with another revision",
			mutate: func(guidance *domain.RunbookGuidance) {
				second := guidance.Items[0]
				second.Revision = "r2"
				guidance.Items = append(guidance.Items, second)
			},
			want: "duplicate runbook guidance entry ID",
		},
		{
			name: "complete status with incomplete source",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.SourceComplete = false
			},
			want: "inconsistent source metadata",
		},
		{
			name: "inconclusive status with complete source",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Status = domain.RunbookGuidanceInconclusive
			},
			want: "inconsistent source metadata",
		},
		{
			name: "skipped despite trigger",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.Status = domain.RunbookGuidanceSkippedNoTrigger
			},
			want: "skipped despite",
		},
		{
			name: "unsupported method version",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.MethodVersion = "future-version"
			},
			want: "unsupported method version",
		},
		{
			name: "provider-authored data source",
			mutate: func(guidance *domain.RunbookGuidance) {
				guidance.DataSource = "PROVIDER_REPORTED_REAL"
			},
			want: "unsupported runbook guidance data source",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, report := validRunbookValidationFixture(t)
			test.mutate(report.RunbookGuidance)
			err := ValidateEngineOutput(report.InvestigationID, evidence, report)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateEngineOutputAcceptsGroundedHumanOnlyRunbookGuidance(t *testing.T) {
	evidence, report := validRunbookValidationFixture(t)
	if err := ValidateEngineOutput(report.InvestigationID, evidence, report); err != nil {
		t.Fatalf("valid governed guidance rejected: %v", err)
	}
}

func validRunbookValidationFixture(t *testing.T) ([]domain.Evidence, domain.Report) {
	t.Helper()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	evidence := []domain.Evidence{
		{ID: "ev-current", QueryID: "query-current", QuerySpecHash: "hash-current", ResourceID: "mock/order/prod", Name: "current", Complete: true, ErrorCount: 120, ErrorPatterns: []domain.CountBucket{{Label: "payment_timeout", Count: 120}}, Instances: []domain.CountBucket{{Label: "order-pod-a", Count: 120}}},
		{ID: "ev-baseline", QueryID: "query-baseline", QuerySpecHash: "hash-baseline", ResourceID: "mock/order/prod", Name: "baseline", Complete: true, ErrorCount: 20, ErrorPatterns: []domain.CountBucket{{Label: "payment_timeout", Count: 20}}, Instances: []domain.CountBucket{{Label: "order-pod-a", Count: 20}}},
	}
	applyRunbookGovernanceFixture(evidence, now, "mock/order/prod")
	entry := domain.RunbookEntry{
		ID: "rb-error-triage", Revision: "r1", ResourceID: "mock/order/prod",
		Title: "错误突增人工核查", OwnerTeam: "order-oncall", UpdatedAt: now,
		MatchedRecommendationCodes: []string{"inspect_top_error_pattern"},
		Steps:                      []domain.RunbookStep{runbookTestStep("verify-pattern", domain.RunbookStepCodeVerifyErrorPattern)},
	}
	fingerprint, err := domain.RunbookEntryFingerprint(entry)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.Report{
		InvestigationID: "inv-runbook-validation", Outcome: "spike_detected", GeneratedAt: now,
		Findings: []domain.Finding{{
			Code: "error_spike", Statement: "错误量显著增长。", Confidence: .95, Conclusive: true,
			EvidenceIDs: []string{"ev-current", "ev-baseline"},
		}},
		Recommendations: []domain.Recommendation{{
			Code: "inspect_top_error_pattern", Statement: "核对主要错误模式。", EvidenceIDs: []string{"ev-current", "ev-baseline"},
		}},
		Evidence: evidence,
		RunbookGuidance: &domain.RunbookGuidance{
			Status: domain.RunbookGuidanceComplete, DataSource: domain.RunbookGuidanceSourceSyntheticMock,
			MethodVersion: domain.RunbookGuidanceVersion,
			SourceVersion: "mock-runbook-v1", SourceComplete: true,
			Items: []domain.RunbookGuidanceItem{{
				EntryID: entry.ID, Revision: entry.Revision, Fingerprint: fingerprint,
				Title: entry.Title, Owner: entry.OwnerTeam, UpdatedAt: entry.UpdatedAt,
				RecommendationCodes: append([]string(nil), entry.MatchedRecommendationCodes...),
				EvidenceIDs:         []string{"ev-baseline", "ev-current"},
				Steps:               append([]domain.RunbookStep(nil), entry.Steps...),
				ExecutionMode:       domain.RunbookExecutionHumanReviewOnly,
			}},
		},
	}
	return evidence, report
}
