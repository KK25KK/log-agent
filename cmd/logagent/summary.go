package main

import (
	"fmt"
	"time"

	"logagent/internal/adapters/summarymock"
	"logagent/internal/adapters/volcark"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/ports"
)

func buildSummaryService(loaded config.Config, now func() time.Time) (*application.SummaryService, error) {
	if loaded.LLM.Mode == "disabled" {
		return nil, nil
	}
	var provider ports.ReportSummarizer
	switch loaded.LLM.Mode {
	case "mock":
		provider = summarymock.New()
	case "volcengine":
		adapter, err := volcark.New(volcark.Config{
			APIKey: loaded.LLM.APIKey, Model: loaded.LLM.Model,
			BaseURL: loaded.LLM.BaseURL, Timeout: loaded.LLM.Timeout,
		})
		if err != nil {
			return nil, fmt.Errorf("configure Volcengine Ark summary adapter: %w", err)
		}
		provider = adapter
	default:
		return nil, fmt.Errorf("unsupported LLM mode %q", loaded.LLM.Mode)
	}
	service, err := application.NewSummaryService(provider, loaded.LLM.Timeout, now)
	if err != nil {
		return nil, fmt.Errorf("configure report summary service: %w", err)
	}
	return service, nil
}
