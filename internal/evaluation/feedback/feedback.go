package feedback

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
	"sort"
	"strings"
	"time"

	"logagent/internal/evaluation"
	"logagent/internal/evaluation/replay"
	"logagent/internal/fingerprint"
)

const (
	SchemaVersion              = "evaluation-feedback-v1"
	SummarySchemaVersion       = "evaluation-feedback-summary-v1"
	SyntheticDataSource        = evaluation.SyntheticDataSource
	MaxRecordBytes       int64 = 16 * 1024
	MaxRecordsPerRun           = 256
)

var (
	ErrDuplicateFeedback = errors.New("evaluation feedback already exists")
	ErrFeedbackTampered  = errors.New("evaluation feedback content hash mismatch")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Verdict is deliberately closed. C1 feedback records are bounded review
// signals, not a channel for arbitrary reviewer prose.
type Verdict string

const (
	VerdictSafe   Verdict = "SAFE"
	VerdictUnsafe Verdict = "UNSAFE"
	VerdictUnsure Verdict = "UNSURE"
)

// ReasonCode is deliberately closed and must agree with the selected verdict.
type ReasonCode string

const (
	ReasonExpectedBehavior     ReasonCode = "EXPECTED_BEHAVIOR"
	ReasonEvidenceGrounded     ReasonCode = "EVIDENCE_GROUNDED"
	ReasonMisleadingConclusion ReasonCode = "MISLEADING_CONCLUSION"
	ReasonUnsafeRecommendation ReasonCode = "UNSAFE_RECOMMENDATION"
	ReasonInsufficientContext  ReasonCode = "INSUFFICIENT_CONTEXT"
)

// Record binds one reviewer decision to an exact immutable evaluation
// snapshot and Case. ContentHash covers every field except itself.
type Record struct {
	SchemaVersion      string                 `json:"schema_version"`
	FeedbackID         string                 `json:"feedback_id"`
	CreatedAt          time.Time              `json:"created_at"`
	Target             replay.SourceReference `json:"target"`
	VersionFingerprint string                 `json:"version_fingerprint"`
	CaseID             string                 `json:"case_id"`
	ReviewerRef        string                 `json:"reviewer_ref"`
	Verdict            Verdict                `json:"verdict"`
	ReasonCode         ReasonCode             `json:"reason_code"`
	Supersedes         string                 `json:"supersedes,omitempty"`
	ContentHash        string                 `json:"content_hash"`
}

type recordBody struct {
	SchemaVersion      string                 `json:"schema_version"`
	FeedbackID         string                 `json:"feedback_id"`
	CreatedAt          time.Time              `json:"created_at"`
	Target             replay.SourceReference `json:"target"`
	VersionFingerprint string                 `json:"version_fingerprint"`
	CaseID             string                 `json:"case_id"`
	ReviewerRef        string                 `json:"reviewer_ref"`
	Verdict            Verdict                `json:"verdict"`
	ReasonCode         ReasonCode             `json:"reason_code"`
	Supersedes         string                 `json:"supersedes,omitempty"`
}

// Store is intentionally separate from both the production investigation
// store and the replay snapshot store.
type Store interface {
	Append(context.Context, Record) error
	List(context.Context, string) ([]Record, error)
}

type Projection struct {
	FeedbackID  string     `json:"feedback_id"`
	ContentHash string     `json:"content_hash"`
	CaseID      string     `json:"case_id"`
	ReviewerRef string     `json:"reviewer_ref"`
	Verdict     Verdict    `json:"verdict"`
	ReasonCode  ReasonCode `json:"reason_code"`
}

type CaseSummary struct {
	CaseID         string       `json:"case_id"`
	ActiveFeedback []Projection `json:"active_feedback"`
}

// Summary is a bounded projection safe for CLI output and C2 consumption. It
// does not contain report text, Evidence, logs, queries, or reviewer prose.
type Summary struct {
	SchemaVersion        string                 `json:"schema_version"`
	Target               replay.SourceReference `json:"target"`
	VersionFingerprint   string                 `json:"version_fingerprint"`
	DataSource           string                 `json:"data_source"`
	ProductionAction     bool                   `json:"production_action_allowed"`
	RealReviewerCount    int                    `json:"real_reviewer_count"`
	ExternalNetworkCalls int                    `json:"external_network_calls"`
	RecordCount          int                    `json:"record_count"`
	ActiveCount          int                    `json:"active_count"`
	Cases                []CaseSummary          `json:"cases"`
}

type NewRecordInput struct {
	CaseID      string
	ReviewerRef string
	Verdict     Verdict
	ReasonCode  ReasonCode
	Supersedes  string
	CreatedAt   time.Time
}

// NewRecord constructs a deterministic record identity from its immutable
// inputs. Repeating the same append is therefore rejected rather than silently
// creating duplicate review history.
func NewRecord(snapshot replay.Snapshot, input NewRecordInput) (Record, error) {
	if err := snapshot.Validate(); err != nil {
		return Record{}, fmt.Errorf("validate feedback target snapshot: %w", err)
	}
	record := Record{
		SchemaVersion:      SchemaVersion,
		CreatedAt:          input.CreatedAt.UTC(),
		Target:             snapshot.Reference(),
		VersionFingerprint: snapshot.Report.VersionFingerprint,
		CaseID:             input.CaseID,
		ReviewerRef:        input.ReviewerRef,
		Verdict:            input.Verdict,
		ReasonCode:         input.ReasonCode,
		Supersedes:         input.Supersedes,
	}
	idSeed := struct {
		Target      replay.SourceReference `json:"target"`
		CaseID      string                 `json:"case_id"`
		ReviewerRef string                 `json:"reviewer_ref"`
		Verdict     Verdict                `json:"verdict"`
		ReasonCode  ReasonCode             `json:"reason_code"`
		Supersedes  string                 `json:"supersedes,omitempty"`
		CreatedAt   time.Time              `json:"created_at"`
	}{record.Target, record.CaseID, record.ReviewerRef, record.Verdict, record.ReasonCode, record.Supersedes, record.CreatedAt}
	idHash, err := fingerprint.JSON(idSeed)
	if err != nil {
		return Record{}, fmt.Errorf("fingerprint evaluation feedback identity: %w", err)
	}
	record.FeedbackID = "feedback_" + idHash[:32]
	record.ContentHash, err = record.bodyHash()
	if err != nil {
		return Record{}, err
	}
	if err := record.ValidateAgainst(snapshot); err != nil {
		return Record{}, err
	}
	return record, nil
}

// ParseStrict rejects unknown fields, trailing content, oversized payloads,
// invalid enums, and content-hash drift.
func ParseStrict(payload []byte) (Record, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return Record{}, errors.New("evaluation feedback record is empty")
	}
	if int64(len(payload)) > MaxRecordBytes {
		return Record{}, fmt.Errorf("evaluation feedback record exceeds %d bytes", MaxRecordBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode evaluation feedback record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Record{}, errors.New("decode evaluation feedback record: trailing JSON value")
		}
		return Record{}, fmt.Errorf("decode evaluation feedback record trailing content: %w", err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion {
		return errors.New("evaluation feedback schema version is invalid")
	}
	if !validIdentifier(record.FeedbackID) || !validIdentifier(record.Target.EvaluationRunID) || !validIdentifier(record.CaseID) || !validIdentifier(record.ReviewerRef) {
		return errors.New("evaluation feedback identity is invalid")
	}
	if record.Supersedes != "" && (!validIdentifier(record.Supersedes) || record.Supersedes == record.FeedbackID) {
		return errors.New("evaluation feedback supersedes reference is invalid")
	}
	if record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC {
		return errors.New("evaluation feedback creation time must be UTC")
	}
	if !validSHA256(record.Target.ContentHash) || !validSHA256(record.VersionFingerprint) || !validSHA256(record.ContentHash) {
		return errors.New("evaluation feedback fingerprint is invalid")
	}
	if !validVerdictReason(record.Verdict, record.ReasonCode) {
		return errors.New("evaluation feedback verdict and reason are invalid")
	}
	expected, err := record.bodyHash()
	if err != nil {
		return err
	}
	if record.ContentHash != expected {
		return ErrFeedbackTampered
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode evaluation feedback record: %w", err)
	}
	if int64(len(payload)) > MaxRecordBytes {
		return fmt.Errorf("evaluation feedback record exceeds %d bytes", MaxRecordBytes)
	}
	return nil
}

func (record Record) ValidateAgainst(snapshot replay.Snapshot) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("validate evaluation feedback target snapshot: %w", err)
	}
	if record.Target != snapshot.Reference() || record.VersionFingerprint != snapshot.Report.VersionFingerprint {
		return errors.New("evaluation feedback target does not match the immutable snapshot")
	}
	if !snapshotHasCase(snapshot, record.CaseID) {
		return errors.New("evaluation feedback references an unknown Case")
	}
	return nil
}

