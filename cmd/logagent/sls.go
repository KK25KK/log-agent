package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logagent/internal/adapters/aliyunsls"
	"logagent/internal/adapters/changecatalog"
	"logagent/internal/adapters/resourcecatalog"
	"logagent/internal/adapters/slsmock"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	queryapp "logagent/internal/application/query"
	"logagent/internal/config"
	"logagent/internal/domain"
	"logagent/internal/ids"
	"logagent/internal/ports"
)

func newMockExecutor() application.GovernedSLSExecutor {
	return &slsmock.Executor{}
}

type workerGovernanceStore interface {
	ports.QueryAuditor
	ports.QueryQuotaStore
}

func buildWorkerExecutor(config config.Config, store workerGovernanceStore) (application.GovernedSLSExecutor, error) {
	var base application.GovernedSLSExecutor
	if config.SLS.Mode == "mock" {
		base = newMockExecutor()
	} else {
		catalog, backend, err := buildAliyunDependencies(config)
		if err != nil {
			return nil, err
		}
		gateway, err := queryapp.NewGateway(catalog, backend, store, queryBudget(config.SLS))
		if err != nil {
			return nil, err
		}
		base = gateway
	}
	return application.NewQuotaExecutor(base, store, tenantQuotaPolicy(config.Quota), time.Now)
}

func buildChangeSource(config config.Config) (ports.ChangeSource, error) {
	if config.ChangeCatalogPath == "" {
		disabled := changecatalog.NewDisabled()
		return disabled, nil
	}
	return changecatalog.Load(config.ChangeCatalogPath)
}

func buildAliyunDependencies(config config.Config) (*resourcecatalog.Catalog, *aliyunsls.Backend, error) {
	catalog, err := resourcecatalog.Load(config.SLS.CatalogPath)
	if err != nil {
		return nil, nil, err
	}
	backend, err := aliyunsls.New(aliyunsls.Config{
		CredentialMode:  config.SLS.CredentialMode,
		AccessKeyID:     config.SLS.AccessKeyID,
		AccessKeySecret: config.SLS.AccessKeySecret,
		SecurityToken:   config.SLS.SecurityToken,
		ECSRAMRoleName:  config.SLS.ECSRAMRoleName,
		RequestTimeout:  config.SLS.RequestTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	return catalog, backend, nil
}

func queryBudget(config config.SLSConfig) queryapp.Budget {
	return queryapp.Budget{
		MaxWindow:         config.MaxWindow,
		IngestionGrace:    config.IngestionGrace,
		Timeout:           config.QueryTimeout,
		MaxRows:           config.MaxRows,
		MaxAPICalls:       config.MaxAPICalls,
		MaxProcessedBytes: config.MaxProcessedBytes,
		MaxConcurrent:     config.MaxConcurrent,
		SchemaTTL:         config.SchemaTTL,
	}
}

func tenantQuotaPolicy(config config.QuotaConfig) domain.TenantQuotaPolicy {
	return domain.TenantQuotaPolicy{
		Version:                     application.TenantQuotaPolicyVersion,
		Window:                      config.Window,
		MaxObservations:             config.MaxObservations,
		MaxAPICalls:                 config.MaxAPICalls,
		MaxProcessedBytes:           config.MaxProcessedBytes,
		ReservedBytesPerObservation: config.ReservedBytesPerObservation,
	}
}

func runSLSCheck(config config.Config) error {
	if config.SLS.Mode != "aliyun" {
		return errors.New("sls-check requires LOG_AGENT_SLS_MODE=aliyun")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	catalog, backend, err := buildAliyunDependencies(config)
	if err != nil {
		return err
	}
	checks, err := backend.CheckResources(ctx, catalog.Resources())
	if err != nil {
		return err
	}
	return printJSON(checks)
}

func runSLSSmoke(config config.Config, service, environment, rawWindow string) error {
	if config.SLS.Mode != "aliyun" {
		return errors.New("sls-smoke requires LOG_AGENT_SLS_MODE=aliyun")
	}
	principal := domain.Principal{
		AppID: config.SmokePrincipal.AppID, TenantKey: config.SmokePrincipal.TenantKey, UserID: config.SmokePrincipal.UserID,
	}
	if !principal.Complete() {
		return errors.New("sls-smoke requires LOG_AGENT_SMOKE_APP_ID, LOG_AGENT_SMOKE_TENANT_KEY, and LOG_AGENT_SMOKE_USER_ID")
	}
	window, err := time.ParseDuration(rawWindow)
	if err != nil || window <= 0 {
		return errors.New("sls-smoke duration must be a positive Go duration such as 30m")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := sqlite.Open(config.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	catalog, backend, err := buildAliyunDependencies(config)
	if err != nil {
		return err
	}
	gateway, err := queryapp.NewGateway(catalog, backend, store, queryBudget(config.SLS))
	if err != nil {
		return err
	}
	investigationID, err := ids.New("smoke")
	if err != nil {
		return err
	}
	end := time.Now().UTC().Add(-config.SLS.IngestionGrace).Truncate(time.Second)
	result, err := gateway.Execute(ctx, domain.QuerySpec{
		InvestigationID: investigationID,
		Name:            "smoke",
		TemplateID:      domain.ErrorSummaryTemplateID,
		Service:         service,
		Environment:     environment,
		StartTime:       end.Add(-window),
		EndTime:         end,
		Requester:       principal,
	})
	if err != nil {
		return err
	}
	return printJSON(result)
}

func printJSON(value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode command output: %w", err)
	}
	fmt.Println(string(payload))
	return nil
}
