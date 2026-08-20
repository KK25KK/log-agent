package rollout

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"logagent/internal/evaluation"
	"logagent/internal/evaluation/feedback"
	"logagent/internal/evaluation/replay"
	"logagent/internal/fingerprint"
)

const (
	PolicySchemaVersion   = "rollout-rehearsal-policy-v1"
	DecisionSchemaVersion = "rollout-rehearsal-decision-v1"
)

var (
	ErrRehearsalNotPassed = errors.New("rollout rehearsal did not pass")
	policyVersionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Phase string

const (
	PhasePreflight            Phase = "PREFLIGHT"
	PhaseSimulatedActivePilot Phase = "SIMULATED_ACTIVE_PILOT"
)

type Status string

const (
	StatusPassed               Status = "REHEARSAL_PASSED"
	StatusBlocked              Status = "REHEARSAL_BLOCKED"
	StatusRollbackRecommended  Status = "REHEARSAL_ROLLBACK_RECOMMENDED"
	StatusInsufficientEvidence Status = "REHEARSAL_INSUFFICIENT_EVIDENCE"
)

type ReasonCode string

const (
	ReasonRunsIncomparable     ReasonCode = "RUNS_INCOMPARABLE"
	ReasonFeedbackCoverage     ReasonCode = "FEEDBACK_COVERAGE_INCOMPLETE"
	ReasonReviewerQuorum       ReasonCode = "REVIEWER_QUORUM_NOT_MET"
	ReasonReviewerUnsure       ReasonCode = "REVIEWER_UNSURE"
	ReasonReviewerDisagreement ReasonCode = "REVIEWER_DISAGREEMENT"
	ReasonCandidateFailed      ReasonCode = "CANDIDATE_EVALUATION_FAILED"
	ReasonComparisonRegression ReasonCode = "COMPARISON_REGRESSION"
	ReasonRequiredGateMissing  ReasonCode = "REQUIRED_GATE_MISSING"
	ReasonRequiredGateFailed   ReasonCode = "REQUIRED_GATE_FAILED"
	ReasonUnsafeFeedback       ReasonCode = "UNSAFE_FEEDBACK"
)

type Policy struct {
	SchemaVersion           string   `json:"schema_version"`
	Version                 string   `json:"version"`
	ReviewerQuorum          int      `json:"reviewer_quorum"`
	RequiredGateCodes       []string `json:"required_gate_codes"`
	BlockOnAnyRegression    bool     `json:"block_on_any_regression"`
	DataSource              string   `json:"data_source"`
	ProductionActionAllowed bool     `json:"production_action_allowed"`
}

type FeedbackCounts struct {
	TotalCases        int `json:"total_cases"`
	CoveredCases      int `json:"covered_cases"`
	ActiveRecords     int `json:"active_records"`
	SafeRecords       int `json:"safe_records"`
	UnsafeRecords     int `json:"unsafe_records"`
	UnsureRecords     int `json:"unsure_records"`
	DisagreementCases int `json:"disagreement_cases"`
}

// Decision is a non-actionable projection. It deliberately excludes report
// content, Evidence, reviewer prose, deployment targets, and executable steps.
type Decision struct {
	SchemaVersion           string                  `json:"schema_version"`
	Status                  Status                  `json:"status"`
	Phase                   Phase                   `json:"phase"`
	Base                    replay.SourceReference  `json:"base"`
	Candidate               replay.SourceReference  `json:"candidate"`
	ComparisonStatus        replay.ComparisonStatus `json:"comparison_status"`
	PolicyVersion           string                  `json:"policy_version"`
	PolicyFingerprint       string                  `json:"policy_fingerprint"`
	Feedback                FeedbackCounts          `json:"feedback"`
	ReasonCodes             []ReasonCode            `json:"reason_codes"`
	DataSource              string                  `json:"data_source"`
	ProductionActionAllowed bool                    `json:"production_action_allowed"`
}

var defaultRequiredGates = []string{
	"all_cases_executed",
	"call_budget_breaches",
	"case_pass_rate",
	"cause_exact_accuracy",
	"cause_verdict_accuracy",
	"conclusive_recall",
	"cost_budget_breaches",
	"evidence_contract_accuracy",
	"evidence_coverage",
	"finding_exact_accuracy",
	"misleading_rate",
	"outcome_accuracy",
	"production_output_accuracy",
	"query_contract_accuracy",
	"recommendation_exact_accuracy",
	"trace_contract_accuracy",
	"trace_dropped_events",
	"unexpected_cause_verdicts",
}

func DefaultPolicy() Policy {
	return Policy{
		SchemaVersion: PolicySchemaVersion, Version: "synthetic-rollout-policy-v1", ReviewerQuorum: 2,
		RequiredGateCodes: append([]string(nil), defaultRequiredGates...), BlockOnAnyRegression: true,
		DataSource: feedback.SyntheticDataSource, ProductionActionAllowed: false,
	}
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != PolicySchemaVersion || !policyVersionPattern.MatchString(policy.Version) {
		return errors.New("rollout rehearsal policy identity is invalid")
	}
	if policy.ReviewerQuorum < 2 || policy.ReviewerQuorum > 10 {
		return errors.New("rollout rehearsal reviewer quorum is invalid")
	}
	if !policy.BlockOnAnyRegression {
		return errors.New("rollout rehearsal policy cannot ignore regressions")
	}
	if policy.DataSource != feedback.SyntheticDataSource || policy.ProductionActionAllowed {
		return errors.New("rollout rehearsal policy violates the synthetic non-actionable boundary")
	}
	if len(policy.RequiredGateCodes) == 0 || len(policy.RequiredGateCodes) > 64 {
		return errors.New("rollout rehearsal required Gate set is invalid")
	}
	previous := ""
	for _, code := range policy.RequiredGateCodes {
		if !policyVersionPattern.MatchString(code) || code <= previous {
			return errors.New("rollout rehearsal required Gate codes must be unique and sorted")
		}
		previous = code
	}
	return nil
}

func (policy Policy) Fingerprint() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	hash, err := fingerprint.JSON(policy)
	if err != nil {
		return "", fmt.Errorf("fingerprint rollout rehearsal policy: %w", err)
	}
	return hash, nil
}

