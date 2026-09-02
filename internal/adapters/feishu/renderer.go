package feishu

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"logagent/internal/domain"
)

const (
	maxCardBytes             = 30 * 1024
	maxFindingItems          = 6
	maxRecommendationItems   = 3
	maxEvidenceItems         = 2
	maxCauseSummaryItems     = 1
	maxCauseEvidenceItems    = 3
	maxTimelineSummaryItems  = 3
	maxTimelineEvidenceItems = 6
	maxRunbookItems          = 2
	maxRunbookSteps          = 3
	maxAISummaryNotes        = 2
	maxTraceMembers          = 8
	maxTraceEvents           = 12
	maxAggregateItems        = 5
	maxStatementRunes        = 480
	maxAggregateRunes        = 96
	maxIdentifierRunes       = 96
)

var shanghaiLocation = fixedShanghaiLocation()

// cardDocument is deliberately private. Feishu's JSON 2.0 protocol must not
// become an application or domain dependency.
type cardDocument struct {
	Schema string     `json:"schema"`
	Header cardHeader `json:"header"`
	Body   cardBody   `json:"body"`
}

type cardHeader struct {
	Title    cardText `json:"title"`
	Template string   `json:"template,omitempty"`
}

type cardBody struct {
	Elements []any `json:"elements"`
}

type cardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type cardMarkdown struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type cardDivider struct {
	Tag string `json:"tag"`
}

type cardColumnSet struct {
	Tag               string       `json:"tag"`
	HorizontalSpacing string       `json:"horizontal_spacing,omitempty"`
	Columns           []cardColumn `json:"columns"`
}

type cardColumn struct {
	Tag      string       `json:"tag"`
	Width    string       `json:"width"`
	Weight   int          `json:"weight,omitempty"`
	Elements []cardButton `json:"elements"`
}

type cardButton struct {
	Tag       string         `json:"tag"`
	Text      cardText       `json:"text"`
	Type      string         `json:"type,omitempty"`
	Width     string         `json:"width,omitempty"`
	Behaviors []cardBehavior `json:"behaviors"`
}

type cardBehavior struct {
	Type  string            `json:"type"`
	Value map[string]string `json:"value"`
}

func renderDeliveryCard(delivery domain.DeliveryJob) (cardDocument, error) {
	if delivery.Investigation.ID == "" {
		return cardDocument{}, errors.New("investigation ID is required for card rendering")
	}
	switch delivery.Kind {
	case domain.DeliveryQueued:
		return renderQueuedCard(delivery.Investigation), nil
	case domain.DeliveryRunning:
		return renderRunningCard(delivery.Investigation), nil
	case domain.DeliverySucceeded:
		return renderReportCard(delivery.Investigation), nil
	case domain.DeliveryFailed:
		return renderFailedCard(delivery.Investigation), nil
	case domain.DeliveryCancelled:
		return renderCancelledCard(delivery.Investigation), nil
	case domain.DeliveryNeedsReview:
		return renderNeedsReviewCard(delivery.Investigation), nil
	default:
		return cardDocument{}, fmt.Errorf("unsupported delivery kind %q", delivery.Kind)
	}
}

func renderActionCard(result domain.ActionResult) (cardDocument, error) {
	switch result.View {
	case domain.ActionViewQueuedCard:
		return renderQueuedCard(result.Investigation), nil
	case domain.ActionViewRunningCard:
		return renderRunningCard(result.Investigation), nil
	case domain.ActionViewCancelledCard:
		return renderCancelledCard(result.Investigation), nil
	case domain.ActionViewNeedsReviewCard:
		return renderNeedsReviewCard(result.Investigation), nil
	case domain.ActionViewReportCard:
		return renderReportCard(result.Investigation), nil
	case domain.ActionViewEvidenceCard:
		return renderEvidenceCard(result.Investigation)
	default:
		return cardDocument{}, fmt.Errorf("unsupported action view %q", result.View)
	}
}