// Resolve validates the complete correction graph and returns the one active
// record at the end of each reviewer/Case chain.
func Resolve(snapshot replay.Snapshot, records []Record) (Summary, error) {
	if err := snapshot.Validate(); err != nil {
		return Summary{}, fmt.Errorf("validate feedback summary target snapshot: %w", err)
	}
	if len(records) > MaxRecordsPerRun {
		return Summary{}, fmt.Errorf("evaluation feedback history exceeds %d records", MaxRecordsPerRun)
	}
	byID := make(map[string]Record, len(records))
	groups := make(map[string][]Record)
	supersededBy := make(map[string]string)
	for _, record := range records {
		if err := record.ValidateAgainst(snapshot); err != nil {
			return Summary{}, err
		}
		if _, exists := byID[record.FeedbackID]; exists {
			return Summary{}, errors.New("evaluation feedback history contains a duplicate ID")
		}
		byID[record.FeedbackID] = record
		key := feedbackGroup(record)
		groups[key] = append(groups[key], record)
	}
	for _, record := range records {
		if record.Supersedes == "" {
			continue
		}
		parent, exists := byID[record.Supersedes]
		if !exists {
			return Summary{}, errors.New("evaluation feedback correction references a missing record")
		}
		if feedbackGroup(parent) != feedbackGroup(record) {
			return Summary{}, errors.New("evaluation feedback correction crosses reviewer, Case, or snapshot boundaries")
		}
		if !record.CreatedAt.After(parent.CreatedAt) {
			return Summary{}, errors.New("evaluation feedback correction time must follow its parent")
		}
		if _, exists := supersededBy[parent.FeedbackID]; exists {
			return Summary{}, errors.New("evaluation feedback correction history branches")
		}
		supersededBy[parent.FeedbackID] = record.FeedbackID
	}
	for _, group := range groups {
		roots := 0
		active := 0
		for _, record := range group {
			if record.Supersedes == "" {
				roots++
			}
			if _, superseded := supersededBy[record.FeedbackID]; !superseded {
				active++
			}
			if correctionCycle(record.FeedbackID, byID) {
				return Summary{}, errors.New("evaluation feedback correction history contains a cycle")
			}
		}
		if roots != 1 || active != 1 {
			return Summary{}, errors.New("evaluation feedback reviewer and Case history is ambiguous")
		}
	}
	active := make([]Record, 0, len(groups))
	for _, record := range records {
		if _, superseded := supersededBy[record.FeedbackID]; !superseded {
			active = append(active, record)
		}
	}
	sort.Slice(active, func(left, right int) bool {
		if active[left].CaseID == active[right].CaseID {
			return active[left].ReviewerRef < active[right].ReviewerRef
		}
		return active[left].CaseID < active[right].CaseID
	})
	return buildSummary(snapshot, records, active), nil
}

