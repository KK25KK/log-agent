package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const (
	defaultCommandOutputLimit = 512 * 1024
	maxGitCommands            = 48
)

var (
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	ipv4Pattern  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
)

type Provider struct {
	catalog        ports.CodeRepositoryCatalog
	gitPath        string
	maxOutputBytes int
}

type fileSnapshot struct {
	lines    []string
	blobHash string
}

type RepositoryCheck struct {
	RepositoryID       string `json:"repository_id"`
	CatalogVersion     string `json:"catalog_version"`
	CommitAvailable    bool   `json:"commit_available"`
	PreviousAvailable  bool   `json:"previous_commit_available"`
	AllowedPathCount   int    `json:"allowed_path_count"`
	ForbiddenPathCount int    `json:"forbidden_path_count"`
	GitCommands        int    `json:"git_commands"`
}

func New(catalog ports.CodeRepositoryCatalog, gitPath string, maxOutputBytes int) (*Provider, error) {
	if catalog == nil {
		return nil, errors.New("code repository catalog is required")
	}
	if strings.TrimSpace(gitPath) == "" {
		gitPath = "git"
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = defaultCommandOutputLimit
	}
	if maxOutputBytes < 64*1024 || maxOutputBytes > 4*1024*1024 {
		return nil, errors.New("Git command output limit must be between 65536 and 4194304")
	}
	return &Provider{catalog: catalog, gitPath: gitPath, maxOutputBytes: maxOutputBytes}, nil
}

// CheckRepository validates only approved repository metadata and immutable
// Commit availability. It does not read source-code contents.
func (provider *Provider) CheckRepository(ctx context.Context, repositoryID, commitSHA, previousCommitSHA string) (RepositoryCheck, error) {
	if !domain.ValidCodeIdentifier(repositoryID) || !domain.ValidFullCommitSHA(commitSHA) ||
		(previousCommitSHA != "" && !domain.ValidFullCommitSHA(previousCommitSHA)) {
		return RepositoryCheck{}, errors.New("repository check scope is invalid")
	}
	repository, err := provider.catalog.ResolveCodeRepository(ctx, repositoryID)
	if err != nil {
		return RepositoryCheck{}, errors.New("resolve approved code repository")
	}
	if err := validateRepository(repository, repositoryID); err != nil {
		return RepositoryCheck{}, err
	}
	usage := domain.CodeSearchResult{}
	if err := provider.requireRepositoryRoot(ctx, repository, &usage); err != nil {
		return RepositoryCheck{}, err
	}
	if err := provider.requireCommit(ctx, repository, commitSHA, &usage); err != nil {
		return RepositoryCheck{}, err
	}
	check := RepositoryCheck{
		RepositoryID: repository.ID, CatalogVersion: repository.CatalogVersion, CommitAvailable: true,
		PreviousAvailable: previousCommitSHA == "", AllowedPathCount: len(repository.AllowedPaths),
		ForbiddenPathCount: len(repository.ForbiddenPaths), GitCommands: usage.CommandsRun,
	}
	if previousCommitSHA != "" {
		if err := provider.requireCommit(ctx, repository, previousCommitSHA, &usage); err != nil {
			return RepositoryCheck{}, errors.New("trusted previous deployment Commit is unavailable")
		}
		check.PreviousAvailable = true
		check.GitCommands = usage.CommandsRun
	}
	return check, nil
}