func renderIntentPreviewCard(resolution domain.IntentResolution) (cardDocument, error) {
	if resolution.ID == "" || resolution.Problem.Text == "" || resolution.Status == domain.IntentResolutionParsing {
		return cardDocument{}, errors.New("intent resolution is not ready for card rendering")
	}
	planDetails := fmt.Sprintf(
		"**解析状态：** %s\n\n**意图：** %s\n\n**逻辑范围：** %s / %s\n\n**时间窗口：** %s\n\n**固定模板：** %s\n\n**置信度：** %.0f%%",
		safeMarkdown(string(resolution.Status), maxIdentifierRunes), safeMarkdown(string(resolution.Intent), maxIdentifierRunes),
		safeMarkdown(resolution.Service, maxIdentifierRunes), safeMarkdown(resolution.Environment, maxIdentifierRunes),
		time.Duration(resolution.DurationSeconds)*time.Second, safeMarkdown(resolution.TemplateID, maxIdentifierRunes), resolution.Confidence*100,
	)
	if resolution.TraceIDHint != "" {
		planDetails += "\n\n**TraceID：** " + safeMarkdown(resolution.TraceIDHint, maxIdentifierRunes)
	}
	elements := []any{
		markdown("**用户描述（未验证）：** " + safeMarkdown(resolution.Problem.Text, maxStatementRunes)),
		cardDivider{Tag: "hr"},
		markdown(planDetails),
	}
	if resolution.Status == domain.IntentResolutionResolved &&
		((resolution.Intent == domain.IntentErrorSpike && resolution.TemplateID == domain.ErrorCountTemplateID) ||
			(resolution.Intent == domain.IntentTraceSearch && resolution.TemplateID == domain.TraceSearchTemplateID)) {
		elements = append(elements,
			markdown("当前尚未访问日志。只有点击确认后才会创建调查并执行受控只读查询。"),
			intentConfirmButton(resolution.ID),
		)
	} else {
		elements = append(elements, markdown("当前解析结果不能启动调查，请使用严格 `/investigate` 命令补充或修正范围。"))
	}
	return newCard("日志调查意图预览", "blue", elements), nil
}

func intentConfirmButton(resolutionID string) cardColumnSet {
	return cardColumnSet{
		Tag: "column_set", HorizontalSpacing: "8px",
		Columns: []cardColumn{{
			Tag: "column", Width: "weighted", Weight: 1,
			Elements: []cardButton{{
				Tag: "button", Text: cardText{Tag: "plain_text", Content: "确认并调查"}, Type: "primary", Width: "fill",
				Behaviors: []cardBehavior{{Type: "callback", Value: map[string]string{"action": "confirm_intent", "resolution_id": resolutionID}}},
			}},
		}},
	}
}

func renderQueuedCard(investigation domain.Investigation) cardDocument {
	elements := summaryElements(investigation)
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 已接单，正在等待 Worker 执行。"),
		buttonRow(investigation.ID, buttonSpec{label: "取消调查", action: domain.ActionCancel, style: "danger"}),
	)
	return newCard("日志调查已接单", "blue", elements)
}

func renderRunningCard(investigation domain.Investigation) cardDocument {
	elements := summaryElements(investigation)
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 调查执行中\n\n正在执行受控只读聚合查询，并核对当前窗口与基线窗口。"),
		buttonRow(investigation.ID, buttonSpec{label: "取消调查", action: domain.ActionCancel, style: "danger"}),
	)
	return newCard("日志调查进行中", "blue", elements)
}

func renderReportCard(investigation domain.Investigation) cardDocument {
	if investigation.Status == domain.StatusFailed {
		return renderFailedCard(investigation)
	}
	if investigation.Status == domain.StatusCancelled {
		return renderCancelledCard(investigation)
	}
	elements := summaryElements(investigation)
	template := "green"
	title := "日志调查完成"
	if investigation.Report == nil {
		template = "orange"
		elements = append(elements, cardDivider{Tag: "hr"}, markdown("报告暂不可用，请稍后重新运行调查。"))
		elements = append(elements, buttonRow(investigation.ID,
			buttonSpec{label: "重新运行", action: domain.ActionRerun, style: "primary"},
		))
		return newCard(title, template, elements)
	}

	report := investigation.Report
	if report.Outcome == "data_insufficient" {
		template = "orange"
		title = "日志调查完成：证据不足"
	}
	elements = append(elements, cardDivider{Tag: "hr"})
	elements = append(elements, markdown("**调查结果：** "+safeMarkdown(report.Outcome, maxAggregateRunes)))
	elements = appendTraceSummary(elements, report.TraceInvestigation)
	elements = appendReportSummary(elements, report.Summary)
	for index, finding := range boundedFindings(report.Findings) {
		conclusion := "非确定性"
		if finding.Conclusive {
			conclusion = "确定性"
		}
		content := fmt.Sprintf("**结论 %d（%s，置信度 %.0f%%）：** %s", index+1, conclusion, finding.Confidence*100, safeMarkdown(finding.Statement, maxStatementRunes))
		elements = append(elements, markdown(content))
	}
	elements = appendCauseSummary(elements, report.CauseAnalysis)
	elements = appendIncidentTimelineSummary(elements, report.IncidentTimeline)
	for index, recommendation := range boundedRecommendations(report.Recommendations) {
		elements = append(elements, markdown(fmt.Sprintf("**建议 %d：** %s", index+1, safeMarkdown(recommendation.Statement, maxStatementRunes))))
	}
	elements = appendRunbookGuidance(elements, report.RunbookGuidance)
	elements = append(elements, buttonRow(investigation.ID,
		buttonSpec{label: "查看证据", action: domain.ActionViewEvidence, style: "primary"},
		buttonSpec{label: "扩大时间窗", action: domain.ActionExpandWindow},
		buttonSpec{label: "重新运行", action: domain.ActionRerun},
	))
	return newCard(title, template, elements)
}

