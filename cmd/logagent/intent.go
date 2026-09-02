package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/adapters/intentmock"
	"logagent/internal/adapters/resourcecatalog"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/adapters/volcark"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
	"logagent/internal/ports"
)

func buildIntentResolutionService(loaded config.Config, store *sqlite.Store, intake *application.Intake) (*application.IntentResolutionService, error) {
	if loaded.Intent.Mode == "disabled" {
		return nil, nil
	}
	catalog, err := resourcecatalog.Load(loaded.SLS.CatalogPath)
	if err != nil {
		return nil, err
	}
	var parser ports.InvestigationIntentParser
	provider := ""
	model := ""
	promptHash := ""
	switch loaded.Intent.Mode {
	case "mock":
		parser = intentmock.New()
		provider = intentmock.Provider
		model = intentmock.Model
		promptHash = intentmock.PromptFingerprint()
	case "volcengine":
		arkParser, parserErr := volcark.NewIntentParser(volcark.IntentConfig{
			APIKey: loaded.Intent.APIKey, Model: loaded.Intent.Model, BaseURL: loaded.Intent.BaseURL,
			Timeout: loaded.Intent.Timeout, MaxOutputBytes: loaded.Intent.MaxOutputBytes, MaxTokens: loaded.Intent.MaxTokens,
		})
		if parserErr != nil {
			return nil, parserErr
		}
		parser = arkParser
		provider = "volcengine_ark"
		model = loaded.Intent.Model
		promptHash = arkParser.PromptFingerprint()
	default:
		return nil, errors.New("unsupported intent parser mode")
	}
	return application.NewIntentResolutionService(store, parser, catalog, intake, application.IntentPolicy{
		MaxInputRunes: loaded.Intent.MaxInputRunes, MinConfidence: loaded.Intent.MinConfidence,
		MaxWindow: loaded.SLS.MaxWindow, IngestionGrace: loaded.SLS.IngestionGrace,
		ResolutionTTL: loaded.Intent.ResolutionTTL, Provider: provider, Model: model, PromptHash: promptHash,
	}, application.WithIntentQuota(store, domain.IntentQuotaPolicy{
		Version: application.IntentQuotaPolicyVersion, Window: loaded.IntentQuota.Window,
		MaxRequests: loaded.IntentQuota.MaxRequests, MaxTokens: loaded.IntentQuota.MaxTokens,
		ReservedTokensPerRequest: loaded.IntentQuota.ReservedTokensPerRequest,
	}))
}

func runIntentCheck(loaded config.Config) error {
	if loaded.Intent.Mode == "disabled" {
		return errors.New("intent-check requires LOG_AGENT_INTENT_MODE=mock or volcengine")
	}
	catalog, err := resourcecatalog.Load(loaded.SLS.CatalogPath)
	if err != nil {
		return err
	}
	principal := domain.Principal{AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID}
	capabilities, err := catalog.ListAllowedCapabilities(context.Background(), principal)
	if err != nil {
		return err
	}
	if len(capabilities) == 0 {
		return errors.New("intent-check found no error_count_v1 capability allowed for the fixed Web principal")
	}
	return printJSON(map[string]any{
		"status": "READY", "mode": loaded.Intent.Mode, "model": loaded.Intent.Model,
		"prompt_version": domain.IntentPromptVersion, "capabilities": capabilities,
		"network_calls": 0, "sls_calls": 0,
	})
}

func runIntentSmoke(loaded config.Config, problem string) error {
	if loaded.Intent.Mode != "volcengine" {
		return errors.New("intent-smoke requires LOG_AGENT_INTENT_MODE=volcengine")
	}
	store, err := sqlite.Open(":memory:")
	if err != nil {
		return err
	}
	defer store.Close()
	intake := application.NewIntake(store)
	service, err := buildIntentResolutionService(loaded, store, intake)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	resolution, _, err := service.Resolve(context.Background(), domain.InboundMessage{
		AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID,
		MessageID: fmt.Sprintf("intent-smoke-%d", now.UnixNano()), ChatID: loaded.Web.ChatID,
		Text: problem, ReceivedAt: now,
	}, problem)
	if err != nil {
		return err
	}
	if err := printJSON(map[string]any{
		"status": "COMPLETED", "resolution": resolution, "confirmed": false, "sls_calls": 0,
	}); err != nil {
		return err
	}
	if resolution.Status != domain.IntentResolutionResolved {
		return fmt.Errorf("intent smoke completed but result is not confirmable: %s", resolution.Status)
	}
	return nil
}
