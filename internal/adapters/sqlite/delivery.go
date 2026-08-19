package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

func prepareInteractionTarget(ctx context.Context, tx *sql.Tx, inbound domain.InboundMessage, investigationID string, nowMillis int64) error {
	sourceMessageID := inbound.ReplyToMessageID
	if sourceMessageID == "" {
		sourceMessageID = inbound.MessageID
	}
	cardMessageID := ""
	if inbound.ReplyToMessageID != "" && inbound.ReplyToMessageID != inbound.MessageID {
		if inbound.ExpectedInvestigationID == "" {
			return ports.ErrActionForbidden
		}
		cardMessageID = inbound.ReplyToMessageID
		var previousInvestigationID string
		err := tx.QueryRowContext(ctx, `
UPDATE interaction_targets
SET card_message_id = '', updated_at = ?
WHERE investigation_id = ? AND app_id = ? AND tenant_key = ? AND chat_id = ? AND card_message_id = ?
RETURNING investigation_id`,
			nowMillis, inbound.ExpectedInvestigationID,
			inbound.AppID, inbound.TenantKey, inbound.ChatID, cardMessageID).Scan(&previousInvestigationID)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrActionForbidden
		}
		if err != nil {
			return fmt.Errorf("move interaction card: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE delivery_events
SET status = ?, lease_owner = '', lease_until = 0,
    last_error = 'interaction_rebound', updated_at = ?
WHERE investigation_id = ? AND status IN (?, ?)`,
			domain.DeliveryDead, nowMillis, previousInvestigationID,
			domain.DeliveryPending, domain.DeliverySending); err != nil {
			return fmt.Errorf("supersede previous card deliveries: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO interaction_targets(
    investigation_id, app_id, tenant_key, chat_id,
    source_message_id, card_message_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		investigationID, inbound.AppID, inbound.TenantKey, inbound.ChatID,
		sourceMessageID, cardMessageID, nowMillis, nowMillis); err != nil {
		return fmt.Errorf("insert interaction target: %w", err)
	}
	return nil
}

func enqueueDelivery(ctx context.Context, tx *sql.Tx, investigationID string, kind domain.DeliveryKind, nowMillis int64) error {
	sequence, err := deliverySequence(kind)
	if err != nil {
		return err
	}
	deliveryID := "delivery:" + investigationID + ":" + string(kind)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO delivery_events(
    id, investigation_id, kind, sequence, status,
    available_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(investigation_id, kind) DO NOTHING`,
		deliveryID, investigationID, kind, sequence, domain.DeliveryPending,
		nowMillis, nowMillis, nowMillis); err != nil {
		return fmt.Errorf("enqueue %s delivery: %w", kind, err)
	}
	return nil
}

func deliverySequence(kind domain.DeliveryKind) (int, error) {
	switch kind {
	case domain.DeliveryQueued:
		return 10, nil
	case domain.DeliveryRunning:
		return 20, nil
	case domain.DeliverySucceeded, domain.DeliveryFailed, domain.DeliveryCancelled, domain.DeliveryNeedsReview:
		return 30, nil
	default:
		return 0, fmt.Errorf("unknown delivery kind %q", kind)
	}
}

// ClaimDelivery leases the oldest eligible card update. Later updates wait for
// earlier live attempts. A dead progress patch can be skipped, while a dead
// initial receipt keeps blocking because there is no remote card to patch.
func (s *Store) ClaimDelivery(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (domain.DeliveryJob, bool, error) {
	if workerID == "" || leaseDuration <= 0 {
		return domain.DeliveryJob{}, false, errors.New("delivery worker ID and positive lease are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.DeliveryJob{}, false, fmt.Errorf("begin delivery claim: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	leaseUntil := now.UTC().Add(leaseDuration)
	var delivery domain.DeliveryJob
	var kind string
	var leaseUntilMillis int64
	err = tx.QueryRowContext(ctx, `
WITH candidate AS (
    SELECT d.id
    FROM delivery_events d
    JOIN interaction_targets t ON t.investigation_id = d.investigation_id
    WHERE (
        (d.status = ? AND d.available_at <= ?)
        OR (d.status = ? AND d.lease_until <= ?)
    )
      AND (
        (d.kind = ? AND (t.source_message_id <> '' OR t.card_message_id <> ''))
        OR (d.kind <> ? AND t.card_message_id <> '')
      )
      AND NOT EXISTS (
        SELECT 1
        FROM delivery_events earlier
        WHERE earlier.investigation_id = d.investigation_id
          AND earlier.sequence < d.sequence
          AND (
            earlier.status NOT IN (?, ?)
            OR (
              earlier.status = ? AND earlier.kind = ?
              AND t.card_message_id = ''
            )
          )
      )
    ORDER BY d.created_at, d.sequence, d.id
    LIMIT 1
)
UPDATE delivery_events
SET status = ?, attempts = attempts + 1, lease_owner = ?, lease_until = ?, updated_at = ?
WHERE id = (SELECT id FROM candidate)
RETURNING id, investigation_id, kind, attempts, lease_until`,
		domain.DeliveryPending, nowMillis,
		domain.DeliverySending, nowMillis,
		domain.DeliveryQueued, domain.DeliveryQueued,
		domain.DeliverySent, domain.DeliveryDead,
		domain.DeliveryDead, domain.DeliveryQueued,
		domain.DeliverySending, workerID, leaseUntil.UnixMilli(), nowMillis,
	).Scan(&delivery.ID, &delivery.Investigation.ID, &kind, &delivery.Attempt, &leaseUntilMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DeliveryJob{}, false, nil
	}
	if err != nil {
		return domain.DeliveryJob{}, false, fmt.Errorf("claim delivery: %w", err)
	}
	delivery.Kind = domain.DeliveryKind(kind)
	delivery.LeaseOwner = workerID
	delivery.LeaseUntil = time.UnixMilli(leaseUntilMillis).UTC()

	if err := tx.QueryRowContext(ctx, `
SELECT app_id, tenant_key, chat_id, source_message_id, card_message_id
FROM interaction_targets WHERE investigation_id = ?`, delivery.Investigation.ID).Scan(
		&delivery.Target.AppID,
		&delivery.Target.TenantKey,
		&delivery.Target.ChatID,
		&delivery.Target.SourceMessageID,
		&delivery.Target.CardMessageID,
	); err != nil {
		return domain.DeliveryJob{}, false, fmt.Errorf("load delivery target: %w", err)
	}
	loaded, err := loadInvestigationTx(ctx, tx, delivery.Investigation.ID)
	if err != nil {
		return domain.DeliveryJob{}, false, err
	}
	delivery.Investigation = loaded
	if err := tx.Commit(); err != nil {
		return domain.DeliveryJob{}, false, fmt.Errorf("commit delivery claim: %w", err)
	}
	return delivery, true, nil
}

func loadInvestigationTx(ctx context.Context, tx *sql.Tx, investigationID string) (domain.Investigation, error) {
	var item domain.Investigation
	var requestJSON, reportJSON []byte
	var createdMillis, updatedMillis int64
	err := tx.QueryRowContext(ctx, `
SELECT id, status, request_json, COALESCE(report_json, ''), last_error, created_at, updated_at
FROM investigations WHERE id = ?`, investigationID).Scan(
		&item.ID, &item.Status, &requestJSON, &reportJSON, &item.LastError, &createdMillis, &updatedMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Investigation{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.Investigation{}, fmt.Errorf("load delivery investigation: %w", err)
	}
	if err := json.Unmarshal(requestJSON, &item.Request); err != nil {
		return domain.Investigation{}, fmt.Errorf("decode delivery request: %w", err)
	}
	if len(reportJSON) > 0 {
		var report domain.Report
		if err := json.Unmarshal(reportJSON, &report); err != nil {
			return domain.Investigation{}, fmt.Errorf("decode delivery report: %w", err)
		}
		item.Report = &report
	}
	item.CreatedAt = time.UnixMilli(createdMillis).UTC()
	item.UpdatedAt = time.UnixMilli(updatedMillis).UTC()
	return item, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, delivery domain.DeliveryJob, remoteMessageID string, now time.Time) error {
	if remoteMessageID == "" {
		return errors.New("remote card message ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delivery completion: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, `
UPDATE delivery_events
SET status = ?, lease_owner = '', lease_until = 0, last_error = '', updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ?
  AND lease_owner = ? AND attempts = ? AND lease_until >= ?`,
		domain.DeliverySent, nowMillis,
		delivery.ID, delivery.Investigation.ID, domain.DeliverySending,
		delivery.LeaseOwner, delivery.Attempt, nowMillis)
	if err != nil {
		return fmt.Errorf("complete delivery: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delivery completion result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}

	if delivery.Target.CardMessageID == "" {
		result, err = tx.ExecContext(ctx, `
UPDATE interaction_targets
SET card_message_id = ?, updated_at = ?
WHERE investigation_id = ? AND card_message_id = ''`,
			remoteMessageID, nowMillis, delivery.Investigation.ID)
	} else {
		if remoteMessageID != delivery.Target.CardMessageID {
			return ports.ErrLeaseLost
		}
		result, err = tx.ExecContext(ctx, `
UPDATE interaction_targets
SET updated_at = ?
WHERE investigation_id = ? AND card_message_id = ?`,
			nowMillis, delivery.Investigation.ID, delivery.Target.CardMessageID)
	}
	if err != nil {
		return fmt.Errorf("bind interaction card: %w", err)
	}
	bound, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read interaction binding result: %w", err)
	}
	if bound != 1 {
		return ports.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delivery completion: %w", err)
	}
	return nil
}

func (s *Store) FailDelivery(ctx context.Context, delivery domain.DeliveryJob, reason string, retryAt time.Time, dead bool, now time.Time) error {
	if len(reason) > 128 {
		reason = reason[:128]
	}
	status := domain.DeliveryPending
	if dead {
		status = domain.DeliveryDead
	}
	nowMillis := now.UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
UPDATE delivery_events
SET status = ?, lease_owner = '', lease_until = 0,
    available_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND investigation_id = ? AND status = ?
  AND lease_owner = ? AND attempts = ? AND lease_until >= ?`,
		status, retryAt.UTC().UnixMilli(), reason, nowMillis,
		delivery.ID, delivery.Investigation.ID, domain.DeliverySending,
		delivery.LeaseOwner, delivery.Attempt, nowMillis)
	if err != nil {
		return fmt.Errorf("fail delivery: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delivery failure result: %w", err)
	}
	if updated != 1 {
		return ports.ErrLeaseLost
	}
	return nil
}

func (s *Store) ResolveInteraction(ctx context.Context, appID, tenantKey, chatID, cardMessageID string) (string, error) {
	var investigationID string
	err := s.db.QueryRowContext(ctx, `
SELECT investigation_id
FROM interaction_targets
WHERE app_id = ? AND tenant_key = ? AND chat_id = ? AND card_message_id = ? AND card_message_id <> ''`,
		appID, tenantKey, chatID, cardMessageID).Scan(&investigationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ports.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve interaction: %w", err)
	}
	return investigationID, nil
}

func (s *Store) ResolveActionReplay(
	ctx context.Context,
	appID, tenantKey, chatID, userID, eventID string,
	action domain.InvestigationAction,
) (string, error) {
	var investigationID string
	err := s.db.QueryRowContext(ctx, `
SELECT investigation_id
FROM inbox
WHERE app_id = ? AND tenant_key = ? AND message_id = ?
  AND chat_id = ? AND user_id = ? AND raw_text = ?`,
		appID, tenantKey, "card:"+eventID, chatID, userID, string(action)).Scan(&investigationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ports.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve action replay: %w", err)
	}
	return investigationID, nil
}

var (
	_ ports.DeliveryStore       = (*Store)(nil)
	_ ports.InteractionResolver = (*Store)(nil)
)
