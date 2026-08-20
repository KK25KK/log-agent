package ports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"logagent/internal/domain"
)

var (
	ErrDeliveryReplayUnsafe = errors.New("delivery replay is unsafe")
	ErrTenantQuotaExceeded  = errors.New("tenant query quota exceeded")
	ErrQuotaUsageReplayed   = errors.New("query quota usage key was already reserved")
	ErrApprovalInvalid      = errors.New("approval transition is invalid")
)

// OperationError lets adapters communicate a closed retry decision without
// leaking provider errors or SDK response types.
type OperationError struct {
	Disposition domain.FailureDisposition
	Code        string
	cause       error
}

func (failure *OperationError) Error() string {
	if failure == nil || failure.Code == "" {
		return "external operation failed"
	}
	return failure.Code
}

func (failure *OperationError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func NewOperationError(disposition domain.FailureDisposition, code string, cause error) error {
	return &OperationError{Disposition: disposition, Code: code, cause: cause}
}

func ClassifyOperationError(err error) domain.DeliveryFailure {
	var failure *OperationError
	if errors.As(err, &failure) && failure.Code != "" {
		return domain.DeliveryFailure{Disposition: failure.Disposition, ReasonCode: failure.Code}
	}
	return domain.DeliveryFailure{Disposition: domain.FailurePermanent, ReasonCode: "unclassified_external_failure"}
}

type DeliveryOperations interface {
	ListDeadDeliveries(ctx context.Context, limit int) ([]domain.DeadDelivery, error)
	ReplayDeadDelivery(ctx context.Context, deliveryID, operatorRef string, now time.Time) (domain.DeliveryReplayResult, error)
	ListDeliveryAttempts(ctx context.Context, deliveryID string, limit int) ([]domain.DeliveryAttempt, error)
}

type QueryQuotaStore interface {
	ReserveQueryQuota(ctx context.Context, reservation domain.QueryQuotaReservation, policy domain.TenantQuotaPolicy) error
	SettleQueryQuota(ctx context.Context, usageKey string, status domain.QuotaReservationStatus, actualAPICalls, actualBytes int64, reasonCode string, now time.Time) error
	GetTenantQuotaUsage(ctx context.Context, tenantID string, windowStart, windowEnd time.Time, policy domain.TenantQuotaPolicy) (domain.TenantQuotaUsage, error)
}

type ApprovalStore interface {
	CreateApproval(ctx context.Context, request domain.ApprovalRequest) error
	DecideApproval(ctx context.Context, approvalID string, decision domain.ApprovalStatus, actor domain.Principal, now time.Time) (domain.ApprovalRequest, error)
	ConsumeApproval(ctx context.Context, approvalID, payloadHash string, now time.Time) (domain.ApprovalRequest, error)
	GetApproval(ctx context.Context, approvalID string) (domain.ApprovalRequest, error)
}

func ValidateFailureDisposition(disposition domain.FailureDisposition) error {
	switch disposition {
	case domain.FailureRetryable, domain.FailurePermanent, domain.FailureOutcomeUnknown, domain.FailureCancelled:
		return nil
	default:
		return fmt.Errorf("unsupported failure disposition %q", disposition)
	}
}
