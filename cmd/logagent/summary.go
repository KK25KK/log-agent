package main

import (
	"fmt"
	"time"

	"logagent/internal/adapters/summarymock"
	"logagent/internal/adapters/volcark"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

func buildSummaryService(loaded config.Config, quotaStore ports.SummaryQuotaStore, now func() time.Time) (*application.SummaryService, error) {
	provider, err := buildSummaryProvider(loaded)
	if err != nil || provider == nil {
		return nil, err
	}
	return buildSummaryServiceWithProvider(loaded, quotaStore, now, provider)
}

func buildSummaryProvider(loaded config.Config) (ports.ReportSummarizer, error) {
	switch loaded.LLM.Mode {
	case "disabled":
		return nil, nil
	case "mock":
		return summarymock.New(), nil
	case "volcengine":
		adapter, err := volcark.New(volcark.Config{
			APIKey: loaded.LLM.APIKey, Model: loaded.LLM.Model,
			BaseURL: loaded.LLM.BaseURL, Timeout: loaded.LLM.Timeout,
		})
		if err != nil {
			return nil, fmt.Errorf("configure Volcengine Ark summary adapter: %w", err)
		}
		return adapter, nil
	default:
		return nil, fmt.Errorf("unsupported LLM mode %q", loaded.LLM.Mode)
	}
}

func buildSummaryServiceWithProvider(loaded config.Config, quotaStore ports.SummaryQuotaStore, now func() time.Time, provider ports.ReportSummarizer) (*application.SummaryService, error) {
	if quotaStore == nil {
		return nil, fmt.Errorf("summary quota store is required when LLM summaries are enabled")
	}
	policy := domain.SummaryQuotaPolicy{
		Version: application.SummaryQuotaPolicyVersion,
		Window:  loaded.LLMQuota.Window, MaxRequests: loaded.LLMQuota.MaxRequests,
		MaxTokens: loaded.LLMQuota.MaxTokens, ReservedTokensPerRequest: loaded.LLMQuota.ReservedTokensPerRequest,
	}
	service, err := application.NewSummaryService(provider, loaded.LLM.Timeout, now, application.WithSummaryQuota(quotaStore, policy))
	if err != nil {
		return nil, fmt.Errorf("configure report summary service: %w", err)
	}
	return service, nil
}
