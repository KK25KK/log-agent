package main

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"logagent/internal/adapters/codecatalog"
	"logagent/internal/adapters/gitcode"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
)

func buildCodeEvidenceService(loaded config.Config) (*application.CodeEvidenceService, error) {
	if loaded.Code.Mode == "disabled" {
		return nil, nil
	}
	catalog, provider, err := buildCodeDependencies(loaded)
	if err != nil {
		return nil, err
	}
	return application.NewCodeEvidenceService(catalog, provider, loaded.Code.Timeout)
}

func buildCodeDependencies(loaded config.Config) (*codecatalog.Catalog, *gitcode.Provider, error) {
	if loaded.Code.Mode != "localgit" {
		return nil, nil, errors.New("code evidence dependencies require LOG_AGENT_CODE_MODE=localgit")
	}
	catalog, err := codecatalog.Load(loaded.Code.CatalogPath)
	if err != nil {
		return nil, nil, err
	}
	provider, err := gitcode.New(catalog, loaded.Code.GitPath, loaded.Code.MaxCommandOutput)
	if err != nil {
		return nil, nil, err
	}
	return catalog, provider, nil
}

func runCodeCheck(loaded config.Config, service, environment, rawAt string) error {
	if loaded.Code.Mode != "localgit" {
		return errors.New("code-check requires LOG_AGENT_CODE_MODE=localgit")
	}
	at := time.Now().UTC()
	if rawAt != "" {
		parsed, err := time.Parse(time.RFC3339, rawAt)
		if err != nil {
			return errors.New("code-check time must be RFC3339")
		}
		at = parsed.UTC()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	catalog, provider, err := buildCodeDependencies(loaded)
	if err != nil {
		return err
	}
	deployment, err := catalog.ResolveDeployment(ctx, domain.DeploymentQuery{Service: service, Environment: environment, At: at})
	if err != nil {
		return err
	}
	if deployment.Status != domain.DeploymentComplete {
		return fmt.Errorf("deployment resolution is %s (%s)", deployment.Status, deployment.ReasonCode)
	}
	check, err := provider.CheckRepository(ctx, deployment.RepositoryID, deployment.CommitSHA, deployment.PreviousCommitSHA)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{
		"status": "READY", "code_reads": 0, "deployment": deployment,
		"repository": check,
	})
}
