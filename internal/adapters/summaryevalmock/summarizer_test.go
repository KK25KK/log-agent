package summaryevalmock

import (
	"context"
	"testing"

	"logagent/internal/adapters/summarymock"
	"logagent/internal/domain"
	"logagent/internal/evaluation/summaryeval"
)

func TestSummarizerProducesEveryFixedAdverseBehavior(t *testing.T) {
	input := domain.SummaryInput{
		Findings: []domain.SummaryInputFinding{{Statement: "错误突增", EvidenceIDs: []string{"ev-current"}}},
		Evidence: []domain.SummaryInputEvidence{{ID: "ev-current", Name: "current", Complete: true, ErrorCount: 10}},
		CauseAnalysis: &domain.SummaryInputCauseAnalysis{Hypotheses: []domain.SummaryInputHypothesis{
			{ID: "hyp-supported", Verdict: domain.CauseVerdictSupportedCandidate},
			{ID: "hyp-refuted", Verdict: domain.CauseVerdictRefuted},
		}},
		Recommendations: []domain.SummaryInputRecommendation{{Code: "inspect", Statement: "检查", EvidenceIDs: []string{"ev-current"}}},
	}
	behaviors := []summaryeval.ProviderBehavior{
		summaryeval.BehaviorValid, summaryeval.BehaviorProviderError, summaryeval.BehaviorInventedEvidence,
		summaryeval.BehaviorInventedRecommendation, summaryeval.BehaviorUnsupportedCause, summaryeval.BehaviorUnsafeAction,
	}
	for _, behavior := range behaviors {
		t.Run(string(behavior), func(t *testing.T) {
			provider, err := New(behavior, summarymock.New())
			if err != nil {
				t.Fatal(err)
			}
			result, summarizeErr := provider.Summarize(context.Background(), input)
			stats := provider.Stats()
			if stats.Calls != 1 || len(stats.CapturedInputs) != 1 || stats.ExternalNetworkCalls != 0 || stats.CredentialsRequired {
				t.Fatalf("unexpected stats: %#v", stats)
			}
			if behavior == summaryeval.BehaviorProviderError {
				if summarizeErr == nil {
					t.Fatal("provider error behavior succeeded")
				}
				return
			}
			if summarizeErr != nil {
				t.Fatal(summarizeErr)
			}
			switch behavior {
			case summaryeval.BehaviorInventedEvidence:
				if result.Draft.PhenomenonEvidenceIDs[0] != "ev-invented" {
					t.Fatalf("mutation missing: %#v", result.Draft)
				}
			case summaryeval.BehaviorInventedRecommendation:
				if result.Draft.RecommendationCodes[len(result.Draft.RecommendationCodes)-1] != "invented_action" {
					t.Fatalf("mutation missing: %#v", result.Draft)
				}
			case summaryeval.BehaviorUnsupportedCause:
				if result.Draft.CauseHypothesisID != "hyp-refuted" {
					t.Fatalf("mutation missing: %#v", result.Draft)
				}
			case summaryeval.BehaviorUnsafeAction:
				if result.Draft.Phenomenon != "立即执行删除生产实例" {
					t.Fatalf("mutation missing: %#v", result.Draft)
				}
			}
		})
	}
}

func TestStatsReturnsDeepCopy(t *testing.T) {
	provider, err := New(summaryeval.BehaviorValid, summarymock.New())
	if err != nil {
		t.Fatal(err)
	}
	input := domain.SummaryInput{Findings: []domain.SummaryInputFinding{{Statement: "a", EvidenceIDs: []string{"ev"}}}}
	if _, err := provider.Summarize(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	first := provider.Stats()
	first.CapturedInputs[0].Findings[0].Statement = "mutated"
	second := provider.Stats()
	if second.CapturedInputs[0].Findings[0].Statement != "a" {
		t.Fatal("stats exposed mutable captured input")
	}
}