// Rehearse strictly validates all immutable inputs and applies the documented
// precedence: insufficient evidence, then blocked/rollback, then passed.
func Rehearse(base, candidate replay.Snapshot, records []feedback.Record, policy Policy, phase Phase) (Decision, error) {
	if phase != PhasePreflight && phase != PhaseSimulatedActivePilot {
		return Decision{}, errors.New("rollout rehearsal phase is invalid")
	}
	if err := base.Validate(); err != nil {
		return Decision{}, fmt.Errorf("validate rollout rehearsal base snapshot: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return Decision{}, fmt.Errorf("validate rollout rehearsal candidate snapshot: %w", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return Decision{}, err
	}
	comparison, err := replay.Compare(base, candidate)
	if err != nil {
		return Decision{}, fmt.Errorf("compare rollout rehearsal snapshots: %w", err)
	}
	summary, err := feedback.Resolve(candidate, records)
	if err != nil {
		return Decision{}, fmt.Errorf("resolve rollout rehearsal feedback: %w", err)
	}
	counts, coverageReasons := evaluateFeedback(summary, policy.ReviewerQuorum)
	decision := Decision{
		SchemaVersion: DecisionSchemaVersion, Phase: phase, Base: base.Reference(), Candidate: candidate.Reference(),
		ComparisonStatus: comparison.Status, PolicyVersion: policy.Version, PolicyFingerprint: policyFingerprint,
		Feedback: counts, DataSource: feedback.SyntheticDataSource, ProductionActionAllowed: false,
	}
	if comparison.Status == replay.ComparisonIncomparable {
		coverageReasons = append([]ReasonCode{ReasonRunsIncomparable}, coverageReasons...)
	}
	if len(coverageReasons) > 0 {
		decision.Status = StatusInsufficientEvidence
		decision.ReasonCodes = uniqueReasons(coverageReasons)
		return decision, decision.Validate()
	}
	blockingReasons, err := evaluateBlocking(candidate, comparison, summary, policy)
	if err != nil {
		return Decision{}, err
	}
	if len(blockingReasons) > 0 {
		if phase == PhaseSimulatedActivePilot {
			decision.Status = StatusRollbackRecommended
		} else {
			decision.Status = StatusBlocked
		}
		decision.ReasonCodes = uniqueReasons(blockingReasons)
		return decision, decision.Validate()
	}
	decision.Status = StatusPassed
	decision.ReasonCodes = []ReasonCode{}
	return decision, decision.Validate()
}

func (decision Decision) Validate() error {
	if decision.SchemaVersion != DecisionSchemaVersion || decision.Base.EvaluationRunID == "" || decision.Base.ContentHash == "" ||
		decision.Candidate.EvaluationRunID == "" || decision.Candidate.ContentHash == "" || decision.PolicyVersion == "" || decision.PolicyFingerprint == "" {
		return errors.New("rollout rehearsal decision identity is invalid")
	}
	if decision.Phase != PhasePreflight && decision.Phase != PhaseSimulatedActivePilot {
		return errors.New("rollout rehearsal decision phase is invalid")
	}
	if decision.DataSource != feedback.SyntheticDataSource || decision.ProductionActionAllowed {
		return errors.New("rollout rehearsal decision violates the synthetic non-actionable boundary")
	}
	switch decision.Status {
	case StatusPassed:
		if len(decision.ReasonCodes) != 0 || decision.ComparisonStatus != replay.ComparisonComparable {
			return errors.New("passed rollout rehearsal decision contains failure reasons")
		}
	case StatusBlocked, StatusRollbackRecommended, StatusInsufficientEvidence:
		if len(decision.ReasonCodes) == 0 {
			return errors.New("non-passed rollout rehearsal decision has no reason")
		}
	default:
		return errors.New("rollout rehearsal decision status is invalid")
	}
	if decision.Status == StatusRollbackRecommended && decision.Phase != PhaseSimulatedActivePilot {
		return errors.New("rollback recommendation requires the simulated active-pilot phase")
	}
	if decision.Status == StatusBlocked && decision.Phase != PhasePreflight {
		return errors.New("blocked decision requires the preflight phase")
	}
	if !sort.SliceIsSorted(decision.ReasonCodes, func(left, right int) bool { return decision.ReasonCodes[left] < decision.ReasonCodes[right] }) {
		return errors.New("rollout rehearsal decision reasons are not sorted")
	}
	return nil
}

func evaluateFeedback(summary feedback.Summary, quorum int) (FeedbackCounts, []ReasonCode) {
	counts := FeedbackCounts{TotalCases: len(summary.Cases)}
	reasons := make([]ReasonCode, 0, 4)
	for _, item := range summary.Cases {
		verdicts := make(map[feedback.Verdict]struct{})
		if len(item.ActiveFeedback) > 0 {
			counts.CoveredCases++
		}
		if len(item.ActiveFeedback) < quorum {
			reasons = append(reasons, ReasonReviewerQuorum)
		}
		for _, record := range item.ActiveFeedback {
			counts.ActiveRecords++
			verdicts[record.Verdict] = struct{}{}
			switch record.Verdict {
			case feedback.VerdictSafe:
				counts.SafeRecords++
			case feedback.VerdictUnsafe:
				counts.UnsafeRecords++
			case feedback.VerdictUnsure:
				counts.UnsureRecords++
			}
		}
		if _, unsure := verdicts[feedback.VerdictUnsure]; unsure {
			reasons = append(reasons, ReasonReviewerUnsure)
		}
		if len(verdicts) > 1 {
			counts.DisagreementCases++
			reasons = append(reasons, ReasonReviewerDisagreement)
		}
	}
	if counts.CoveredCases != counts.TotalCases {
		reasons = append(reasons, ReasonFeedbackCoverage)
	}
	return counts, uniqueReasons(reasons)
}

func evaluateBlocking(candidate replay.Snapshot, comparison replay.Comparison, summary feedback.Summary, policy Policy) ([]ReasonCode, error) {
	reasons := make([]ReasonCode, 0, 5)
	if candidate.Report.Status != evaluation.EvaluationPassed || candidate.FailureCode != replay.FailureNone {
		reasons = append(reasons, ReasonCandidateFailed)
	}
	if len(comparison.Regressions) > 0 {
		reasons = append(reasons, ReasonComparisonRegression)
	}
	gates := make(map[string]bool, len(candidate.Report.Gates))
	for _, gate := range candidate.Report.Gates {
		if _, exists := gates[gate.Code]; exists {
			return nil, errors.New("candidate evaluation contains duplicate Gate codes")
		}
		gates[gate.Code] = gate.Passed
	}
	for _, required := range policy.RequiredGateCodes {
		passed, exists := gates[required]
		if !exists {
			reasons = append(reasons, ReasonRequiredGateMissing)
			continue
		}
		if !passed {
			reasons = append(reasons, ReasonRequiredGateFailed)
		}
	}
	for _, item := range summary.Cases {
		for _, record := range item.ActiveFeedback {
			if record.Verdict == feedback.VerdictUnsafe {
				reasons = append(reasons, ReasonUnsafeFeedback)
			}
		}
	}
	return uniqueReasons(reasons), nil
}

func uniqueReasons(values []ReasonCode) []ReasonCode {
	seen := make(map[ReasonCode]struct{}, len(values))
	result := make([]ReasonCode, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}
