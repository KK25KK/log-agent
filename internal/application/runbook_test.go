package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

type runbookSourceFunc func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error)

func (function runbookSourceFunc) Lookup(ctx context.Context, query domain.RunbookQuery) (domain.RunbookSet, error) {
	return function(ctx, query)
}

type runbookTestCatalog struct {
	resource   domain.LogResource
	resolveErr error
	allowed    bool
}

func (catalog *runbookTestCatalog) Resolve(context.Context, string, string) (domain.LogResource, error) {
	return catalog.resource, catalog.resolveErr
}

func (catalog *runbookTestCatalog) Allowed(context.Context, domain.Principal, string) bool {
	return catalog.allowed
}

func TestRunbookServiceBuildsCompleteGroundedGuidance(t *testing.T) {
	evidence, report := runbookServiceFixture()
	report.Recommendations = []domain.Recommendation{
		{Code: "inspect_top_error_pattern", Statement: "核对错误模式。", EvidenceIDs: []string{"ev-current", "ev-baseline"}},
		{Code: "inspect_hot_instance", Statement: "观察高频实例。", EvidenceIDs: []string{"ev-current"}},
	}
	var captured domain.RunbookQuery
	calls := 0
	entries := []domain.RunbookEntry{
		runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_hot_instance", "inspect_top_error_pattern"}),
		runbookServiceEntry("runbook-b", "rev-2", []string{"inspect_hot_instance"}),
	}
	service := mustRunbookService(t, runbookSourceFunc(func(_ context.Context, query domain.RunbookQuery) (domain.RunbookSet, error) {
		calls++
		captured = query
		return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: entries, Complete: true}, nil
	}))

	result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("source calls=%d want 1", calls)
	}
	wantQuery := domain.RunbookQuery{
		ResourceID:          "resource-order-prod",
		RecommendationCodes: []string{"inspect_hot_instance", "inspect_top_error_pattern"},
		Limit:               domain.MaxRunbookEntries,
	}
	if !reflect.DeepEqual(captured, wantQuery) {
		t.Fatalf("query=%#v want %#v", captured, wantQuery)
	}
	guidance := result.RunbookGuidance
	if guidance == nil || guidance.Status != domain.RunbookGuidanceComplete || guidance.MethodVersion != domain.RunbookGuidanceVersion ||
		guidance.DataSource != domain.RunbookGuidanceSourceSyntheticMock || guidance.SourceVersion != "mock-runbook-v1" ||
		!guidance.SourceComplete || guidance.SourceTruncated || len(guidance.Items) != 2 {
		t.Fatalf("unexpected complete guidance: %#v", guidance)
	}
	if guidance.Items[0].EntryID != "runbook-a" || guidance.Items[1].EntryID != "runbook-b" {
		t.Fatalf("guidance items are not stably ordered: %#v", guidance.Items)
	}
	if !reflect.DeepEqual(guidance.Items[0].EvidenceIDs, []string{"ev-baseline", "ev-current"}) ||
		!reflect.DeepEqual(guidance.Items[1].EvidenceIDs, []string{"ev-current"}) {
		t.Fatalf("guidance evidence union is not sorted and grounded: %#v", guidance.Items)
	}
	for index, item := range guidance.Items {
		fingerprint, fingerprintErr := domain.RunbookEntryFingerprint(entries[index])
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		if item.Fingerprint != fingerprint || item.ExecutionMode != domain.RunbookExecutionHumanReviewOnly {
			t.Fatalf("item %d fingerprint/mode mismatch: %#v", index, item)
		}
	}
	assertRunbookReportPreserved(t, report, result)
	assertRunbookGuidanceValid(t, evidence, result)
}

