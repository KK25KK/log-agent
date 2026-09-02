package jointrca

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

const (
	DatasetSchemaVersion = "joint-rca-evaluation-dataset-v1"
	DatasetID            = "joint-rca-synthetic-v1"
	EvaluationVersion    = "joint-rca-evaluation-v1"
	GatePolicyVersion    = "joint-rca-gate-v1"
	SyntheticDataSource  = "SYNTHETIC_MOCK"
)

var (
	ErrGateFailed     = errors.New("joint RCA evaluation gate failed")
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

//go:embed fixtures/synthetic-v1.json
var fixtureFiles embed.FS

type Dataset struct {
	SchemaVersion          string `json:"schema_version"`
	DatasetID              string `json:"dataset_id"`
	DataSource             string `json:"data_source"`
	RealIncidentCount      int    `json:"real_incident_count"`
	ExpertLabelCount       int    `json:"expert_label_count"`
	CredentialsRequired    bool   `json:"credentials_required"`
	ExternalNetworkCalls   int    `json:"external_network_calls"`
	ProductionClaimAllowed bool   `json:"production_claim_allowed"`
	Cases                  []Case `json:"cases"`
	Fingerprint            string `json:"-"`
}

type Case struct {
	ID                     string                          `json:"id"`
	TraceState             string                          `json:"trace_state"`
	CodeState              domain.CodeInvestigationStatus  `json:"code_state"`
	DeploymentState        domain.DeploymentStatus         `json:"deployment_state"`
	AnchorKind             string                          `json:"anchor_kind"`
	DiffChecked            bool                            `json:"diff_checked"`
	Changed                bool                            `json:"changed"`
	ExpectedStatus         domain.JointRCAStatus           `json:"expected_status"`
	ExpectedReasonCode     string                          `json:"expected_reason_code,omitempty"`
	ExpectedCandidateCount int                             `json:"expected_candidate_count"`
	ExpectedVerdict        domain.JointRCACandidateVerdict `json:"expected_verdict,omitempty"`
	ExpectedChangeRelation domain.JointRCAChangeRelation   `json:"expected_change_relation,omitempty"`
	ExpectedConfidence     float64                         `json:"expected_confidence"`
}

type DataBoundary struct {
	DataSource             string `json:"data_source"`
	RealIncidentCount      int    `json:"real_incident_count"`
	ExpertLabelCount       int    `json:"expert_label_count"`
	CredentialsRequired    bool   `json:"credentials_required"`
	ExternalNetworkCalls   int    `json:"external_network_calls"`
	ProductionClaimAllowed bool   `json:"production_claim_allowed"`
}

type GatePolicy struct {
	Version                    string  `json:"version"`
	MinCasePassRate            float64 `json:"min_case_pass_rate"`
	MinStatusAccuracy          float64 `json:"min_status_accuracy"`
	MinVerdictAccuracy         float64 `json:"min_verdict_accuracy"`
	MinReferenceIntegrity      float64 `json:"min_reference_integrity"`
	MinDeterministicReplayRate float64 `json:"min_deterministic_replay_rate"`
	MaxUnsafeClaims            int     `json:"max_unsafe_claims"`
	MaxAutomaticActions        int     `json:"max_automatic_actions"`
	MaxExternalNetworkCalls    int     `json:"max_external_network_calls"`
}

type Metrics struct {
	TotalCases              int     `json:"total_cases"`
	PassedCases             int     `json:"passed_cases"`
	CasePassRate            float64 `json:"case_pass_rate"`
	StatusAccuracy          float64 `json:"status_accuracy"`
	VerdictAccuracy         float64 `json:"verdict_accuracy"`
	ReferenceIntegrity      float64 `json:"reference_integrity"`
	DeterministicReplayRate float64 `json:"deterministic_replay_rate"`
	UnsafeClaims            int     `json:"unsafe_claims"`
	AutomaticActions        int     `json:"automatic_actions"`
	ExternalNetworkCalls    int     `json:"external_network_calls"`
}

type CaseResult struct {
	ID                  string                          `json:"id"`
	Passed              bool                            `json:"passed"`
	FailureReasons      []string                        `json:"failure_reasons,omitempty"`
	ExpectedStatus      domain.JointRCAStatus           `json:"expected_status"`
	ActualStatus        domain.JointRCAStatus           `json:"actual_status"`
	ExpectedVerdict     domain.JointRCACandidateVerdict `json:"expected_verdict,omitempty"`
	ActualVerdict       domain.JointRCACandidateVerdict `json:"actual_verdict,omitempty"`
	ReferenceIntegrity  bool                            `json:"reference_integrity"`
	DeterministicReplay bool                            `json:"deterministic_replay"`
}

type GateResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type Report struct {
	EvaluationVersion  string       `json:"evaluation_version"`
	DatasetID          string       `json:"dataset_id"`
	DatasetFingerprint string       `json:"dataset_fingerprint"`
	JointRCAVersion    string       `json:"joint_rca_version"`
	ConfidenceMethod   string       `json:"confidence_method"`
	DataBoundary       DataBoundary `json:"data_boundary"`
	Policy             GatePolicy   `json:"gate_policy"`
	Status             string       `json:"status"`
	Metrics            Metrics      `json:"metrics"`
	Gates              []GateResult `json:"gates"`
	Cases              []CaseResult `json:"cases"`
	GeneratedAt        time.Time    `json:"generated_at"`
}

func LoadSyntheticV1() (Dataset, error) {
	payload, err := fixtureFiles.ReadFile("fixtures/synthetic-v1.json")
	if err != nil {
		return Dataset{}, err
	}
	return ParseDataset(payload)
}

func ParseDataset(payload []byte) (Dataset, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("decode joint RCA dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Dataset{}, errors.New("decode joint RCA dataset: trailing JSON value")
		}
		return Dataset{}, fmt.Errorf("decode joint RCA dataset trailing content: %w", err)
	}
	if err := validateDataset(dataset); err != nil {
		return Dataset{}, err
	}
	dataset.Fingerprint, _ = fingerprint.JSON(dataset)
	return dataset, nil
}

func validateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != DatasetSchemaVersion || dataset.DatasetID != DatasetID || dataset.DataSource != SyntheticDataSource {
		return errors.New("joint RCA dataset identity is invalid")
	}
	if dataset.RealIncidentCount != 0 || dataset.ExpertLabelCount != 0 || dataset.CredentialsRequired || dataset.ExternalNetworkCalls != 0 || dataset.ProductionClaimAllowed {
		return errors.New("synthetic joint RCA dataset cannot claim real data, expert labels, credentials, network, or production approval")
	}
	if len(dataset.Cases) < 6 || len(dataset.Cases) > 32 {
		return errors.New("joint RCA dataset must contain between 6 and 32 cases")
	}
	seen := make(map[string]struct{}, len(dataset.Cases))
	coverage := make(map[domain.JointRCAStatus]bool)
	for _, item := range dataset.Cases {
		if !identifierPattern.MatchString(item.ID) {
			return fmt.Errorf("invalid joint RCA case ID %q", item.ID)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("duplicate joint RCA case ID %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if err := validateCase(item); err != nil {
			return fmt.Errorf("case %q: %w", item.ID, err)
		}
		coverage[item.ExpectedStatus] = true
	}
	for _, status := range []domain.JointRCAStatus{domain.JointRCAComplete, domain.JointRCAInconclusive, domain.JointRCAUnavailable, domain.JointRCANeedsReview, domain.JointRCASkipped} {
		if !coverage[status] {
			return fmt.Errorf("joint RCA dataset lacks %s coverage", status)
		}
	}
	return nil
}

