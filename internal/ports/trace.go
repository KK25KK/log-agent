package ports

import (
	"context"
	"time"

	"logagent/internal/domain"
)

type TraceResourceCatalog interface {
	ResolveTraceGroup(ctx context.Context, service, environment string) (domain.TraceResourceGroup, error)
	AllowedTraceGroup(ctx context.Context, principal domain.Principal, groupID string) bool
	ListAllowedCapabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error)
}

type TraceBackend interface {
	GetTraceSchema(ctx context.Context, member domain.TraceResourceMember) (domain.IndexSchema, error)
	SearchTrace(ctx context.Context, query domain.ApprovedTraceQuery) (domain.TraceBackendResult, error)
}

// GovernedTraceExecutor is distinct from the aggregate SLSExecutor. It
// exposes a fixed resource-group plan and one approved member operation.
type GovernedTraceExecutor interface {
	ResolveTraceGovernance(ctx context.Context, spec domain.TraceSearchSpec) (domain.TracePlan, error)
	ExecuteTraceMember(ctx context.Context, plan domain.TracePlan, memberID string) (domain.TraceMemberResult, error)
}

type TraceAuditor interface {
	RecordTraceAudit(ctx context.Context, audit domain.TraceAudit) error
}

type TraceQueryStepStore interface {
	PrepareTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash string, now time.Time) (domain.TraceQueryStepDecision, error)
	CompleteTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash string, result domain.TraceMemberResult, now time.Time) error
	FailTraceQueryStep(ctx context.Context, job domain.Job, memberID, inputHash, reasonCode string, now time.Time) error
}