func (provider *Provider) SearchCode(ctx context.Context, request domain.CodeSearchRequest) (domain.CodeSearchResult, error) {
	result := domain.CodeSearchResult{
		Version: domain.CodeEvidenceVersion, PolicyVersion: request.PolicyVersion,
		RepositoryID: request.RepositoryID, CommitSHA: request.CommitSHA, Complete: true,
	}
	if err := validateRequest(request); err != nil {
		return domain.CodeSearchResult{}, err
	}
	repository, err := provider.catalog.ResolveCodeRepository(ctx, request.RepositoryID)
	if err != nil {
		return domain.CodeSearchResult{}, errors.New("resolve approved code repository")
	}
	if err := validateRepository(repository, request.RepositoryID); err != nil {
		return domain.CodeSearchResult{}, err
	}
	if err := provider.requireRepositoryRoot(ctx, repository, &result); err != nil {
		return domain.CodeSearchResult{}, err
	}
	if err := provider.requireCommit(ctx, repository, request.CommitSHA, &result); err != nil {
		return domain.CodeSearchResult{}, err
	}
	changed := make(map[string]struct{})
	if request.PreviousCommitSHA != "" {
		if err := provider.requireCommit(ctx, repository, request.PreviousCommitSHA, &result); err != nil {
			return domain.CodeSearchResult{}, errors.New("trusted previous deployment Commit is unavailable")
		}
		files, truncated, err := provider.changedFiles(ctx, repository, request.PreviousCommitSHA, request.CommitSHA, &result)
		if err != nil {
			return domain.CodeSearchResult{}, err
		}
		result.DiffChecked = true
		result.ChangedFiles = files
		for _, file := range files {
			changed[file] = struct{}{}
		}
		result.Truncated = result.Truncated || truncated
	}

	cache := make(map[string]fileSnapshot)
	seenMatches := make(map[string]struct{})
	for _, anchor := range request.Anchors {
		if len(result.Matches) >= request.MaxMatches || result.CommandsRun >= maxGitCommands {
			result.Truncated = true
			break
		}
		result.AnchorsSearched++
		locations, truncated, err := provider.locationsForAnchor(ctx, repository, request.CommitSHA, anchor, &result)
		if err != nil {
			return domain.CodeSearchResult{}, err
		}
		result.Truncated = result.Truncated || truncated
		for _, location := range locations {
			if len(result.Matches) >= request.MaxMatches {
				result.Truncated = true
				break
			}
			key := anchor.ID + "\x00" + location.file + "\x00" + strconv.Itoa(location.line)
			if _, duplicate := seenMatches[key]; duplicate {
				continue
			}
			seenMatches[key] = struct{}{}
			if _, allowed := allowedFile(repository, location.file); !allowed {
				continue
			}
			snapshot, exists := cache[location.file]
			if !exists {
				if len(cache) >= request.MaxFiles {
					result.Truncated = true
					continue
				}
				snapshot, err = provider.readFile(ctx, repository, request.CommitSHA, location.file, &result)
				if err != nil {
					return domain.CodeSearchResult{}, err
				}
				cache[location.file] = snapshot
				result.FilesRead = len(cache)
			}
			match, sensitive, ok := buildMatch(request, anchor, location, snapshot, changed)
			if sensitive {
				result.SensitiveSkips++
				result.Truncated = true
				continue
			}
			if !ok || result.LinesReturned+(match.EndLine-match.StartLine+1) > request.MaxLines || result.BytesReturned+len(match.Snippet) > request.MaxBytes {
				result.Truncated = true
				continue
			}
			result.LinesReturned += match.EndLine - match.StartLine + 1
			result.BytesReturned += len(match.Snippet)
			result.Matches = append(result.Matches, match)
		}
	}
	result.Complete = !result.Truncated
	sort.SliceStable(result.Matches, func(left, right int) bool {
		if result.Matches[left].File != result.Matches[right].File {
			return result.Matches[left].File < result.Matches[right].File
		}
		if result.Matches[left].MatchLine != result.Matches[right].MatchLine {
			return result.Matches[left].MatchLine < result.Matches[right].MatchLine
		}
		return result.Matches[left].AnchorID < result.Matches[right].AnchorID
	})
	return result, nil
}

type location struct {
	file string
	line int
	kind domain.CodeMatchKind
}