func appendRunbookGuidance(elements []any, guidance *domain.RunbookGuidance) []any {
	if guidance == nil || guidance.Status == domain.RunbookGuidanceSkippedNoTrigger {
		return elements
	}
	elements = append(elements, cardDivider{Tag: "hr"})
	if !domain.ValidateRunbookGuidanceDataSource(guidance.DataSource) {
		return append(elements, markdown("**SOP 参考（来源未确认）：** 当前不可用；既有调查结论和建议不受影响。\n\n仅供人工核查，不会自动执行处置。"))
	}
	heading := runbookGuidanceHeading(guidance.DataSource)
	switch guidance.Status {
	case domain.RunbookGuidanceNoMatch:
		return append(elements, markdown(fmt.Sprintf("**%s：** 当前受控目录未匹配到适用条目；这不代表企业不存在相关 SOP。\n\n仅供人工核查，不会自动执行处置。", heading)))
	case domain.RunbookGuidanceInconclusive:
		return append(elements, markdown(fmt.Sprintf("**%s：** 当前目录结果不完整，暂不展示 SOP 条目；不能据此推断没有相关 SOP。\n\n仅供人工核查，不会自动执行处置。", heading)))
	case domain.RunbookGuidanceUnavailable:
		return append(elements, markdown(fmt.Sprintf("**%s：** 当前不可用；既有调查结论和建议不受影响。\n\n仅供人工核查，不会自动执行处置。", heading)))
	case domain.RunbookGuidanceComplete:
		return appendCompleteRunbookGuidance(elements, heading, guidance.Items)
	default:
		return append(elements, markdown(fmt.Sprintf("**%s：** 当前不可用；既有调查结论和建议不受影响。\n\n仅供人工核查，不会自动执行处置。", heading)))
	}
}

func runbookGuidanceHeading(source domain.RunbookGuidanceDataSource) string {
	switch source {
	case domain.RunbookGuidanceSourceSyntheticMock:
		return "受控 SOP 参考（Mock）"
	case domain.RunbookGuidanceSourceEnterpriseGoverned:
		return "受控 SOP 参考"
	default:
		return "SOP 参考（来源未确认）"
	}
}

func appendCompleteRunbookGuidance(elements []any, heading string, items []domain.RunbookGuidanceItem) []any {
	if len(items) == 0 {
		return append(elements, markdown(fmt.Sprintf("**%s：** 当前目录结果不完整，暂不展示 SOP 条目；不能据此推断没有相关 SOP。\n\n仅供人工核查，不会自动执行处置。", heading)))
	}
	if len(items) > maxRunbookItems {
		items = items[:maxRunbookItems]
	}
	elements = append(elements, markdown(fmt.Sprintf("**%s：**", heading)))
	for index, item := range items {
		content := fmt.Sprintf(
			"**SOP %d：** %s\n\n版本：%s｜负责人：%s｜更新时间：%s",
			index+1,
			safeMarkdown(item.Title, maxStatementRunes),
			safeMarkdown(item.Revision, maxIdentifierRunes),
			safeMarkdown(item.Owner, maxAggregateRunes),
			formatRunbookUpdatedAt(item.UpdatedAt),
		)
		steps := item.Steps
		if len(steps) > maxRunbookSteps {
			steps = steps[:maxRunbookSteps]
		}
		for stepIndex, step := range steps {
			content += fmt.Sprintf(
				"\n\n%d. 【%s】%s",
				stepIndex+1,
				runbookStepKindLabel(step.Kind),
				safeMarkdown(step.Instruction, maxStatementRunes),
			)
		}
		elements = append(elements, markdown(content))
	}
	return append(elements, markdown("仅供人工核查，不会自动执行处置。"))
}

