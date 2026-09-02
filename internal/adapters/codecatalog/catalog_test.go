package codecatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestCatalogResolvesDeploymentAtInvestigatedTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "catalog.json")
	payload := fmt.Sprintf(`{
  "version":"catalog-v1",
  "repositories":[{"id":"dam","root_path":%q,"allowed_paths":["internal"]}],
  "deployments":[{"service":"dam-server","environment":"test","repository_id":"dam","commit_sha":"%s","deployed_at":"2026-09-01T00:00:00Z","retired_at":"2026-09-03T00:00:00Z"}]
}`, filepath.ToSlash(root), repeatHex("a"))
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.ResolveDeployment(context.Background(), domain.DeploymentQuery{
		Service: "dam-server", Environment: "test", At: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || resolved.Status != domain.DeploymentComplete || resolved.RepositoryID != "dam" || resolved.Fingerprint == "" {
		t.Fatalf("unexpected deployment: %#v err=%v", resolved, err)
	}
	missing, err := catalog.ResolveDeployment(context.Background(), domain.DeploymentQuery{
		Service: "dam-server", Environment: "test", At: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || missing.Status != domain.DeploymentUnavailable || missing.ReasonCode != domain.CodeReasonDeploymentNotFound {
		t.Fatalf("unexpected unavailable deployment: %#v err=%v", missing, err)
	}
	repository, err := catalog.ResolveCodeRepository(context.Background(), "dam")
	if err != nil || !filepath.IsAbs(repository.RootPath) || repository.AllowedPaths[0] != "internal" {
		t.Fatalf("unexpected repository: %#v err=%v", repository, err)
	}
}

func TestCatalogReportsOverlappingDeploymentsAsConflict(t *testing.T) {
	root := t.TempDir()
	deployments := []deploymentEntry{
		{Service: "dam", Environment: "test", RepositoryID: "dam", CommitSHA: repeatHex("a"), DeployedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{Service: "dam", Environment: "test", RepositoryID: "dam", CommitSHA: repeatHex("b"), DeployedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)},
	}
	catalog, err := New("catalog-v1", deployments, []repositoryEntry{{ID: "dam", RootPath: root, AllowedPaths: []string{"internal"}}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.ResolveDeployment(context.Background(), domain.DeploymentQuery{Service: "dam", Environment: "test", At: time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC)})
	if err != nil || resolved.Status != domain.DeploymentConflict || resolved.CommitSHA != "" {
		t.Fatalf("expected conflict without guessed commit: %#v err=%v", resolved, err)
	}
}

func TestCatalogRejectsUnknownFieldsAndTraversal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "catalog.json")
	payload := fmt.Sprintf(`{"version":"v1","unknown":true,"repositories":[{"id":"dam","root_path":%q,"allowed_paths":["../internal"]}],"deployments":[]}`, filepath.ToSlash(root))
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected strict catalog rejection")
	}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 40 {
		result += value
	}
	return result[:40]
}
