package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"logagent/internal/adapters/feedbackfs"
	"logagent/internal/adapters/replayfs"
	"logagent/internal/evaluation/feedback"
	"logagent/internal/evaluation/replay"
	"logagent/internal/evaluation/rollout"
)

func runFeedbackSeedCommand(args []string) error {
	snapshotDirectory, feedbackDirectory, runID, err := parseFeedbackSeedOptions(args)
	if err != nil {
		return err
	}
	snapshotStore, err := replayfs.New(snapshotDirectory)
	if err != nil {
		return err
	}
	feedbackStore, err := feedbackfs.New(feedbackDirectory)
	if err != nil {
		return err
	}
	summary, err := executeSyntheticFeedbackSeed(context.Background(), snapshotStore, feedbackStore, runID)
	if err != nil {
		return err
	}
	return printJSON(summary)
}

func executeSyntheticFeedbackSeed(ctx context.Context, snapshotStore replay.Store, feedbackStore feedback.Store, runID string) (feedback.Summary, error) {
	if snapshotStore == nil || feedbackStore == nil {
		return feedback.Summary{}, errors.New("snapshot and feedback stores are required")
	}
	snapshot, err := snapshotStore.Load(ctx, runID)
	if err != nil {
		return feedback.Summary{}, fmt.Errorf("load feedback target snapshot: %w", err)
	}
	existing, err := feedbackStore.List(ctx, runID)
	if err != nil {
		return feedback.Summary{}, fmt.Errorf("load existing evaluation feedback: %w", err)
	}
	fixture, err := feedback.BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		return feedback.Summary{}, fmt.Errorf("build synthetic evaluation feedback: %w", err)
	}
	existingHashes := make(map[string]string, len(existing))
	for _, record := range existing {
		existingHashes[record.FeedbackID] = record.ContentHash
	}
	for _, record := range fixture {
		if contentHash, exists := existingHashes[record.FeedbackID]; exists {
			if contentHash != record.ContentHash {
				return feedback.Summary{}, errors.New("existing synthetic feedback identity has different content")
			}
			continue
		}
		if err := feedbackStore.Append(ctx, record); err != nil && !errors.Is(err, feedback.ErrDuplicateFeedback) {
			return feedback.Summary{}, fmt.Errorf("append synthetic evaluation feedback: %w", err)
		}
	}
	records, err := feedbackStore.List(ctx, runID)
	if err != nil {
		return feedback.Summary{}, fmt.Errorf("reload synthetic evaluation feedback: %w", err)
	}
	summary, err := feedback.Resolve(snapshot, records)
	if err != nil {
		return feedback.Summary{}, fmt.Errorf("resolve synthetic evaluation feedback: %w", err)
	}
	return summary, nil
}

func runRolloutRehearseCommand(args []string) error {
	snapshotDirectory, feedbackDirectory, baseRunID, candidateRunID, phase, err := parseRolloutRehearseOptions(args)
	if err != nil {
		return err
	}
	snapshotStore, err := replayfs.New(snapshotDirectory)
	if err != nil {
		return err
	}
	feedbackStore, err := feedbackfs.New(feedbackDirectory)
	if err != nil {
		return err
	}
	decision, err := executeRolloutRehearsal(
		context.Background(), snapshotStore, feedbackStore, baseRunID, candidateRunID, rollout.DefaultPolicy(), phase,
	)
	if err != nil {
		return err
	}
	if err := printJSON(decision); err != nil {
		return err
	}
	if decision.Status != rollout.StatusPassed {
		return rollout.ErrRehearsalNotPassed
	}
	return nil
}

func executeRolloutRehearsal(
	ctx context.Context,
	snapshotStore replay.Store,
	feedbackStore feedback.Store,
	baseRunID string,
	candidateRunID string,
	policy rollout.Policy,
	phase rollout.Phase,
) (rollout.Decision, error) {
	if snapshotStore == nil || feedbackStore == nil {
		return rollout.Decision{}, errors.New("snapshot and feedback stores are required")
	}
	base, err := snapshotStore.Load(ctx, baseRunID)
	if err != nil {
		return rollout.Decision{}, fmt.Errorf("load rollout rehearsal base snapshot: %w", err)
	}
	candidate, err := snapshotStore.Load(ctx, candidateRunID)
	if err != nil {
		return rollout.Decision{}, fmt.Errorf("load rollout rehearsal candidate snapshot: %w", err)
	}
	records, err := feedbackStore.List(ctx, candidateRunID)
	if err != nil {
		return rollout.Decision{}, fmt.Errorf("load rollout rehearsal feedback: %w", err)
	}
	decision, err := rollout.Rehearse(base, candidate, records, policy, phase)
	if err != nil {
		return rollout.Decision{}, err
	}
	return decision, nil
}

func parseFeedbackSeedOptions(args []string) (string, string, string, error) {
	flags := flag.NewFlagSet("feedback-seed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotDirectory := flags.String("snapshot-dir", "", "directory containing append-only evaluation snapshots")
	feedbackDirectory := flags.String("feedback-dir", "", "directory containing append-only synthetic feedback")
	runID := flags.String("run-id", "", "candidate evaluation run ID")
	if err := flags.Parse(args); err != nil {
		return "", "", "", fmt.Errorf("usage: logagent feedback-seed --snapshot-dir <directory> --feedback-dir <directory> --run-id <evaluation-run-id>: %w", err)
	}
	if flags.NArg() != 0 || *snapshotDirectory == "" || *feedbackDirectory == "" || *runID == "" {
		return "", "", "", errors.New("usage: logagent feedback-seed --snapshot-dir <directory> --feedback-dir <directory> --run-id <evaluation-run-id>")
	}
	return *snapshotDirectory, *feedbackDirectory, *runID, nil
}

func parseRolloutRehearseOptions(args []string) (string, string, string, string, rollout.Phase, error) {
	flags := flag.NewFlagSet("rollout-rehearse", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotDirectory := flags.String("snapshot-dir", "", "directory containing append-only evaluation snapshots")
	feedbackDirectory := flags.String("feedback-dir", "", "directory containing append-only synthetic feedback")
	baseRunID := flags.String("base-run-id", "", "baseline evaluation run ID")
	candidateRunID := flags.String("candidate-run-id", "", "candidate evaluation run ID")
	phaseValue := flags.String("phase", "preflight", "preflight or simulated-active-pilot")
	if err := flags.Parse(args); err != nil {
		return "", "", "", "", "", fmt.Errorf("usage: logagent rollout-rehearse --snapshot-dir <directory> --feedback-dir <directory> --base-run-id <id> --candidate-run-id <id> [--phase preflight|simulated-active-pilot]: %w", err)
	}
	if flags.NArg() != 0 || *snapshotDirectory == "" || *feedbackDirectory == "" || *baseRunID == "" || *candidateRunID == "" {
		return "", "", "", "", "", errors.New("usage: logagent rollout-rehearse --snapshot-dir <directory> --feedback-dir <directory> --base-run-id <id> --candidate-run-id <id> [--phase preflight|simulated-active-pilot]")
	}
	var phase rollout.Phase
	switch *phaseValue {
	case "preflight":
		phase = rollout.PhasePreflight
	case "simulated-active-pilot":
		phase = rollout.PhaseSimulatedActivePilot
	default:
		return "", "", "", "", "", errors.New("rollout rehearsal phase must be preflight or simulated-active-pilot")
	}
	return *snapshotDirectory, *feedbackDirectory, *baseRunID, *candidateRunID, phase, nil
}