func runbookStepKindLabel(kind domain.RunbookStepKind) string {
	switch kind {
	case domain.RunbookStepVerify:
		return "核对"
	case domain.RunbookStepObserve:
		return "观察"
	case domain.RunbookStepEscalate:
		return "升级联系"
	default:
		return "人工核查"
	}
}

func formatRunbookUpdatedAt(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.In(shanghaiLocation).Format("2006-01-02 15:04:05")
}

func appendReportSummary(elements []any, summary *domain.ReportSummary) []any {
	if summary == nil || summary.Status != domain.SummaryGenerated {
		return elements
	}
	label := "AI 证据摘要"
	if summary.Mode == domain.SummaryModeMock {
		label += "（Mock）"
	}
	content := fmt.Sprintf("**%s：** %s", label, safeMarkdown(summary.Phenomenon, maxStatementRunes))
	if summary.PossibleCause != "" {
		content += "\n\n**可能原因候选：** " + safeMarkdown(summary.PossibleCause, maxStatementRunes)
	}
	for index, note := range summary.EvidenceNotes {
		if index >= maxAISummaryNotes {
			break
		}
		content += fmt.Sprintf("\n\n**证据说明 %d：** %s", index+1, safeMarkdown(note.Statement, maxStatementRunes))
	}
	content += "\n\n_摘要只改写已治理证据，确定性结论和权限边界仍以下方报告为准。_"
	return append(elements, markdown(content))
}

func renderEvidenceCard(investigation domain.Investigation) (cardDocument, error) {
	if investigation.Report == nil {
		return cardDocument{}, errors.New("report is required for evidence view")
	}
	elements := summaryElements(investigation)
	elements = append(elements, cardDivider{Tag: "hr"})
	if investigation.Report.TraceInvestigation != nil {
		elements = appendTraceEvidence(elements, investigation.Report.TraceInvestigation)
		elements = append(elements, buttonRow(investigation.ID,
			buttonSpec{label: "返回报告", action: domain.ActionViewReport, style: "primary"},
			buttonSpec{label: "扩大时间窗", action: domain.ActionExpandWindow},
			buttonSpec{label: "重新运行", action: domain.ActionRerun},
		))
		return newCard("Trace 调查证据", "blue", elements), nil
	}
	evidence := investigation.Report.Evidence
	if len(evidence) == 0 {
		elements = append(elements, markdown("当前报告没有可展示的证据。"))
	}
	if len(evidence) > maxEvidenceItems {
		evidence = evidence[:maxEvidenceItems]
	}
	for index, item := range evidence {
		quality := "完整"
		if !item.Complete || item.Truncated {
			quality = "不完整"
		}
		content := fmt.Sprintf(
			"**证据 %d · %s**\n\n窗口：%s\n\n质量：%s｜错误数：%d｜处理量：%s\n\nTop 错误：%s（%d）",
			index+1,
			safeMarkdown(item.Name, maxAggregateRunes),
			formatRange(item.StartTime, item.EndTime),
			quality,
			item.ErrorCount,
			formatBytes(item.ProcessedBytes),
			safeMarkdown(valueOrDash(item.TopError), maxAggregateRunes),
			item.TopErrorCount,
		)
		if item.TemplateID == domain.ErrorCountTemplateID {
			content = fmt.Sprintf(
				"**证据 %d · %s**\n\n窗口：%s\n\n质量：%s｜错误数：%d｜处理量：%s\n\n分析范围：仅错误计数\n\n错误类型：本模板不适用\n\n实例分布：本模板不适用",
				index+1, safeMarkdown(item.Name, maxAggregateRunes), formatRange(item.StartTime, item.EndTime), quality, item.ErrorCount, formatBytes(item.ProcessedBytes),
			)
		}
		elements = append(elements, markdown(content))
		if item.TemplateID != domain.ErrorCountTemplateID {
			if buckets := formatBuckets("新增/主要错误模式", item.ErrorPatterns); buckets != "" {
				elements = append(elements, markdown(buckets))
			}
			if buckets := formatBuckets("实例分布", item.Instances); buckets != "" {
				elements = append(elements, markdown(buckets))
			}
		}
	}
	elements = appendCauseEvidence(elements, investigation.Report.CauseAnalysis)
	elements = appendIncidentTimelineEvidence(elements, investigation.Report.IncidentTimeline)
	elements = append(elements, buttonRow(investigation.ID,
		buttonSpec{label: "返回报告", action: domain.ActionViewReport, style: "primary"},
		buttonSpec{label: "扩大时间窗", action: domain.ActionExpandWindow},
		buttonSpec{label: "重新运行", action: domain.ActionRerun},
	))
	return newCard("日志调查证据", "blue", elements), nil
}

