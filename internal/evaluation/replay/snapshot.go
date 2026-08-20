package replay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/evaluation"
)

const MaxSnapshotBytes int64 = 8 * 1024 * 1024

var (
	ErrDuplicateSnapshot = errors.New("evaluation replay snapshot already exists")
	ErrSnapshotTampered  = errors.New("evaluation replay snapshot content hash mismatch")
	ErrIncompatible      = errors.New("evaluation replay source is incompatible with the current synthetic dataset")

	replayIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// FailureCode is intentionally closed and does not persist raw execution
// errors. Detailed, bounded gate failures remain available in Report.
type FailureCode string

const (
	FailureNone       FailureCode = "NONE"
	FailureGate       FailureCode = "GATE_FAILED"
	FailureEvaluation FailureCode = "EVALUATION_ABORTED"
)

// SourceReference binds a replay to the exact verified source snapshot.
type SourceReference struct {
	EvaluationRunID string `json:"evaluation_run_id"`
	ContentHash     string `json:"content_hash"`
}

// Snapshot is the append-only persisted projection of one synthetic
// evaluation run. ContentHash covers every field except itself.
type Snapshot struct {
	SchemaVersion   string                      `json:"schema_version"`
	EvaluationRunID string                      `json:"evaluation_run_id"`
	CreatedAt       time.Time                   `json:"created_at"`
	ReplayOf        *SourceReference            `json:"replay_of,omitempty"`
	FailureCode     FailureCode                 `json:"failure_code"`
	Report          evaluation.EvaluationReport `json:"report"`
	ContentHash     string                      `json:"content_hash"`
}

// Store is deliberately separate from the production investigation store.
// Implementations must preserve append-only semantics.
type Store interface {
	Append(context.Context, Snapshot) error
	Load(context.Context, string) (Snapshot, error)
}

type snapshotBody struct {
	SchemaVersion   string                      `json:"schema_version"`
	EvaluationRunID string                      `json:"evaluation_run_id"`
	CreatedAt       time.Time                   `json:"created_at"`
	ReplayOf        *SourceReference            `json:"replay_of,omitempty"`
	FailureCode     FailureCode                 `json:"failure_code"`
	Report          evaluation.EvaluationReport `json:"report"`
}

// New creates and hashes a strict snapshot. A run that failed a gate is still
// a valid replay artifact; raw errors are reduced to FailureCode.
func New(report evaluation.EvaluationReport, runErr error, replayOf *SourceReference, now time.Time) (Snapshot, error) {
	snapshot := Snapshot{
		SchemaVersion:   domain.ReplaySchemaVersion,
		EvaluationRunID: report.EvaluationRunID,
		CreatedAt:       now.UTC(),
		ReplayOf:        cloneSourceReference(replayOf),
		FailureCode:     classifyFailure(runErr),
		Report:          report,
	}
	hash, err := snapshot.bodyHash()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.ContentHash = hash
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ParseStrict rejects unknown fields, trailing JSON, oversized payloads, and
// any content that no longer matches the persisted hash.
func ParseStrict(payload []byte) (Snapshot, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return Snapshot{}, errors.New("evaluation replay snapshot is empty")
	}
	if int64(len(payload)) > MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("evaluation replay snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode evaluation replay snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Snapshot{}, errors.New("decode evaluation replay snapshot: trailing JSON value")
		}
		return Snapshot{}, fmt.Errorf("decode evaluation replay snapshot trailing content: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// Validate checks both the content hash and the stable synthetic replay
// contract. A failed gate may still be archived, but its serialized fields
// must remain internally consistent.
func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != domain.ReplaySchemaVersion {
		return errors.New("evaluation replay snapshot schema version is invalid")
	}
	if !validIdentifier(snapshot.EvaluationRunID) || snapshot.EvaluationRunID != snapshot.Report.EvaluationRunID {
		return errors.New("evaluation replay snapshot run identity is invalid")
	}
	if snapshot.CreatedAt.IsZero() || snapshot.CreatedAt.Location() != time.UTC {
		return errors.New("evaluation replay snapshot creation time must be UTC")
	}
	if err := validateSourceReference(snapshot.ReplayOf, snapshot.EvaluationRunID); err != nil {
		return err
	}
	expectedHash, err := snapshot.bodyHash()
	if err != nil {
		return err
	}
	if snapshot.ContentHash != expectedHash {
		return ErrSnapshotTampered
	}
	if err := validateReportIdentity(snapshot.Report); err != nil {
		return err
	}
	if err := validateFailureCode(snapshot.FailureCode, snapshot.Report.Status); err != nil {
		return err
	}
	return nil
}

// Reference returns the immutable identity required by a child replay.
func (snapshot Snapshot) Reference() SourceReference {
	return SourceReference{EvaluationRunID: snapshot.EvaluationRunID, ContentHash: snapshot.ContentHash}
}

// ValidateSourceCompatibility ensures replay never silently switches datasets
// or crosses from the all-Mock execution profile into a real-system boundary.
func ValidateSourceCompatibility(snapshot Snapshot, dataset evaluation.Dataset) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	report := snapshot.Report
	boundary := report.DataBoundary
	if report.DatasetID != dataset.DatasetID || report.DatasetVersion != dataset.SchemaVersion || report.DatasetFingerprint != dataset.Fingerprint ||
		report.VersionManifest.DatasetID != dataset.DatasetID || report.VersionManifest.DatasetSchemaVersion != dataset.SchemaVersion || report.VersionManifest.DatasetFingerprint != dataset.Fingerprint ||
		report.VersionManifest.ExecutorProfile != evaluation.SyntheticMockExecutorProfile ||
		boundary.DataSource != dataset.DataSource || boundary.RealIncidentCount != dataset.RealIncidentCount || boundary.ExpertLabelCount != dataset.ExpertLabelCount ||
		boundary.CredentialsRequired != dataset.CredentialsRequired || boundary.ExternalNetworkCalls != dataset.ExternalNetworkCalls || boundary.ProductionClaimAllowed != dataset.ProductionClaimAllowed {
		return ErrIncompatible
	}
	return nil
}

func (snapshot Snapshot) bodyHash() (string, error) {
	body := snapshotBody{
		SchemaVersion:   snapshot.SchemaVersion,
		EvaluationRunID: snapshot.EvaluationRunID,
		CreatedAt:       snapshot.CreatedAt,
		ReplayOf:        cloneSourceReference(snapshot.ReplayOf),
		FailureCode:     snapshot.FailureCode,
		Report:          snapshot.Report,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode evaluation replay snapshot body: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validateReportIdentity(report evaluation.EvaluationReport) error {
	if !validIdentifier(report.EvaluationRunID) || !validIdentifier(report.DatasetID) || report.DatasetVersion == "" || report.DatasetFingerprint == "" {
		return errors.New("evaluation replay report identity is invalid")
	}
	if err := report.VersionManifest.Validate(); err != nil {
		return fmt.Errorf("validate evaluation replay version manifest: %w", err)
	}
	fingerprint, err := report.VersionManifest.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint evaluation replay version manifest: %w", err)
	}
	if report.VersionFingerprint != fingerprint || report.EvaluationVersion != report.VersionManifest.EvaluationVersion ||
		report.DatasetID != report.VersionManifest.DatasetID || report.DatasetVersion != report.VersionManifest.DatasetSchemaVersion ||
		report.DatasetFingerprint != report.VersionManifest.DatasetFingerprint || !versionInfoMatchesManifest(report.Versions, report.VersionManifest) ||
		report.Policy.Version != report.EvaluationVersion {
		return errors.New("evaluation replay report version identity is inconsistent")
	}
	if report.DataBoundary.DataSource != evaluation.SyntheticDataSource || report.DataBoundary.CredentialsRequired || report.DataBoundary.ExternalNetworkCalls != 0 || report.DataBoundary.ProductionClaimAllowed {
		return errors.New("evaluation replay report is outside the synthetic Mock boundary")
	}
	if len(report.Cases) == 0 || len(report.Cases) > 100 {
		return errors.New("evaluation replay report case count is invalid")
	}
	seenCases := make(map[string]struct{}, len(report.Cases))
	for _, result := range report.Cases {
		if !validIdentifier(result.ID) {
			return errors.New("evaluation replay report contains an invalid case ID")
		}
		if _, duplicate := seenCases[result.ID]; duplicate {
			return errors.New("evaluation replay report contains a duplicate case ID")
		}
		seenCases[result.ID] = struct{}{}
		if err := domain.ValidateAgentTrace(result.AgentTrace); err != nil {
			return fmt.Errorf("validate evaluation replay trace for case %q: %w", result.ID, err)
		}
		if result.AgentTrace.EvaluationRunID != report.EvaluationRunID || result.AgentTrace.CaseID != result.ID || result.AgentTrace.VersionFingerprint != report.VersionFingerprint {
			return errors.New("evaluation replay report contains a trace with inconsistent identity")
		}
	}
	return nil
}

func validateSourceReference(reference *SourceReference, runID string) error {
	if reference == nil {
		return nil
	}
	if !validIdentifier(reference.EvaluationRunID) || reference.EvaluationRunID == runID || !validSHA256(reference.ContentHash) {
		return errors.New("evaluation replay source reference is invalid")
	}
	return nil
}

func validateFailureCode(code FailureCode, status evaluation.EvaluationStatus) error {
	switch code {
	case FailureNone:
		if status != evaluation.EvaluationPassed {
			return errors.New("successful replay snapshot requires a passed evaluation report")
		}
	case FailureGate, FailureEvaluation:
		if status != evaluation.EvaluationFailed {
			return errors.New("failed replay snapshot requires a failed evaluation report")
		}
	default:
		return errors.New("evaluation replay failure code is invalid")
	}
	return nil
}

func versionInfoMatchesManifest(versions evaluation.VersionInfo, manifest domain.AgentVersionManifest) bool {
	return versions.GraphVersion == manifest.GraphVersion &&
		versions.QueryTemplateID == manifest.TemplateID &&
		versions.QueryTemplateVersion == manifest.TemplateVersion &&
		versions.QueryPolicyVersion == manifest.PolicyVersion &&
		versions.CauseMethod == manifest.CauseVersion &&
		versions.ExecutorProfile == manifest.ExecutorProfile &&
		versions.PromptUsed == manifest.PromptUsed &&
		versions.PromptVersion == manifest.PromptVersion
}

func classifyFailure(runErr error) FailureCode {
	if runErr == nil {
		return FailureNone
	}
	if errors.Is(runErr, evaluation.ErrGateFailed) {
		return FailureGate
	}
	return FailureEvaluation
}

func cloneSourceReference(reference *SourceReference) *SourceReference {
	if reference == nil {
		return nil
	}
	copy := *reference
	return &copy
}

func validIdentifier(value string) bool {
	return replayIdentifierPattern.MatchString(value)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
