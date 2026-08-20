package summarymock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const mockPromptContract = "mock evidence summary selects governed findings evidence hypotheses and recommendations v1"

type Summarizer struct{}

func New() *Summarizer {
	return &Summarizer{}
}

func (summarizer *Summarizer) Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SummaryProviderResult{}, err
	}
	draft := domain.SummaryDraft{}
	if len(input.Findings) > 0 {
		draft.Phenomenon = input.Findings[0].Statement
		draft.PhenomenonEvidenceIDs = append([]string(nil), input.Findings[0].EvidenceIDs...)
	}
	if input.CauseAnalysis != nil {
		for _, hypothesis := range input.CauseAnalysis.Hypotheses {
			if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate {
				draft.CauseHypothesisID = hypothesis.ID
				break
			}
		}
	}
	for _, evidence := range input.Evidence {
		quality := "完整"
		if !evidence.Complete || evidence.Truncated {
			quality = "不完整"
		}
		statement := fmt.Sprintf("%s 窗口证据%s，错误数为 %d。", evidence.Name, quality, evidence.ErrorCount)
		draft.EvidenceNotes = append(draft.EvidenceNotes, domain.SummaryEvidenceNote{
			Statement: statement, EvidenceIDs: []string{evidence.ID},
		})
	}
	for _, recommendation := range input.Recommendations {
		draft.RecommendationCodes = append(draft.RecommendationCodes, recommendation.Code)
	}
	digest := sha256.Sum256([]byte(mockPromptContract))
	return domain.SummaryProviderResult{
		Draft: draft, Mode: domain.SummaryModeMock,
		Provider: "summary_mock", Model: "deterministic_mock_v1",
		PromptVersion:     domain.EvidenceSummaryPromptVersion,
		PromptFingerprint: hex.EncodeToString(digest[:]),
	}, nil
}

var _ ports.ReportSummarizer = (*Summarizer)(nil)