func appendTraceSummary(elements []any, trace *domain.TraceInvestigation) []any {
	if trace == nil {
		return elements
	}
	content := fmt.Sprintf(
		"**Trace 时间线：** %s｜成员 %d｜事件 %d｜API %d｜扫描 %s\n\nTrace 指纹：%s",
		safeMarkdown(string(trace.Status), maxIdentifierRunes), len(trace.Members), len(trace.Events),
		trace.TotalAPICalls, formatBytes(trace.TotalProcessedBytes), safeMarkdown(shortFingerprint(trace.TraceIDFingerprint), maxIdentifierRunes),
	)
	return append(elements, markdown(content))
}

func appendTraceEvidence(elements []any, trace *domain.TraceInvestigation) []any {
	members := trace.Members
	if len(members) > maxTraceMembers {
		members = members[:maxTraceMembers]
	}
	for index, member := range members {
		content := fmt.Sprintf(
			"**成员 %d · %s**\n\n状态：%s｜事件：%d｜API：%d｜扫描：%s",
			index+1, safeMarkdown(member.MemberID, maxIdentifierRunes),
			safeMarkdown(string(member.Status), maxIdentifierRunes), member.EventCount,
			member.APICalls, formatBytes(member.ProcessedBytes),
		)
		if member.IncompleteReason != "" {
			content += "\n\n不完整原因：" + safeMarkdown(member.IncompleteReason, maxIdentifierRunes)
		}
		elements = append(elements, markdown(content))
	}
	events := trace.Events
	if len(events) > maxTraceEvents {
		events = events[:maxTraceEvents]
	}
	for index, event := range events {
		content := fmt.Sprintf(
			"**时间线 %d · %s · %s**\n\n%s",
			index+1, formatTraceEventTime(event.EventTime), safeMarkdown(event.MemberID, maxIdentifierRunes),
			safeMarkdown(event.Message, maxStatementRunes),
		)
		if event.Level != "" || event.Operation != "" {
			content += "\n\n级别：" + safeMarkdown(valueOrDash(event.Level), maxAggregateRunes) +
				"｜操作：" + safeMarkdown(valueOrDash(event.Operation), maxAggregateRunes)
		}
		elements = append(elements, markdown(content))
	}
	if len(trace.Events) > len(events) {
		elements = append(elements, markdown(fmt.Sprintf("其余 %d 条脱敏事件未在卡片中展示。", len(trace.Events)-len(events))))
	}
	return elements
}

func shortFingerprint(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12] + "…"
}

func formatTraceEventTime(value time.Time) string {
	if value.IsZero() {
		return "时间未知"
	}
	return value.In(shanghaiLocation).Format("01-02 15:04:05.000")
}

func appendCauseSummary(elements []any, analysis *domain.CauseAnalysis) []any {
	if analysis == nil || analysis.Status == domain.CauseAnalysisSkippedNoSpike {
		return elements
	}
	elements = append(elements, cardDivider{Tag: "hr"})
	if analysis.Status == domain.CauseAnalysisUnavailable {
		return append(elements, markdown("**变更关联：** 不可用\n\n未配置或无法读取治理变更目录；既有日志结论不受影响。"))
	}
	if len(analysis.Hypotheses) == 0 {
		return append(elements, markdown("**变更关联：** 证据不足\n\n没有形成可验证的变更关联候选。"))
	}
	limit := len(analysis.Hypotheses)
	if limit > maxCauseSummaryItems {
		limit = maxCauseSummaryItems
	}
	for index, hypothesis := range analysis.Hypotheses[:limit] {
		content := fmt.Sprintf(
			"**变更关联候选 %d（%s，证据强度 %.0f%%）：** %s\n\n方法：%s。关联候选不等于根因确认。",
			index+1,
			causeVerdictLabel(hypothesis.Verdict),
			hypothesis.Confidence*100,
			safeMarkdown(hypothesis.Statement, maxStatementRunes),
			safeMarkdown(hypothesis.ConfidenceMethod, maxAggregateRunes),
		)
		elements = append(elements, markdown(content))
	}
	return elements
}

