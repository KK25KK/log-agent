package evaluationsnapshot

import (
	"context"
	"fmt"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/evalmock"
	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/evaluation/replay"
	"logagent/internal/observability"
)

// Passed builds a fully valid synthetic evaluation snapshot for tests outside
// the evaluation package. It uses the same deterministic Graph and Fixture
// Mock assembly as the CLI while making no external calls.
func Passed(ctx context.Context, createdAt time.Time) (replay.Snapshot, error) {
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		return replay.Snapshot{}, err
	}
	versions := evaluation.DefaultVersionInfo()
	versions.GraphVersion = eino.GraphVersion
	report, evaluationErr := evaluation.EvaluateWithVersions(ctx, dataset, versions, func(ctx context.Context, evaluationCase evaluation.EvaluationCase) (domain.Report, evaluation.ExecutionStats, error) {
		run, ok := observability.RunContextFrom(ctx)
		if !ok {
			return domain.Report{}, evaluation.ExecutionStats{}, fmt.Errorf("evaluation case %q has no Agent run context", evaluationCase.ID)
		}
		recorder := observability.NewBoundedRecorder(32, run)
		investigationID := "test_" + evaluationCase.ID + "_" + run.RunID
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
	if evaluationErr != nil {
		return replay.Snapshot{}, fmt.Errorf("execute synthetic evaluation fixture: %w", evaluationErr)
	}
	return replay.New(report, nil, nil, createdAt.UTC())
}
