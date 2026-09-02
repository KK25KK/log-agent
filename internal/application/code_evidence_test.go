package application

import (
	"context"
	"testing"
	"time"

	"logagent/internal/application/anchors"
	"logagent/internal/domain"
)

type deploymentSourceFunc func(context.Context, domain.DeploymentQuery) (domain.DeploymentEvidence, error)

func (function deploymentSourceFunc) ResolveDeployment(ctx context.Context, query domain.DeploymentQuery) (domain.DeploymentEvidence, error) {
	return function(ctx, query)
}

type codeProviderFunc func(context.Context, domain.CodeSearchRequest) (domain.CodeSearchResult, error)

func (function codeProviderFunc) SearchCode(ctx context.Context, request domain.CodeSearchRequest) (domain.CodeSearchResult, error) {
	return function(ctx, request)
}

func TestCodeEvidenceServiceRequiresDeploymentAndProducesNoMatch(t *testing.T) {
	end := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	events, set := anchors.Extract([]domain.TraceEvent{{ID: "event-1", MemberID: "dam-server", Message: "processing failed: payment timeout"}}, true)
	deployment := domain.DeploymentEvidence{
		Version: domain.DeploymentEvidenceVersion, Status: domain.DeploymentComplete, Service: "dam-server", Environment: "test",
		RepositoryID: "dam", CommitSHA: repeatCommit("a"), DeployedAt: end.Add(-time.Hour), SourceVersion: "catalog-v1",
	}
	deployment.Fingerprint, _ = domain.DeploymentEvidenceFingerprint(deployment)
	calls := 0
	service, err := NewCodeEvidenceService(
		deploymentSourceFunc(func(_ context.Context, query domain.DeploymentQuery) (domain.DeploymentEvidence, error) {
			if !query.At.Equal(end) {
				t.Fatalf("deployment query time=%s", query.At)
			}
			return deployment, nil
		}),
		codeProviderFunc(func(_ context.Context, request domain.CodeSearchRequest) (domain.CodeSearchResult, error) {
			calls++
			return domain.CodeSearchResult{
				Version: domain.CodeEvidenceVersion, PolicyVersion: domain.CodeSearchPolicyVersion,
				RepositoryID: "dam", CommitSHA: deployment.CommitSHA, Complete: true,
				AnchorsSearched: len(request.Anchors), CommandsRun: 2,
			}, nil
		}),
		5*time.Second, WithCodeEvidenceClock(func() time.Time { return end.Add(time.Second) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.Report{InvestigationID: "investigation-1", TraceInvestigation: &domain.TraceInvestigation{
		Status: domain.TraceInvestigationComplete, Complete: true, EndTime: end, Events: events, AnchorSet: &set,
	}}
	report = service.Enrich(context.Background(), domain.InvestigationRequest{Service: "dam-server", Environment: "test", EndTime: end}, report)
	if calls != 1 || report.CodeInvestigation == nil || report.CodeInvestigation.Status != domain.CodeInvestigationNoMatch || !report.CodeInvestigation.Complete {
		t.Fatalf("unexpected code investigation: calls=%d value=%#v", calls, report.CodeInvestigation)
	}
	if err := validateCodeInvestigation(report.CodeInvestigation, report.TraceInvestigation); err != nil {
		t.Fatal(err)
	}
}

func TestCodeEvidenceServiceNeverCallsProvidersForIncompleteTrace(t *testing.T) {
	calls := 0
	service, err := NewCodeEvidenceService(
		deploymentSourceFunc(func(context.Context, domain.DeploymentQuery) (domain.DeploymentEvidence, error) {
			calls++
			return domain.DeploymentEvidence{}, nil
		}),
		codeProviderFunc(func(context.Context, domain.CodeSearchRequest) (domain.CodeSearchResult, error) {
			calls++
			return domain.CodeSearchResult{}, nil
		}),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.Report{InvestigationID: "investigation-1", TraceInvestigation: &domain.TraceInvestigation{Status: domain.TraceInvestigationPartial}}
	report = service.Enrich(context.Background(), domain.InvestigationRequest{}, report)
	if calls != 0 || report.CodeInvestigation.Status != domain.CodeInvestigationSkipped || report.CodeInvestigation.ReasonCode != domain.CodeReasonTraceIncomplete {
		t.Fatalf("expected zero-call skip: calls=%d value=%#v", calls, report.CodeInvestigation)
	}
}

func repeatCommit(value string) string {
	result := ""
	for len(result) < 40 {
		result += value
	}
	return result[:40]
}
