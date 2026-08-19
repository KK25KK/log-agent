package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type checkpointDelegate struct {
	mu            sync.Mutex
	calls         int
	resolveCalls  int
	governance    string
	governanceErr error
	execute       func(context.Context, domain.QuerySpec) (domain.QueryResult, error)
}

func (d *checkpointDelegate) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return d.execute(ctx, spec)
}

func (d *checkpointDelegate) ResolveQueryGovernance(_ context.Context, _ domain.QuerySpec) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolveCalls++
	if d.governanceErr != nil {
		return "", d.governanceErr
	}
	if d.governance == "" {
		return strings.Repeat("b", 64), nil
	}
	return d.governance, nil
}

func (d *checkpointDelegate) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type preparedCheckpoint struct {
	job       domain.Job
	stepKey   string
	inputHash string
}

type completedCheckpoint struct {
	preparedCheckpoint
	result domain.QueryResult
}

type failedCheckpoint struct {
	preparedCheckpoint
	reasonCode string
}

type checkpointStore struct {
	decision domain.QueryStepDecision
	prepare  []preparedCheckpoint
	complete []completedCheckpoint
	failed   []failedCheckpoint
}

func (s *checkpointStore) PrepareQueryStep(_ context.Context, job domain.Job, stepKey, inputHash string, _ time.Time) (domain.QueryStepDecision, error) {
	s.prepare = append(s.prepare, preparedCheckpoint{job: job, stepKey: stepKey, inputHash: inputHash})
	return s.decision, nil
}

func (s *checkpointStore) CompleteQueryStep(_ context.Context, job domain.Job, stepKey, inputHash string, result domain.QueryResult, _ time.Time) error {
	s.complete = append(s.complete, completedCheckpoint{
		preparedCheckpoint: preparedCheckpoint{job: job, stepKey: stepKey, inputHash: inputHash},
		result:             cloneQueryResult(result),
	})
	return nil
}

func (s *checkpointStore) FailQueryStep(_ context.Context, job domain.Job, stepKey, inputHash, reasonCode string, _ time.Time) error {
	s.failed = append(s.failed, failedCheckpoint{
		preparedCheckpoint: preparedCheckpoint{job: job, stepKey: stepKey, inputHash: inputHash},
		reasonCode:         reasonCode,
	})
	return nil
}

func TestCheckpointExecutorReusesValidatedDeepCopyWithoutDelegate(t *testing.T) {
	job := checkpointJob()
	spec := checkpointSpec(job, "current")
	cached := checkpointResult(spec, "cached")
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepReuse, Result: &cached}}
	delegate := &checkpointDelegate{execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
		t.Fatal("delegate was called for a reusable checkpoint")
		return domain.QueryResult{}, nil
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	result, err := executor.Execute(withRunJob(context.Background(), job), spec)
	if err != nil {
		t.Fatal(err)
	}
	if delegate.callCount() != 0 || result.QueryID != cached.QueryID {
		t.Fatalf("unexpected reuse result: calls=%d result=%+v", delegate.callCount(), result)
	}
	result.ErrorPatterns[0].Label = "mutated"
	result.Instances[0].Label = "mutated"
	if cached.ErrorPatterns[0].Label == "mutated" || cached.Instances[0].Label == "mutated" {
		t.Fatal("cached result slices were returned without a defensive copy")
	}
}

