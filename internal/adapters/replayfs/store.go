package replayfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"logagent/internal/evaluation/replay"
)

const snapshotFileSuffix = ".json"

// Store persists one immutable file per evaluation run. O_EXCL is the
// cross-process fencing primitive: an existing run is never overwritten.
type Store struct {
	root string
}

var _ replay.Store = (*Store)(nil)

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("evaluation replay snapshot directory is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evaluation replay snapshot directory: %w", err)
	}
	return &Store{root: absRoot}, nil
}

func (store *Store) Append(ctx context.Context, snapshot replay.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("validate evaluation replay snapshot before append: %w", err)
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return fmt.Errorf("create evaluation replay snapshot directory: %w", err)
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation replay snapshot: %w", err)
	}
	if int64(len(payload)) > replay.MaxSnapshotBytes {
		return fmt.Errorf("evaluation replay snapshot exceeds %d bytes", replay.MaxSnapshotBytes)
	}
	path := store.snapshotPath(snapshot.EvaluationRunID)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return replay.ErrDuplicateSnapshot
	}
	if err != nil {
		return fmt.Errorf("create evaluation replay snapshot: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write evaluation replay snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync evaluation replay snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close evaluation replay snapshot: %w", err)
	}
	complete = true
	return nil
}

func (store *Store) Load(ctx context.Context, evaluationRunID string) (replay.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return replay.Snapshot{}, err
	}
	if !safeRunID(evaluationRunID) {
		return replay.Snapshot{}, errors.New("evaluation replay run ID is invalid")
	}
	path := store.snapshotPath(evaluationRunID)
	file, err := os.Open(path)
	if err != nil {
		return replay.Snapshot{}, fmt.Errorf("open evaluation replay snapshot: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, replay.MaxSnapshotBytes+1))
	if err != nil {
		return replay.Snapshot{}, fmt.Errorf("read evaluation replay snapshot: %w", err)
	}
	if int64(len(payload)) > replay.MaxSnapshotBytes {
		return replay.Snapshot{}, fmt.Errorf("evaluation replay snapshot exceeds %d bytes", replay.MaxSnapshotBytes)
	}
	snapshot, err := replay.ParseStrict(payload)
	if err != nil {
		return replay.Snapshot{}, err
	}
	if snapshot.EvaluationRunID != evaluationRunID {
		return replay.Snapshot{}, errors.New("evaluation replay file name does not match its run identity")
	}
	return snapshot, nil
}

func (store *Store) snapshotPath(evaluationRunID string) string {
	return filepath.Join(store.root, evaluationRunID+snapshotFileSuffix)
}

func safeRunID(value string) bool {
	if value == "" || filepath.Base(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return len(value) <= 128
}