func validateCase(item Case) error {
	if item.TraceState != "COMPLETE" && item.TraceState != "INCOMPLETE" {
		return errors.New("trace state is invalid")
	}
	switch item.CodeState {
	case domain.CodeInvestigationComplete, domain.CodeInvestigationNoMatch, domain.CodeInvestigationPartial, domain.CodeInvestigationSkipped, domain.CodeInvestigationUnavailable:
	default:
		return errors.New("code state is invalid")
	}
	switch item.DeploymentState {
	case domain.DeploymentComplete, domain.DeploymentUnavailable, domain.DeploymentConflict:
	default:
		return errors.New("deployment state is invalid")
	}
	if (item.CodeState == domain.CodeInvestigationComplete || item.CodeState == domain.CodeInvestigationPartial || item.CodeState == domain.CodeInvestigationNoMatch) && item.DeploymentState != domain.DeploymentComplete {
		return errors.New("search result requires a complete deployment")
	}
	switch item.ExpectedStatus {
	case domain.JointRCAComplete, domain.JointRCAInconclusive, domain.JointRCAUnavailable, domain.JointRCANeedsReview, domain.JointRCASkipped:
	default:
		return errors.New("expected status is invalid")
	}
	if item.AnchorKind != "NONE" && item.AnchorKind != string(domain.RuntimeAnchorStackFrame) && item.AnchorKind != string(domain.RuntimeAnchorErrorText) {
		return errors.New("anchor kind is invalid")
	}
	if item.Changed && !item.DiffChecked {
		return errors.New("changed requires a trusted diff")
	}
	if item.ExpectedCandidateCount < 0 || item.ExpectedCandidateCount > domain.JointRCAMaxCandidates || math.IsNaN(item.ExpectedConfidence) || item.ExpectedConfidence < 0 || item.ExpectedConfidence > .75 {
		return errors.New("expected candidate count or confidence is invalid")
	}
	if item.ExpectedCandidateCount == 0 && (item.ExpectedVerdict != "" || item.ExpectedChangeRelation != "" || item.ExpectedConfidence != 0) {
		return errors.New("zero-candidate case contains candidate expectations")
	}
	if item.ExpectedCandidateCount > 0 && (item.ExpectedVerdict == "" || item.ExpectedChangeRelation == "") {
		return errors.New("candidate case lacks verdict or change relation")
	}
	if item.ExpectedCandidateCount > 0 {
		switch item.ExpectedVerdict {
		case domain.JointRCASupportedCandidate, domain.JointRCACandidateRefuted, domain.JointRCACandidateUnknown:
		default:
			return errors.New("expected verdict is invalid")
		}
		switch item.ExpectedChangeRelation {
		case domain.JointRCAChangeOverlap, domain.JointRCAChangeUnchanged, domain.JointRCAChangeUnknown:
		default:
			return errors.New("expected change relation is invalid")
		}
	}
	return nil
}

func FixedGatePolicy() GatePolicy {
	return GatePolicy{
		Version: GatePolicyVersion, MinCasePassRate: 1, MinStatusAccuracy: 1, MinVerdictAccuracy: 1,
		MinReferenceIntegrity: 1, MinDeterministicReplayRate: 1, MaxUnsafeClaims: 0,
		MaxAutomaticActions: 0, MaxExternalNetworkCalls: 0,
	}
}

func Evaluate(dataset Dataset, generatedAt time.Time) (Report, error) {
	if err := validateDataset(dataset); err != nil {
		return Report{}, err
	}
	policy := FixedGatePolicy()
	datasetFingerprint, err := fingerprint.JSON(dataset)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		EvaluationVersion: EvaluationVersion, DatasetID: dataset.DatasetID, DatasetFingerprint: datasetFingerprint,
		JointRCAVersion: domain.JointRCAVersion, ConfidenceMethod: domain.JointRCAConfidenceMethod,
		DataBoundary: DataBoundary{
			DataSource: dataset.DataSource, RealIncidentCount: dataset.RealIncidentCount, ExpertLabelCount: dataset.ExpertLabelCount,
			CredentialsRequired: dataset.CredentialsRequired, ExternalNetworkCalls: dataset.ExternalNetworkCalls,
			ProductionClaimAllowed: dataset.ProductionClaimAllowed,
		},
		Policy: policy, GeneratedAt: generatedAt.UTC(),
	}
	statusMatches, verdictMatches, referenceMatches, replayMatches := 0, 0, 0, 0
	verdictCases := 0
	for _, item := range dataset.Cases {
		trace, code := buildInputs(item)
		analysis := application.BuildJointRCA(trace, code, generatedAt)
		again := application.BuildJointRCA(trace, code, generatedAt)
		result := evaluateCase(item, analysis, again)
		report.Cases = append(report.Cases, result)
		if result.Passed {
			report.Metrics.PassedCases++
		}
		if result.ExpectedStatus == result.ActualStatus {
			statusMatches++
		}
		if item.ExpectedCandidateCount > 0 {
			verdictCases++
			if result.ExpectedVerdict == result.ActualVerdict {
				verdictMatches++
			}
		}
		if result.ReferenceIntegrity {
			referenceMatches++
		}
		if result.DeterministicReplay {
			replayMatches++
		}
		report.Metrics.UnsafeClaims += unsafeClaimCount(analysis)
		for _, action := range analysis.Actions {
			if action.ExecutionMode != "HUMAN_REVIEW_ONLY" {
				report.Metrics.AutomaticActions++
			}
		}
	}
	report.Metrics.TotalCases = len(dataset.Cases)
	report.Metrics.ExternalNetworkCalls = dataset.ExternalNetworkCalls
	report.Metrics.CasePassRate = ratio(report.Metrics.PassedCases, report.Metrics.TotalCases)
	report.Metrics.StatusAccuracy = ratio(statusMatches, report.Metrics.TotalCases)
	if verdictCases == 0 {
		report.Metrics.VerdictAccuracy = 1
	} else {
		report.Metrics.VerdictAccuracy = ratio(verdictMatches, verdictCases)
	}
	report.Metrics.ReferenceIntegrity = ratio(referenceMatches, report.Metrics.TotalCases)
	report.Metrics.DeterministicReplayRate = ratio(replayMatches, report.Metrics.TotalCases)
	report.Gates = []GateResult{
		{"case_pass_rate", report.Metrics.CasePassRate >= policy.MinCasePassRate},
		{"status_accuracy", report.Metrics.StatusAccuracy >= policy.MinStatusAccuracy},
		{"verdict_accuracy", report.Metrics.VerdictAccuracy >= policy.MinVerdictAccuracy},
		{"reference_integrity", report.Metrics.ReferenceIntegrity >= policy.MinReferenceIntegrity},
		{"deterministic_replay", report.Metrics.DeterministicReplayRate >= policy.MinDeterministicReplayRate},
		{"unsafe_claims", report.Metrics.UnsafeClaims <= policy.MaxUnsafeClaims},
		{"automatic_actions", report.Metrics.AutomaticActions <= policy.MaxAutomaticActions},
		{"external_network_calls", report.Metrics.ExternalNetworkCalls <= policy.MaxExternalNetworkCalls},
	}
	report.Status = "PASSED"
	for _, gate := range report.Gates {
		if !gate.Passed {
			report.Status = "FAILED"
			return report, ErrGateFailed
		}
	}
	return report, nil
}

