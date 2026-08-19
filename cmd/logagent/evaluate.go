package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/evalmock"
	"logagent/internal/domain"
	"logagent/internal/evaluation"
)

// runEvaluate executes the repository-owned synthetic M5-A golden set. The
// command intentionally has no configuration or credential inputs, so this
// path cannot instantiate real Feishu, SLS, or change-platform clients.
func runEvaluate() error {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		return err
	}
	report, evaluationErr := executeSyntheticEvaluation(context.Background(), dataset)
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation report: %w", err)
	}
	fmt.Println(string(payload))
	return evaluationErr
}

func executeSyntheticEvaluation(ctx context.Context, dataset evaluation.Dataset) (evaluation.EvaluationReport, error) {
	versions := evaluation.DefaultVersionInfo()
	versions.GraphVersion = eino.GraphVersion
	return evaluation.EvaluateWithVersions(ctx, dataset, versions, func(ctx context.Context, evaluationCase evaluation.EvaluationCase) (domain.Report, evaluation.ExecutionStats, error) {
		investigationID := "eval_" + evaluationCase.ID
		scenario, err := evalmock.New(evaluationCase, investigationID)
		if err != nil {
			return domain.Report{}, evaluation.ExecutionStats{}, err
		}
		generatedAt := evaluationCase.Request.EndTime.Add(time.Second)
		engine, err := eino.New(
			ctx,
			scenario.Executor,
			func() time.Time { return generatedAt },
			eino.WithChangeSource(scenario.ChangeSource),
		)
		if err != nil {
			return domain.Report{}, scenario.ExecutionStats(), err
		}
		evidence, report, err := engine.Run(ctx, investigationID, evaluationCase.Request)
		stats := scenario.ExecutionStats()
		stats.EngineEvidence = append([]domain.Evidence(nil), evidence...)
		return report, stats, err
	})
}
