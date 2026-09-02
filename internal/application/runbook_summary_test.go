package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestSummaryInputExcludesRunbookGuidance(t *testing.T) {
	evidence, report := summaryFixture()
	const sentinel = "SOPSENTINELMUSTNOTREACHMODEL"
	for index := range evidence {
		evidence[index].ResourceID = "mock/order/prod"
		report.Evidence[index].ResourceID = "mock/order/prod"
	}
	applyRunbookGovernanceFixture(evidence, report.GeneratedAt, "mock/order/prod")
	report.Evidence = append([]domain.Evidence(nil), evidence...)
	report.Recommendations[0] = domain.Recommendation{
		Code: "inspect_top_error_pattern", Statement: "核对主要错误模式。",
		EvidenceIDs: []string{"ev-current", "ev-baseline"},
	}
	entry := domain.RunbookEntry{
		ID: "rb-sentinel", Revision: "r1", ResourceID: "mock/order/prod",
		Title: sentinel, OwnerTeam: "test-owner", UpdatedAt: report.GeneratedAt,
		MatchedRecommendationCodes: []string{report.Recommendations[0].Code},
		Steps:                      []domain.RunbookStep{runbookTestStep("verify-sentinel", domain.RunbookStepCodeVerifyErrorPattern)},
	}
	fingerprint, err := domain.RunbookEntryFingerprint(entry)
	if err != nil {
		t.Fatal(err)
	}
	report.RunbookGuidance = &domain.RunbookGuidance{
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
	}
	called := false
	service, err := NewSummaryService(summaryProviderFunc(func(_ context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
		called = true
		payload, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(payload), sentinel) || strings.Contains(string(payload), "runbook") {
			t.Fatalf("runbook guidance crossed the LLM input boundary: %s", payload)
		}
		return domain.SummaryProviderResult{}, errors.New("intentional provider stop")
	}), time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	result := service.Enrich(context.Background(), domain.Principal{}, evidence, report)
	if !called {
		t.Fatal("summary provider was not called")
	}
	if result.RunbookGuidance == nil || result.RunbookGuidance.Items[0].Title != sentinel {
		t.Fatal("summary degradation changed the independent runbook projection")
	}
}

func TestSummaryInputExcludesCodeEvidence(t *testing.T) {
	_, report := summaryFixture()
	const sentinel = "PRIVATECODESENTINELMUSTNOTREACHMODEL"
	report.CodeInvestigation = &domain.CodeInvestigation{
		Version: domain.CodeEvidenceVersion, Status: domain.CodeInvestigationComplete, Complete: true,
		Deployment: &domain.DeploymentEvidence{RepositoryID: "dam", CommitSHA: strings.Repeat("a", 40)},
		Matches:    []domain.CodeMatch{{File: "internal/private.go", Snippet: sentinel}},
	}
	report.JointRCA = &domain.JointRCA{
		Version: domain.JointRCAVersion, Status: domain.JointRCAComplete, HumanReviewOnly: true,
		Candidates: []domain.JointRCACandidate{{Statement: sentinel, File: "internal/private.go"}},
	}
	payload, err := json.Marshal(BuildSummaryInput(report))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), sentinel) || strings.Contains(string(payload), "internal/private.go") || strings.Contains(string(payload), "commit_sha") || strings.Contains(string(payload), "joint_rca") {
		t.Fatalf("code evidence crossed the LLM input boundary: %s", payload)
	}
}