func TestCheckpointExecutorCompletesCurrentAndBaseline(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	delegate := &checkpointDelegate{execute: func(_ context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
		return checkpointResult(spec, spec.Name), nil
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)
	ctx := withRunJob(context.Background(), job)

	for _, name := range []string{"current", "baseline"} {
		if _, err := executor.Execute(ctx, checkpointSpec(job, name)); err != nil {
			t.Fatalf("execute %s checkpoint: %v", name, err)
		}
	}
	if delegate.callCount() != 2 || len(steps.complete) != 2 {
		t.Fatalf("two steps were not completed: calls=%d complete=%d", delegate.callCount(), len(steps.complete))
	}
	if steps.complete[0].stepKey != currentQueryStep || steps.complete[1].stepKey != baselineQueryStep {
		t.Fatalf("unexpected step keys: %#v", steps.complete)
	}
	for index := range steps.complete {
		if steps.complete[index].inputHash == "" || steps.complete[index].inputHash != steps.prepare[index].inputHash {
			t.Fatalf("input fingerprint was not stable: prepare=%+v complete=%+v", steps.prepare[index], steps.complete[index])
		}
	}
}

func TestCheckpointExecutorAcceptsGatewayBoundedUnicodeLabels(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	label := strings.Repeat("错", 199) + "🔥"
	delegate := &checkpointDelegate{execute: func(_ context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
		result := checkpointResult(spec, "unicode")
		result.TopError = label
		result.ErrorPatterns[0].Label = label
		return result, nil
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	result, err := executor.Execute(withRunJob(context.Background(), job), checkpointSpec(job, "current"))
	if err != nil {
		t.Fatalf("gateway-bounded Unicode result was rejected: %v", err)
	}
	if result.TopError != label || len(steps.complete) != 1 {
		t.Fatalf("Unicode checkpoint was not completed: result=%+v complete=%d", result, len(steps.complete))
	}
}

func TestCheckpointExecutorFailsClosedOnContextScopeAndCachedHash(t *testing.T) {
	job := checkpointJob()
	validSpec := checkpointSpec(job, "current")
	validResult := checkpointResult(validSpec, "cached")
	tests := []struct {
		name     string
		ctx      context.Context
		spec     domain.QuerySpec
		decision domain.QueryStepDecision
	}{
		{name: "missing claimed job", ctx: context.Background(), spec: validSpec, decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}},
		{name: "unknown step name", ctx: withRunJob(context.Background(), job), spec: func() domain.QuerySpec { value := validSpec; value.Name = "ad-hoc"; return value }(), decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}},
		{name: "different investigation", ctx: withRunJob(context.Background(), job), spec: func() domain.QuerySpec { value := validSpec; value.InvestigationID = "inv-other"; return value }(), decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}},
		{name: "missing cached hash", ctx: withRunJob(context.Background(), job), spec: validSpec, decision: domain.QueryStepDecision{Action: domain.QueryStepReuse, Result: func() *domain.QueryResult { value := validResult; value.QuerySpecHash = ""; return &value }()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := &checkpointStore{decision: test.decision}
			delegate := &checkpointDelegate{execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
				t.Fatal("delegate reached after checkpoint validation failed")
				return domain.QueryResult{}, nil
			}}
			executor := newTestCheckpointExecutor(t, delegate, steps)
			if _, err := executor.Execute(test.ctx, test.spec); err == nil {
				t.Fatal("invalid checkpoint request was accepted")
			}
			if delegate.callCount() != 0 {
				t.Fatalf("delegate calls=%d, want 0", delegate.callCount())
			}
		})
	}
}

func TestCheckpointExecutorRecordsOnlyDeterministicPreProviderFailure(t *testing.T) {
	job := checkpointJob()
	spec := checkpointSpec(job, "current")
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	delegate := &checkpointDelegate{execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
		return domain.QueryResult{}, errors.Join(errors.New("budget policy"), ports.ErrQueryBudgetExceeded)
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	_, err := executor.Execute(withRunJob(context.Background(), job), spec)
	if !errors.Is(err, ports.ErrQueryBudgetExceeded) {
		t.Fatalf("want original budget error, got %v", err)
	}
	if len(steps.failed) != 1 || steps.failed[0].reasonCode != "query_budget_exceeded" {
		t.Fatalf("deterministic failure not checkpointed: %#v", steps.failed)
	}
	if code, ok := ports.QueryStepFailureCode(err); !ok || code != "query_budget_exceeded" {
		t.Fatalf("stable query-step failure code was lost: code=%q ok=%v err=%v", code, ok, err)
	}
}

func TestCheckpointExecutorDoesNotCreateStepWhenGovernanceResolutionFails(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	wantErr := errors.Join(errors.New("catalog denied"), ports.ErrQueryDenied)
	delegate := &checkpointDelegate{
		governanceErr: wantErr,
		execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
			t.Fatal("query execution reached after governance denial")
			return domain.QueryResult{}, nil
		},
	}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	_, err := executor.Execute(withRunJob(context.Background(), job), checkpointSpec(job, "current"))
	if !errors.Is(err, ports.ErrQueryDenied) {
		t.Fatalf("want governance denial, got %v", err)
	}
	if len(steps.prepare) != 0 || delegate.callCount() != 0 {
		t.Fatalf("governance denial created or executed a step: prepare=%d execute=%d", len(steps.prepare), delegate.callCount())
	}
}

func TestCheckpointExecutorRejectsCachedGovernanceDriftWithoutQueryExecution(t *testing.T) {
	job := checkpointJob()
	spec := checkpointSpec(job, "current")
	cached := checkpointResult(spec, "cached")
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepReuse, Result: &cached}}
	delegate := &checkpointDelegate{
		governance: strings.Repeat("c", 64),
		execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
			t.Fatal("query execution reached after cached governance drift")
			return domain.QueryResult{}, nil
		},
	}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	if _, err := executor.Execute(withRunJob(context.Background(), job), spec); err == nil {
		t.Fatal("cached result from old governance was reused")
	}
	if delegate.callCount() != 0 {
		t.Fatalf("query calls=%d, want 0", delegate.callCount())
	}
}