func TestRunbookServiceMapsSourceCoverageStates(t *testing.T) {
	tests := []struct {
		name        string
		set         domain.RunbookSet
		wantStatus  domain.RunbookGuidanceStatus
		wantMissing []string
		wantItems   int
	}{
		{
			name: "complete without match is no match",
			set: domain.RunbookSet{
				SourceVersion: "mock-runbook-v1", Complete: true,
			},
			wantStatus: domain.RunbookGuidanceNoMatch,
		},
		{
			name: "incomplete without returned match is inconclusive",
			set: domain.RunbookSet{
				SourceVersion: "mock-runbook-v1", Complete: false, ReasonCode: domain.RunbookReasonIncomplete,
			},
			wantStatus:  domain.RunbookGuidanceInconclusive,
			wantMissing: []string{runbookMissingCompleteSet, runbookMissingMatch},
		},
		{
			name: "truncated result with a match is inconclusive",
			set: domain.RunbookSet{
				SourceVersion: "mock-runbook-v1", Complete: false, Truncated: true, ReasonCode: domain.RunbookReasonTruncated,
				Entries: []domain.RunbookEntry{runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})},
			},
			wantStatus:  domain.RunbookGuidanceInconclusive,
			wantMissing: []string{runbookMissingCompleteSet, runbookMissingUntruncatedSet},
			wantItems:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, report := runbookServiceFixture()
			calls := 0
			service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				calls++
				return test.set, nil
			}))

			result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
			if err != nil {
				t.Fatal(err)
			}
			guidance := result.RunbookGuidance
			if calls != 1 || guidance == nil || guidance.Status != test.wantStatus || len(guidance.Items) != test.wantItems ||
				!reflect.DeepEqual(guidance.MissingInputs, test.wantMissing) {
				t.Fatalf("calls=%d guidance=%#v", calls, guidance)
			}
			assertRunbookReportPreserved(t, report, result)
			assertRunbookGuidanceValid(t, evidence, result)
		})
	}
}

func TestRunbookServiceDegradesOptionalSourceFailures(t *testing.T) {
	tests := []struct {
		name        string
		lookup      runbookSourceFunc
		wantMissing string
	}{
		{
			name: "source error",
			lookup: func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				return domain.RunbookSet{}, errors.New("provider unavailable with secret detail")
			},
			wantMissing: runbookMissingSourceAvailable,
		},
		{
			name: "disabled source",
			lookup: func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				return domain.RunbookSet{Complete: false, ReasonCode: domain.RunbookReasonDisabled}, nil
			},
			wantMissing: runbookMissingSourceDisabled,
		},
		{
			name: "invalid source set",
			lookup: func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				entry := runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})
				entry.ResourceID = "different-resource"
				return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: []domain.RunbookEntry{entry}, Complete: true}, nil
			},
			wantMissing: runbookMissingValidSet,
		},
		{
			name: "unserializable source update time",
			lookup: func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				entry := runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})
				entry.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: []domain.RunbookEntry{entry}, Complete: true}, nil
			},
			wantMissing: runbookMissingValidSet,
		},
		{
			name: "source update time after report clock skew",
			lookup: func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				entry := runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})
				entry.UpdatedAt = time.Date(2026, 8, 24, 8, 6, 0, 0, time.UTC)
				return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: []domain.RunbookEntry{entry}, Complete: true}, nil
			},
			wantMissing: runbookMissingValidSet,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, report := runbookServiceFixture()
			calls := 0
			service := mustRunbookService(t, runbookSourceFunc(func(ctx context.Context, query domain.RunbookQuery) (domain.RunbookSet, error) {
				calls++
				return test.lookup(ctx, query)
			}))

			result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
			if err != nil {
				t.Fatal(err)
			}
			guidance := result.RunbookGuidance
			if calls != 1 || guidance == nil || guidance.Status != domain.RunbookGuidanceUnavailable ||
				!reflect.DeepEqual(guidance.MissingInputs, []string{test.wantMissing}) || guidance.SourceVersion != "" || len(guidance.Items) != 0 {
				t.Fatalf("calls=%d guidance=%#v", calls, guidance)
			}
			assertRunbookReportPreserved(t, report, result)
			assertRunbookGuidanceValid(t, evidence, result)
		})
	}
}

