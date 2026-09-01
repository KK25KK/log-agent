package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

const llmSmokeModelInput = "SYNTHETIC_COUNT_ONLY"

type llmCheckResult struct {
	Status               string `json:"status"`
	Mode                 string `json:"mode"`
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	BaseURL              string `json:"base_url"`
	Timeout              string `json:"timeout"`
	PromptVersion        string `json:"prompt_version"`
	APIKeyConfigured     bool   `json:"api_key_configured"`
	InputStorageDisabled bool   `json:"input_storage_disabled"`
	NetworkCalls         int    `json:"network_calls"`
}

type llmSmokeResult struct {
	Status                 string               `json:"status"`
	FailureCode            string               `json:"failure_code,omitempty"`
	ProviderFailureCode    string               `json:"provider_failure_code,omitempty"`
	InputSource            string               `json:"input_source"`
	ProviderCalls          int64                `json:"provider_calls"`
	ExternalSLSCalls       int                  `json:"external_sls_calls"`
	ExternalFeishuCalls    int                  `json:"external_feishu_calls"`
	ModelTextPrinted       bool                 `json:"model_text_printed"`
	SummaryStatus          domain.SummaryStatus `json:"summary_status,omitempty"`
	SummaryMode            domain.SummaryMode   `json:"summary_mode,omitempty"`
	Provider               string               `json:"provider,omitempty"`
	Model                  string               `json:"model,omitempty"`
	RequestID              string               `json:"request_id,omitempty"`
	PromptVersion          string               `json:"prompt_version,omitempty"`
	PromptFingerprint      string               `json:"prompt_fingerprint,omitempty"`
	InputTokens            int64                `json:"input_tokens,omitempty"`
	OutputTokens           int64                `json:"output_tokens,omitempty"`
	TotalTokens            int64                `json:"total_tokens,omitempty"`
	LatencyMillis          int64                `json:"latency_millis,omitempty"`
	EvidenceReferenceCount int                  `json:"evidence_reference_count,omitempty"`
	RecommendationCodes    []string             `json:"recommendation_codes,omitempty"`
}

type countingSummarizer struct {
	inner           ports.ReportSummarizer
	calls           atomic.Int64
	lastFailureCode string
}

func (summarizer *countingSummarizer) Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	summarizer.calls.Add(1)
	result, err := summarizer.inner.Summarize(ctx, input)
	summarizer.lastFailureCode = classifyLLMSmokeProviderError(err)
	return result, err
}

func classifyLLMSmokeProviderError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "Volcengine Ark request rejected with status "):
		status := strings.TrimPrefix(message, "Volcengine Ark request rejected with status ")
		if status == "400" || status == "401" || status == "403" || status == "404" || status == "408" || status == "429" || status == "500" || status == "502" || status == "503" || status == "504" {
			return "ark_http_" + status
		}
		return "ark_http_other"
	case message == "Volcengine Ark request failed":
		return "ark_transport_failed"
	case message == "Volcengine Ark response exceeds limit":
		return "ark_response_too_large"
	case message == "Volcengine Ark response is incomplete":
		return "ark_response_incomplete"
	case message == "Volcengine Ark response has no output text":
		return "ark_output_text_missing"
	case message == "Volcengine Ark returned an invalid summary object":
		return "ark_summary_json_invalid"
	default:
		return "provider_error_other"
	}
}