func appendIncidentTimelineSummary(elements []any, timeline *domain.IncidentTimeline) []any {
	if timeline == nil || timeline.Status == domain.TimelineSkippedNoSpike {
		return elements
	}
	if timeline.Status == domain.TimelineUnavailable {
		return append(elements, markdown("**跨信号时间线：** 当前指标/Trace 聚合不可用，日志与变更结论不受影响。"))
	}
	status := "完整"
	if timeline.Status == domain.TimelineInconclusive {
		status = "不完整"
	}
	content := fmt.Sprintf("**跨信号时间线（%s）：**", status)
	items := timeline.Items
	if len(items) > maxTimelineSummaryItems {
		items = items[:maxTimelineSummaryItems]
	}
	for _, item := range items {
		content += fmt.Sprintf("\n\n- %s · %s", item.StartedAt.In(shanghaiLocation).Format("15:04:05"), safeMarkdown(item.Statement, maxStatementRunes))
	}
	content += "\n\n限制：时间相关不等于因果证明。"
	return append(elements, markdown(content))
}

func appendIncidentTimelineEvidence(elements []any, timeline *domain.IncidentTimeline) []any {
	if timeline == nil || timeline.Status == domain.TimelineSkippedNoSpike {
		return elements
	}
	if timeline.Status == domain.TimelineUnavailable {
		return append(elements, markdown("**跨信号证据：** 当前不可用；未展示原始 Span、TraceID、指标标签或 Provider 错误。"))
	}
	items := timeline.Items
	if len(items) > maxTimelineEvidenceItems {
		items = items[:maxTimelineEvidenceItems]
	}
	if len(items) == 0 {
		return append(elements, markdown("**跨信号证据：** 受控数据不足，未形成时间线条目。"))
	}
	elements = append(elements, markdown("**跨信号证据时间线：**"))
	for index, item := range items {
		state := "上下文"
		if item.Kind != domain.TimelineItemChange {
			state = "未达到异常阈值"
			if item.Anomalous {
				state = "达到异常阈值"
			}
		}
		content := fmt.Sprintf(
			"**时间线 %d · %s · %s**\n\n窗口：%s\n\n%s",
			index+1,
			safeMarkdown(string(item.Kind), maxAggregateRunes),
			state,
			formatRange(item.StartedAt, item.CompletedAt),
			safeMarkdown(item.Statement, maxStatementRunes),
		)
		elements = append(elements, markdown(content))
	}
	return append(elements, markdown("限制：时间相关不等于因果证明；当前仅展示受控聚合，不包含原始 Span、TraceID 或指标标签。"))
}

func appendCauseEvidence(elements []any, analysis *domain.CauseAnalysis) []any {
	if analysis == nil || analysis.Status == domain.CauseAnalysisSkippedNoSpike {
		return elements
	}
	elements = append(elements, cardDivider{Tag: "hr"})
	if analysis.Status == domain.CauseAnalysisUnavailable {
		return append(elements, markdown("**支持与反证账本：** 不可用\n\n变更源未配置或读取失败，不能据此推断没有变更。"))
	}
	if len(analysis.MissingInputs) > 0 {
		missing := analysis.MissingInputs
		if len(missing) > maxCauseEvidenceItems {
			missing = missing[:maxCauseEvidenceItems]
		}
		elements = append(elements, markdown(fmt.Sprintf(
			"**未知输入：** %s",
			safeMarkdown(strings.Join(missing, "、"), maxStatementRunes),
		)))
	}

	changes := analysis.Changes
	if len(changes) > maxCauseEvidenceItems {
		changes = changes[:maxCauseEvidenceItems]
	}
	for index, change := range changes {
		version := valueOrDash(change.ToVersion)
		if change.FromVersion != "" {
			version = change.FromVersion + " → " + valueOrDash(change.ToVersion)
		}
		content := fmt.Sprintf(
			"**治理变更 %d · %s：** %s\n\n时间：%s\n\n版本：%s｜负责人：%s\n\n%s",
			index+1,
			safeMarkdown(change.ID, maxIdentifierRunes),
			safeMarkdown(string(change.Kind), maxAggregateRunes),
			formatRange(change.StartedAt, change.CompletedAt),
			safeMarkdown(version, maxAggregateRunes),
			safeMarkdown(change.Owner, maxAggregateRunes),
			safeMarkdown(change.Summary, maxStatementRunes),
		)
		elements = append(elements, markdown(content))
	}

	ledger := make(map[string]domain.EvidenceLedgerEntry, len(analysis.Ledger))
	for _, entry := range analysis.Ledger {
		ledger[entry.ID] = entry
	}
	hypotheses := analysis.Hypotheses
	if len(hypotheses) > maxCauseEvidenceItems {
		hypotheses = hypotheses[:maxCauseEvidenceItems]
	}
	for index, hypothesis := range hypotheses {
		content := fmt.Sprintf(
			"**候选 %d（%s，证据强度 %.0f%%）：** %s",
			index+1,
			causeVerdictLabel(hypothesis.Verdict),
			hypothesis.Confidence*100,
			safeMarkdown(hypothesis.Statement, maxStatementRunes),
		)
		if entry, ok := representativeLedgerEntry(ledger, hypothesis.SupportEntryIDs, []domain.EvidenceTestResult{
			domain.EvidenceTestUnknown,
			domain.EvidenceTestFail,
			domain.EvidenceTestPass,
		}); ok {
			content += fmt.Sprintf("\n\n支持测试（%s）：%s", evidenceTestLabel(entry.Result), safeMarkdown(entry.Statement, maxStatementRunes))
		}
		if entry, ok := representativeLedgerEntry(ledger, hypothesis.CounterEntryIDs, []domain.EvidenceTestResult{
			domain.EvidenceTestPass,
			domain.EvidenceTestUnknown,
			domain.EvidenceTestFail,
		}); ok {
			content += fmt.Sprintf("\n\n反证测试（%s）：%s", evidenceTestLabel(entry.Result), safeMarkdown(entry.Statement, maxStatementRunes))
		}
		content += "\n\n限制：关联候选不等于因果证明。"
		elements = append(elements, markdown(content))
	}
	return elements
}

