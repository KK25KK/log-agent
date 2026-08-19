package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const (
	currentQueryStep  = "sls.current"
	baselineQueryStep = "sls.baseline"
)

// CheckpointExecutor makes the two metered SLS observations recoverable. It
// sits outside the orchestration framework so the same durability contract is
// retained if Eino is upgraded or replaced.
type CheckpointExecutor struct {
	delegate GovernedSLSExecutor
	steps    ports.QueryStepStore
	now      func() time.Time
}

// GovernedSLSExecutor prevents a checkpoint from being placed in front of an
// executor that cannot independently resolve the current administrator-owned
// catalog, policy, schema and budget identity.
type GovernedSLSExecutor interface {
	ports.SLSExecutor
	ports.QueryGovernanceResolver
}

type checkpointInput struct {
	Spec                  domain.QuerySpec `json:"spec"`
	GovernanceFingerprint string           `json:"governance_fingerprint"`
}

func NewCheckpointExecutor(delegate GovernedSLSExecutor, steps ports.QueryStepStore, now func() time.Time) (*CheckpointExecutor, error) {
	if delegate == nil || steps == nil {
		return nil, errors.New("SLS delegate and query-step store are required")
	}
	if now == nil {
		return nil, errors.New("checkpoint clock is required")
	}
	return &CheckpointExecutor{delegate: delegate, steps: steps, now: now}, nil
}

func (e *CheckpointExecutor) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	job, ok := runJobFromContext(ctx)
	if !ok {
		return domain.QueryResult{}, errors.New("query checkpoint requires a claimed job context")
	}
	stepKey, err := validateCheckpointScope(job, spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	governanceFingerprint, err := e.delegate.ResolveQueryGovernance(ctx, spec)
	if err != nil {
		// Governance resolution never creates a query step. Policy, ACL,
		// catalog, schema and preflight denials are already audited by the
		// governed delegate and remain safe to retry explicitly.
		return domain.QueryResult{}, err
	}
	if !checkpointHash(governanceFingerprint) {
		return domain.QueryResult{}, errors.New("resolved query governance fingerprint is invalid")
	}
	inputHash, err := fingerprint.JSON(checkpointInput{Spec: spec, GovernanceFingerprint: governanceFingerprint})
	if err != nil {
		return domain.QueryResult{}, fmt.Errorf("fingerprint query checkpoint input: %w", err)
	}
	if inputHash == "" {
		return domain.QueryResult{}, errors.New("query checkpoint input hash is empty")
	}

	decision, err := e.steps.PrepareQueryStep(ctx, job, stepKey, inputHash, e.now().UTC())
	if err != nil {
		return domain.QueryResult{}, fmt.Errorf("prepare query checkpoint %s: %w", stepKey, err)
	}
	switch decision.Action {
	case domain.QueryStepReuse:
		if decision.Result == nil {
			return domain.QueryResult{}, fmt.Errorf("reuse query checkpoint %s: cached result is missing", stepKey)
		}
		result := cloneQueryResult(*decision.Result)
		if err := validateCheckpointResult(spec, governanceFingerprint, result); err != nil {
			return domain.QueryResult{}, fmt.Errorf("reuse query checkpoint %s: %w", stepKey, err)
		}
		return result, nil
	case domain.QueryStepExecute:
		if decision.Result != nil {
			return domain.QueryResult{}, fmt.Errorf("execute query checkpoint %s unexpectedly contains a cached result", stepKey)
		}
	default:
		return domain.QueryResult{}, fmt.Errorf("query checkpoint %s returned unsupported action %q", stepKey, decision.Action)
	}

	result, executeErr := e.delegate.Execute(ctx, spec)
	if executeErr != nil {
		// A process shutdown or lost lease intentionally leaves STARTED in place.
		// The next claimed attempt will turn that ambiguity into NEEDS_REVIEW;
		// this worker must not write a misleading terminal step meanwhile.
		if ctx.Err() != nil {
			return domain.QueryResult{}, executeErr
		}
		if reasonCode, deterministic := deterministicQueryFailure(executeErr); deterministic {
			if failErr := e.steps.FailQueryStep(ctx, job, stepKey, inputHash, reasonCode, e.now().UTC()); failErr != nil {
				return domain.QueryResult{}, fmt.Errorf("%w: persist deterministic checkpoint failure: %v", executeErr, failErr)
			}
			return domain.QueryResult{}, ports.NewQueryStepFailure(reasonCode, executeErr)
		}
		return domain.QueryResult{}, fmt.Errorf("%w: query step %s", ports.ErrExternalOutcomeUnknown, stepKey)
	}

	result = cloneQueryResult(result)
	if err := validateCheckpointResult(spec, governanceFingerprint, result); err != nil {
		// The delegate may already have consumed provider quota. An invalid
		// normalized envelope is therefore not safe to retry automatically.
		return domain.QueryResult{}, fmt.Errorf("%w: query step %s returned an invalid result", ports.ErrExternalOutcomeUnknown, stepKey)
	}
	if err := e.steps.CompleteQueryStep(ctx, job, stepKey, inputHash, result, e.now().UTC()); err != nil {
		if ctx.Err() != nil {
			return domain.QueryResult{}, ctx.Err()
		}
		return domain.QueryResult{}, fmt.Errorf("%w: persist query step %s result", ports.ErrExternalOutcomeUnknown, stepKey)
	}
	return cloneQueryResult(result), nil
}

