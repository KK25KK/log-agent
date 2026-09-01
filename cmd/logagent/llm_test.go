package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"logagent/internal/config"
	"logagent/internal/domain"
)

type llmSmokeProvider struct {
	result domain.SummaryProviderResult
	err    error
	calls  int
}

func (provider *llmSmokeProvider) Summarize(_ context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	provider.calls++
	if provider.err != nil {
		return domain.SummaryProviderResult{}, provider.err
	}
	result := provider.result
	result.Draft.Phenomenon = input.Findings[0].Statement
	result.Draft.PhenomenonEvidenceIDs = append([]string(nil), input.Findings[0].EvidenceIDs...)
	for _, evidence := range input.Evidence {
		result.Draft.EvidenceNotes = append(result.Draft.EvidenceNotes, domain.SummaryEvidenceNote{
			Statement: "该窗口提供了完整的合成错误计数证据。", EvidenceIDs: []string{evidence.ID},
		})
	}
	for _, recommendation := range input.Recommendations {
		result.Draft.RecommendationCodes = append(result.Draft.RecommendationCodes, recommendation.Code)
	}
	return result, nil
}

func TestCheckLLMConfigurationIsLocalOnly(t *testing.T) {
	loaded := llmSmokeConfig()
	result, err := checkLLMConfiguration(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "PASSED" || result.Provider != "volcengine_ark" || !result.APIKeyConfigured || result.NetworkCalls != 0 || !result.InputStorageDisabled {
		t.Fatalf("unexpected check result: %#v", result)
	}
	if strings.Contains(result.Model+result.BaseURL+result.PromptVersion, loaded.LLM.APIKey) {
		t.Fatal("llm-check leaked the API key")
	}
}

func TestExecuteLLMSmokeUsesOneProviderCallAndPrintsNoModelText(t *testing.T) {
	provider := &llmSmokeProvider{result: validLLMSmokeProviderResult()}
	result, err := executeLLMSmoke(context.Background(), llmSmokeConfig(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "PASSED" || result.ProviderCalls != 1 || provider.calls != 1 || result.ExternalSLSCalls != 0 || result.ExternalFeishuCalls != 0 || result.ModelTextPrinted {
		t.Fatalf("unexpected smoke result: %#v", result)
	}
	if result.SummaryMode != domain.SummaryModeModel || result.TotalTokens != 120 || result.EvidenceReferenceCount != 2 || len(result.RecommendationCodes) != 1 || result.RecommendationCodes[0] != "narrow_window_and_observe" {
		t.Fatalf("unexpected summary metadata: %#v", result)
	}
}

func TestExecuteLLMSmokeFailsClosedOnProviderError(t *testing.T) {
	provider := &llmSmokeProvider{err: errors.New("synthetic provider failure")}
	result, err := executeLLMSmoke(context.Background(), llmSmokeConfig(), provider)
	if err == nil {
		t.Fatal("want smoke failure")
	}
	if result.FailureCode != "model_summary_fallback" || result.ProviderFailureCode != "provider_error_other" ||
		result.ProviderCalls != 1 || result.SummaryStatus != domain.SummaryFallback || result.ModelTextPrinted {
		t.Fatalf("unexpected fail-closed result: %#v", result)
	}
}

func TestClassifyLLMSmokeProviderErrorExposesOnlyStableCode(t *testing.T) {
	if got := classifyLLMSmokeProviderError(errors.New("Volcengine Ark request rejected with status 400")); got != "ark_http_400" {
		t.Fatalf("unexpected HTTP diagnostic: %q", got)
	}
	if got := classifyLLMSmokeProviderError(errors.New("secret provider body")); got != "provider_error_other" || strings.Contains(got, "secret") {
		t.Fatalf("provider diagnostic leaked detail: %q", got)
	}
}

func llmSmokeConfig() config.Config {
	return config.Config{
		LLM: config.LLMConfig{
			Mode: "volcengine", APIKey: "test-key", Model: "doubao-seed-2-0-mini-260428",
			BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Timeout: 2 * time.Second,
		},
		LLMQuota: config.LLMQuotaConfig{
			Window: time.Hour, MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: 4096,
		},
	}
}

func validLLMSmokeProviderResult() domain.SummaryProviderResult {
	return domain.SummaryProviderResult{
		Mode: domain.SummaryModeModel, Provider: "volcengine_ark", Model: "doubao-seed-2-0-mini-260428",
		RequestID: "resp_smoke", PromptVersion: domain.EvidenceSummaryPromptVersion,
		PromptFingerprint: strings.Repeat("a", 64), InputTokens: 100, OutputTokens: 20, TotalTokens: 120, LatencyMillis: 80,
	}
}