func (provider *Provider) locationsForAnchor(ctx context.Context, repository domain.CodeRepository, commit string, anchor domain.RuntimeAnchor, result *domain.CodeSearchResult) ([]location, bool, error) {
	if anchor.Kind == domain.RuntimeAnchorStackFrame && anchor.File != "" {
		if _, allowed := allowedFile(repository, anchor.File); !allowed {
			return nil, false, nil
		}
		return []location{{file: anchor.File, line: anchor.Line, kind: domain.CodeMatchStackFrame}}, false, nil
	}
	value := anchor.Value
	if anchor.Kind == domain.RuntimeAnchorSymbol {
		value = anchor.Symbol
	}
	if value == "" || strings.ContainsAny(value, "\r\n\x00") || utf8.RuneCountInString(value) > 192 {
		return nil, false, nil
	}
	args := []string{"-C", repository.RootPath, "grep", "-n", "-F", "--full-name", "-e", value, commit, "--"}
	args = append(args, repository.AllowedPaths...)
	output, exitCode, err := provider.run(ctx, result, args...)
	if err != nil {
		if exitCode == 1 {
			return nil, false, nil
		}
		return nil, false, errors.New("bounded Git exact-text search failed")
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	locations := make([]location, 0, len(lines))
	truncated := false
	for _, line := range lines {
		parts := strings.SplitN(strings.TrimSuffix(line, "\r"), ":", 4)
		if len(parts) != 4 {
			return nil, false, errors.New("Git exact-text result shape is invalid")
		}
		lineNumber, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil || lineNumber <= 0 {
			return nil, false, errors.New("Git exact-text result line is invalid")
		}
		file := filepath.ToSlash(parts[1])
		if _, allowed := allowedFile(repository, file); !allowed {
			continue
		}
		if len(locations) >= requestLocationLimit {
			truncated = true
			break
		}
		locations = append(locations, location{file: file, line: lineNumber, kind: domain.CodeMatchExactText})
	}
	return locations, truncated, nil
}

const requestLocationLimit = 16

func (provider *Provider) readFile(ctx context.Context, repository domain.CodeRepository, commit, file string, result *domain.CodeSearchResult) (fileSnapshot, error) {
	output, exitCode, err := provider.run(ctx, result, "-C", repository.RootPath, "show", commit+":"+file)
	if err != nil || exitCode != 0 || !utf8.ValidString(output) {
		return fileSnapshot{}, errors.New("read bounded code context from immutable Commit")
	}
	blobHash, exitCode, err := provider.run(ctx, result, "-C", repository.RootPath, "rev-parse", commit+":"+file)
	blobHash = strings.TrimSpace(blobHash)
	if err != nil || exitCode != 0 || !domain.ValidGitBlobHash(blobHash) {
		return fileSnapshot{}, errors.New("resolve immutable code blob")
	}
	return fileSnapshot{lines: strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n"), blobHash: blobHash}, nil
}

func buildMatch(request domain.CodeSearchRequest, anchor domain.RuntimeAnchor, location location, snapshot fileSnapshot, changed map[string]struct{}) (domain.CodeMatch, bool, bool) {
	if location.line <= 0 || location.line > len(snapshot.lines) {
		return domain.CodeMatch{}, false, false
	}
	start := location.line - request.ContextRadius
	if start < 1 {
		start = 1
	}
	end := location.line + request.ContextRadius
	if end > len(snapshot.lines) {
		end = len(snapshot.lines)
	}
	snippet := strings.Join(snapshot.lines[start-1:end], "\n")
	if domain.CodeSnippetContainsCredential(snippet) {
		return domain.CodeMatch{}, true, false
	}
	snippet = emailPattern.ReplaceAllString(snippet, "[REDACTED_EMAIL]")
	snippet = ipv4Pattern.ReplaceAllString(snippet, "[REDACTED_IP]")
	if !domain.ValidateCodeSnippet(snippet) {
		return domain.CodeMatch{}, false, false
	}
	_, changedSincePrevious := changed[location.file]
	queryFingerprint, err := fingerprint.JSON(struct {
		AnchorID, RepositoryID, CommitSHA string
	}{anchor.ID, request.RepositoryID, request.CommitSHA})
	if err != nil {
		return domain.CodeMatch{}, false, false
	}
	match := domain.CodeMatch{
		Kind: location.kind, AnchorID: anchor.ID, RepositoryID: request.RepositoryID, CommitSHA: request.CommitSHA,
		File: location.file, StartLine: start, EndLine: end, MatchLine: location.line, Symbol: anchor.Symbol,
		Snippet: snippet, BlobHash: snapshot.blobHash, QueryFingerprint: queryFingerprint,
		ChangedSincePrevious: changedSincePrevious,
	}
	contentFingerprint, err := domain.CodeMatchFingerprint(match)
	if err != nil {
		return domain.CodeMatch{}, false, false
	}
	match.ContentFingerprint = contentFingerprint
	digest := sha256.Sum256([]byte(anchor.ID + "|" + queryFingerprint + "|" + contentFingerprint))
	match.ID = "code_" + hex.EncodeToString(digest[:12])
	return match, false, true
}

func (provider *Provider) changedFiles(ctx context.Context, repository domain.CodeRepository, previous, current string, result *domain.CodeSearchResult) ([]string, bool, error) {
	args := []string{"-C", repository.RootPath, "diff", "--name-only", "--diff-filter=ACMRT", previous, current, "--"}
	args = append(args, repository.AllowedPaths...)
	output, exitCode, err := provider.run(ctx, result, args...)
	if err != nil || exitCode != 0 {
		return nil, false, errors.New("read trusted deployment diff file list")
	}
	seen := make(map[string]struct{})
	files := make([]string, 0)
	truncated := false
	for _, raw := range strings.Split(strings.TrimSpace(output), "\n") {
		file := filepath.ToSlash(strings.TrimSpace(raw))
		if file == "" {
			continue
		}
		if _, allowed := allowedFile(repository, file); !allowed {
			continue
		}
		if _, duplicate := seen[file]; duplicate {
			continue
		}
		if len(files) >= 64 {
			truncated = true
			break
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	sort.Strings(files)
	return files, truncated, nil
}

func (provider *Provider) requireCommit(ctx context.Context, repository domain.CodeRepository, commit string, result *domain.CodeSearchResult) error {
	_, exitCode, err := provider.run(ctx, result, "-C", repository.RootPath, "cat-file", "-e", commit+"^{commit}")
	if err != nil || exitCode != 0 {
		return errors.New("trusted deployment Commit is unavailable")
	}
	return nil
}

func (provider *Provider) requireRepositoryRoot(ctx context.Context, repository domain.CodeRepository, result *domain.CodeSearchResult) error {
	output, exitCode, err := provider.run(ctx, result, "-C", repository.RootPath, "rev-parse", "--show-toplevel")
	if err != nil || exitCode != 0 {
		return errors.New("approved code repository is not a Git work tree")
	}
	actual, err := filepath.Abs(strings.TrimSpace(output))
	if err != nil {
		return errors.New("approved code repository root is invalid")
	}
	expected, expectedErr := filepath.EvalSymlinks(repository.RootPath)
	resolvedActual, actualErr := filepath.EvalSymlinks(actual)
	if expectedErr != nil || actualErr != nil || !strings.EqualFold(filepath.Clean(expected), filepath.Clean(resolvedActual)) {
		return errors.New("approved code repository root must equal the Git top level")
	}
	return nil
}

func (provider *Provider) run(ctx context.Context, result *domain.CodeSearchResult, args ...string) (string, int, error) {
	if result.CommandsRun >= maxGitCommands {
		return "", -1, errors.New("Git command budget exceeded")
	}
	result.CommandsRun++
	command := exec.CommandContext(ctx, provider.gitPath, args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = provider.maxOutputBytes, 16*1024
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return "", -1, errors.New("Git command output budget exceeded")
	}
	if err == nil {
		return stdout.String(), 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout.String(), exitError.ExitCode(), err
	}
	return "", -1, err
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (writer *limitedBuffer) Write(payload []byte) (int, error) {
	if writer.buffer.Len()+len(payload) > writer.limit {
		remaining := writer.limit - writer.buffer.Len()
		if remaining > 0 {
			_, _ = writer.buffer.Write(payload[:remaining])
		}
		writer.exceeded = true
		return len(payload), nil
	}
	return writer.buffer.Write(payload)
}

func (writer *limitedBuffer) String() string { return writer.buffer.String() }

func validateRequest(request domain.CodeSearchRequest) error {
	if request.InvestigationID == "" || !domain.ValidCodeIdentifier(request.RepositoryID) || !domain.ValidFullCommitSHA(request.CommitSHA) ||
		(request.PreviousCommitSHA != "" && !domain.ValidFullCommitSHA(request.PreviousCommitSHA)) || request.PolicyVersion != domain.CodeSearchPolicyVersion ||
		request.RequestedAt.IsZero() || len(request.Anchors) == 0 || len(request.Anchors) > domain.CodeSearchMaxAnchors ||
		request.MaxMatches != domain.CodeSearchMaxMatches || request.MaxFiles != domain.CodeSearchMaxFiles ||
		request.MaxLines != domain.CodeSearchMaxLines || request.MaxBytes != domain.CodeSearchMaxBytes || request.ContextRadius != domain.CodeSearchContextRadius {
		return errors.New("code search request violates the fixed policy")
	}
	return nil
}

func validateRepository(repository domain.CodeRepository, expectedID string) error {
	if repository.ID != expectedID || !domain.ValidCodeIdentifier(repository.ID) || !filepath.IsAbs(repository.RootPath) ||
		repository.CatalogVersion == "" || len(repository.AllowedPaths) == 0 {
		return errors.New("approved code repository is invalid")
	}
	for _, path := range append(append([]string(nil), repository.AllowedPaths...), repository.ForbiddenPaths...) {
		if !domain.ValidCodePath(path) {
			return errors.New("approved code repository path policy is invalid")
		}
	}
	return nil
}

func allowedFile(repository domain.CodeRepository, file string) (string, bool) {
	file = filepath.ToSlash(strings.TrimSpace(file))
	if !domain.ValidCodePath(file) || !supportedFile(file) || containsForbiddenMarker(file) {
		return "", false
	}
	for _, forbidden := range repository.ForbiddenPaths {
		if file == forbidden || strings.HasPrefix(file, forbidden+"/") || strings.Contains("/"+file+"/", "/"+forbidden+"/") {
			return "", false
		}
	}
	for _, allowed := range repository.AllowedPaths {
		if file == allowed || strings.HasPrefix(file, allowed+"/") {
			return file, true
		}
	}
	return "", false
}

func containsForbiddenMarker(file string) bool {
	lower := strings.ToLower("/" + file + "/")
	for _, marker := range []string{"/.env", "/secret", "/credential", "/private_key", "/id_rsa"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func supportedFile(file string) bool {
	lower := strings.ToLower(file)
	return strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".java") || strings.HasSuffix(lower, ".py")
}

var _ ports.CodeEvidenceProvider = (*Provider)(nil)
