package gitcode_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/gitcode"
	"logagent/internal/application/anchors"
	"logagent/internal/domain"
)

type repositoryCatalog struct{ repository domain.CodeRepository }

func (catalog repositoryCatalog) ResolveCodeRepository(_ context.Context, id string) (domain.CodeRepository, error) {
	return catalog.repository, nil
}

func TestProviderReadsOnlyImmutableCommitAndRedactsSafeContext(t *testing.T) {
	root, previous, current, matchLine := makeRepository(t)
	provider, err := gitcode.New(repositoryCatalog{repository: domain.CodeRepository{
		ID: "dam", RootPath: root, AllowedPaths: []string{"internal"},
		ForbiddenPaths: []string{".env", "vendor", "secrets"}, CatalogVersion: "catalog-v1",
	}}, "git", 512*1024)
	if err != nil {
		t.Fatal(err)
	}
	events, set := anchors.Extract([]domain.TraceEvent{
		{ID: "event-1", MemberID: "dam-server", Message: "processing failed: payment timeout\nexample.com/dam/internal/payment.(*Client).Charge\n\t/app/internal/payment/client.go:" + strconv.Itoa(matchLine) + " +0x1"},
	}, true)
	_ = events
	request := fixedRequest(previous, current, set.Anchors)
	result, err := provider.SearchCode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Matches) < 2 || !result.DiffChecked || !contains(result.ChangedFiles, "internal/payment/client.go") {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, match := range result.Matches {
		if match.CommitSHA != current || strings.Contains(match.Snippet, "dev@example.com") || strings.Contains(match.Snippet, "10.1.2.3") ||
			strings.Contains(match.Snippet, "working tree only") {
			t.Fatalf("untrusted content leaked into immutable match: %#v", match)
		}
		if !strings.Contains(match.Snippet, "[REDACTED_EMAIL]") || !strings.Contains(match.Snippet, "[REDACTED_IP]") {
			t.Fatalf("expected deterministic snippet redaction: %q", match.Snippet)
		}
	}
	check, err := provider.CheckRepository(context.Background(), "dam", current, previous)
	if err != nil || !check.CommitAvailable || !check.PreviousAvailable || check.GitCommands != 3 {
		t.Fatalf("unexpected repository check: %#v err=%v", check, err)
	}
}

func TestProviderDoesNotFallBackToWorkingTree(t *testing.T) {
	root, _, current, _ := makeRepository(t)
	path := filepath.Join(root, "internal", "payment", "client.go")
	if err := os.WriteFile(path, []byte("package payment\n// processing failed: working tree only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := gitcode.New(repositoryCatalog{repository: domain.CodeRepository{
		ID: "dam", RootPath: root, AllowedPaths: []string{"internal"}, CatalogVersion: "catalog-v1",
	}}, "git", 512*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, set := anchors.Extract([]domain.TraceEvent{{ID: "event-2", MemberID: "dam-server", Message: "processing failed: working tree only"}}, true)
	request := fixedRequest("", current, set.Anchors)
	result, err := provider.SearchCode(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("working-tree content must not be searched: %#v", result.Matches)
	}
}

func TestProviderSkipsCredentialBearingContextAndForbiddenFiles(t *testing.T) {
	root, _, current, _ := makeRepository(t)
	unsafe := "package payment\nvar password = \"super-secret-value\"\n// processing failed: credential breach\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "payment", "unsafe.go"), []byte(unsafe), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "package payment\n// processing failed: hidden file\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "payment", "secret.go"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "unsafe fixture")
	current = strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	provider, err := gitcode.New(repositoryCatalog{repository: domain.CodeRepository{
		ID: "dam", RootPath: root, AllowedPaths: []string{"internal"}, CatalogVersion: "catalog-v1",
	}}, "git", 512*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, set := anchors.Extract([]domain.TraceEvent{
		{ID: "event-3", MemberID: "dam-server", Message: "processing failed: credential breach"},
		{ID: "event-4", MemberID: "dam-server", Message: "processing failed: hidden file"},
	}, true)
	result, err := provider.SearchCode(context.Background(), fixedRequest("", current, set.Anchors))
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.SensitiveSkips != 1 || len(result.Matches) != 0 {
		t.Fatalf("sensitive or forbidden code must fail closed: %#v", result)
	}
}

func fixedRequest(previous, current string, runtimeAnchors []domain.RuntimeAnchor) domain.CodeSearchRequest {
	return domain.CodeSearchRequest{
		InvestigationID: "investigation-1", RepositoryID: "dam", CommitSHA: current, PreviousCommitSHA: previous,
		Anchors: runtimeAnchors, MaxMatches: domain.CodeSearchMaxMatches, MaxFiles: domain.CodeSearchMaxFiles,
		MaxLines: domain.CodeSearchMaxLines, MaxBytes: domain.CodeSearchMaxBytes,
		ContextRadius: domain.CodeSearchContextRadius, PolicyVersion: domain.CodeSearchPolicyVersion, RequestedAt: time.Now().UTC(),
	}
}

func makeRepository(t *testing.T) (string, string, string, int) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	runGit(t, root, "config", "user.name", "Fixture")
	path := filepath.Join(root, "internal", "payment", "client.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	previousSource := "package payment\n\nfunc Charge() error {\n\treturn nil\n}\n"
	if err := os.WriteFile(path, []byte(previousSource), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "previous")
	previous := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	currentSource := "package payment\n\nimport \"errors\"\n\n// owner dev@example.com endpoint 10.1.2.3\nfunc Charge() error {\n\treturn errors.New(\"payment timeout\")\n}\n"
	if err := os.WriteFile(path, []byte(currentSource), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "current")
	current := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	return root, previous, current, 7
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
