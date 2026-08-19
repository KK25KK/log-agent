package ports

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrLeaseLost           = errors.New("job lease is no longer active")
	ErrStateConflict       = errors.New("durable state changed concurrently")
	ErrQueryDenied         = errors.New("query denied")
	ErrQueryBudgetExceeded = errors.New("query budget exceeded")
	ErrInvalidQuerySchema  = errors.New("invalid query schema")
)

// Store owns durable idempotency and investigation lifecycle semantics.
type Store interface {
	AcceptOnce(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest, investigationID, jobID string) (storedInvestigationID string, created bool, err error)
	ClaimNext(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (domain.Job, bool, error)
	RenewLease(ctx context.Context, job domain.Job, now time.Time, leaseDuration time.Duration) error
	FinishSuccess(ctx context.Context, job domain.Job, evidence []domain.Evidence, report domain.Report, now time.Time) error
	FinishFailure(ctx context.Context, job domain.Job, cause string, now time.Time) error
	RequestCancel(ctx context.Context, investigationID string, now time.Time) error
	GetInvestigation(ctx context.Context, investigationID string) (domain.Investigation, error)
}

// InvestigationEngine hides the selected orchestration framework from the application.
type InvestigationEngine interface {
	Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error)
}

// SLSExecutor is the replaceable, read-only query boundary.
type SLSExecutor interface {
	Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error)
}

// ChangeSource provides bounded, read-only governance context after SLS has
// established the resource identity. It must not be queried with user-owned
// physical resource identifiers.
type ChangeSource interface {
	List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error)
}

// ResourceCatalog resolves logical scope and owns default-deny ACL bindings.
type ResourceCatalog interface {
	Resolve(ctx context.Context, service, environment string) (domain.LogResource, error)
	Allowed(ctx context.Context, principal domain.Principal, resourceID string) bool
}

// SLSBackend is the narrow provider boundary used only after policy approval.
type SLSBackend interface {
	GetSchema(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error)
	Execute(ctx context.Context, query domain.ApprovedQuery) (domain.QueryResult, error)
}

// QueryAuditor records append-only security and usage decisions.
type QueryAuditor interface {
	RecordQueryAudit(ctx context.Context, audit domain.QueryAudit) error
}
