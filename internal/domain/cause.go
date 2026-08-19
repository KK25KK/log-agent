package domain

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	CauseConfidenceMethod = "change-correlation-v1"
	CauseConfidenceCap    = 0.85
	MaxChangeEvents       = 10
	MaxAffectedInstances  = 20

	MaxChangeSourceVersionBytes = 64
	MaxChangeIdentifierBytes    = 128
	MaxChangeVersionBytes       = 128
	MaxChangeOwnerBytes         = 128
	MaxChangeSummaryBytes       = 512
	MaxAffectedInstanceBytes    = 128
)

var (
	changeIdentifierPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	resourceIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
)

// ChangeKind is an administrator-governed change category. The first M3 slice
// deliberately supports only release and configuration events.
type ChangeKind string

const (
	ChangeKindRelease ChangeKind = "RELEASE"
	ChangeKindConfig  ChangeKind = "CONFIG"
)

// ChangeQuery is derived from governed Evidence, never from user-provided
// resource identifiers.
type ChangeQuery struct {
	ResourceID string    `json:"resource_id"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	Limit      int       `json:"limit"`
}

// ChangeEvent is bounded, administrator-owned context. It is correlation
// evidence, not proof that a change caused an incident.
type ChangeEvent struct {
	ID                        string     `json:"id"`
	ResourceID                string     `json:"resource_id"`
	Kind                      ChangeKind `json:"kind"`
	StartedAt                 time.Time  `json:"started_at"`
	CompletedAt               time.Time  `json:"completed_at"`
	FromVersion               string     `json:"from_version,omitempty"`
	ToVersion                 string     `json:"to_version,omitempty"`
	Owner                     string     `json:"owner"`
	Summary                   string     `json:"summary"`
	AffectedInstances         []string   `json:"affected_instances"`
	AffectedInstancesComplete bool       `json:"affected_instances_complete"`
}

// ChangeSet carries source completeness separately from the returned events so
// a bounded absence cannot be mistaken for counterevidence.
type ChangeSet struct {
	SourceVersion string        `json:"source_version,omitempty"`
	Events        []ChangeEvent `json:"events,omitempty"`
	Complete      bool          `json:"complete"`
	Truncated     bool          `json:"truncated"`
	ReasonCode    string        `json:"reason_code,omitempty"`
}

// ValidateChangeSourceVersion validates the immutable catalog/source version
// that makes an enrichment result auditable.
func ValidateChangeSourceVersion(version string) error {
	return validateRequiredChangeText("change source version", version, MaxChangeSourceVersionBytes)
}

// ValidateResourceID keeps the governed resource identity compatible across
// the SLS, mock, and change-catalog adapters. It is opaque and never used as a
// filesystem path or provider query fragment.
func ValidateResourceID(resourceID string) error {
	if !resourceIdentifierPattern.MatchString(resourceID) {
		return errors.New("resource ID is missing or invalid")
	}
	return nil
}

// ValidateChangeEvent owns the application-wide invariants shared by every
// replaceable ChangeSource, not only the built-in JSON adapter.
func ValidateChangeEvent(event ChangeEvent) error {
	if !changeIdentifierPattern.MatchString(event.ID) {
		return errors.New("change ID is missing or invalid")
	}
	if err := ValidateResourceID(event.ResourceID); err != nil {
		return err
	}
	if event.Kind != ChangeKindRelease && event.Kind != ChangeKindConfig {
		return fmt.Errorf("unsupported change kind %q", event.Kind)
	}
	if event.StartedAt.IsZero() || !event.CompletedAt.After(event.StartedAt) {
		return errors.New("change time range is invalid")
	}
	if err := validateOptionalChangeText("from version", event.FromVersion, MaxChangeVersionBytes); err != nil {
		return err
	}
	if err := validateOptionalChangeText("to version", event.ToVersion, MaxChangeVersionBytes); err != nil {
		return err
	}
	if event.Kind == ChangeKindRelease && event.ToVersion == "" {
		return errors.New("release change requires a to version")
	}
	if event.FromVersion != "" && event.ToVersion != "" && event.FromVersion == event.ToVersion {
		return errors.New("change versions must differ")
	}
	if err := validateRequiredChangeText("change owner", event.Owner, MaxChangeOwnerBytes); err != nil {
		return err
	}
	if err := validateRequiredChangeText("change summary", event.Summary, MaxChangeSummaryBytes); err != nil {
		return err
	}
	if len(event.AffectedInstances) > MaxAffectedInstances {
		return fmt.Errorf("change has more than %d affected instances", MaxAffectedInstances)
	}
	seen := make(map[string]struct{}, len(event.AffectedInstances))
	for _, instance := range event.AffectedInstances {
		if err := validateRequiredChangeText("affected instance", instance, MaxAffectedInstanceBytes); err != nil {
			return err
		}
		if _, duplicate := seen[instance]; duplicate {
			return fmt.Errorf("change contains duplicate affected instance %q", instance)
		}
		seen[instance] = struct{}{}
	}
	return nil
}

func validateRequiredChangeText(name, value string, maxBytes int) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and cannot have surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains control characters", name)
	}
	return nil
}

func validateOptionalChangeText(name, value string, maxBytes int) error {
	if value == "" {
		return nil
	}
	return validateRequiredChangeText(name, value, maxBytes)
}

type CauseAnalysisStatus string

const (
	CauseAnalysisComplete       CauseAnalysisStatus = "COMPLETE"
	CauseAnalysisInconclusive   CauseAnalysisStatus = "INCONCLUSIVE"
	CauseAnalysisUnavailable    CauseAnalysisStatus = "UNAVAILABLE"
	CauseAnalysisSkippedNoSpike CauseAnalysisStatus = "SKIPPED_NO_SPIKE"
)

type CauseVerdict string

const (
	CauseVerdictSupportedCandidate CauseVerdict = "SUPPORTED_CANDIDATE"
	CauseVerdictRefuted            CauseVerdict = "REFUTED"
	CauseVerdictInconclusive       CauseVerdict = "INCONCLUSIVE"
)

type EvidenceTestRole string

const (
	EvidenceTestSupport EvidenceTestRole = "SUPPORT"
	EvidenceTestCounter EvidenceTestRole = "COUNTER"
)

type EvidenceTestResult string

const (
	EvidenceTestPass    EvidenceTestResult = "PASS"
	EvidenceTestFail    EvidenceTestResult = "FAIL"
	EvidenceTestUnknown EvidenceTestResult = "UNKNOWN"
)

// EvidenceLedgerEntry records one explicit hypothesis test. PASS on a counter
// test means counterevidence was found; FAIL means it was tested and not found.
type EvidenceLedgerEntry struct {
	ID             string             `json:"id"`
	HypothesisID   string             `json:"hypothesis_id"`
	Code           string             `json:"code"`
	Role           EvidenceTestRole   `json:"role"`
	Result         EvidenceTestResult `json:"result"`
	Weight         float64            `json:"weight"`
	Statement      string             `json:"statement"`
	EvidenceIDs    []string           `json:"evidence_ids,omitempty"`
	ChangeEventIDs []string           `json:"change_event_ids,omitempty"`
}

type CauseHypothesis struct {
	ID               string       `json:"id"`
	Code             string       `json:"code"`
	Statement        string       `json:"statement"`
	Verdict          CauseVerdict `json:"verdict"`
	Confidence       float64      `json:"confidence"`
	ConfidenceMethod string       `json:"confidence_method"`
	SupportEntryIDs  []string     `json:"support_entry_ids"`
	CounterEntryIDs  []string     `json:"counter_entry_ids"`
	Limitations      []string     `json:"limitations"`
}

// CauseAnalysis is an optional M3 projection. A nil value remains a valid M2
// report when older persisted reports are decoded.
type CauseAnalysis struct {
	Status        CauseAnalysisStatus   `json:"status"`
	SourceVersion string                `json:"source_version,omitempty"`
	Changes       []ChangeEvent         `json:"changes,omitempty"`
	Hypotheses    []CauseHypothesis     `json:"hypotheses,omitempty"`
	Ledger        []EvidenceLedgerEntry `json:"evidence_ledger,omitempty"`
	MissingInputs []string              `json:"missing_inputs,omitempty"`
}

// DisabledChangeSource is the safe default when no governed change catalog is
// configured. It allows M2 facts to remain available without inventing context.
type DisabledChangeSource struct{}

func (DisabledChangeSource) List(ctx context.Context, _ ChangeQuery) (ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{Complete: false, ReasonCode: "change_source_disabled"}, nil
}
