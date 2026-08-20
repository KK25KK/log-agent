package feedbackfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logagent/internal/evaluation/feedback"
	"logagent/internal/testsupport/evaluationsnapshot"
)

func TestStorePersistsAppendOnlyFeedbackAcrossReopen(t *testing.T) {
	ctx := context.Background()
	snapshot, err := evaluationsnapshot.Passed(ctx, time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	records, err := feedback.BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := store.Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append(ctx, records[0]); !errors.Is(err, feedback.ErrDuplicateFeedback) {
		t.Fatalf("duplicate append was not rejected: %v", err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.List(ctx, snapshot.EvaluationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(records) {
		t.Fatalf("unexpected persisted feedback count: %d", len(loaded))
	}
	summary, err := feedback.Resolve(snapshot, loaded)
	if err != nil || summary.ActiveCount != len(records) {
		t.Fatalf("persisted feedback did not resolve: summary=%#v err=%v", summary, err)
	}
}

func TestStoreRejectsTamperedUnexpectedAndUnsafePaths(t *testing.T) {
	ctx := context.Background()
	snapshot, err := evaluationsnapshot.Passed(ctx, time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	records, err := feedback.BuildSyntheticFixture(snapshot, snapshot.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, records[0]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.runDirectory(snapshot.EvaluationRunID), records[0].FeedbackID+recordFileSuffix)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), "synthetic-reviewer-a", "synthetic-reviewer-c", 1))
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx, snapshot.EvaluationRunID); !errors.Is(err, feedback.ErrFeedbackTampered) {
		t.Fatalf("tampered feedback was not rejected: %v", err)
	}

	cleanRoot := t.TempDir()
	cleanStore, err := New(cleanRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanStore.Append(ctx, records[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanStore.runDirectory(snapshot.EvaluationRunID), "unexpected.txt"), []byte("unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanStore.List(ctx, snapshot.EvaluationRunID); err == nil {
		t.Fatal("unexpected feedback directory entry was accepted")
	}
	if _, err := cleanStore.List(ctx, "../escape"); err == nil {
		t.Fatal("unsafe feedback run path was accepted")
	}
}

func TestStoreReturnsEmptyHistoryForUnknownSafeRun(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.List(context.Background(), "evalrun_unknown")
	if err != nil || len(records) != 0 {
		t.Fatalf("unknown safe run did not return an empty history: records=%#v err=%v", records, err)
	}
}