func buildInputs(item Case) (*domain.TraceInvestigation, *domain.CodeInvestigation) {
	trace := &domain.TraceInvestigation{Status: domain.TraceInvestigationComplete, Complete: true}
	if item.TraceState == "INCOMPLETE" {
		trace.Status, trace.Complete = domain.TraceInvestigationPartial, false
	}
	if item.AnchorKind == "NONE" {
		trace.AnchorSet = &domain.RuntimeAnchorSet{Version: domain.RuntimeAnchorVersion, Status: domain.RuntimeAnchorsNone}
	} else {
		anchor := domain.RuntimeAnchor{ID: "anchor-" + item.ID, Kind: domain.RuntimeAnchorKind(item.AnchorKind), EventID: "event-" + item.ID, MemberID: "member-1", Value: "payment timeout"}
		if anchor.Kind == domain.RuntimeAnchorStackFrame {
			anchor.Value, anchor.File, anchor.Line, anchor.Symbol = "", "internal/payment.go", 42, "charge"
		}
		trace.AnchorSet = &domain.RuntimeAnchorSet{Version: domain.RuntimeAnchorVersion, Status: domain.RuntimeAnchorsComplete, Anchors: []domain.RuntimeAnchor{anchor}}
	}
	code := &domain.CodeInvestigation{Version: domain.CodeEvidenceVersion, Status: item.CodeState, DiffChecked: item.DiffChecked}
	if item.CodeState == domain.CodeInvestigationComplete || item.CodeState == domain.CodeInvestigationPartial || item.CodeState == domain.CodeInvestigationNoMatch {
		code.Deployment = testDeployment(domain.DeploymentComplete)
	}
	if item.CodeState == domain.CodeInvestigationUnavailable && item.DeploymentState == domain.DeploymentConflict {
		code.ReasonCode = domain.CodeReasonDeploymentConflict
		code.Deployment = testDeployment(domain.DeploymentConflict)
	} else if item.CodeState == domain.CodeInvestigationUnavailable {
		code.ReasonCode = domain.CodeReasonProviderUnavailable
	}
	if item.CodeState == domain.CodeInvestigationPartial {
		code.ReasonCode = domain.CodeReasonResultTruncated
	}
	if (item.CodeState == domain.CodeInvestigationComplete || item.CodeState == domain.CodeInvestigationPartial) && item.AnchorKind != "NONE" {
		anchor := trace.AnchorSet.Anchors[0]
		kind := domain.CodeMatchExactText
		if anchor.Kind == domain.RuntimeAnchorStackFrame {
			kind = domain.CodeMatchStackFrame
		}
		code.Matches = []domain.CodeMatch{{ID: "code-" + item.ID, Kind: kind, AnchorID: anchor.ID, File: "internal/payment.go", MatchLine: 42, ChangedSincePrevious: item.Changed}}
	}
	return trace, code
}

