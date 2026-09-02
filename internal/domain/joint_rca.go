package domain

import "time"

const (
	JointRCAVersion          = "joint-rca-v1"
	JointRCAConfidenceMethod = "deterministic-joint-evidence-score-v1"
	JointRCAMaxCandidates    = 8
	JointRCAMaxFactors       = JointRCAMaxCandidates * 5
	JointRCAMaxActions       = JointRCAMaxCandidates * 3
)

type JointRCAStatus string

const (
	JointRCAComplete     JointRCAStatus = "COMPLETE"
	JointRCAInconclusive JointRCAStatus = "INCONCLUSIVE"
	JointRCAUnavailable  JointRCAStatus = "UNAVAILABLE"
	JointRCANeedsReview  JointRCAStatus = "NEEDS_REVIEW"
	JointRCASkipped      JointRCAStatus = "SKIPPED"
)

const (
	JointRCAReasonTraceIncomplete  = "trace_incomplete"
	JointRCAReasonNoRuntimeAnchors = "no_runtime_anchors"
	JointRCAReasonCodeUnavailable  = "code_evidence_unavailable"
	JointRCAReasonDeploymentReview = "deployment_requires_review"
	JointRCAReasonNoCodeMatch      = "no_code_match"
	JointRCAReasonCodePartial      = "code_evidence_partial"
)

type JointRCACandidateVerdict string

const (
	JointRCASupportedCandidate JointRCACandidateVerdict = "SUPPORTED_CANDIDATE"
	JointRCACandidateRefuted   JointRCACandidateVerdict = "REFUTED"
	JointRCACandidateUnknown   JointRCACandidateVerdict = "INCONCLUSIVE"
)

type JointRCAFactorRole string

const (
	JointRCAFactorSupport JointRCAFactorRole = "SUPPORT"
	JointRCAFactorCounter JointRCAFactorRole = "COUNTER"
	JointRCAFactorMissing JointRCAFactorRole = "MISSING"
)

type JointRCAFactorResult string

const (
	JointRCAFactorPass    JointRCAFactorResult = "PASS"
	JointRCAFactorFail    JointRCAFactorResult = "FAIL"
	JointRCAFactorUnknown JointRCAFactorResult = "UNKNOWN"
)

type JointRCAChangeRelation string

const (
	JointRCAChangeOverlap   JointRCAChangeRelation = "CHANGED_SINCE_PREVIOUS"
	JointRCAChangeUnchanged JointRCAChangeRelation = "UNCHANGED_SINCE_PREVIOUS"
	JointRCAChangeUnknown   JointRCAChangeRelation = "NOT_CHECKED"
)

// JointRCACandidate is a bounded, machine-derived hypothesis about a deployed
// code path. SUPPORTED_CANDIDATE never means a confirmed root cause.
type JointRCACandidate struct {
	ID               string                   `json:"id"`
	Kind             string                   `json:"kind"`
	Verdict          JointRCACandidateVerdict `json:"verdict"`
	Statement        string                   `json:"statement"`
	Confidence       float64                  `json:"confidence"`
	ConfidenceMethod string                   `json:"confidence_method"`
	File             string                   `json:"file"`
	Line             int                      `json:"line"`
	ChangeRelation   JointRCAChangeRelation   `json:"change_relation"`
	RuntimeAnchorIDs []string                 `json:"runtime_anchor_ids"`
	CodeMatchIDs     []string                 `json:"code_match_ids"`
	FactorIDs        []string                 `json:"factor_ids"`
	MissingInputs    []string                 `json:"missing_inputs"`
	Limitations      []string                 `json:"limitations"`
}

// JointRCAFactor is the auditable support/counter/missing-input ledger for one
// candidate. References bind back to already validated evidence objects.
type JointRCAFactor struct {
	ID                    string               `json:"id"`
	CandidateID           string               `json:"candidate_id"`
	Code                  string               `json:"code"`
	Role                  JointRCAFactorRole   `json:"role"`
	Result                JointRCAFactorResult `json:"result"`
	Statement             string               `json:"statement"`
	RuntimeAnchorIDs      []string             `json:"runtime_anchor_ids,omitempty"`
	CodeMatchIDs          []string             `json:"code_match_ids,omitempty"`
	DeploymentFingerprint string               `json:"deployment_fingerprint,omitempty"`
}

type JointRCAAction struct {
	Code             string   `json:"code"`
	CandidateID      string   `json:"candidate_id"`
	Statement        string   `json:"statement"`
	ExecutionMode    string   `json:"execution_mode"`
	RuntimeAnchorIDs []string `json:"runtime_anchor_ids"`
	CodeMatchIDs     []string `json:"code_match_ids"`
}

type JointRCA struct {
	Version         string              `json:"version"`
	Status          JointRCAStatus      `json:"status"`
	ReasonCode      string              `json:"reason_code,omitempty"`
	Candidates      []JointRCACandidate `json:"candidates,omitempty"`
	Factors         []JointRCAFactor    `json:"factors,omitempty"`
	Actions         []JointRCAAction    `json:"actions,omitempty"`
	MissingInputs   []string            `json:"missing_inputs,omitempty"`
	Limitations     []string            `json:"limitations"`
	HumanReviewOnly bool                `json:"human_review_only"`
	GeneratedAt     time.Time           `json:"generated_at"`
}
