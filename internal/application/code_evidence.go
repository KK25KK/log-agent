package application

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type CodeEvidenceService struct {
	deployments ports.DeploymentVersionSource
	provider    ports.CodeEvidenceProvider
	timeout     time.Duration
	now         func() time.Time
}

type CodeEvidenceOption func(*CodeEvidenceService)

func WithCodeEvidenceClock(now func() time.Time) CodeEvidenceOption {
	return func(service *CodeEvidenceService) {
		if now != nil {
			service.now = now
		}
	}
}

func NewCodeEvidenceService(deployments ports.DeploymentVersionSource, provider ports.CodeEvidenceProvider, timeout time.Duration, options ...CodeEvidenceOption) (*CodeEvidenceService, error) {
	if deployments == nil || provider == nil {
		return nil, errors.New("deployment source and code evidence provider are required")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("code evidence timeout must be in (0, 30s]")
	}
	service := &CodeEvidenceService{deployments: deployments, provider: provider, timeout: timeout, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (service *CodeEvidenceService) Enrich(ctx context.Context, request domain.InvestigationRequest, report domain.Report) domain.Report {
	generatedAt := service.now().UTC()
	base := &domain.CodeInvestigation{
		Version: domain.CodeEvidenceVersion, Status: domain.CodeInvestigationSkipped,
		Limitations: []string{
			"代码片段只能解释可能路径，不能单独证明运行时实际执行。",
			"所有结果仅供人工审核，系统不会自动修改代码或环境。",
		},
		GeneratedAt: generatedAt,
	}
	report.CodeInvestigation = base
	trace := report.TraceInvestigation
	if trace == nil || !trace.Complete || trace.Status != domain.TraceInvestigationComplete || trace.AnchorSet == nil || trace.AnchorSet.Status == domain.RuntimeAnchorsPartial {
		base.ReasonCode = domain.CodeReasonTraceIncomplete
		return report
	}
	if len(trace.AnchorSet.Anchors) == 0 || trace.AnchorSet.Status == domain.RuntimeAnchorsNone {
		base.ReasonCode = domain.CodeReasonNoAnchors
		return report
	}
	deploymentQuery := domain.DeploymentQuery{Service: request.Service, Environment: request.Environment, At: request.EndTime.UTC()}
	resolveCtx, cancelResolve := context.WithTimeout(ctx, service.timeout)
	deployment, err := service.deployments.ResolveDeployment(resolveCtx, deploymentQuery)
	cancelResolve()
	if err != nil || validateDeploymentEvidence(deploymentQuery, deployment) != nil {
		base.Status, base.ReasonCode = domain.CodeInvestigationUnavailable, domain.CodeReasonDeploymentInvalid
		return report
	}
	base.Deployment = &deployment
	if deployment.Status != domain.DeploymentComplete {
		base.Status, base.ReasonCode = domain.CodeInvestigationUnavailable, deployment.ReasonCode
		return report
	}
	anchors := append([]domain.RuntimeAnchor(nil), trace.AnchorSet.Anchors...)
	if len(anchors) > domain.CodeSearchMaxAnchors {
		anchors = anchors[:domain.CodeSearchMaxAnchors]
	}
	searchRequest := domain.CodeSearchRequest{
		InvestigationID: report.InvestigationID, RepositoryID: deployment.RepositoryID,
		CommitSHA: deployment.CommitSHA, PreviousCommitSHA: deployment.PreviousCommitSHA,
		Anchors: anchors, MaxMatches: domain.CodeSearchMaxMatches, MaxFiles: domain.CodeSearchMaxFiles,
		MaxLines: domain.CodeSearchMaxLines, MaxBytes: domain.CodeSearchMaxBytes,
		ContextRadius: domain.CodeSearchContextRadius, PolicyVersion: domain.CodeSearchPolicyVersion,
		RequestedAt: generatedAt,
	}
	searchCtx, cancelSearch := context.WithTimeout(ctx, service.timeout)
	result, err := service.provider.SearchCode(searchCtx, searchRequest)
	cancelSearch()
	if err != nil {
		base.Status, base.ReasonCode = domain.CodeInvestigationUnavailable, domain.CodeReasonProviderUnavailable
		return report
	}
	if err := validateCodeSearchResult(searchRequest, result); err != nil {
		base.Status, base.ReasonCode = domain.CodeInvestigationUnavailable, domain.CodeReasonResultInvalid
		return report
	}
	base.Complete = result.Complete
	base.Matches = append([]domain.CodeMatch(nil), result.Matches...)
	base.AnchorsUsed, base.FilesRead = result.AnchorsSearched, result.FilesRead
	base.LinesReturned, base.BytesReturned, base.CommandsRun = result.LinesReturned, result.BytesReturned, result.CommandsRun
	base.SensitiveSkips = result.SensitiveSkips
	base.DiffChecked, base.ChangedFiles = result.DiffChecked, append([]string(nil), result.ChangedFiles...)
	if result.DiffChecked {
		base.Limitations = append(base.Limitations, "部署版本间文件重叠只表示相关，不构成因果证明。")
	}
	switch {
	case result.Truncated:
		base.Status, base.ReasonCode = domain.CodeInvestigationPartial, domain.CodeReasonResultTruncated
	case len(result.Matches) == 0:
		base.Status = domain.CodeInvestigationNoMatch
	default:
		base.Status = domain.CodeInvestigationComplete
	}
	return report
}
