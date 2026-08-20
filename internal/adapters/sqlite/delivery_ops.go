package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type deadDeliveryRow struct {
	item            domain.DeadDelivery
	sequence        int
	cardMessageID   string
	sourceMessageID string
	lastError       string
	laterCount      int
	laterCountKnown bool
}

func (s *Store) ListDeadDeliveries(ctx context.Context, limit int) ([]domain.DeadDelivery, error) {
	if limit <= 0 || limit > 200 {
		return nil, errors.New("dead-delivery limit must be between 1 and 200")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT d.id, d.investigation_id, d.kind, d.sequence, d.attempts,
       d.last_error, d.updated_at, t.source_message_id, t.card_message_id,
       (SELECT COUNT(*) FROM delivery_events later
        WHERE later.investigation_id = d.investigation_id AND later.sequence > d.sequence)
FROM delivery_events d
JOIN interaction_targets t ON t.investigation_id = d.investigation_id
WHERE d.status = ?
ORDER BY d.updated_at, d.id
LIMIT ?`, domain.DeliveryDead, limit)
	if err != nil {
		return nil, fmt.Errorf("list dead deliveries: %w", err)
	}
	defer rows.Close()

	items := make([]domain.DeadDelivery, 0)
	for rows.Next() {
		var row deadDeliveryRow
		var kind string
		var updatedMillis int64
		if err := rows.Scan(
			&row.item.ID, &row.item.InvestigationID, &kind, &row.sequence,
			&row.item.Attempt, &row.lastError, &updatedMillis,
			&row.sourceMessageID, &row.cardMessageID, &row.laterCount,
		); err != nil {
			return nil, fmt.Errorf("scan dead delivery: %w", err)
		}
		row.item.Kind = domain.DeliveryKind(kind)
		row.laterCountKnown = true
		row.item.ReasonCode = row.lastError
		row.item.UpdatedAt = time.UnixMilli(updatedMillis).UTC()
		replayable, reason, err := s.deliveryReplayability(ctx, nil, row)
		if err != nil {
			return nil, err
		}
		row.item.Replayable = replayable
		row.item.ReplayReason = reason
		items = append(items, row.item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dead deliveries: %w", err)
	}
	return items, nil
}

func (s *Store) ReplayDeadDelivery(ctx context.Context, deliveryID, operatorRef string, now time.Time) (domain.DeliveryReplayResult, error) {
	if !boundedSafeCode(deliveryID, 1, 256) || !boundedOperatorRef(operatorRef) {
		return domain.DeliveryReplayResult{}, errors.New("delivery ID or operator reference is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.DeliveryReplayResult{}, fmt.Errorf("begin dead-delivery replay: %w", err)
	}
	defer tx.Rollback()

	var row deadDeliveryRow
	var status, kind string
	var updatedMillis int64
	err = tx.QueryRowContext(ctx, `
SELECT d.id, d.investigation_id, d.kind, d.sequence, d.attempts,
       d.status, d.last_error, d.updated_at, t.source_message_id, t.card_message_id
FROM delivery_events d
JOIN interaction_targets t ON t.investigation_id = d.investigation_id
WHERE d.id = ?`, deliveryID).Scan(
		&row.item.ID, &row.item.InvestigationID, &kind, &row.sequence,
		&row.item.Attempt, &status, &row.lastError, &updatedMillis,
		&row.sourceMessageID, &row.cardMessageID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeliveryReplayResult{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.DeliveryReplayResult{}, fmt.Errorf("load dead delivery: %w", err)
	}
	if domain.DeliveryStatus(status) != domain.DeliveryDead {
		return domain.DeliveryReplayResult{}, ports.ErrStateConflict
	}
	row.item.Kind = domain.DeliveryKind(kind)
	replayable, reason, err := s.deliveryReplayability(ctx, tx, row)
	if err != nil {
		return domain.DeliveryReplayResult{}, err
	}
	if !replayable {
		return domain.DeliveryReplayResult{}, fmt.Errorf("%w: %s", ports.ErrDeliveryReplayUnsafe, reason)
	}

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE delivery_events
SET status = ?, attempts = 0, lease_owner = '', lease_until = 0,
    available_at = ?, last_error = '', updated_at = ?
WHERE id = ? AND status = ?`,
		domain.DeliveryPending, nowMillis, nowMillis, deliveryID, domain.DeliveryDead)
	if err != nil {
		return domain.DeliveryReplayResult{}, fmt.Errorf("requeue dead delivery: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return domain.DeliveryReplayResult{}, ports.ErrStateConflict
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO delivery_operations(delivery_id, operation, operator_ref, reason_code, occurred_at)
VALUES (?, 'REPLAY', ?, 'operator_replay', ?)`, deliveryID, operatorRef, nowMillis); err != nil {
		return domain.DeliveryReplayResult{}, fmt.Errorf("audit dead-delivery replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.DeliveryReplayResult{}, fmt.Errorf("commit dead-delivery replay: %w", err)
	}
	return domain.DeliveryReplayResult{
		DeliveryID: deliveryID, Replayed: true, ReasonCode: "operator_replay", OccurredAt: now.UTC(),
	}, nil
}

func (s *Store) deliveryReplayability(ctx context.Context, tx *sql.Tx, row deadDeliveryRow) (bool, string, error) {
	if row.lastError == "interaction_rebound" {
		return false, "interaction_rebound", nil
	}
	if row.item.Kind == domain.DeliveryQueued && row.cardMessageID == "" {
		if row.sourceMessageID == "" {
			return false, "missing_source_message", nil
		}
		return true, "initial_receipt", nil
	}
	if row.cardMessageID == "" {
		return false, "card_not_bound", nil
	}

	if row.laterCountKnown {
		if row.laterCount > 0 {
			return false, "newer_projection_exists", nil
		}
		return true, "latest_bound_projection", nil
	}
	queryer := interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}(s.db)
	if tx != nil {
		queryer = tx
	}
	var later int
	if err := queryer.QueryRowContext(ctx, `
SELECT COUNT(*) FROM delivery_events
WHERE investigation_id = ? AND sequence > ?`, row.item.InvestigationID, row.sequence).Scan(&later); err != nil {
		return false, "", fmt.Errorf("check newer card projection: %w", err)
	}
	if later > 0 {
		return false, "newer_projection_exists", nil
	}
	return true, "latest_bound_projection", nil
}

func (s *Store) ListDeliveryAttempts(ctx context.Context, deliveryID string, limit int) ([]domain.DeliveryAttempt, error) {
	if !boundedSafeCode(deliveryID, 1, 256) || limit <= 0 || limit > 200 {
		return nil, errors.New("delivery ID or attempt limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT delivery_id, investigation_id, attempt, outcome,
       failure_disposition, reason_code, occurred_at
FROM delivery_attempts
WHERE delivery_id = ?
ORDER BY attempt DESC
LIMIT ?`, deliveryID, limit)
	if err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}
	defer rows.Close()
	items := make([]domain.DeliveryAttempt, 0)
	for rows.Next() {
		var item domain.DeliveryAttempt
		var disposition, reason string
		var occurredMillis int64
		if err := rows.Scan(
			&item.DeliveryID, &item.InvestigationID, &item.Attempt, &item.Outcome,
			&disposition, &reason, &occurredMillis,
		); err != nil {
			return nil, fmt.Errorf("scan delivery attempt: %w", err)
		}
		if disposition != "" {
			failure := domain.DeliveryFailure{Disposition: domain.FailureDisposition(disposition), ReasonCode: reason}
			item.Failure = &failure
		}
		item.OccurredAt = time.UnixMilli(occurredMillis).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate delivery attempts: %w", err)
	}
	return items, nil
}

func boundedSafeCode(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	if length < minimum || length > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("_-.:/", character) {
			continue
		}
		return false
	}
	return true
}

func boundedOperatorRef(value string) bool {
	return boundedSafeCode(value, 1, 128)
}

var _ ports.DeliveryOperations = (*Store)(nil)
