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
	"logagent/internal/observability"
)

// runEvaluate executes the repository-owned synthetic golden set and the
// M5-B/B1 trace gate. The command intentionally has no configuration or
// credential inputs, so this path cannot instantiate real Feishu, SLS, or
// change-platform clients.
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
		run, ok := observability.RunContextFrom(ctx)
		if !ok {
			return domain.Report{}, evaluation.ExecutionStats{}, fmt.Errorf("evaluation case %q has no Agent run context", evaluationCase.ID)
		}
		recorder := observability.NewBoundedRecorder(32, run)
		investigationID := "eval_" + evaluationCase.ID + "_" + run.RunID
		scenario, err := evalmock.New(evaluationCase, investigationID, evalmock.WithObserver(recorder))
		if err != nil {
			return domain.Report{}, evaluation.ExecutionStats{}, err
		}
		generatedAt := evaluationCase.Request.EndTime.Add(time.Second)
		engine, err := eino.New(
			ctx,
			scenario.Executor,
			func() time.Time { return generatedAt },
			eino.WithChangeSource(scenario.ChangeSource),
			eino.WithObserver(recorder),
		)
		if err != nil {
			stats := scenario.ExecutionStats()
			stats.AgentTrace = recorder.Snapshot()
			return domain.Report{}, stats, err
		}
		evidence, report, err := engine.Run(ctx, investigationID, evaluationCase.Request)
		stats := scenario.ExecutionStats()
		stats.EngineEvidence = append([]domain.Evidence(nil), evidence...)
		stats.AgentTrace = recorder.Snapshot()
		return report, stats, err
	})
}