func TestCheckpointExecutorDoesNotCheckpointResultFromChangedGovernance(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	delegate := &checkpointDelegate{
		governance: strings.Repeat("c", 64),
		execute: func(_ context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
			// Model a catalog/schema change between preflight resolution and the
			// provider result returned by Execute.
			return checkpointResult(spec, "old-governance"), nil
		},
	}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	_, err := executor.Execute(withRunJob(context.Background(), job), checkpointSpec(job, "current"))
	if !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("want unknown outcome after mid-flight governance drift, got %v", err)
	}
	if len(steps.complete) != 0 || len(steps.failed) != 0 {
		t.Fatalf("drifted result became a terminal checkpoint: complete=%#v failed=%#v", steps.complete, steps.failed)
	}
}

func TestCheckpointExecutorTreatsProviderErrorAsUnknown(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	delegate := &checkpointDelegate{execute: func(context.Context, domain.QuerySpec) (domain.QueryResult, error) {
		return domain.QueryResult{}, errors.New("provider connection reset after write")
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	_, err := executor.Execute(withRunJob(context.Background(), job), checkpointSpec(job, "current"))
	if !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("want unknown external outcome, got %v", err)
	}
	if len(steps.failed) != 0 || len(steps.complete) != 0 {
		t.Fatalf("ambiguous result was written as terminal: failed=%#v complete=%#v", steps.failed, steps.complete)
	}
	if strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("raw provider error escaped the conservative boundary: %v", err)
	}
}