func representativeLedgerEntry(entries map[string]domain.EvidenceLedgerEntry, ids []string, priority []domain.EvidenceTestResult) (domain.EvidenceLedgerEntry, bool) {
	for _, result := range priority {
		for _, id := range ids {
			if entry, exists := entries[id]; exists && entry.Result == result {
				return entry, true
			}
		}
	}
	return domain.EvidenceLedgerEntry{}, false
}

func causeVerdictLabel(verdict domain.CauseVerdict) string {
	switch verdict {
	case domain.CauseVerdictSupportedCandidate:
		return "有支持"
	case domain.CauseVerdictRefuted:
		return "已反证"
	default:
		return "不确定"
	}
}

func evidenceTestLabel(result domain.EvidenceTestResult) string {
	switch result {
	case domain.EvidenceTestPass:
		return "成立"
	case domain.EvidenceTestFail:
		return "未发现"
	default:
		return "未知"
	}
}

func renderFailedCard(investigation domain.Investigation) cardDocument {
	elements := summaryElements(investigation)
	// LastError is intentionally excluded: dependency errors are internal
	// diagnostics and may contain provider or deployment details.
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 调查失败\n\n未生成可验证结论。请稍后重新运行；如持续失败，请携带调查 ID 联系维护人员。"),
		buttonRow(investigation.ID, buttonSpec{label: "重新运行", action: domain.ActionRerun, style: "primary"}),
	)
	return newCard("日志调查失败", "red", elements)
}

func renderCancelledCard(investigation domain.Investigation) cardDocument {
	elements := summaryElements(investigation)
	if investigation.LastError == domain.CancelReasonExternalQueryOutcomeUnknown {
		// The stable reason code is used only as a branch condition and is never
		// rendered. It cannot contain provider or deployment diagnostics.
		elements = append(elements,
			cardDivider{Tag: "hr"},
			markdown("**状态：** 已取消，但查询结果未知\n\n取消已生效，系统不会接收或自动重试上一次结果；该只读查询可能已经到达云端。确认潜在重复查询成本后，才能新建调查。"),
			buttonRow(investigation.ID, buttonSpec{label: "确认成本并重新运行", action: domain.ActionRerunWithCostAck, style: "primary"}),
		)
		return newCard("日志调查已取消：需要确认", "orange", elements)
	}
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 已取消\n\n该调查不会再提交新的有效结果。"),
		buttonRow(investigation.ID, buttonSpec{label: "重新运行", action: domain.ActionRerun, style: "primary"}),
	)
	return newCard("日志调查已取消", "grey", elements)
}

func renderNeedsReviewCard(investigation domain.Investigation) cardDocument {
	elements := summaryElements(investigation)
	// LastError is intentionally excluded: the review state communicates only
	// the bounded recovery contract, never provider or deployment diagnostics.
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 需要人工确认\n\n上一次只读查询可能已执行但结果未落盘；系统没有自动重试；确认潜在重复查询成本后可新建调查。"),
		buttonRow(investigation.ID, buttonSpec{label: "确认成本并重新运行", action: domain.ActionRerunWithCostAck, style: "primary"}),
	)
	return newCard("日志调查需要确认", "orange", elements)
}

