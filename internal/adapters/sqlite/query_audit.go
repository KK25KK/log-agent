package sqlite

import (
	"context"
	"fmt"
	"time"

	"logagent/internal/domain"
)

// RecordQueryAudit appends one security or usage decision without retaining
// provider query text, log bodies, or credentials.
func (s *Store) RecordQueryAudit(ctx context.Context, audit domain.QueryAudit) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO query_audit(
    investigation_id,
    principal_app_id,
    principal_tenant_key,
    principal_user_id,
    resource_id,
    template_id,
    template_version,
    query_spec_hash,
    schema_fingerprint,
    policy_version,
    outcome,
    reason,
    provider_request_id,
    progress,
    complete,
    truncated,
    processed_rows,
    processed_bytes,
    occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		audit.InvestigationID,
		audit.Principal.AppID,
		audit.Principal.TenantKey,
		audit.Principal.UserID,
		audit.ResourceID,
		audit.TemplateID,
		audit.TemplateVersion,
		audit.QuerySpecHash,
		audit.SchemaFingerprint,
		audit.PolicyVersion,
		audit.Outcome,
		audit.Reason,
		audit.ProviderRequestID,
		audit.Progress,
		audit.Complete,
		audit.Truncated,
		audit.ProcessedRows,
		audit.ProcessedBytes,
		audit.OccurredAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("record query audit: %w", err)
	}
	return nil
}

// ListQueryAudits returns audit events in append order for tests and local
// diagnostics. Query audit has no update or delete operation.
func (s *Store) ListQueryAudits(ctx context.Context, investigationID string) ([]domain.QueryAudit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    investigation_id,
    principal_app_id,
    principal_tenant_key,
    principal_user_id,
    resource_id,
    template_id,
    template_version,
    query_spec_hash,
    schema_fingerprint,
    policy_version,
    outcome,
    reason,
    provider_request_id,
    progress,
    complete,
    truncated,
    processed_rows,
    processed_bytes,
    occurred_at
FROM query_audit
WHERE investigation_id = ?
ORDER BY audit_id`, investigationID)
	if err != nil {
		return nil, fmt.Errorf("list query audits: %w", err)
	}
	defer rows.Close()

	audits := make([]domain.QueryAudit, 0)
	for rows.Next() {
		var (
			audit      domain.QueryAudit
			complete   int64
			truncated  int64
			occurredAt string
		)
		if err := rows.Scan(
			&audit.InvestigationID,
			&audit.Principal.AppID,
			&audit.Principal.TenantKey,
			&audit.Principal.UserID,
			&audit.ResourceID,
			&audit.TemplateID,
			&audit.TemplateVersion,
			&audit.QuerySpecHash,
			&audit.SchemaFingerprint,
			&audit.PolicyVersion,
			&audit.Outcome,
			&audit.Reason,
			&audit.ProviderRequestID,
			&audit.Progress,
			&complete,
			&truncated,
			&audit.ProcessedRows,
			&audit.ProcessedBytes,
			&occurredAt,
		); err != nil {
			return nil, fmt.Errorf("scan query audit: %w", err)
		}
		parsedTime, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("parse query audit occurrence time: %w", err)
		}
		audit.Complete = complete != 0
		audit.Truncated = truncated != 0
		audit.OccurredAt = parsedTime
		audits = append(audits, audit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate query audits: %w", err)
	}
	return audits, nil
}