func TestRunbookServiceDoesNotCallSourceWithoutGovernedTrigger(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func([]domain.Evidence, *domain.Report)
		wantStatus  domain.RunbookGuidanceStatus
		wantMissing []string
	}{
		{
			name: "no conclusive spike",
			mutate: func(_ []domain.Evidence, report *domain.Report) {
				report.Outcome = "no_significant_spike"
				report.Findings[0].Conclusive = false
			},
			wantStatus: domain.RunbookGuidanceSkippedNoTrigger,
		},
		{
			name: "current and baseline resource mismatch",
			mutate: func(evidence []domain.Evidence, _ *domain.Report) {
				evidence[1].ResourceID = "resource-other-prod"
			},
			wantStatus:  domain.RunbookGuidanceUnavailable,
			wantMissing: []string{runbookMissingResourceIdentity},
		},
		{
			name: "zero baseline remains insufficient",
			mutate: func(evidence []domain.Evidence, _ *domain.Report) {
				evidence[1].ErrorCount = 0
				evidence[1].TopError = ""
				evidence[1].TopErrorCount = 0
				evidence[1].ErrorPatterns = nil
				evidence[1].Instances = nil
				evidence[1].ErrorPatternsExhaustive = true
				evidence[1].InstancesExhaustive = true
			},
			wantStatus:  domain.RunbookGuidanceUnavailable,
			wantMissing: []string{runbookMissingDeterministicAdvice},
		},
		{
			name: "governance fingerprint is absent",
			mutate: func(evidence []domain.Evidence, _ *domain.Report) {
				evidence[0].GovernanceFingerprint = ""
			},
			wantStatus:  domain.RunbookGuidanceUnavailable,
			wantMissing: []string{runbookMissingResourceIdentity},
		},
		{
			name: "fixed template identity is absent",
			mutate: func(evidence []domain.Evidence, _ *domain.Report) {
				evidence[0].TemplateID = ""
			},
			wantStatus:  domain.RunbookGuidanceUnavailable,
			wantMissing: []string{runbookMissingResourceIdentity},
		},
		{
			name: "baseline and current windows are not adjacent",
			mutate: func(evidence []domain.Evidence, _ *domain.Report) {
				evidence[1].EndTime = evidence[1].EndTime.Add(-time.Minute)
			},
			wantStatus:  domain.RunbookGuidanceUnavailable,
			wantMissing: []string{runbookMissingResourceIdentity},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, report := runbookServiceFixture()
			test.mutate(evidence, &report)
			calls := 0
			service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				calls++
				return domain.RunbookSet{}, nil
			}))

			result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || result.RunbookGuidance == nil || result.RunbookGuidance.Status != test.wantStatus ||
				!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, test.wantMissing) {
				t.Fatalf("calls=%d guidance=%#v", calls, result.RunbookGuidance)
			}
			assertRunbookReportPreserved(t, report, result)
			assertRunbookGuidanceValid(t, evidence, result)
		})
	}
}

func TestRunbookServiceBindsLookupToTrustedCatalogScopeAndACL(t *testing.T) {
	tests := []struct {
		name       string
		context    func() context.Context
		mutate     func([]domain.Evidence)
		catalog    *runbookTestCatalog
		wantReason string
	}{
		{
			name:    "both evidence records forge another resource",
			context: func() context.Context { return runbookServiceContext(context.Background()) },
			mutate: func(evidence []domain.Evidence) {
				evidence[0].ResourceID = "resource-payment-prod"
				evidence[1].ResourceID = "resource-payment-prod"
			},
			catalog:    defaultRunbookTestCatalog(),
			wantReason: runbookMissingResourceIdentity,
		},
		{
			name:       "requester is not authorized",
			context:    func() context.Context { return runbookServiceContext(context.Background()) },
			mutate:     func([]domain.Evidence) {},
			catalog:    &runbookTestCatalog{resource: defaultRunbookTestCatalog().resource, allowed: false},
			wantReason: runbookMissingResourceIdentity,
		},
		{
			name:       "claimed job context is absent",
			context:    context.Background,
			mutate:     func([]domain.Evidence) {},
			catalog:    defaultRunbookTestCatalog(),
			wantReason: runbookMissingResourceIdentity,
		},
		{
			name: "evidence window does not match trusted request",
			context: func() context.Context {
				now := runbookTestClock()
				return withRunJob(context.Background(), domain.Job{Request: domain.InvestigationRequest{
					Service: "order-service", Environment: "prod",
					StartTime: now.Add(-29 * time.Minute), EndTime: now,
					Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
				}})
			},
			mutate:     func([]domain.Evidence) {},
			catalog:    defaultRunbookTestCatalog(),
			wantReason: runbookMissingResourceIdentity,
		},
		{
			name:       "catalog lookup fails with a healthy parent context",
			context:    func() context.Context { return runbookServiceContext(context.Background()) },
			mutate:     func([]domain.Evidence) {},
			catalog:    &runbookTestCatalog{resolveErr: context.DeadlineExceeded, allowed: true},
			wantReason: runbookMissingResourceIdentity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, report := runbookServiceFixture()
			test.mutate(evidence)
			calls := 0
			service, err := NewRunbookService(runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				calls++
				return domain.RunbookSet{}, nil
			}), test.catalog, domain.RunbookGuidanceSourceSyntheticMock, WithRunbookClock(runbookTestClock))
			if err != nil {
				t.Fatal(err)
			}

			result, err := service.Enrich(test.context(), evidence, report)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
				!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{test.wantReason}) {
				t.Fatalf("calls=%d guidance=%#v", calls, result.RunbookGuidance)
			}
			assertRunbookReportPreserved(t, report, result)
			assertRunbookGuidanceValid(t, evidence, result)
		})
	}
}