func testDeployment(status domain.DeploymentStatus) *domain.DeploymentEvidence {
	value := &domain.DeploymentEvidence{
		Version: domain.DeploymentEvidenceVersion, Status: status, Service: "dam-server", Environment: "test", SourceVersion: "evaluation-v1",
	}
	if status == domain.DeploymentComplete {
		value.RepositoryID, value.CommitSHA = "dam", strings.Repeat("a", 40)
	}
	value.Fingerprint, _ = domain.DeploymentEvidenceFingerprint(*value)
	return value
}

func evaluateCase(item Case, analysis, replayed *domain.JointRCA) CaseResult {
	result := CaseResult{ID: item.ID, ExpectedStatus: item.ExpectedStatus, ActualStatus: analysis.Status, ExpectedVerdict: item.ExpectedVerdict}
	if len(analysis.Candidates) > 0 {
		result.ActualVerdict = analysis.Candidates[0].Verdict
	}
	if analysis.Status != item.ExpectedStatus {
		result.FailureReasons = append(result.FailureReasons, "status_mismatch")
	}
	if analysis.ReasonCode != item.ExpectedReasonCode {
		result.FailureReasons = append(result.FailureReasons, "reason_code_mismatch")
	}
	if len(analysis.Candidates) != item.ExpectedCandidateCount {
		result.FailureReasons = append(result.FailureReasons, "candidate_count_mismatch")
	}
	if item.ExpectedCandidateCount > 0 && len(analysis.Candidates) > 0 {
		candidate := analysis.Candidates[0]
		if candidate.Verdict != item.ExpectedVerdict {
			result.FailureReasons = append(result.FailureReasons, "verdict_mismatch")
		}
		if candidate.ChangeRelation != item.ExpectedChangeRelation {
			result.FailureReasons = append(result.FailureReasons, "change_relation_mismatch")
		}
		if math.Abs(candidate.Confidence-item.ExpectedConfidence) > .0001 {
			result.FailureReasons = append(result.FailureReasons, "confidence_mismatch")
		}
	}
	result.ReferenceIntegrity = referencesValid(analysis)
	if !result.ReferenceIntegrity {
		result.FailureReasons = append(result.FailureReasons, "reference_integrity_failed")
	}
	left, _ := json.Marshal(analysis)
	right, _ := json.Marshal(replayed)
	result.DeterministicReplay = bytes.Equal(left, right)
	if !result.DeterministicReplay {
		result.FailureReasons = append(result.FailureReasons, "deterministic_replay_failed")
	}
	if unsafeClaimCount(analysis) > 0 {
		result.FailureReasons = append(result.FailureReasons, "unsafe_claim")
	}
	for _, action := range analysis.Actions {
		if action.ExecutionMode != "HUMAN_REVIEW_ONLY" {
			result.FailureReasons = append(result.FailureReasons, "automatic_action")
		}
	}
	sort.Strings(result.FailureReasons)
	result.Passed = len(result.FailureReasons) == 0
	return result
}

func referencesValid(analysis *domain.JointRCA) bool {
	candidates := make(map[string]domain.JointRCACandidate, len(analysis.Candidates))
	for _, candidate := range analysis.Candidates {
		if candidate.ID == "" {
			return false
		}
		candidates[candidate.ID] = candidate
	}
	factors := make(map[string]domain.JointRCAFactor, len(analysis.Factors))
	for _, factor := range analysis.Factors {
		if factor.ID == "" || candidates[factor.CandidateID].ID == "" {
			return false
		}
		factors[factor.ID] = factor
	}
	for _, candidate := range analysis.Candidates {
		if len(candidate.FactorIDs) != 5 {
			return false
		}
		for _, factorID := range candidate.FactorIDs {
			factor, exists := factors[factorID]
			if !exists || factor.CandidateID != candidate.ID {
				return false
			}
		}
	}
	for _, action := range analysis.Actions {
		if candidates[action.CandidateID].ID == "" || action.ExecutionMode != "HUMAN_REVIEW_ONLY" {
			return false
		}
	}
	return true
}

func unsafeClaimCount(analysis *domain.JointRCA) int {
	count := 0
	for _, candidate := range analysis.Candidates {
		lower := strings.ToLower(candidate.Statement)
		for _, forbidden := range []string{"根因是", "已确定根因", "根因已经确认", "human_confirmed"} {
			if strings.Contains(lower, strings.ToLower(forbidden)) {
				count++
			}
		}
	}
	for _, action := range analysis.Actions {
		for _, forbidden := range []string{"自动修复", "自动回滚", "自动修改生产"} {
			if strings.Contains(action.Statement, forbidden) {
				count++
			}
		}
	}
	return count
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}
