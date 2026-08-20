package feedbackfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"logagent/internal/evaluation/feedback"
)

const recordFileSuffix = ".json"

// Store persists one immutable file per feedback record. O_EXCL preserves the
// append-only contract across multiple local processes.
type Store struct {
	root string
}

var _ feedback.Store = (*Store)(nil)

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("evaluation feedback directory is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evaluation feedback directory: %w", err)
	}
	return &Store{root: absRoot}, nil
}

func (store *Store) Append(ctx context.Context, record feedback.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return fmt.Errorf("validate evaluation feedback before append: %w", err)
	}
	directory := store.runDirectory(record.Target.EvaluationRunID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create evaluation feedback directory: %w", err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation feedback: %w", err)
	}
	if int64(len(payload)) > feedback.MaxRecordBytes {
		return fmt.Errorf("evaluation feedback record exceeds %d bytes", feedback.MaxRecordBytes)
	}
	path := filepath.Join(directory, record.FeedbackID+recordFileSuffix)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return feedback.ErrDuplicateFeedback
	}
	if err != nil {
		return fmt.Errorf("create evaluation feedback record: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write evaluation feedback record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync evaluation feedback record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close evaluation feedback record: %w", err)
	}
	complete = true
	return nil
}

func (store *Store) List(ctx context.Context, evaluationRunID string) ([]feedback.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeIdentifier(evaluationRunID) {
		return nil, errors.New("evaluation feedback run ID is invalid")
	}
	directory := store.runDirectory(evaluationRunID)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []feedback.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read evaluation feedback directory: %w", err)
	}
	if len(entries) > feedback.MaxRecordsPerRun {
		return nil, fmt.Errorf("evaluation feedback history exceeds %d records", feedback.MaxRecordsPerRun)
	}
	records := make([]feedback.Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), recordFileSuffix) {
			return nil, errors.New("evaluation feedback directory contains an unexpected entry")
		}
		feedbackID := strings.TrimSuffix(entry.Name(), recordFileSuffix)
		if !safeIdentifier(feedbackID) {
			return nil, errors.New("evaluation feedback file name is invalid")
		}
		record, err := store.loadRecord(ctx, directory, entry.Name())
		if err != nil {
			return nil, err
		}
		if record.FeedbackID != feedbackID || record.Target.EvaluationRunID != evaluationRunID {
			return nil, errors.New("evaluation feedback file name does not match its identity")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].FeedbackID < records[right].FeedbackID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	return records, nil
}

func (store *Store) loadRecord(ctx context.Context, directory, name string) (feedback.Record, error) {
	if err := ctx.Err(); err != nil {
		return feedback.Record{}, err
	}
	path := filepath.Join(directory, name)
	file, err := os.Open(path)
	if err != nil {
		return feedback.Record{}, fmt.Errorf("open evaluation feedback record: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, feedback.MaxRecordBytes+1))
	if err != nil {
		return feedback.Record{}, fmt.Errorf("read evaluation feedback record: %w", err)
	}
	if int64(len(payload)) > feedback.MaxRecordBytes {
		return feedback.Record{}, fmt.Errorf("evaluation feedback record exceeds %d bytes", feedback.MaxRecordBytes)
	}
	record, err := feedback.ParseStrict(payload)
	if err != nil {
		return feedback.Record{}, err
	}
	return record, nil
}

func (store *Store) runDirectory(evaluationRunID string) string {
	return filepath.Join(store.root, evaluationRunID)
}

func safeIdentifier(value string) bool {
	if value == "" || filepath.Base(value) != value || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}
