package main

import (
	"context"
	"testing"

	"logagent/internal/adapters/feishumock"
	"logagent/internal/domain"
)

func TestExecuteMockE2ECoversInboundAnalysisAndCardLifecycle(t *testing.T) {
	result, err := executeMockE2E(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Safety.ExternalNetworkCalls != 0 || result.Safety.CredentialsRequired {
		t.Fatalf("mock unexpectedly requires external access: %#v", result.Safety)
	}
	if !result.Feishu.FirstMessageCreated || !result.Feishu.DuplicateReplayDeduplicated {
		t.Fatalf("mock intake idempotency failed: %#v", result.Feishu)
	}
	wantOperations := []feishumock.DeliveryOperation{
		feishumock.OperationReply,
		feishumock.OperationPatch,
		feishumock.OperationPatch,
	}
	wantKinds := []domain.DeliveryKind{
		domain.DeliveryQueued,
		domain.DeliveryRunning,
		domain.DeliverySucceeded,
	}
	if len(result.Feishu.Deliveries) != len(wantKinds) {
		t.Fatalf("unexpected delivery count: %#v", result.Feishu.Deliveries)
	}
	for index, delivery := range result.Feishu.Deliveries {
		if delivery.Operation != wantOperations[index] || delivery.Kind != wantKinds[index] || delivery.CardMessageID != result.Feishu.CardMessageID {
			t.Fatalf("unexpected delivery %d: %#v", index, delivery)
		}
		if delivery.InvestigationStatus != domain.Status(wantKinds[index]) {
			t.Fatalf("delivery %d did not capture the live lifecycle state: %#v", index, delivery)
		}
	}
	if result.AlibabaSLS.Mode != "mock" || result.AlibabaSLS.LogicalObservations != 2 || result.AlibabaSLS.SchemaCalls != 1 || result.AlibabaSLS.BackendExecuteCalls != 2 || result.AlibabaSLS.ProviderAPICalls != 8 || result.AlibabaSLS.QueryAuditEvents != 4 || result.AlibabaSLS.QueryStepCheckpoints != 2 || result.AlibabaSLS.RawLogRowsReturned != 0 {
		t.Fatalf("unexpected mock SLS summary: %#v", result.AlibabaSLS)
	}
	if result.TenantQuota.Observations != 2 || result.TenantQuota.APICalls != 8 || result.TenantQuota.ProcessedBytes <= 0 || result.TenantQuota.CircuitOpen {
		t.Fatalf("unexpected mock tenant quota: %#v", result.TenantQuota)
	}
	if result.LLMQuota.Requests != 1 || result.LLMQuota.Tokens != 0 || result.LLMQuota.CircuitOpen {
		t.Fatalf("unexpected mock LLM quota: %#v", result.LLMQuota)
	}
	if result.LLMSummary.Mode != domain.SummaryModeMock || result.LLMSummary.Status != domain.SummaryGenerated || result.LLMSummary.Provider != "summary_mock" || result.LLMSummary.ExternalAPICalls != 0 || result.LLMSummary.CredentialsNeeded {
		t.Fatalf("unexpected mock LLM summary: %#v", result.LLMSummary)
	}
	if result.OperationalSignals.Mode != "mock" || result.OperationalSignals.SourceCalls != 1 || result.OperationalSignals.TimelineStatus != domain.TimelineComplete || result.OperationalSignals.Signals != 2 || result.OperationalSignals.TimelineItems != 3 {
		t.Fatalf("unexpected mock operational timeline: %#v", result.OperationalSignals)
	}
	if result.RunbookKnowledge.Mode != domain.RunbookGuidanceSourceSyntheticMock || result.RunbookKnowledge.SourceCalls != 1 || result.RunbookKnowledge.Status != domain.RunbookGuidanceComplete || result.RunbookKnowledge.Items != 1 || result.RunbookKnowledge.Steps != 3 {
		t.Fatalf("unexpected mock runbook knowledge: %#v", result.RunbookKnowledge)
	}
	if result.Investigation.Status != domain.StatusSucceeded || result.Investigation.Report == nil || result.Investigation.Report.Outcome != "spike_detected" {
		t.Fatalf("unexpected investigation: %#v", result.Investigation)
	}
	if result.Investigation.Request.Requester != (domain.Principal{AppID: "mock-feishu-app", TenantKey: "mock-tenant", UserID: "mock-feishu-user"}) {
		t.Fatalf("trusted mock Feishu identity was not persisted: %#v", result.Investigation.Request.Requester)
	}
	if result.Investigation.Report.GeneratedAt.Before(result.Investigation.CreatedAt) {
		t.Fatalf("report predates its investigation: created=%s generated=%s", result.Investigation.CreatedAt, result.Investigation.Report.GeneratedAt)
	}
	if result.Investigation.Report.CauseAnalysis == nil || len(result.Investigation.Report.CauseAnalysis.Hypotheses) != 1 {
		t.Fatalf("mock change correlation missing: %#v", result.Investigation.Report.CauseAnalysis)
	}
	if result.Investigation.Report.IncidentTimeline == nil || len(result.Investigation.Report.IncidentTimeline.Items) != 3 {
		t.Fatalf("mock cross-signal timeline missing: %#v", result.Investigation.Report.IncidentTimeline)
	}
	if result.Investigation.Report.RunbookGuidance == nil || result.Investigation.Report.RunbookGuidance.Status != domain.RunbookGuidanceComplete || len(result.Investigation.Report.RunbookGuidance.Items) != 1 {
		t.Fatalf("mock governed runbook guidance missing: %#v", result.Investigation.Report.RunbookGuidance)
	}
}

func TestExecuteMockE2ECountOnlyUsesMockLLMAndFeishuWithoutDimensionSources(t *testing.T) {
	result, err := executeMockE2EWithTemplate(context.Background(), domain.ErrorCountTemplateID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Safety.ExternalNetworkCalls != 0 || result.Safety.CredentialsRequired || result.Feishu.Mode != "mock" || result.LLMSummary.Mode != domain.SummaryModeMock || result.LLMSummary.ExternalAPICalls != 0 {
		t.Fatalf("count-only E2E crossed a real downstream boundary: %#v", result)
	}
	if result.AlibabaSLS.ProviderAPICalls != 4 || result.TenantQuota.APICalls != 4 || result.AlibabaSLS.RawLogRowsReturned != 0 {
		t.Fatalf("unexpected count-only query budget: sls=%#v quota=%#v", result.AlibabaSLS, result.TenantQuota)
	}
	if result.OperationalSignals.SourceCalls != 0 || result.OperationalSignals.TimelineStatus != domain.TimelineInconclusive || result.RunbookKnowledge.SourceCalls != 0 || result.RunbookKnowledge.Status != domain.RunbookGuidanceInconclusive {
		t.Fatalf("count-only E2E called dimensional sources: signals=%#v runbook=%#v", result.OperationalSignals, result.RunbookKnowledge)
	}
	if result.Investigation.Request.TemplateID != domain.ErrorCountTemplateID || result.Investigation.Report == nil || result.Investigation.Report.Summary == nil || result.Investigation.Report.Summary.PossibleCause != "" {
		t.Fatalf("unexpected count-only investigation: %#v", result.Investigation)
	}
	for _, evidence := range result.Investigation.Report.Evidence {
		if evidence.APICalls != 2 || evidence.TopError != "" || len(evidence.ErrorPatterns) != 0 || len(evidence.Instances) != 0 {
			t.Fatalf("count-only evidence leaked dimensions: %#v", evidence)
		}
	}
}