func TestRunbookServiceRecomputesClosedDeterministicRecommendationCodes(t *testing.T) {
	evidence, report := runbookServiceFixture()
	report.Recommendations = []domain.Recommendation{{
		Code: "compare_recent_changes", Statement: "伪造一个当前证据不支持的建议。",
		EvidenceIDs: []string{"ev-current", "ev-baseline"},
	}}
	calls := 0
	service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
		calls++
		return domain.RunbookSet{}, nil
	}))

	result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
		!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{runbookMissingDeterministicAdvice}) {
		t.Fatalf("calls=%d guidance=%#v", calls, result.RunbookGuidance)
	}
	assertRunbookGuidanceValid(t, evidence, result)
}

func TestNewRunbookServiceRequiresSourceAndCatalog(t *testing.T) {
	if _, err := NewRunbookService(nil, defaultRunbookTestCatalog(), domain.RunbookGuidanceSourceSyntheticMock); err == nil {
		t.Fatal("nil source was accepted")
	}
	if _, err := NewRunbookService(runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
		return domain.RunbookSet{}, nil
	}), nil, domain.RunbookGuidanceSourceSyntheticMock); err == nil {
		t.Fatal("nil catalog was accepted")
	}
	if _, err := NewRunbookService(runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
		return domain.RunbookSet{}, nil
	}), defaultRunbookTestCatalog(), "PROVIDER_REPORTED_REAL"); err == nil {
		t.Fatal("untrusted data-source mode was accepted")
	}
	if _, err := NewRunbookService(runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
		return domain.RunbookSet{}, nil
	}), defaultRunbookTestCatalog(), domain.RunbookGuidanceSourceSyntheticMock, WithRunbookTimeout(0)); err == nil {
		t.Fatal("non-positive source timeout was accepted")
	}
}

func TestRunbookServicePropagatesContextCancellation(t *testing.T) {
	t.Run("cancelled before enrichment", func(t *testing.T) {
		evidence, report := runbookServiceFixture()
		calls := 0
		service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
			calls++
			return domain.RunbookSet{}, nil
		}))
		ctx, cancel := context.WithCancel(runbookServiceContext(context.Background()))
		cancel()

		result, err := service.Enrich(ctx, evidence, report)
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
		assertRunbookReportPreserved(t, report, result)
	})

	t.Run("cancelled by source", func(t *testing.T) {
		evidence, report := runbookServiceFixture()
		ctx, cancel := context.WithCancel(runbookServiceContext(context.Background()))
		calls := 0
		service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
			calls++
			cancel()
			return domain.RunbookSet{}, errors.New("provider returned after cancellation")
		}))

		result, err := service.Enrich(ctx, evidence, report)
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
		assertRunbookReportPreserved(t, report, result)
	})
}

