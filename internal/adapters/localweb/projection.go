package localweb

import (
	"context"
	"errors"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type ScopeView struct {
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	TemplateID  string    `json:"template_id"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
}

type EvidenceView struct {
	ID                 string               `json:"id"`
	Name               string               `json:"name"`
	StartTime          time.Time            `json:"start_time"`
	EndTime            time.Time            `json:"end_time"`
	Progress           string               `json:"progress"`
	Complete           bool                 `json:"complete"`
	Truncated          bool                 `json:"truncated"`
	ProcessedRows      int64                `json:"processed_rows"`
	ProcessedBytes     int64                `json:"processed_bytes"`
	ElapsedMillisecond int64                `json:"elapsed_millisecond"`
	APICalls           int                  `json:"api_calls"`
	ErrorCount         int64                `json:"error_count"`
	TopError           string               `json:"top_error,omitempty"`
	TopErrorCount      int64                `json:"top_error_count,omitempty"`
	ErrorPatterns      []domain.CountBucket `json:"error_patterns,omitempty"`
	Instances          []domain.CountBucket `json:"instances,omitempty"`
}

type ReportView struct {
	Outcome         string                        `json:"outcome"`
	Findings        []domain.Finding              `json:"findings"`
	Recommendations []domain.Recommendation       `json:"recommendations,omitempty"`
	Evidence        []EvidenceView                `json:"evidence"`
	CauseStatus     domain.CauseAnalysisStatus    `json:"cause_status,omitempty"`
	CauseHypotheses []domain.CauseHypothesis      `json:"cause_hypotheses,omitempty"`
	TimelineStatus  domain.TimelineStatus         `json:"timeline_status,omitempty"`
	TimelineItems   []domain.IncidentTimelineItem `json:"timeline_items,omitempty"`
	RunbookGuidance *domain.RunbookGuidance       `json:"runbook_guidance,omitempty"`
	Summary         *SummaryView                  `json:"summary,omitempty"`
	GeneratedAt     time.Time                     `json:"generated_at"`
}

type SummaryView struct {
	Status        domain.SummaryStatus         `json:"status"`
	Mode          domain.SummaryMode           `json:"mode"`
	Provider      string                       `json:"provider"`
	Model         string                       `json:"model,omitempty"`
	Phenomenon    string                       `json:"phenomenon"`
	PossibleCause string                       `json:"possible_cause,omitempty"`
	EvidenceNotes []domain.SummaryEvidenceNote `json:"evidence_notes"`
	Limitations   []string                     `json:"limitations"`
	NextSteps     []domain.SummaryNextStep     `json:"next_steps"`
	InputTokens   int64                        `json:"input_tokens"`
	OutputTokens  int64                        `json:"output_tokens"`
	TotalTokens   int64                        `json:"total_tokens"`
	LatencyMillis int64                        `json:"latency_millis"`
	GeneratedAt   time.Time                    `json:"generated_at"`
}

type InvestigationView struct {
	ID        string           `json:"id"`
	Status    domain.Status    `json:"status"`
	Scope     ScopeView        `json:"scope"`
	Report    *ReportView      `json:"report,omitempty"`
	Delivery  DeliverySnapshot `json:"delivery"`
	Actions   []string         `json:"actions"`
	Notice    string           `json:"notice,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func (s *Server) loadView(ctx context.Context, investigationID string) (InvestigationView, error) {
	investigation, err := s.store.GetInvestigation(ctx, investigationID)
	if err != nil {
		return InvestigationView{}, err
	}
	target, targetErr := s.store.GetInteractionTarget(ctx, investigationID)
	if targetErr != nil && !errors.Is(targetErr, ports.ErrNotFound) {
		return InvestigationView{}, targetErr
	}
	delivery, ok := s.sender.Snapshot(investigationID)
	if !ok {
		delivery.CardReady = target.CardMessageID != ""
	}
	view := InvestigationView{
		ID: investigation.ID, Status: investigation.Status,
		Scope: ScopeView{
			Service: investigation.Request.Service, Environment: investigation.Request.Environment,
			TemplateID: investigation.Request.TemplateID, StartTime: investigation.Request.StartTime,
			EndTime: investigation.Request.EndTime,
		},
		Delivery: delivery, CreatedAt: investigation.CreatedAt, UpdatedAt: investigation.UpdatedAt,
	}
	if investigation.Report != nil {
		view.Report = projectReport(*investigation.Report)
	}
	if delivery.CardReady {
		view.Actions = availableActions(investigation)
	}
	switch investigation.Status {
	case domain.StatusFailed:
		view.Notice = "调查失败，未生成可验证结论；Provider 原始错误未在页面暴露。"
	case domain.StatusNeedsReview:
		view.Notice = "上一次外部查询结果未知，系统没有自动重试；重跑前需要确认潜在重复查询成本。"
	case domain.StatusCancelled:
		view.Notice = "调查已取消，不会再提交新的有效结果。"
	}
	return view, nil
}

func projectReport(report domain.Report) *ReportView {
	view := &ReportView{
		Outcome: report.Outcome, Findings: append([]domain.Finding(nil), report.Findings...),
		Recommendations: append([]domain.Recommendation(nil), report.Recommendations...),
		GeneratedAt:     report.GeneratedAt,
	}
	for _, evidence := range report.Evidence {
		view.Evidence = append(view.Evidence, EvidenceView{
			ID: evidence.ID, Name: evidence.Name, StartTime: evidence.StartTime, EndTime: evidence.EndTime,
			Progress: evidence.Progress, Complete: evidence.Complete, Truncated: evidence.Truncated,
			ProcessedRows: evidence.ProcessedRows, ProcessedBytes: evidence.ProcessedBytes,
			ElapsedMillisecond: evidence.ElapsedMillisecond, APICalls: evidence.APICalls,
			ErrorCount: evidence.ErrorCount, TopError: evidence.TopError, TopErrorCount: evidence.TopErrorCount,
			ErrorPatterns: append([]domain.CountBucket(nil), evidence.ErrorPatterns...),
			Instances:     append([]domain.CountBucket(nil), evidence.Instances...),
		})
	}
	if report.CauseAnalysis != nil {
		view.CauseStatus = report.CauseAnalysis.Status
		view.CauseHypotheses = append([]domain.CauseHypothesis(nil), report.CauseAnalysis.Hypotheses...)
	}
	if report.IncidentTimeline != nil {
		view.TimelineStatus = report.IncidentTimeline.Status
		view.TimelineItems = append([]domain.IncidentTimelineItem(nil), report.IncidentTimeline.Items...)
	}
	if report.RunbookGuidance != nil {
		copyGuidance := *report.RunbookGuidance
		copyGuidance.Items = append([]domain.RunbookGuidanceItem(nil), report.RunbookGuidance.Items...)
		view.RunbookGuidance = &copyGuidance
	}
	if report.Summary != nil {
		summary := report.Summary
		view.Summary = &SummaryView{
			Status: summary.Status, Mode: summary.Mode, Provider: summary.Provider, Model: summary.Model,
			Phenomenon: summary.Phenomenon, PossibleCause: summary.PossibleCause,
			EvidenceNotes: append([]domain.SummaryEvidenceNote(nil), summary.EvidenceNotes...),
			Limitations:   append([]string(nil), summary.Limitations...),
			NextSteps:     append([]domain.SummaryNextStep(nil), summary.NextSteps...),
			InputTokens:   summary.InputTokens, OutputTokens: summary.OutputTokens,
			TotalTokens: summary.TotalTokens, LatencyMillis: summary.LatencyMillis,
			GeneratedAt: summary.GeneratedAt,
		}
	}
	return view
}

func availableActions(investigation domain.Investigation) []string {
	actions := []string{string(domain.ActionViewReport)}
	if investigation.Report != nil {
		actions = append(actions, string(domain.ActionViewEvidence))
	}
	switch investigation.Status {
	case domain.StatusQueued, domain.StatusRunning:
		actions = append(actions, string(domain.ActionCancel))
	case domain.StatusNeedsReview:
		actions = append(actions, string(domain.ActionRerunWithCostAck))
	case domain.StatusCancelled:
		if investigation.LastError == domain.CancelReasonExternalQueryOutcomeUnknown {
			actions = append(actions, string(domain.ActionRerunWithCostAck))
		} else {
			actions = append(actions, string(domain.ActionExpandWindow), string(domain.ActionRerun))
		}
	case domain.StatusSucceeded, domain.StatusFailed:
		actions = append(actions, string(domain.ActionExpandWindow), string(domain.ActionRerun))
	}
	return actions
}