func validateCheckpointScope(job domain.Job, spec domain.QuerySpec) (string, error) {
	if job.ID == "" || job.InvestigationID == "" || job.Attempt <= 0 || job.LeaseOwner == "" {
		return "", errors.New("query checkpoint job context is incomplete")
	}
	if spec.InvestigationID == "" || spec.InvestigationID != job.InvestigationID {
		return "", errors.New("query checkpoint investigation does not match the claimed job")
	}
	if spec.Service != job.Request.Service || spec.Environment != job.Request.Environment || spec.Requester != job.Request.Requester {
		return "", errors.New("query checkpoint scope does not match the claimed job request")
	}
	if spec.TemplateID != domain.ErrorAnalysisTemplateID {
		return "", errors.New("query checkpoint template is not the fixed error-analysis template")
	}
	duration := job.Request.EndTime.Sub(job.Request.StartTime)
	if duration <= 0 {
		return "", errors.New("query checkpoint job window is invalid")
	}
	switch spec.Name {
	case "current":
		if !spec.StartTime.Equal(job.Request.StartTime) || !spec.EndTime.Equal(job.Request.EndTime) {
			return "", errors.New("current query checkpoint window does not match the claimed job")
		}
		return currentQueryStep, nil
	case "baseline":
		if !spec.StartTime.Equal(job.Request.StartTime.Add(-duration)) || !spec.EndTime.Equal(job.Request.StartTime) {
			return "", errors.New("baseline query checkpoint window is not adjacent to the claimed job")
		}
		return baselineQueryStep, nil
	default:
		return "", fmt.Errorf("query checkpoint name %q is not allowed", spec.Name)
	}
}

func deterministicQueryFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, ports.ErrQueryDenied):
		return "query_denied", true
	case errors.Is(err, ports.ErrQueryBudgetExceeded):
		return "query_budget_exceeded", true
	case errors.Is(err, ports.ErrInvalidQuerySchema):
		return "invalid_query_schema", true
	default:
		return "", false
	}
}

