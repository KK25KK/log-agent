package summaryevalmock

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"logagent/internal/domain"
	"logagent/internal/evaluation/summaryeval"
	"logagent/internal/ports"
)

const providerFailure = "synthetic provider failure"

type Summarizer struct {
	mu       sync.Mutex
	behavior summaryeval.ProviderBehavior
	delegate ports.ReportSummarizer
	calls    int
	inputs   []domain.SummaryInput
}

type Stats struct {
	Calls                int
	CapturedInputs       []domain.SummaryInput
	ExternalNetworkCalls int
	CredentialsRequired  bool
}

func New(behavior summaryeval.ProviderBehavior, delegate ports.ReportSummarizer) (*Summarizer, error) {
	if delegate == nil {
		return nil, errors.New("summary evaluation delegate is required")
	}
	switch behavior {
	case summaryeval.BehaviorValid, summaryeval.BehaviorProviderError, summaryeval.BehaviorInventedEvidence,
		summaryeval.BehaviorInventedRecommendation, summaryeval.BehaviorUnsupportedCause, summaryeval.BehaviorUnsafeAction:
	default:
		return nil, errors.New("summary evaluation behavior is invalid")
	}
	return &Summarizer{behavior: behavior, delegate: delegate}, nil
}

func (summarizer *Summarizer) Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SummaryProviderResult{}, err
	}
	cloned, err := cloneInput(input)
	if err != nil {
		return domain.SummaryProviderResult{}, errors.New("clone summary evaluation input")
	}
	summarizer.mu.Lock()
	summarizer.calls++
	summarizer.inputs = append(summarizer.inputs, cloned)
	summarizer.mu.Unlock()
	if summarizer.behavior == summaryeval.BehaviorProviderError {
		return domain.SummaryProviderResult{}, errors.New(providerFailure)
	}
	result, err := summarizer.delegate.Summarize(ctx, input)
	if err != nil {
		return domain.SummaryProviderResult{}, err
	}
	switch summarizer.behavior {
	case summaryeval.BehaviorValid:
	case summaryeval.BehaviorInventedEvidence:
		result.Draft.PhenomenonEvidenceIDs = []string{"ev-invented"}
	case summaryeval.BehaviorInventedRecommendation:
		result.Draft.RecommendationCodes = append(result.Draft.RecommendationCodes, "invented_action")
	case summaryeval.BehaviorUnsupportedCause:
		result.Draft.CauseHypothesisID = unsupportedHypothesisID(input)
	case summaryeval.BehaviorUnsafeAction:
		result.Draft.Phenomenon = "立即执行删除生产实例"
	}
	return result, nil
}

func (summarizer *Summarizer) Stats() Stats {
	summarizer.mu.Lock()
	defer summarizer.mu.Unlock()
	inputs := make([]domain.SummaryInput, len(summarizer.inputs))
	for index := range summarizer.inputs {
		inputs[index], _ = cloneInput(summarizer.inputs[index])
	}
	return Stats{Calls: summarizer.calls, CapturedInputs: inputs}
}

func unsupportedHypothesisID(input domain.SummaryInput) string {
	if input.CauseAnalysis != nil {
		for _, hypothesis := range input.CauseAnalysis.Hypotheses {
			if hypothesis.Verdict != domain.CauseVerdictSupportedCandidate {
				return hypothesis.ID
			}
		}
	}
	return "hyp-invented"
}

func cloneInput(input domain.SummaryInput) (domain.SummaryInput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return domain.SummaryInput{}, err
	}
	var cloned domain.SummaryInput
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return domain.SummaryInput{}, err
	}
	return cloned, nil
}

var _ ports.ReportSummarizer = (*Summarizer)(nil)
