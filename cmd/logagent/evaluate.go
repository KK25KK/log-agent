package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"logagent/internal/adapters/eino"
	"logagent/internal/adapters/evalmock"
	"logagent/internal/adapters/replayfs"
	"logagent/internal/domain"
	"logagent/internal/evaluation"
	"logagent/internal/evaluation/replay"
	"logagent/internal/observability"
)

// runEvaluate executes the repository-owned synthetic golden set and the
// M5-B/B1 trace gate. The command intentionally has no configuration or
// credential inputs, so this path cannot instantiate real Feishu, SLS, or
// change-platform clients.
func runEvaluate() error {
	return runEvaluateCommand(nil)
}

func runEvaluateCommand(args []string) error {
	snapshotDirectory, err := parseEvaluateOptions(args)
	if err != nil {
		return err
	}
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		return err
	}
	if snapshotDirectory != "" {
		store, err := replayfs.New(snapshotDirectory)
		if err != nil {
			return err
		}
		snapshot, evaluationErr := executeArchivedEvaluation(context.Background(), dataset, store, nil, time.Now)
		if err := printJSON(snapshot); err != nil {
			return err
		}
		return evaluationErr
	}
	report, evaluationErr := executeSyntheticEvaluation(context.Background(), dataset)
	if err := printJSON(report); err != nil {
		return err
	}
	return evaluationErr
}

type replayCommandOutput struct {
	Source   replay.SourceReference `json:"source"`
	Snapshot replay.Snapshot        `json:"snapshot"`
}

func runReplayCompareCommand(args []string) error {
	snapshotDirectory, baseRunID, candidateRunID, err := parseReplayCompareOptions(args)
	if err != nil {
		return err
	}
	store, err := replayfs.New(snapshotDirectory)
	if err != nil {
		return err
	}
	comparison, err := executeReplayComparison(context.Background(), store, baseRunID, candidateRunID)
	if err != nil {
		return err
	}
	if err := printJSON(comparison); err != nil {
		return err
	}
	if comparison.Status == replay.ComparisonIncomparable {
		return replay.ErrComparisonIncomparable
	}
	return nil
}

func executeReplayComparison(ctx context.Context, store replay.Store, baseRunID, candidateRunID string) (replay.Comparison, error) {
	if store == nil {
		return replay.Comparison{}, errors.New("evaluation replay store is required")
	}
	base, err := store.Load(ctx, baseRunID)
	if err != nil {
		return replay.Comparison{}, fmt.Errorf("load base evaluation replay snapshot: %w", err)
	}
	candidate, err := store.Load(ctx, candidateRunID)
	if err != nil {
		return replay.Comparison{}, fmt.Errorf("load candidate evaluation replay snapshot: %w", err)
	}
	comparison, err := replay.Compare(base, candidate)
	if err != nil {
		return replay.Comparison{}, fmt.Errorf("compare evaluation replay snapshots: %w", err)
	}
	return comparison, nil
}

func runReplayCommand(args []string) error {
	snapshotDirectory, evaluationRunID, err := parseReplayOptions(args)
	if err != nil {
		return err
	}
	store, err := replayfs.New(snapshotDirectory)
	if err != nil {
		return err
	}
	ctx := context.Background()
	source, err := store.Load(ctx, evaluationRunID)
	if err != nil {
		return err
	}
	dataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		return err
	}
	if err := replay.ValidateSourceCompatibility(source, dataset); err != nil {
		return err
	}
	reference := source.Reference()
	snapshot, evaluationErr := executeArchivedEvaluation(ctx, dataset, store, &reference, time.Now)
	if err := printJSON(replayCommandOutput{Source: reference, Snapshot: snapshot}); err != nil {
		return err
	}
	return evaluationErr
}

func executeArchivedEvaluation(ctx context.Context, dataset evaluation.Dataset, store replay.Store, source *replay.SourceReference, now func() time.Time) (replay.Snapshot, error) {
	if store == nil {
		return replay.Snapshot{}, errors.New("evaluation replay store is required")
	}
	if now == nil {
		return replay.Snapshot{}, errors.New("evaluation replay clock is required")
	}
	report, evaluationErr := executeSyntheticEvaluation(ctx, dataset)
	snapshot, err := replay.New(report, evaluationErr, source, now())
	if err != nil {
		return replay.Snapshot{}, fmt.Errorf("build evaluation replay snapshot: %w", err)
	}
	if err := store.Append(ctx, snapshot); err != nil {
		return replay.Snapshot{}, fmt.Errorf("append evaluation replay snapshot: %w", err)
	}
	return snapshot, evaluationErr
}

func parseEvaluateOptions(args []string) (string, error) {
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotDirectory := flags.String("snapshot-dir", "", "append the evaluation snapshot to this directory")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("usage: logagent evaluate [--snapshot-dir <directory>]: %w", err)
	}
	if flags.NArg() != 0 {
		return "", errors.New("usage: logagent evaluate [--snapshot-dir <directory>]")
	}
	return *snapshotDirectory, nil
}

func parseReplayOptions(args []string) (string, string, error) {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotDirectory := flags.String("snapshot-dir", "", "directory containing append-only evaluation snapshots")
	evaluationRunID := flags.String("run-id", "", "source evaluation run ID")
	if err := flags.Parse(args); err != nil {
		return "", "", fmt.Errorf("usage: logagent replay --snapshot-dir <directory> --run-id <evaluation-run-id>: %w", err)
	}
	if flags.NArg() != 0 || *snapshotDirectory == "" || *evaluationRunID == "" {
		return "", "", errors.New("usage: logagent replay --snapshot-dir <directory> --run-id <evaluation-run-id>")
	}
	return *snapshotDirectory, *evaluationRunID, nil
}

func parseReplayCompareOptions(args []string) (string, string, string, error) {
	flags := flag.NewFlagSet("replay-compare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotDirectory := flags.String("snapshot-dir", "", "directory containing append-only evaluation snapshots")
	baseRunID := flags.String("base-run-id", "", "baseline evaluation run ID")
	candidateRunID := flags.String("candidate-run-id", "", "candidate evaluation run ID")
	if err := flags.Parse(args); err != nil {
		return "", "", "", fmt.Errorf("usage: logagent replay-compare --snapshot-dir <directory> --base-run-id <evaluation-run-id> --candidate-run-id <evaluation-run-id>: %w", err)
	}
	if flags.NArg() != 0 || *snapshotDirectory == "" || *baseRunID == "" || *candidateRunID == "" {
		return "", "", "", errors.New("usage: logagent replay-compare --snapshot-dir <directory> --base-run-id <evaluation-run-id> --candidate-run-id <evaluation-run-id>")
	}
	return *snapshotDirectory, *baseRunID, *candidateRunID, nil
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