func TestCheckpointExecutorCancellationLeavesStepStarted(t *testing.T) {
	job := checkpointJob()
	steps := &checkpointStore{decision: domain.QueryStepDecision{Action: domain.QueryStepExecute}}
	ctx, cancel := context.WithCancel(withRunJob(context.Background(), job))
	delegate := &checkpointDelegate{execute: func(ctx context.Context, _ domain.QuerySpec) (domain.QueryResult, error) {
		cancel()
		return domain.QueryResult{}, ctx.Err()
	}}
	executor := newTestCheckpointExecutor(t, delegate, steps)

	_, err := executor.Execute(ctx, checkpointSpec(job, "current"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if len(steps.failed) != 0 || len(steps.complete) != 0 {
		t.Fatalf("cancelled step was written as terminal: failed=%#v complete=%#v", steps.failed, steps.complete)
	}
}

type needsReviewStore struct {
	job             domain.Job
	claimed         bool
	needsReview     bool
	failure         bool
	failureCause    string
	finishCtxActive bool
}

func (s *needsReviewStore) AcceptOnce(context.Context, domain.InboundMessage, domain.InvestigationRequest, string, string) (string, bool, error) {
	return "", false, errors.New("unused")
}

func (s *needsReviewStore) ClaimNext(context.Context, string, time.Time, time.Duration) (domain.Job, bool, error) {
	if s.claimed {
		return domain.Job{}, false, nil
	}
	s.claimed = true
	return s.job, true, nil
}

func (*needsReviewStore) RenewLease(context.Context, domain.Job, time.Time, time.Duration) error {
	return nil
}

func (*needsReviewStore) FinishSuccess(context.Context, domain.Job, []domain.Evidence, domain.Report, time.Time) error {
	return errors.New("unexpected success")
}

func (s *needsReviewStore) FinishFailure(_ context.Context, _ domain.Job, cause string, _ time.Time) error {
	s.failure = true
	s.failureCause = cause
	return nil
}

func (s *needsReviewStore) FinishNeedsReview(ctx context.Context, _ domain.Job, reasonCode string, _ time.Time) error {
	s.needsReview = reasonCode == "external_query_outcome_unknown"
	s.finishCtxActive = ctx.Err() == nil
	return nil
}

func (*needsReviewStore) RequestCancel(context.Context, string, time.Time) error { return nil }

func (*needsReviewStore) GetInvestigation(context.Context, string) (domain.Investigation, error) {
	return domain.Investigation{}, ports.ErrNotFound
}

type needsReviewEngine struct {
	waitForCancellation bool
}

func (e needsReviewEngine) Run(ctx context.Context, _ string, _ domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	if e.waitForCancellation {
		<-ctx.Done()
	}
	return nil, domain.Report{}, ports.ErrExternalOutcomeUnknown
}

func TestWorkerPersistsUnknownExternalOutcomeAsNeedsReview(t *testing.T) {
	job := checkpointJob()
	store := &needsReviewStore{job: job}
	worker, err := NewWorker(store, needsReviewEngine{}, "worker-checkpoint", time.Minute, WithWorkerClock(func() time.Time {
		return job.LeaseUntil.Add(-30 * time.Second)
	}))
	if err != nil {
		t.Fatal(err)
	}

	ran, err := worker.RunOne(context.Background())
	if !ran || !errors.Is(err, ports.ErrExternalOutcomeUnknown) {
		t.Fatalf("want recognizable review error, ran=%v err=%v", ran, err)
	}
	if !store.needsReview || !store.finishCtxActive || store.failure {
		t.Fatalf("unknown outcome used wrong terminal path: %+v", store)
	}
}

type fixedFailureEngine struct {
	err error
}

func (e fixedFailureEngine) Run(context.Context, string, domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	return nil, domain.Report{}, e.err
}

func TestWorkerPersistsStableQueryStepFailureCode(t *testing.T) {
	job := checkpointJob()
	store := &needsReviewStore{job: job}
	worker, err := NewWorker(store, fixedFailureEngine{err: ports.NewQueryStepFailure("query_budget_exceeded", errors.New("sensitive provider detail"))}, "worker-checkpoint", time.Minute, WithWorkerClock(func() time.Time {
		return job.LeaseUntil.Add(-30 * time.Second)
	}))
	if err != nil {
		t.Fatal(err)
	}

	ran, runErr := worker.RunOne(context.Background())
	if !ran || runErr == nil {
		t.Fatalf("want deterministic failure, ran=%v err=%v", ran, runErr)
	}
	if !store.failure || store.needsReview || store.failureCause != "query_budget_exceeded" {
		t.Fatalf("worker persisted an unsafe failure cause: %+v", store)
	}
}

func TestWorkerShutdownDoesNotPersistNeedsReview(t *testing.T) {
	job := checkpointJob()
	store := &needsReviewStore{job: job}
	worker, err := NewWorker(store, needsReviewEngine{waitForCancellation: true}, "worker-checkpoint", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := worker.RunOne(ctx)
		done <- runErr
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("want shutdown cancellation, got %v", err)
	}
	if store.needsReview || store.failure {
		t.Fatalf("shutdown wrote a false terminal state: %+v", store)
	}
}

func newTestCheckpointExecutor(t *testing.T, delegate GovernedSLSExecutor, steps ports.QueryStepStore) *CheckpointExecutor {
	t.Helper()
	executor, err := NewCheckpointExecutor(delegate, steps, func() time.Time {
		return time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func checkpointJob() domain.Job {
	start := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	return domain.Job{
		ID: "job-checkpoint", InvestigationID: "inv-checkpoint", Attempt: 1,
		LeaseOwner: "worker-checkpoint", LeaseUntil: start.Add(2 * time.Hour),
		Request: domain.InvestigationRequest{
			Service: "order-service", Environment: "prod", StartTime: start, EndTime: start.Add(30 * time.Minute),
			Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
		},
	}
}

func checkpointSpec(job domain.Job, name string) domain.QuerySpec {
	start, end := job.Request.StartTime, job.Request.EndTime
	if name == "baseline" {
		end = start
		start = start.Add(-job.Request.EndTime.Sub(job.Request.StartTime))
	}
	return domain.QuerySpec{
		InvestigationID: job.InvestigationID, Name: name, TemplateID: domain.ErrorAnalysisTemplateID,
		Service: job.Request.Service, Environment: job.Request.Environment,
		StartTime: start, EndTime: end, Requester: job.Request.Requester,
	}
}

func checkpointResult(spec domain.QuerySpec, token string) domain.QueryResult {
	return domain.QueryResult{
		QueryID: "query-" + token, QuerySpecHash: strings.Repeat("a", 64),
		ResourceID: "resource-order-prod", TemplateID: spec.TemplateID, TemplateVersion: "template-v2",
		SchemaFingerprint: "schema-v2", PolicyVersion: "policy-v2", GovernanceFingerprint: strings.Repeat("b", 64),
		Progress: "Complete", Complete: true,
		UsageKnown: true, APICalls: domain.ErrorAnalysisAPICalls, ErrorCount: 2,
		TopError: "timeout", TopErrorCount: 2,
		ErrorPatterns:           []domain.CountBucket{{Label: "timeout", Count: 2}},
		Instances:               []domain.CountBucket{{Label: "pod-a", Count: 2}},
		ErrorPatternsExhaustive: true, InstancesExhaustive: true,
		PatternLimit: domain.ErrorAnalysisPatternLimit, InstanceLimit: domain.ErrorAnalysisInstanceLimit,
	}
}
