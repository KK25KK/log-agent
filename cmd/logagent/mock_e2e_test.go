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
}