func TestRunbookServiceBoundsSourceWithIndependentTimeout(t *testing.T) {
	evidence, report := runbookServiceFixture()
	service, err := NewRunbookService(
		runbookSourceFunc(func(ctx context.Context, _ domain.RunbookQuery) (domain.RunbookSet, error) {
			<-ctx.Done()
			return domain.RunbookSet{}, ctx.Err()
		}),
		defaultRunbookTestCatalog(), domain.RunbookGuidanceSourceSyntheticMock,
		WithRunbookClock(runbookTestClock), WithRunbookTimeout(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
	if err != nil {
		t.Fatalf("source-local timeout escaped optional boundary: %v", err)
	}
	if result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
		!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{runbookMissingSourceAvailable}) {
		t.Fatalf("unexpected timeout guidance: %#v", result.RunbookGuidance)
	}
}

func TestRunbookServiceRejectsSuccessfulResultAfterSourceDeadline(t *testing.T) {
	evidence, report := runbookServiceFixture()
	service, err := NewRunbookService(
		runbookSourceFunc(func(ctx context.Context, _ domain.RunbookQuery) (domain.RunbookSet, error) {
			<-ctx.Done()
			entry := runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})
			return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: []domain.RunbookEntry{entry}, Complete: true}, nil
		}),
		defaultRunbookTestCatalog(), domain.RunbookGuidanceSourceSyntheticMock,
		WithRunbookClock(runbookTestClock), WithRunbookTimeout(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
	if err != nil {
		t.Fatalf("source-local timeout escaped optional boundary: %v", err)
	}
	if result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
		!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{runbookMissingSourceAvailable}) {
		t.Fatalf("late successful source result was accepted: %#v", result.RunbookGuidance)
	}
}

func TestRunbookServiceRejectsFutureEntryAgainstTrustedClock(t *testing.T) {
	evidence, report := runbookServiceFixture()
	report.GeneratedAt = runbookTestClock().Add(time.Hour)
	entry := runbookServiceEntry("runbook-a", "rev-1", []string{"inspect_top_error_pattern"})
	entry.UpdatedAt = runbookTestClock().Add(maxRunbookFutureSkew + time.Second)
	service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
		return domain.RunbookSet{SourceVersion: "mock-runbook-v1", Entries: []domain.RunbookEntry{entry}, Complete: true}, nil
	}))

	result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
		!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{runbookMissingValidSet}) {
		t.Fatalf("future Engine clock bypassed trusted freshness boundary: %#v", result.RunbookGuidance)
	}
	assertRunbookGuidanceValid(t, evidence, result)
}

