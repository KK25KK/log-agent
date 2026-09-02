package ports

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
)

var (
	ErrNotFound               = errors.New("not found")
	ErrLeaseLost              = errors.New("job lease is no longer active")
	ErrStateConflict          = errors.New("durable state changed concurrently")
	ErrQueryDenied            = errors.New("query denied")
	ErrQueryBudgetExceeded    = errors.New("query budget exceeded")
	ErrInvalidQuerySchema     = errors.New("invalid query schema")
	ErrExternalOutcomeUnknown = errors.New("external query outcome is unknown")
)

// QueryStepFailure preserves a stable, provider-neutral reason across a crash
// between persisting a deterministic step failure and finishing its job.
type QueryStepFailure struct {
	Code  string
	Cause error
}

func (failure *QueryStepFailure) Error() string {
	if failure == nil || failure.Code == "" {
		return "query step failed"
	}
	return failure.Code
}

func (failure *QueryStepFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

func NewQueryStepFailure(code string, cause error) error {
	return &QueryStepFailure{Code: code, Cause: cause}
}

func QueryStepFailureCode(err error) (string, bool) {
	var failure *QueryStepFailure
	if !errors.As(err, &failure) || failure.Code == "" {
		return "", false
	}
	return failure.Code, true
}

// Store owns durable idempotency and investigation lifecycle semantics.
type Store interface {
	AcceptOnce(ctx context.Context, inbound domain.InboundMessage, request domain.InvestigationRequest, investigationID, jobID string) (storedInvestigationID string, created bool, err error)
	ClaimNext(ctx context.Context, workerID string, now time.Time, leaseDuration time.Duration) (domain.Job, bool, error)
	RenewLease(ctx context.Context, job domain.Job, now time.Time, leaseDuration time.Duration) error
	FinishSuccess(ctx context.Context, job domain.Job, evidence []domain.Evidence, report domain.Report, now time.Time) error
	FinishFailure(ctx context.Context, job domain.Job, cause string, now time.Time) error
	FinishNeedsReview(ctx context.Context, job domain.Job, reasonCode string, now time.Time) error
	RequestCancel(ctx context.Context, investigationID string, now time.Time) error
	GetInvestigation(ctx context.Context, investigationID string) (domain.Investigation, error)
}

// QueryStepStore owns crash-safe checkpoints for the two normalized SLS
// observations. Implementations must fence every mutation with the active job
// lease owner and job attempt.
type QueryStepStore interface {
	PrepareQueryStep(ctx context.Context, job domain.Job, stepKey, inputHash string, now time.Time) (domain.QueryStepDecision, error)
	CompleteQueryStep(ctx context.Context, job domain.Job, stepKey, inputHash string, result domain.QueryResult, now time.Time) error
	FailQueryStep(ctx context.Context, job domain.Job, stepKey, inputHash, reasonCode string, now time.Time) error
}

// InvestigationEngine hides the selected orchestration framework from the application.
type InvestigationEngine interface {
	Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error)
}

// ReportSummarizer is a replaceable, evidence-only LLM boundary. It receives
// no raw logs, query strings, credentials, resources or requester identity.
type ReportSummarizer interface {
	Summarize(ctx context.Context, input domain.SummaryInput) (domain.SummaryProviderResult, error)
}

// SLSExecutor is the replaceable, read-only query boundary.
type SLSExecutor interface {
	Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error)
}

// QueryGovernanceResolver resolves and validates the current administrator-
// owned resource, ACL, policy, template, and Schema identity without reading
// log rows. Checkpoint reuse must bind this fingerprint to the logical spec.
type QueryGovernanceResolver interface {
	ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error)
}

// ChangeSource provides bounded, read-only governance context after SLS has
// established the resource identity. It must not be queried with user-owned
// physical resource identifiers.
type ChangeSource interface {
	List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error)
}

// OperationalSignalSource provides a bounded, read-only aggregate projection
// from a metrics or Trace backend after governed SLS Evidence has established
// the resource and time range. It must never return raw spans or labels.
type OperationalSignalSource interface {
	List(ctx context.Context, query domain.OperationalSignalQuery) (domain.OperationalSignalSet, error)
}

// RunbookSource provides bounded, read-only, versioned human-review guidance
// after validated Evidence and deterministic Recommendations establish scope.
// It has no execution, approval, URL, or Evidence-reference capability.
type RunbookSource interface {
	Lookup(ctx context.Context, query domain.RunbookQuery) (domain.RunbookSet, error)
}

// DeploymentVersionSource resolves the immutable code version that was
// actually active for a logical service/environment at an investigated time.
type DeploymentVersionSource interface {
	ResolveDeployment(ctx context.Context, query domain.DeploymentQuery) (domain.DeploymentEvidence, error)
}

// CodeRepositoryCatalog owns administrator-approved repository roots and path
// boundaries. Physical roots never come from users, logs, or a model.
type CodeRepositoryCatalog interface {
	ResolveCodeRepository(ctx context.Context, repositoryID string) (domain.CodeRepository, error)
}

// CodeEvidenceProvider performs only bounded, read-only lookup at the exact
// immutable Commit supplied by a trusted DeploymentVersionSource.
type CodeEvidenceProvider interface {
	SearchCode(ctx context.Context, request domain.CodeSearchRequest) (domain.CodeSearchResult, error)
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