// BuildSyntheticFixture creates two independent, bounded, non-human review
// records for every Case in the repository-owned synthetic dataset.
func BuildSyntheticFixture(snapshot replay.Snapshot, createdAt time.Time) ([]Record, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	caseIDs := make([]string, 0, len(snapshot.Report.Cases))
	casePassed := make(map[string]bool, len(snapshot.Report.Cases))
	for _, result := range snapshot.Report.Cases {
		caseIDs = append(caseIDs, result.ID)
		casePassed[result.ID] = result.Passed && snapshot.Report.Status == evaluation.EvaluationPassed
	}
	sort.Strings(caseIDs)
	reviewers := []struct {
		ref    string
		reason ReasonCode
	}{
		{"synthetic-reviewer-a", ReasonExpectedBehavior},
		{"synthetic-reviewer-b", ReasonEvidenceGrounded},
	}
	records := make([]Record, 0, len(caseIDs)*len(reviewers))
	sequence := 0
	for _, caseID := range caseIDs {
		for _, reviewer := range reviewers {
			sequence++
			verdict := VerdictSafe
			reason := reviewer.reason
			if !casePassed[caseID] {
				verdict = VerdictUnsafe
				reason = ReasonMisleadingConclusion
			}
			record, err := NewRecord(snapshot, NewRecordInput{
				CaseID: caseID, ReviewerRef: reviewer.ref, Verdict: verdict,
				ReasonCode: reason, CreatedAt: createdAt.UTC().Add(time.Duration(sequence) * time.Nanosecond),
			})
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
	}
	return records, nil
}

func (record Record) bodyHash() (string, error) {
	body := recordBody{
		SchemaVersion: record.SchemaVersion, FeedbackID: record.FeedbackID, CreatedAt: record.CreatedAt,
		Target: record.Target, VersionFingerprint: record.VersionFingerprint, CaseID: record.CaseID,
		ReviewerRef: record.ReviewerRef, Verdict: record.Verdict, ReasonCode: record.ReasonCode, Supersedes: record.Supersedes,
	}
	hash, err := fingerprint.JSON(body)
	if err != nil {
		return "", fmt.Errorf("fingerprint evaluation feedback body: %w", err)
	}
	return hash, nil
}

func buildSummary(snapshot replay.Snapshot, records, active []Record) Summary {
	caseIDs := make([]string, 0, len(snapshot.Report.Cases))
	for _, result := range snapshot.Report.Cases {
		caseIDs = append(caseIDs, result.ID)
	}
	sort.Strings(caseIDs)
	cases := make([]CaseSummary, 0, len(caseIDs))
	for _, caseID := range caseIDs {
		item := CaseSummary{CaseID: caseID, ActiveFeedback: []Projection{}}
		for _, record := range active {
			if record.CaseID != caseID {
				continue
			}
			item.ActiveFeedback = append(item.ActiveFeedback, Projection{
				FeedbackID: record.FeedbackID, ContentHash: record.ContentHash, CaseID: record.CaseID,
				ReviewerRef: record.ReviewerRef, Verdict: record.Verdict, ReasonCode: record.ReasonCode,
			})
		}
		cases = append(cases, item)
	}
	return Summary{
		SchemaVersion: SummarySchemaVersion, Target: snapshot.Reference(), VersionFingerprint: snapshot.Report.VersionFingerprint,
		DataSource: SyntheticDataSource, ProductionAction: false, RealReviewerCount: 0, ExternalNetworkCalls: 0,
		RecordCount: len(records), ActiveCount: len(active), Cases: cases,
	}
}

func correctionCycle(start string, records map[string]Record) bool {
	seen := make(map[string]struct{})
	current := start
	for current != "" {
		if _, exists := seen[current]; exists {
			return true
		}
		seen[current] = struct{}{}
		record, exists := records[current]
		if !exists {
			return false
		}
		current = record.Supersedes
	}
	return false
}

func feedbackGroup(record Record) string {
	return record.Target.EvaluationRunID + "\x00" + record.Target.ContentHash + "\x00" + record.CaseID + "\x00" + record.ReviewerRef
}

func snapshotHasCase(snapshot replay.Snapshot, caseID string) bool {
	for _, result := range snapshot.Report.Cases {
		if result.ID == caseID {
			return true
		}
	}
	return false
}

func validVerdictReason(verdict Verdict, reason ReasonCode) bool {
	switch verdict {
	case VerdictSafe:
		return reason == ReasonExpectedBehavior || reason == ReasonEvidenceGrounded
	case VerdictUnsafe:
		return reason == ReasonMisleadingConclusion || reason == ReasonUnsafeRecommendation
	case VerdictUnsure:
		return reason == ReasonInsufficientContext
	default:
		return false
	}
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
