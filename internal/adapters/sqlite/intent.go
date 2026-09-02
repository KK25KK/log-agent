package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type intentRowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) BeginIntentResolution(ctx context.Context, resolution domain.IntentResolution) (domain.IntentResolution, bool, error) {
	if err := validateInitialIntentResolution(resolution); err != nil {
		return domain.IntentResolution{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IntentResolution{}, false, fmt.Errorf("begin intent resolution: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
INSERT INTO intent_resolutions(
    id, app_id, tenant_key, user_id, source_message_id,
    problem_text, problem_fingerprint, problem_redacted, status,
    provider, model, prompt_version, prompt_fingerprint, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, tenant_key, source_message_id) DO NOTHING`,
		resolution.ID, resolution.Principal.AppID, resolution.Principal.TenantKey, resolution.Principal.UserID,
		resolution.SourceMessageID, resolution.Problem.Text, resolution.Problem.Fingerprint, resolution.Problem.Redacted,
		resolution.Status, resolution.Provider, resolution.Model, resolution.PromptVersion, resolution.PromptFingerprint,
		resolution.CreatedAt.UTC().UnixMilli(), resolution.ExpiresAt.UTC().UnixMilli(),
	)
	if err != nil {
		return domain.IntentResolution{}, false, fmt.Errorf("insert intent resolution: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return domain.IntentResolution{}, false, fmt.Errorf("read intent resolution insert result: %w", err)
	}
	if inserted == 1 {
		if err := tx.Commit(); err != nil {
			return domain.IntentResolution{}, false, fmt.Errorf("commit intent resolution: %w", err)
		}
		return resolution, true, nil
	}
	existing, err := scanIntentResolution(tx.QueryRowContext(ctx, `
SELECT `+intentResolutionColumns+` FROM intent_resolutions
WHERE app_id = ? AND tenant_key = ? AND source_message_id = ?`,
		resolution.Principal.AppID, resolution.Principal.TenantKey, resolution.SourceMessageID))
	if err != nil {
		return domain.IntentResolution{}, false, fmt.Errorf("load duplicate intent resolution: %w", err)
	}
	if existing.Principal.UserID != resolution.Principal.UserID || existing.Problem.Fingerprint != resolution.Problem.Fingerprint {
		return domain.IntentResolution{}, false, ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return domain.IntentResolution{}, false, fmt.Errorf("commit duplicate intent lookup: %w", err)
	}
	return existing, false, nil
}

func (s *Store) CompleteIntentResolution(ctx context.Context, resolution domain.IntentResolution) error {
	if err := validateCompletedIntentResolution(resolution); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE intent_resolutions
SET status = ?, intent = ?, service = ?, environment = ?, duration_seconds = ?, template_id = ?, confidence = ?,
    provider = ?, model = ?, request_id = ?, prompt_version = ?, prompt_fingerprint = ?,
    input_tokens = ?, output_tokens = ?, total_tokens = ?, latency_millis = ?, reason_code = ?
WHERE id = ? AND app_id = ? AND tenant_key = ? AND user_id = ? AND status = ?`,
		resolution.Status, resolution.Intent, resolution.Service, resolution.Environment, resolution.DurationSeconds,
		resolution.TemplateID, resolution.Confidence, resolution.Provider, resolution.Model, resolution.RequestID,
		resolution.PromptVersion, resolution.PromptFingerprint, resolution.InputTokens, resolution.OutputTokens,
		resolution.TotalTokens, resolution.LatencyMillis, resolution.ReasonCode,
		resolution.ID, resolution.Principal.AppID, resolution.Principal.TenantKey, resolution.Principal.UserID,
		domain.IntentResolutionParsing,
	)
	if err != nil {
		return fmt.Errorf("complete intent resolution: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read intent resolution completion result: %w", err)
	}
	if updated != 1 {
		return ports.ErrStateConflict
	}
	return nil
}

func (s *Store) GetIntentResolution(ctx context.Context, resolutionID string) (domain.IntentResolution, error) {
	resolution, err := scanIntentResolution(s.db.QueryRowContext(ctx, `
SELECT `+intentResolutionColumns+` FROM intent_resolutions WHERE id = ?`, resolutionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IntentResolution{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.IntentResolution{}, fmt.Errorf("get intent resolution: %w", err)
	}
	return resolution, nil
}

func (s *Store) ConfirmIntentResolution(
	ctx context.Context,
	resolutionID string,
	principal domain.Principal,
	investigationID string,
	now time.Time,
) (string, bool, error) {
	if resolutionID == "" || !principal.Complete() || investigationID == "" || now.IsZero() {
		return "", false, ports.ErrIntentInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin intent confirmation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE intent_resolutions
SET confirmed_at = ?, investigation_id = ?
WHERE id = ? AND app_id = ? AND tenant_key = ? AND user_id = ?
  AND status = ? AND confirmed_at = 0 AND investigation_id IS NULL`,
		now.UTC().UnixMilli(), investigationID, resolutionID,
		principal.AppID, principal.TenantKey, principal.UserID, domain.IntentResolutionResolved,
	)
	if err != nil {
		return "", false, fmt.Errorf("confirm intent resolution: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("read intent confirmation result: %w", err)
	}
	if updated == 1 {
		if err := tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit intent confirmation: %w", err)
		}
		return investigationID, true, nil
	}
	existing, err := scanIntentResolution(tx.QueryRowContext(ctx, `
SELECT `+intentResolutionColumns+` FROM intent_resolutions WHERE id = ?`, resolutionID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ports.ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("load intent confirmation conflict: %w", err)
	}
	if existing.Principal != principal {
		return "", false, ports.ErrIntentForbidden
	}
	if existing.InvestigationID == "" {
		return "", false, ports.ErrIntentInvalid
	}
	if existing.InvestigationID != investigationID {
		return "", false, ports.ErrStateConflict
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit intent confirmation replay: %w", err)
	}
	return existing.InvestigationID, false, nil
}

const intentResolutionColumns = `
id, app_id, tenant_key, user_id, source_message_id,
problem_text, problem_fingerprint, problem_redacted,
status, intent, service, environment, duration_seconds, template_id, confidence,
provider, model, request_id, prompt_version, prompt_fingerprint,
input_tokens, output_tokens, total_tokens, latency_millis, reason_code,
created_at, expires_at, confirmed_at, COALESCE(investigation_id, '')`

func scanIntentResolution(row intentRowScanner) (domain.IntentResolution, error) {
	var resolution domain.IntentResolution
	var redacted bool
	var createdAt, expiresAt, confirmedAt int64
	err := row.Scan(
		&resolution.ID, &resolution.Principal.AppID, &resolution.Principal.TenantKey, &resolution.Principal.UserID,
		&resolution.SourceMessageID, &resolution.Problem.Text, &resolution.Problem.Fingerprint, &redacted,
		&resolution.Status, &resolution.Intent, &resolution.Service, &resolution.Environment,
		&resolution.DurationSeconds, &resolution.TemplateID, &resolution.Confidence,
		&resolution.Provider, &resolution.Model, &resolution.RequestID, &resolution.PromptVersion,
		&resolution.PromptFingerprint, &resolution.InputTokens, &resolution.OutputTokens,
		&resolution.TotalTokens, &resolution.LatencyMillis, &resolution.ReasonCode,
		&createdAt, &expiresAt, &confirmedAt, &resolution.InvestigationID,
	)
	if err != nil {
		return domain.IntentResolution{}, err
	}
	resolution.Problem.Redacted = redacted
	resolution.CreatedAt = time.UnixMilli(createdAt).UTC()
	resolution.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	if confirmedAt > 0 {
		resolution.ConfirmedAt = time.UnixMilli(confirmedAt).UTC()
	}
	return resolution, nil
}

func validateInitialIntentResolution(resolution domain.IntentResolution) error {
	if resolution.ID == "" || !resolution.Principal.Complete() || resolution.SourceMessageID == "" ||
		resolution.Problem.Text == "" || len(resolution.Problem.Fingerprint) != 64 ||
		resolution.Status != domain.IntentResolutionParsing || resolution.Provider == "" || resolution.Model == "" ||
		resolution.PromptVersion != domain.IntentPromptVersion || len(resolution.PromptFingerprint) != 64 ||
		resolution.CreatedAt.IsZero() || !resolution.ExpiresAt.After(resolution.CreatedAt) {
		return ports.ErrIntentInvalid
	}
	return nil
}

func validateCompletedIntentResolution(resolution domain.IntentResolution) error {
	if err := validateInitialIntentResolution(domain.IntentResolution{
		ID: resolution.ID, Principal: resolution.Principal, SourceMessageID: resolution.SourceMessageID,
		Problem: resolution.Problem, Status: domain.IntentResolutionParsing,
		Provider: resolution.Provider, Model: resolution.Model,
		PromptVersion: resolution.PromptVersion, PromptFingerprint: resolution.PromptFingerprint,
		CreatedAt: resolution.CreatedAt, ExpiresAt: resolution.ExpiresAt,
	}); err != nil {
		return err
	}
	switch resolution.Status {
	case domain.IntentResolutionResolved, domain.IntentResolutionUnknown, domain.IntentResolutionIncomplete,
		domain.IntentResolutionRejected, domain.IntentResolutionFallback, domain.IntentResolutionOutcomeUnknown:
	default:
		return ports.ErrIntentInvalid
	}
	if resolution.InputTokens < 0 || resolution.OutputTokens < 0 || resolution.TotalTokens < resolution.InputTokens+resolution.OutputTokens ||
		resolution.LatencyMillis < 0 || resolution.Confidence < 0 || resolution.Confidence > 1 {
		return ports.ErrIntentInvalid
	}
	return nil
}

var _ ports.IntentResolutionStore = (*Store)(nil)
