package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/evalmock"
	"logagent/internal/adapters/summaryevalmock"
	"logagent/internal/adapters/summarymock"
	"logagent/internal/application"
	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/evaluation/summaryeval"
)

const syntheticSensitiveFinding = "Bearer synthetic-sensitive-marker-123456"

// runSummaryEvaluate is a credential-free safety gate around the production
// SummaryService. It runs the real deterministic graph with synthetic fixtures
// and never constructs a real Feishu, SLS, or model client.
func runSummaryEvaluate(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: logagent summary-evaluate")
	}
	dataset, err := summaryeval.LoadSyntheticV1()
	if err != nil {
		return err
	}
	baseDataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		return err
	}
	report, evaluationErr := executeSummaryEvaluation(context.Background(), dataset, baseDataset)
	if err := printJSON(report); err != nil {
		return err
	}
	return evaluationErr
}

func executeSummaryEvaluation(ctx context.Context, dataset summaryeval.Dataset, baseDataset evaluation.Dataset) (summaryeval.Report, error) {
	baseCases := make(map[string]evaluation.EvaluationCase, len(baseDataset.Cases))
	for _, evaluationCase := range baseDataset.Cases {
		baseCases[evaluationCase.ID] = evaluationCase
	}
	return summaryeval.Evaluate(ctx, dataset, func(ctx context.Context, summaryCase summaryeval.Case) (summaryeval.Observation, error) {
		baseCase, exists := baseCases[summaryCase.BaseCaseID]
		if !exists {
			return summaryeval.Observation{}, fmt.Errorf("summary evaluation base case %q is unavailable", summaryCase.BaseCaseID)
		}
		investigationID := "summary_eval_" + summaryCase.ID
		scenario, err := evalmock.New(baseCase, investigationID)
		if err != nil {
			return summaryeval.Observation{}, err
		}
		generatedAt := baseCase.Request.EndTime.Add(time.Second)
		engine, err := eino.New(
			ctx,
			scenario.Executor,
			func() time.Time { return generatedAt },
			eino.WithChangeSource(scenario.ChangeSource),
		)
		if err != nil {
			return summaryeval.Observation{}, err
		}
		evidence, deterministicReport, err := engine.Run(ctx, investigationID, baseCase.Request)
		if err != nil {
			return summaryeval.Observation{}, err
		}
		if summaryCase.ReportMutation == summaryeval.MutationSensitiveFinding {
			if len(deterministicReport.Findings) == 0 {
				return summaryeval.Observation{}, errors.New("summary evaluation report has no finding to mutate")
			}
			deterministicReport.Findings[0].Statement = syntheticSensitiveFinding
		}
		before := deterministicReport
		provider, err := summaryevalmock.New(summaryCase.ProviderBehavior, summarymock.New())
		if err != nil {
			return summaryeval.Observation{}, err
		}
		service, err := application.NewSummaryService(provider, time.Second, func() time.Time { return generatedAt.Add(time.Second) })
		if err != nil {
			return summaryeval.Observation{}, err
		}
		after := service.Enrich(ctx, evidence, deterministicReport)
		providerStats := provider.Stats()
		observation := summaryeval.Observation{
			Requester: baseCase.Request.Requester, Evidence: append([]domain.Evidence(nil), evidence...),
			Before: before, After: after, ProviderCalls: providerStats.Calls,
			ExternalNetworkCalls: providerStats.ExternalNetworkCalls, CredentialsRequired: providerStats.CredentialsRequired,
		}
		if len(providerStats.CapturedInputs) > 0 {
			captured := providerStats.CapturedInputs[0]
			observation.CapturedInput = &captured
		}
		if after.Summary != nil {
			observation.InputTokens = after.Summary.InputTokens
			observation.OutputTokens = after.Summary.OutputTokens
			observation.TotalTokens = after.Summary.TotalTokens
		}
		return observation, nil
	})
}
