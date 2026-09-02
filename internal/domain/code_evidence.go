package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DeploymentEvidenceVersion = "deployment-evidence-v1"
	CodeEvidenceVersion       = "code-evidence-v1"
	CodeSearchPolicyVersion   = "code-search-policy-v1"

	CodeSearchMaxAnchors    = 16
	CodeSearchMaxMatches    = 16
	CodeSearchMaxFiles      = 8
	CodeSearchMaxLines      = 480
	CodeSearchMaxBytes      = 64 * 1024
	CodeSearchContextRadius = 10
)

type DeploymentStatus string

const (
	DeploymentComplete    DeploymentStatus = "COMPLETE"
	DeploymentUnavailable DeploymentStatus = "UNAVAILABLE"
	DeploymentConflict    DeploymentStatus = "CONFLICT"
)

const (
	CodeReasonDisabled              = "code_evidence_disabled"
	CodeReasonTraceIncomplete       = "trace_incomplete"
	CodeReasonNoAnchors             = "no_runtime_anchors"
	CodeReasonDeploymentNotFound    = "deployment_not_found"
	CodeReasonDeploymentConflict    = "deployment_conflict"
	CodeReasonDeploymentInvalid     = "deployment_invalid"
	CodeReasonRepositoryUnavailable = "repository_unavailable"
	CodeReasonProviderUnavailable   = "code_provider_unavailable"
	CodeReasonResultInvalid         = "code_result_invalid"
	CodeReasonResultTruncated       = "code_result_truncated"
)

type DeploymentQuery struct {
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	At          time.Time `json:"at"`
}

// DeploymentEvidence identifies the immutable code version that was actually
// deployed at the investigated time. Branch names and working-tree state are
// deliberately absent.
type DeploymentEvidence struct {
	Version           string           `json:"version"`
	Status            DeploymentStatus `json:"status"`
	Service           string           `json:"service"`
	Environment       string           `json:"environment"`
	RepositoryID      string           `json:"repository_id,omitempty"`
	CommitSHA         string           `json:"commit_sha,omitempty"`
	PreviousCommitSHA string           `json:"previous_commit_sha,omitempty"`
	ArtifactDigest    string           `json:"artifact_digest,omitempty"`
	DeployedAt        time.Time        `json:"deployed_at,omitempty"`
	RetiredAt         time.Time        `json:"retired_at,omitempty"`
	SourceVersion     string           `json:"source_version"`
	ReasonCode        string           `json:"reason_code,omitempty"`
	Fingerprint       string           `json:"fingerprint"`
}

// CodeRepository is administrator-owned physical configuration. RootPath is
// consumed by an adapter and must never be copied into user-facing evidence.
type CodeRepository struct {
	ID             string   `json:"id"`
	RootPath       string   `json:"root_path"`
	AllowedPaths   []string `json:"allowed_paths"`
	ForbiddenPaths []string `json:"forbidden_paths,omitempty"`
	CatalogVersion string   `json:"catalog_version"`
}

type CodeSearchRequest struct {
	InvestigationID   string          `json:"investigation_id"`
	RepositoryID      string          `json:"repository_id"`
	CommitSHA         string          `json:"commit_sha"`
	PreviousCommitSHA string          `json:"previous_commit_sha,omitempty"`
	Anchors           []RuntimeAnchor `json:"anchors"`
	MaxMatches        int             `json:"max_matches"`
	MaxFiles          int             `json:"max_files"`
	MaxLines          int             `json:"max_lines"`
	MaxBytes          int             `json:"max_bytes"`
	ContextRadius     int             `json:"context_radius"`
	PolicyVersion     string          `json:"policy_version"`
	RequestedAt       time.Time       `json:"requested_at"`
}

type CodeMatchKind string

const (
	CodeMatchExactText  CodeMatchKind = "EXACT_TEXT"
	CodeMatchStackFrame CodeMatchKind = "STACK_FRAME"
)

type CodeMatch struct {
	ID                   string        `json:"id"`
	Kind                 CodeMatchKind `json:"kind"`
	AnchorID             string        `json:"anchor_id"`
	RepositoryID         string        `json:"repository_id"`
	CommitSHA            string        `json:"commit_sha"`
	File                 string        `json:"file"`
	StartLine            int           `json:"start_line"`
	EndLine              int           `json:"end_line"`
	MatchLine            int           `json:"match_line"`
	Symbol               string        `json:"symbol,omitempty"`
	Snippet              string        `json:"snippet"`
	BlobHash             string        `json:"blob_hash"`
	ContentFingerprint   string        `json:"content_fingerprint"`
	QueryFingerprint     string        `json:"query_fingerprint"`
	ChangedSincePrevious bool          `json:"changed_since_previous"`
}

// CodeSearchResult is returned by a read-only provider. Complete describes
// provider coverage only; it is never causal confidence.
type CodeSearchResult struct {
	Version         string      `json:"version"`
	PolicyVersion   string      `json:"policy_version"`
	RepositoryID    string      `json:"repository_id"`
	CommitSHA       string      `json:"commit_sha"`
	Complete        bool        `json:"complete"`
	Truncated       bool        `json:"truncated"`
	Matches         []CodeMatch `json:"matches"`
	AnchorsSearched int         `json:"anchors_searched"`
	FilesRead       int         `json:"files_read"`
	LinesReturned   int         `json:"lines_returned"`
	BytesReturned   int         `json:"bytes_returned"`
	CommandsRun     int         `json:"commands_run"`
	SensitiveSkips  int         `json:"sensitive_skips"`
	DiffChecked     bool        `json:"diff_checked"`
	ChangedFiles    []string    `json:"changed_files,omitempty"`
}