func summaryElements(investigation domain.Investigation) []any {
	request := investigation.Request
	elements := []any{markdown(fmt.Sprintf(
		"**调查 ID：** %s\n\n**范围：** %s / %s\n\n**时间窗口：** %s",
		safeMarkdown(investigation.ID, maxIdentifierRunes),
		safeMarkdown(request.Service, maxAggregateRunes),
		safeMarkdown(request.Environment, maxAggregateRunes),
		formatRange(request.StartTime, request.EndTime),
	))}
	if request.Problem != nil && request.Problem.Text != "" {
		elements = append(elements, markdown("**用户描述（未验证）：** "+safeMarkdown(request.Problem.Text, maxStatementRunes)))
	}
	return elements
}

type buttonSpec struct {
	label  string
	action domain.InvestigationAction
	style  string
}

func buttonRow(investigationID string, specs ...buttonSpec) cardColumnSet {
	columns := make([]cardColumn, 0, len(specs))
	for _, spec := range specs {
		style := spec.style
		if style == "" {
			style = "default"
		}
		button := cardButton{
			Tag:   "button",
			Text:  cardText{Tag: "plain_text", Content: spec.label},
			Type:  style,
			Width: "fill",
			Behaviors: []cardBehavior{{
				Type: "callback",
				Value: map[string]string{
					"action":           string(spec.action),
					"investigation_id": investigationID,
				},
			}},
		}
		columns = append(columns, cardColumn{Tag: "column", Width: "weighted", Weight: 1, Elements: []cardButton{button}})
	}
	return cardColumnSet{Tag: "column_set", HorizontalSpacing: "8px", Columns: columns}
}

func newCard(title, template string, elements []any) cardDocument {
	return cardDocument{
		Schema: "2.0",
		Header: cardHeader{Title: cardText{Tag: "plain_text", Content: truncateRunes(cleanText(title), 80)}, Template: template},
		Body:   cardBody{Elements: elements},
	}
}

func markdown(content string) cardMarkdown {
	return cardMarkdown{Tag: "markdown", Content: content}
}

func marshalCard(card cardDocument) (string, error) {
	payload, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("encode Feishu card: %w", err)
	}
	if len(payload) >= maxCardBytes {
		return "", fmt.Errorf("Feishu card is %d bytes; limit is below %d", len(payload), maxCardBytes)
	}
	return string(payload), nil
}

func safeMarkdown(value string, limit int) string {
	value = truncateRunes(cleanText(value), limit)
	replacer := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_",
		"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)",
		"<", "\\<", ">", "\\>", "#", "\\#", "!", "\\!", "|", "\\|",
	)
	return replacer.Replace(value)
}

func cleanText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return ' '
	}, value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func boundedFindings(values []domain.Finding) []domain.Finding {
	if len(values) <= maxFindingItems {
		return values
	}
	selected := append([]domain.Finding(nil), values[:maxFindingItems]...)
	for _, finding := range values[maxFindingItems:] {
		if finding.Code == "instance_distribution" {
			selected[len(selected)-1] = finding
			break
		}
	}
	return selected
}

func boundedRecommendations(values []domain.Recommendation) []domain.Recommendation {
	if len(values) > maxRecommendationItems {
		return values[:maxRecommendationItems]
	}
	return values
}

func formatBuckets(title string, values []domain.CountBucket) string {
	if len(values) == 0 {
		return ""
	}
	if len(values) > maxAggregateItems {
		values = values[:maxAggregateItems]
	}
	var builder strings.Builder
	builder.WriteString("**")
	builder.WriteString(title)
	builder.WriteString("：**")
	for _, item := range values {
		builder.WriteString("\n\n- ")
		builder.WriteString(safeMarkdown(valueOrDash(item.Label), maxAggregateRunes))
		builder.WriteString(fmt.Sprintf("：%d", item.Count))
	}
	return builder.String()
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func formatRange(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return "-"
	}
	return start.In(shanghaiLocation).Format("2006-01-02 15:04:05") + " ~ " + end.In(shanghaiLocation).Format("2006-01-02 15:04:05")
}

func formatBytes(value int64) string {
	if value < 0 {
		return "-"
	}
	const (
		kib = int64(1024)
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case value >= gib:
		return fmt.Sprintf("%.2f GiB", float64(value)/float64(gib))
	case value >= mib:
		return fmt.Sprintf("%.2f MiB", float64(value)/float64(mib))
	case value >= kib:
		return fmt.Sprintf("%.2f KiB", float64(value)/float64(kib))
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func fixedShanghaiLocation() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}