func runLLMCheck(loaded config.Config) error {
	result, err := checkLLMConfiguration(loaded)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func checkLLMConfiguration(loaded config.Config) (llmCheckResult, error) {
	if loaded.LLM.Mode != "volcengine" {
		return llmCheckResult{}, errors.New("llm-check requires LOG_AGENT_LLM_MODE=volcengine")
	}
	if _, err := buildSummaryProvider(loaded); err != nil {
		return llmCheckResult{}, err
	}
	return llmCheckResult{
		Status: "PASSED", Mode: loaded.LLM.Mode, Provider: "volcengine_ark",
		Model: loaded.LLM.Model, BaseURL: loaded.LLM.BaseURL, Timeout: loaded.LLM.Timeout.String(),
		PromptVersion: domain.EvidenceSummaryPromptVersion, APIKeyConfigured: loaded.LLM.APIKey != "",
		InputStorageDisabled: true, NetworkCalls: 0,
	}, nil
}

func runLLMSmoke(loaded config.Config) error {
	provider, err := buildSummaryProvider(loaded)
	if err != nil {
		return err
	}
	result, smokeErr := executeLLMSmoke(context.Background(), loaded, provider)
	if err := printJSON(result); err != nil {
		return err
	}
	return smokeErr
}

func executeLLMSmoke(ctx context.Context, loaded config.Config, provider ports.ReportSummarizer) (llmSmokeResult, error) {
	result := llmSmokeResult{
		Status: "FAILED", InputSource: llmSmokeModelInput,
		ExternalSLSCalls: 0, ExternalFeishuCalls: 0, ModelTextPrinted: false,
	}
	if loaded.LLM.Mode != "volcengine" || provider == nil {
		result.FailureCode = "real_provider_not_configured"
		return result, errors.New("llm-smoke requires the explicit Volcengine provider")
	}

	store, err := sqlite.Open(":memory:")
	if err != nil {
		return result, err
	}
	defer store.Close()

	now := time.Now().UTC()
	requester := domain.Principal{AppID: "llm-smoke", TenantKey: "synthetic", UserID: "operator"}
	engine, err := eino.New(ctx, &slsmock.Executor{}, func() time.Time { return now })
	if err != nil {
		return result, err
	}
	investigationID := fmt.Sprintf("llm_smoke_%d", now.UnixNano())
	evidence, report, err := engine.Run(ctx, investigationID, domain.InvestigationRequest{
		Service: "llm-smoke-service", Environment: "test", TemplateID: domain.ErrorCountTemplateID,
		StartTime: now.Add(-10 * time.Minute), EndTime: now, Requester: requester,
	})
	if err != nil {
		return result, err
	}

	counted := &countingSummarizer{inner: provider}
	service, err := buildSummaryServiceWithProvider(loaded, store, time.Now, counted)
	if err != nil {
		return result, err
	}
	enriched := service.Enrich(ctx, requester, evidence, report)
	result.ProviderCalls = counted.calls.Load()
	result.ProviderFailureCode = counted.lastFailureCode
	if enriched.Summary == nil {
		result.FailureCode = "summary_missing"
		return result, errors.New("llm-smoke did not produce a summary")
	}
	copySummaryMetadata(&result, *enriched.Summary)
	if err := application.ValidateEngineOutput(investigationID, evidence, enriched); err != nil {
		result.FailureCode = "summary_contract_invalid"
		return result, errors.New("llm-smoke summary failed the production contract")
	}
	if result.ProviderCalls != 1 {
		result.FailureCode = "provider_call_count_invalid"
		return result, errors.New("llm-smoke must make exactly one provider call")
	}
	if enriched.Summary.Status != domain.SummaryGenerated || enriched.Summary.Mode != domain.SummaryModeModel || enriched.Summary.Provider != "volcengine_ark" {
		result.FailureCode = "model_summary_fallback"
		return result, errors.New("llm-smoke returned a fallback or unexpected provider summary")
	}
	result.Status = "PASSED"
	return result, nil
}

func copySummaryMetadata(result *llmSmokeResult, summary domain.ReportSummary) {
	result.SummaryStatus = summary.Status
	result.SummaryMode = summary.Mode
	result.Provider = summary.Provider
	result.Model = summary.Model
	result.RequestID = summary.RequestID
	result.PromptVersion = summary.PromptVersion
	result.PromptFingerprint = summary.PromptFingerprint
	result.InputTokens = summary.InputTokens
	result.OutputTokens = summary.OutputTokens
	result.TotalTokens = summary.TotalTokens
	result.LatencyMillis = summary.LatencyMillis
	result.EvidenceReferenceCount = len(summary.PhenomenonEvidenceIDs)
	for _, step := range summary.NextSteps {
		result.RecommendationCodes = append(result.RecommendationCodes, step.Code)
	}
}
