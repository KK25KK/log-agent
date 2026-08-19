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
	maxCardBytes           = 30 * 1024
	maxFindingItems        = 6
	maxRecommendationItems = 3
	maxEvidenceItems       = 2
	maxCauseSummaryItems   = 1
	maxCauseEvidenceItems  = 3
	maxAggregateItems      = 5
	maxStatementRunes      = 480
	maxAggregateRunes      = 96
	maxIdentifierRunes     = 96
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
	case domain.ActionViewReportCard:
		return renderReportCard(result.Investigation), nil
	case domain.ActionViewEvidenceCard:
		return renderEvidenceCard(result.Investigation)
	default:
		return cardDocument{}, fmt.Errorf("unsupported action view %q", result.View)
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
	for index, finding := range boundedFindings(report.Findings) {
		conclusion := "非确定性"
		if finding.Conclusive {
			conclusion = "确定性"
		}
		content := fmt.Sprintf("**结论 %d（%s，置信度 %.0f%%）：** %s", index+1, conclusion, finding.Confidence*100, safeMarkdown(finding.Statement, maxStatementRunes))
		elements = append(elements, markdown(content))
	}
	elements = appendCauseSummary(elements, report.CauseAnalysis)
	for index, recommendation := range boundedRecommendations(report.Recommendations) {
		elements = append(elements, markdown(fmt.Sprintf("**建议 %d：** %s", index+1, safeMarkdown(recommendation.Statement, maxStatementRunes))))
	}
	elements = append(elements, buttonRow(investigation.ID,
		buttonSpec{label: "查看证据", action: domain.ActionViewEvidence, style: "primary"},
		buttonSpec{label: "扩大时间窗", action: domain.ActionExpandWindow},
		buttonSpec{label: "重新运行", action: domain.ActionRerun},
	))
	return newCard(title, template, elements)
}

func renderEvidenceCard(investigation domain.Investigation) (cardDocument, error) {
	if investigation.Report == nil {
		return cardDocument{}, errors.New("report is required for evidence view")
	}
	elements := summaryElements(investigation)
	elements = append(elements, cardDivider{Tag: "hr"})
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
		elements = append(elements, markdown(content))
		if buckets := formatBuckets("新增/主要错误模式", item.ErrorPatterns); buckets != "" {
			elements = append(elements, markdown(buckets))
		}
		if buckets := formatBuckets("实例分布", item.Instances); buckets != "" {
			elements = append(elements, markdown(buckets))
		}
	}
	elements = appendCauseEvidence(elements, investigation.Report.CauseAnalysis)
	elements = append(elements, buttonRow(investigation.ID,
		buttonSpec{label: "返回报告", action: domain.ActionViewReport, style: "primary"},
		buttonSpec{label: "扩大时间窗", action: domain.ActionExpandWindow},
		buttonSpec{label: "重新运行", action: domain.ActionRerun},
	))
	return newCard("日志调查证据", "blue", elements), nil
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
	elements = append(elements,
		cardDivider{Tag: "hr"},
		markdown("**状态：** 已取消\n\n该调查不会再提交新的有效结果。"),
		buttonRow(investigation.ID, buttonSpec{label: "重新运行", action: domain.ActionRerun, style: "primary"}),
	)
	return newCard("日志调查已取消", "grey", elements)
}

func summaryElements(investigation domain.Investigation) []any {
	request := investigation.Request
	return []any{markdown(fmt.Sprintf(
		"**调查 ID：** %s\n\n**范围：** %s / %s\n\n**时间窗口：** %s",
		safeMarkdown(investigation.ID, maxIdentifierRunes),
		safeMarkdown(request.Service, maxAggregateRunes),
		safeMarkdown(request.Environment, maxAggregateRunes),
		formatRange(request.StartTime, request.EndTime),
	))}
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