func mustRunbookService(t *testing.T, source runbookSourceFunc) *RunbookService {
	t.Helper()
	service, err := NewRunbookService(
		source, defaultRunbookTestCatalog(), domain.RunbookGuidanceSourceSyntheticMock,
		WithRunbookClock(runbookTestClock),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func runbookServiceFixture() ([]domain.Evidence, domain.Report) {
	now := runbookTestClock()
	evidence := []domain.Evidence{
		{ID: "ev-current", Name: "current", ResourceID: "resource-order-prod", Complete: true, StartTime: now.Add(-30 * time.Minute), EndTime: now, ErrorCount: 120, ErrorPatterns: []domain.CountBucket{{Label: "payment_timeout", Count: 90}, {Label: "database_timeout", Count: 30}}, Instances: []domain.CountBucket{{Label: "order-pod-a", Count: 90}, {Label: "order-pod-b", Count: 30}}},
		{ID: "ev-baseline", Name: "baseline", ResourceID: "resource-order-prod", Complete: true, StartTime: now.Add(-60 * time.Minute), EndTime: now.Add(-30 * time.Minute), ErrorCount: 20, ErrorPatterns: []domain.CountBucket{{Label: "payment_timeout", Count: 5}, {Label: "database_timeout", Count: 15}}, Instances: []domain.CountBucket{{Label: "order-pod-a", Count: 5}, {Label: "order-pod-b", Count: 15}}},
	}
	applyRunbookGovernanceFixture(evidence, now, "resource-order-prod")
	report := domain.Report{
		InvestigationID: "inv-runbook", Outcome: "spike_detected", GeneratedAt: now,
		Findings: []domain.Finding{{
			Code: "error_spike", Statement: "错误日志较基线显著增长。", Confidence: .95, Conclusive: true,
			EvidenceIDs: []string{"ev-current", "ev-baseline"},
		}},
		Recommendations: []domain.Recommendation{{
			Code: "inspect_top_error_pattern", Statement: "核对主要错误模式。", EvidenceIDs: []string{"ev-current", "ev-baseline"},
		}},
		Evidence: append([]domain.Evidence(nil), evidence...),
	}
	return evidence, report
}

func runbookTestClock() time.Time {
	return time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
}

func applyRunbookGovernanceFixture(evidence []domain.Evidence, currentEnd time.Time, resourceID string) {
	for index := range evidence {
		item := &evidence[index]
		if item.Name == "current" {
			item.StartTime = currentEnd.Add(-30 * time.Minute)
			item.EndTime = currentEnd
		} else {
			item.StartTime = currentEnd.Add(-60 * time.Minute)
			item.EndTime = currentEnd.Add(-30 * time.Minute)
		}
		item.ResourceID = resourceID
		item.QueryID = "query-" + item.Name
		item.QuerySpecHash = strings.Repeat(string(rune('a'+index)), 64)
		item.TemplateID = domain.ErrorAnalysisTemplateID
		item.TemplateVersion = "test-template-v1"
		item.SchemaFingerprint = "test-schema-v1"
		item.PolicyVersion = "test-policy-v1"
		item.GovernanceFingerprint = strings.Repeat("c", 64)
		item.Progress = "Complete"
		item.NanosecondOrderedKnown = true
		item.NanosecondOrdered = true
		item.UsageKnown = true
		item.APICalls = domain.ErrorAnalysisAPICalls
		item.PatternLimit = domain.ErrorAnalysisPatternLimit
		item.InstanceLimit = domain.ErrorAnalysisInstanceLimit
		if item.ErrorCount > 0 && len(item.ErrorPatterns) == 0 {
			label, count := item.TopError, item.TopErrorCount
			if label == "" {
				label = "payment_timeout"
			}
			if count <= 0 {
				count = item.ErrorCount
			}
			item.ErrorPatterns = []domain.CountBucket{{Label: label, Count: count}}
		}
		if item.ErrorCount > 0 && len(item.Instances) == 0 {
			count := item.TopErrorCount
			if count <= 0 {
				count = item.ErrorCount
			}
			item.Instances = []domain.CountBucket{{Label: "order-pod-a", Count: count}}
		}
		item.ErrorPatternsExhaustive = runbookFixtureBucketTotal(item.ErrorPatterns) == item.ErrorCount
		item.InstancesExhaustive = runbookFixtureBucketTotal(item.Instances) == item.ErrorCount
	}
}

func runbookFixtureBucketTotal(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

func runbookServiceEntry(id, revision string, recommendationCodes []string) domain.RunbookEntry {
	return domain.RunbookEntry{
		ID: id, Revision: revision, ResourceID: "resource-order-prod",
		Title: "支付依赖人工核查", OwnerTeam: "team-payment", UpdatedAt: time.Date(2026, 8, 24, 7, 0, 0, 0, time.UTC),
		MatchedRecommendationCodes: append([]string(nil), recommendationCodes...),
		Steps: []domain.RunbookStep{
			runbookTestStep("step-verify", domain.RunbookStepCodeVerifyErrorPattern),
			runbookTestStep("step-observe", domain.RunbookStepCodeObserveHotInstance),
		},
	}
}

func runbookTestStep(id string, code domain.RunbookStepCode) domain.RunbookStep {
	kind, instruction, _ := domain.CanonicalRunbookStep(code)
	return domain.RunbookStep{ID: id, Code: code, Kind: kind, Instruction: instruction}
}

func defaultRunbookTestCatalog() *runbookTestCatalog {
	return &runbookTestCatalog{
		resource: domain.LogResource{ID: "resource-order-prod", Service: "order-service", Environment: "prod"},
		allowed:  true,
	}
}

func runbookServiceContext(ctx context.Context) context.Context {
	now := runbookTestClock()
	return withRunJob(ctx, domain.Job{Request: domain.InvestigationRequest{
		Service: "order-service", Environment: "prod",
		StartTime: now.Add(-30 * time.Minute), EndTime: now,
		Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
	}})
}

func assertRunbookReportPreserved(t *testing.T, before, after domain.Report) {
	t.Helper()
	after.RunbookGuidance = nil
	want, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("runbook enrichment changed deterministic report:\n got %s\nwant %s", got, want)
	}
}

func assertRunbookGuidanceValid(t *testing.T, evidence []domain.Evidence, report domain.Report) {
	t.Helper()
	evidenceByID := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		evidenceByID[item.ID] = item
	}
	if err := validateRunbookGuidance(report.RunbookGuidance, evidenceByID, report); err != nil {
		t.Fatalf("production validation rejected guidance: %v", err)
	}
}
