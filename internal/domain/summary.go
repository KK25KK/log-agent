package domain

import "time"

const EvidenceSummaryPromptVersion = "evidence-summary-zh-v1"

type SummaryMode string

const (
	SummaryModeMock     SummaryMode = "MOCK"
	SummaryModeModel    SummaryMode = "MODEL"
	SummaryModeFallback SummaryMode = "FALLBACK"
)

type SummaryStatus string

const (
	SummaryGenerated SummaryStatus = "GENERATED"
	SummaryFallback  SummaryStatus = "FALLBACK"
)

// SummaryInput is the only projection that may cross an LLM boundary. It
// excludes identities, physical resources, queries, credentials and raw logs.
type SummaryInput struct {
	Outcome         string                       `json:"outcome"`
	AnalysisScope   string                       `json:"analysis_scope"`
	Findings        []SummaryInputFinding        `json:"findings"`
	Evidence        []SummaryInputEvidence       `json:"evidence"`
	CauseAnalysis   *SummaryInputCauseAnalysis   `json:"cause_analysis,omitempty"`
	Recommendations []SummaryInputRecommendation `json:"recommendations"`
}

type SummaryInputFinding struct {
	Code        string   `json:"code"`
	Statement   string   `json:"statement"`
	Confidence  float64  `json:"confidence"`
	Conclusive  bool     `json:"conclusive"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type SummaryInputEvidence struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Complete      bool   `json:"complete"`
	Truncated     bool   `json:"truncated"`
	ErrorCount    int64  `json:"error_count"`
	TopError      string `json:"top_error,omitempty"`
	TopErrorCount int64  `json:"top_error_count,omitempty"`
}

type SummaryInputCauseAnalysis struct {
	Status     CauseAnalysisStatus      `json:"status"`
	Hypotheses []SummaryInputHypothesis `json:"hypotheses,omitempty"`
	Missing    []string                 `json:"missing_inputs,omitempty"`
}

type SummaryInputHypothesis struct {
	ID          string       `json:"id"`
	Statement   string       `json:"statement"`
	Verdict     CauseVerdict `json:"verdict"`
	Confidence  float64      `json:"confidence"`
	Limitations []string     `json:"limitations"`
}

type SummaryInputRecommendation struct {
	Code        string   `json:"code"`
	Statement   string   `json:"statement"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// SummaryDraft is the provider-neutral strict model response. Application
// validation resolves hypothesis/recommendation references back to the
// deterministic report before anything is persisted or rendered.
type SummaryDraft struct {
	Phenomenon            string                `json:"phenomenon"`
	PhenomenonEvidenceIDs []string              `json:"phenomenon_evidence_ids"`
	CauseHypothesisID     string                `json:"cause_hypothesis_id,omitempty"`
	EvidenceNotes         []SummaryEvidenceNote `json:"evidence_notes"`
	RecommendationCodes   []string              `json:"recommendation_codes"`
}

type SummaryEvidenceNote struct {
	Statement   string   `json:"statement"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type SummaryProviderResult struct {
	Draft             SummaryDraft `json:"draft"`
	Mode              SummaryMode  `json:"mode"`
	Provider          string       `json:"provider"`
	Model             string       `json:"model"`
	RequestID         string       `json:"request_id,omitempty"`
	PromptVersion     string       `json:"prompt_version"`
	PromptFingerprint string       `json:"prompt_fingerprint"`
	InputTokens       int64        `json:"input_tokens"`
	OutputTokens      int64        `json:"output_tokens"`
	TotalTokens       int64        `json:"total_tokens"`
	LatencyMillis     int64        `json:"latency_millis"`
}

type SummaryNextStep struct {
	Code        string   `json:"code"`
	Statement   string   `json:"statement"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// ReportSummary is an additive projection. Deterministic Findings, Evidence,
// cause verdicts and recommendations remain the source of truth.
type ReportSummary struct {
	Status                SummaryStatus         `json:"status"`
	Mode                  SummaryMode           `json:"mode"`
	Provider              string                `json:"provider"`
	Model                 string                `json:"model"`
	RequestID             string                `json:"request_id,omitempty"`
	PromptVersion         string                `json:"prompt_version"`
	PromptFingerprint     string                `json:"prompt_fingerprint"`
	Phenomenon            string                `json:"phenomenon"`
	PhenomenonEvidenceIDs []string              `json:"phenomenon_evidence_ids"`
	PossibleCause         string                `json:"possible_cause,omitempty"`
	CauseHypothesisID     string                `json:"cause_hypothesis_id,omitempty"`
	EvidenceNotes         []SummaryEvidenceNote `json:"evidence_notes"`
	Limitations           []string              `json:"limitations"`
	NextSteps             []SummaryNextStep     `json:"next_steps"`
	InputTokens           int64                 `json:"input_tokens"`
	OutputTokens          int64                 `json:"output_tokens"`
	TotalTokens           int64                 `json:"total_tokens"`
	LatencyMillis         int64                 `json:"latency_millis"`
	GeneratedAt           time.Time             `json:"generated_at"`
}