func validateCheckpointResult(spec domain.QuerySpec, governanceFingerprint string, result domain.QueryResult) error {
	if !checkpointBoundedString(result.QueryID, 1, 4096) || !checkpointHash(result.QuerySpecHash) {
		return errors.New("cached query identity or hash is missing")
	}
	if !checkpointBoundedString(result.ResourceID, 1, 256) || result.TemplateID != spec.TemplateID ||
		!checkpointBoundedString(result.TemplateVersion, 1, 256) || !checkpointBoundedString(result.SchemaFingerprint, 1, 256) ||
		!checkpointBoundedString(result.PolicyVersion, 1, 256) || !checkpointBoundedString(result.Progress, 1, 128) ||
		!checkpointBoundedString(result.IncompleteReason, 0, 2048) {
		return errors.New("cached query governance metadata is incomplete")
	}
	if !checkpointHash(result.GovernanceFingerprint) || result.GovernanceFingerprint != governanceFingerprint {
		return errors.New("cached query governance fingerprint does not match the current resolution")
	}
	if result.ProcessedRows < 0 || result.ProcessedBytes < 0 || result.ElapsedMillisecond < 0 ||
		result.ErrorCount < 0 || result.TopErrorCount < 0 || result.TopErrorCount > result.ErrorCount {
		return errors.New("cached query counts are inconsistent")
	}
	if result.APICalls != domain.ErrorAnalysisAPICalls || result.PatternLimit != domain.ErrorAnalysisPatternLimit || result.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
		return errors.New("cached query fixed-template limits are inconsistent")
	}
	patternTotal, err := validateCheckpointBuckets(result.ErrorPatterns, result.PatternLimit, result.ErrorCount)
	if err != nil {
		return fmt.Errorf("cached error patterns: %w", err)
	}
	instanceTotal, err := validateCheckpointBuckets(result.Instances, result.InstanceLimit, result.ErrorCount)
	if err != nil {
		return fmt.Errorf("cached instances: %w", err)
	}
	if result.ErrorCount > 0 && (len(result.ErrorPatterns) == 0 || len(result.Instances) == 0) {
		return errors.New("cached non-zero result lacks aggregate buckets")
	}
	if (result.ErrorPatternsExhaustive && patternTotal != result.ErrorCount) || (result.InstancesExhaustive && instanceTotal != result.ErrorCount) {
		return errors.New("cached aggregate exhaustiveness marker is inconsistent")
	}
	if result.Complete && !result.Truncated && (result.ErrorPatternsExhaustive != (patternTotal == result.ErrorCount) || result.InstancesExhaustive != (instanceTotal == result.ErrorCount)) {
		return errors.New("cached complete result has inconsistent exhaustiveness")
	}
	if len(result.ErrorPatterns) == 0 {
		if result.TopError != "" || result.TopErrorCount != 0 {
			return errors.New("cached top error has no corresponding pattern")
		}
	} else if result.TopError != result.ErrorPatterns[0].Label || result.TopErrorCount != result.ErrorPatterns[0].Count {
		return errors.New("cached top error does not match the first pattern")
	}
	if result.Complete {
		if result.Truncated || !result.UsageKnown || !strings.EqualFold(result.Progress, "complete") || result.IncompleteReason != "" {
			return errors.New("cached complete result markers are inconsistent")
		}
	} else if result.IncompleteReason == "" {
		return errors.New("cached incomplete result lacks a reason")
	}
	return nil
}

func validateCheckpointBuckets(buckets []domain.CountBucket, limit int, total int64) (int64, error) {
	if len(buckets) > limit {
		return 0, errors.New("bucket limit exceeded")
	}
	labels := make(map[string]struct{}, len(buckets))
	var sum int64
	for _, bucket := range buckets {
		if !checkpointBoundedString(bucket.Label, 1, 512) || bucket.Count <= 0 || bucket.Count > total-sum {
			return 0, errors.New("bucket label or count is invalid")
		}
		if _, duplicate := labels[bucket.Label]; duplicate {
			return 0, errors.New("bucket labels are duplicated")
		}
		labels[bucket.Label] = struct{}{}
		sum += bucket.Count
	}
	return sum, nil
}

func checkpointBoundedString(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func checkpointHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneQueryResult(result domain.QueryResult) domain.QueryResult {
	result.ErrorPatterns = append([]domain.CountBucket(nil), result.ErrorPatterns...)
	result.Instances = append([]domain.CountBucket(nil), result.Instances...)
	return result
}

var _ ports.SLSExecutor = (*CheckpointExecutor)(nil)