type CodeInvestigationStatus string

const (
	CodeInvestigationComplete    CodeInvestigationStatus = "COMPLETE"
	CodeInvestigationNoMatch     CodeInvestigationStatus = "NO_MATCH"
	CodeInvestigationPartial     CodeInvestigationStatus = "PARTIAL"
	CodeInvestigationSkipped     CodeInvestigationStatus = "SKIPPED"
	CodeInvestigationUnavailable CodeInvestigationStatus = "UNAVAILABLE"
)

// CodeInvestigation is an additive, human-review-only report section. It does
// not enter the existing external LLM summary projection.
type CodeInvestigation struct {
	Version        string                  `json:"version"`
	Status         CodeInvestigationStatus `json:"status"`
	Complete       bool                    `json:"complete"`
	ReasonCode     string                  `json:"reason_code,omitempty"`
	Deployment     *DeploymentEvidence     `json:"deployment,omitempty"`
	Matches        []CodeMatch             `json:"matches,omitempty"`
	AnchorsUsed    int                     `json:"anchors_used"`
	FilesRead      int                     `json:"files_read"`
	LinesReturned  int                     `json:"lines_returned"`
	BytesReturned  int                     `json:"bytes_returned"`
	CommandsRun    int                     `json:"commands_run"`
	SensitiveSkips int                     `json:"sensitive_skips"`
	DiffChecked    bool                    `json:"diff_checked"`
	ChangedFiles   []string                `json:"changed_files,omitempty"`
	Limitations    []string                `json:"limitations"`
	GeneratedAt    time.Time               `json:"generated_at"`
}

var (
	fullCommitPattern      = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	codeIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	codePathPattern        = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*(?:\.(?:go|java|py))?$`)
	gitBlobPattern         = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	codeCredentialPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
		regexp.MustCompile(`\bLTAI[A-Za-z0-9]{12,}\b`),
		regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?secret|password)\s*[:=]\s*["'][^"']{8,}["']`),
		regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{16,}=*`),
	}
	codeEmailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	codeIPv4Pattern  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
)

func ValidCodeIdentifier(value string) bool { return codeIDPattern.MatchString(value) }

func ValidFullCommitSHA(value string) bool { return fullCommitPattern.MatchString(value) }

func ValidCodePath(value string) bool {
	return codePathPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.HasPrefix(value, "/")
}

func ValidGitBlobHash(value string) bool { return gitBlobPattern.MatchString(value) }

func ValidArtifactDigest(value string) bool {
	return len(value) <= 160 && !strings.ContainsAny(value, "\r\n\t {}[];$`")
}

func DeploymentEvidenceFingerprint(value DeploymentEvidence) (string, error) {
	projection := struct {
		Version           string           `json:"version"`
		Status            DeploymentStatus `json:"status"`
		Service           string           `json:"service"`
		Environment       string           `json:"environment"`
		RepositoryID      string           `json:"repository_id,omitempty"`
		CommitSHA         string           `json:"commit_sha,omitempty"`
		PreviousCommitSHA string           `json:"previous_commit_sha,omitempty"`
		ArtifactDigest    string           `json:"artifact_digest,omitempty"`
		DeployedAt        time.Time        `json:"deployed_at,omitempty"`
		RetiredAt         time.Time        `json:"retired_at,omitempty"`
		SourceVersion     string           `json:"source_version"`
		ReasonCode        string           `json:"reason_code,omitempty"`
	}{
		Version: value.Version, Status: value.Status, Service: value.Service, Environment: value.Environment,
		RepositoryID: value.RepositoryID, CommitSHA: value.CommitSHA, PreviousCommitSHA: value.PreviousCommitSHA,
		ArtifactDigest: value.ArtifactDigest, DeployedAt: value.DeployedAt, RetiredAt: value.RetiredAt,
		SourceVersion: value.SourceVersion, ReasonCode: value.ReasonCode,
	}
	return codeEvidenceJSONFingerprint(projection)
}

func CodeMatchFingerprint(value CodeMatch) (string, error) {
	if value.Snippet == "" {
		return "", errors.New("code match snippet is required")
	}
	projection := struct {
		Kind                 CodeMatchKind `json:"kind"`
		AnchorID             string        `json:"anchor_id"`
		RepositoryID         string        `json:"repository_id"`
		CommitSHA            string        `json:"commit_sha"`
		File                 string        `json:"file"`
		StartLine            int           `json:"start_line"`
		EndLine              int           `json:"end_line"`
		MatchLine            int           `json:"match_line"`
		Symbol               string        `json:"symbol,omitempty"`
		Snippet              string        `json:"snippet"`
		BlobHash             string        `json:"blob_hash"`
		ChangedSincePrevious bool          `json:"changed_since_previous"`
	}{
		Kind: value.Kind, AnchorID: value.AnchorID, RepositoryID: value.RepositoryID, CommitSHA: value.CommitSHA,
		File: value.File, StartLine: value.StartLine, EndLine: value.EndLine, MatchLine: value.MatchLine,
		Symbol: value.Symbol, Snippet: value.Snippet, BlobHash: value.BlobHash, ChangedSincePrevious: value.ChangedSincePrevious,
	}
	return codeEvidenceJSONFingerprint(projection)
}

func ValidateCodeSnippet(value string) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= CodeSearchMaxBytes && !strings.ContainsRune(value, '\x00') &&
		!CodeSnippetContainsCredential(value) && !codeEmailPattern.MatchString(value) && !codeIPv4Pattern.MatchString(value)
}

func CodeSnippetContainsCredential(value string) bool {
	for _, pattern := range codeCredentialPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func codeEvidenceJSONFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
