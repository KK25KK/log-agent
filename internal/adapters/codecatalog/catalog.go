package codecatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const maxCatalogBytes = 2 * 1024 * 1024

type Catalog struct {
	version      string
	deployments  []deploymentEntry
	repositories map[string]domain.CodeRepository
}

type catalogFile struct {
	Version      string            `json:"version"`
	Deployments  []deploymentEntry `json:"deployments"`
	Repositories []repositoryEntry `json:"repositories"`
}

type deploymentEntry struct {
	Service           string    `json:"service"`
	Environment       string    `json:"environment"`
	RepositoryID      string    `json:"repository_id"`
	CommitSHA         string    `json:"commit_sha"`
	PreviousCommitSHA string    `json:"previous_commit_sha,omitempty"`
	ArtifactDigest    string    `json:"artifact_digest,omitempty"`
	DeployedAt        time.Time `json:"deployed_at"`
	RetiredAt         time.Time `json:"retired_at,omitempty"`
}

type repositoryEntry struct {
	ID             string   `json:"id"`
	RootPath       string   `json:"root_path"`
	AllowedPaths   []string `json:"allowed_paths"`
	ForbiddenPaths []string `json:"forbidden_paths,omitempty"`
}

func Load(path string) (*Catalog, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read code catalog: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxCatalogBytes {
		return nil, errors.New("code catalog size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var file catalogFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode code catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("code catalog contains trailing content")
	}
	return New(file.Version, file.Deployments, file.Repositories)
}

func New(version string, deployments []deploymentEntry, repositories []repositoryEntry) (*Catalog, error) {
	if !domain.ValidCodeIdentifier(version) || len(deployments) == 0 || len(repositories) == 0 {
		return nil, errors.New("code catalog version, deployments, and repositories are required")
	}
	catalog := &Catalog{version: version, repositories: make(map[string]domain.CodeRepository, len(repositories))}
	for _, source := range repositories {
		repository, err := normalizeRepository(version, source)
		if err != nil {
			return nil, err
		}
		if _, duplicate := catalog.repositories[repository.ID]; duplicate {
			return nil, fmt.Errorf("duplicate code repository %q", repository.ID)
		}
		catalog.repositories[repository.ID] = repository
	}
	seenDeployments := make(map[string]struct{}, len(deployments))
	for _, item := range deployments {
		if err := validateDeploymentEntry(item, catalog.repositories); err != nil {
			return nil, err
		}
		key := item.Service + "\x00" + item.Environment + "\x00" + item.DeployedAt.UTC().Format(time.RFC3339Nano)
		if _, duplicate := seenDeployments[key]; duplicate {
			return nil, fmt.Errorf("duplicate deployment boundary for %s/%s", item.Service, item.Environment)
		}
		seenDeployments[key] = struct{}{}
		catalog.deployments = append(catalog.deployments, item)
	}
	sort.SliceStable(catalog.deployments, func(left, right int) bool {
		if catalog.deployments[left].Service != catalog.deployments[right].Service {
			return catalog.deployments[left].Service < catalog.deployments[right].Service
		}
		if catalog.deployments[left].Environment != catalog.deployments[right].Environment {
			return catalog.deployments[left].Environment < catalog.deployments[right].Environment
		}
		return catalog.deployments[left].DeployedAt.Before(catalog.deployments[right].DeployedAt)
	})
	return catalog, nil
}

func (catalog *Catalog) ResolveDeployment(ctx context.Context, query domain.DeploymentQuery) (domain.DeploymentEvidence, error) {
	if err := ctx.Err(); err != nil {
		return domain.DeploymentEvidence{}, err
	}
	base := domain.DeploymentEvidence{
		Version: domain.DeploymentEvidenceVersion, Service: query.Service, Environment: query.Environment,
		SourceVersion: catalog.version,
	}
	if !domain.ValidCodeIdentifier(query.Service) || !domain.ValidCodeIdentifier(query.Environment) || query.At.IsZero() {
		base.Status, base.ReasonCode = domain.DeploymentUnavailable, domain.CodeReasonDeploymentInvalid
		return fingerprintDeployment(base)
	}
	matches := make([]deploymentEntry, 0, 2)
	for _, item := range catalog.deployments {
		if item.Service != query.Service || item.Environment != query.Environment || query.At.Before(item.DeployedAt) {
			continue
		}
		if !item.RetiredAt.IsZero() && !query.At.Before(item.RetiredAt) {
			continue
		}
		matches = append(matches, item)
	}
	if len(matches) == 0 {
		base.Status, base.ReasonCode = domain.DeploymentUnavailable, domain.CodeReasonDeploymentNotFound
		return fingerprintDeployment(base)
	}
	if len(matches) != 1 {
		base.Status, base.ReasonCode = domain.DeploymentConflict, domain.CodeReasonDeploymentConflict
		return fingerprintDeployment(base)
	}
	item := matches[0]
	base.Status = domain.DeploymentComplete
	base.RepositoryID, base.CommitSHA, base.PreviousCommitSHA = item.RepositoryID, item.CommitSHA, item.PreviousCommitSHA
	base.ArtifactDigest, base.DeployedAt, base.RetiredAt = item.ArtifactDigest, item.DeployedAt.UTC(), item.RetiredAt.UTC()
	return fingerprintDeployment(base)
}

func (catalog *Catalog) ResolveCodeRepository(ctx context.Context, repositoryID string) (domain.CodeRepository, error) {
	if err := ctx.Err(); err != nil {
		return domain.CodeRepository{}, err
	}
	repository, ok := catalog.repositories[repositoryID]
	if !ok {
		return domain.CodeRepository{}, ports.ErrNotFound
	}
	repository.AllowedPaths = append([]string(nil), repository.AllowedPaths...)
	repository.ForbiddenPaths = append([]string(nil), repository.ForbiddenPaths...)
	return repository, nil
}

func (catalog *Catalog) RepositoryIDs() []string {
	ids := make([]string, 0, len(catalog.repositories))
	for id := range catalog.repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func normalizeRepository(version string, source repositoryEntry) (domain.CodeRepository, error) {
	if !domain.ValidCodeIdentifier(source.ID) || strings.TrimSpace(source.RootPath) == "" || len(source.AllowedPaths) == 0 || len(source.AllowedPaths) > 16 {
		return domain.CodeRepository{}, fmt.Errorf("code repository %q is invalid", source.ID)
	}
	root, err := filepath.Abs(source.RootPath)
	if err != nil {
		return domain.CodeRepository{}, fmt.Errorf("resolve code repository %q root: %w", source.ID, err)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return domain.CodeRepository{}, fmt.Errorf("code repository %q root is not an existing directory", source.ID)
	}
	allowed, err := normalizePaths(source.AllowedPaths)
	if err != nil {
		return domain.CodeRepository{}, fmt.Errorf("code repository %q allowed paths: %w", source.ID, err)
	}
	forbidden, err := normalizePaths(append(defaultForbiddenPaths(), source.ForbiddenPaths...))
	if err != nil {
		return domain.CodeRepository{}, fmt.Errorf("code repository %q forbidden paths: %w", source.ID, err)
	}
	return domain.CodeRepository{ID: source.ID, RootPath: root, AllowedPaths: allowed, ForbiddenPaths: forbidden, CatalogVersion: version}, nil
}

func normalizePaths(source []string) ([]string, error) {
	seen := make(map[string]struct{}, len(source))
	result := make([]string, 0, len(source))
	for _, value := range source {
		value = strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"), "/")
		if value == "" || !domain.ValidCodePath(value) {
			return nil, fmt.Errorf("invalid repository-relative path %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validateDeploymentEntry(item deploymentEntry, repositories map[string]domain.CodeRepository) error {
	if !domain.ValidCodeIdentifier(item.Service) || !domain.ValidCodeIdentifier(item.Environment) ||
		!domain.ValidCodeIdentifier(item.RepositoryID) || !domain.ValidFullCommitSHA(item.CommitSHA) || item.DeployedAt.IsZero() {
		return fmt.Errorf("deployment for %s/%s is invalid", item.Service, item.Environment)
	}
	if item.PreviousCommitSHA != "" && !domain.ValidFullCommitSHA(item.PreviousCommitSHA) {
		return fmt.Errorf("deployment for %s/%s has invalid previous commit", item.Service, item.Environment)
	}
	if !item.RetiredAt.IsZero() && !item.RetiredAt.After(item.DeployedAt) {
		return fmt.Errorf("deployment for %s/%s has invalid active interval", item.Service, item.Environment)
	}
	if _, exists := repositories[item.RepositoryID]; !exists {
		return fmt.Errorf("deployment for %s/%s references unknown repository", item.Service, item.Environment)
	}
	if !domain.ValidArtifactDigest(item.ArtifactDigest) {
		return fmt.Errorf("deployment for %s/%s has invalid artifact digest", item.Service, item.Environment)
	}
	return nil
}

func fingerprintDeployment(value domain.DeploymentEvidence) (domain.DeploymentEvidence, error) {
	fingerprint, err := domain.DeploymentEvidenceFingerprint(value)
	if err != nil {
		return domain.DeploymentEvidence{}, err
	}
	value.Fingerprint = fingerprint
	return value, nil
}

func defaultForbiddenPaths() []string {
	return []string{".env", ".git", "vendor", "node_modules", "generated", "secrets", "credentials", "private_keys"}
}

var _ ports.DeploymentVersionSource = (*Catalog)(nil)
var _ ports.CodeRepositoryCatalog = (*Catalog)(nil)
