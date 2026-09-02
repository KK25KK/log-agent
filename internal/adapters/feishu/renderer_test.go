package feishu

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestRendererUsesJSON2AndOnlyAllowedButtons(t *testing.T) {
	tests := []struct {
		name    string
		kind    domain.DeliveryKind
		item    domain.Investigation
		actions []string
	}{
		{name: "queued", kind: domain.DeliveryQueued, item: cardInvestigation(domain.StatusQueued), actions: []string{"cancel"}},
		{name: "running", kind: domain.DeliveryRunning, item: cardInvestigation(domain.StatusRunning), actions: []string{"cancel"}},
		{name: "succeeded", kind: domain.DeliverySucceeded, item: cardInvestigation(domain.StatusSucceeded), actions: []string{"expand_window", "rerun", "view_evidence"}},
		{name: "failed", kind: domain.DeliveryFailed, item: cardInvestigation(domain.StatusFailed), actions: []string{"rerun"}},
		{name: "cancelled", kind: domain.DeliveryCancelled, item: cardInvestigation(domain.StatusCancelled), actions: []string{"rerun"}},
		{name: "needs review", kind: domain.DeliveryNeedsReview, item: cardInvestigation(domain.StatusNeedsReview), actions: []string{"rerun_with_cost_ack"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			card, err := renderDeliveryCard(domain.DeliveryJob{Kind: test.kind, Investigation: test.item})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := marshalCard(card)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &document); err != nil {
				t.Fatal(err)
			}
			if document["schema"] != "2.0" {
				t.Fatalf("want JSON 2.0 card, got %#v", document["schema"])
			}
			got := collectButtonActions(t, document)
			sort.Strings(got)
			sort.Strings(test.actions)
			if strings.Join(got, ",") != strings.Join(test.actions, ",") {
				t.Fatalf("actions=%v want %v", got, test.actions)
			}
		})
	}
}

func TestRendererShowsBoundedGovernedTraceTimelineWithoutRawTraceID(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	item.Report.Summary = nil
	item.Report.CauseAnalysis = nil
	item.Report.IncidentTimeline = nil
	item.Report.RunbookGuidance = nil
	item.Report.TraceInvestigation = &domain.TraceInvestigation{
		Status: domain.TraceInvestigationComplete, Complete: true, TraceIDFingerprint: strings.Repeat("a", 64),
		Members: []domain.TraceMemberSummary{{MemberID: "dam-server", Status: domain.TraceMemberComplete, EventCount: 1, APICalls: 1}},
		Events:  []domain.TraceEvent{{MemberID: "dam-server", EventTime: time.Now().UTC(), Level: "error", Message: "[TRACE_ID] processing failed"}},
		AnchorSet: &domain.RuntimeAnchorSet{
			Version: domain.RuntimeAnchorVersion, Status: domain.RuntimeAnchorsComplete, SourceEventCount: 1,
			AnchoredEventCount: 1, Limit: domain.RuntimeAnchorGlobalLimit,
			Anchors: []domain.RuntimeAnchor{{Kind: domain.RuntimeAnchorStackFrame, File: "internal/payment/client.go", Line: 87, Symbol: "example.com/dam/internal/payment.(*Client).Charge"}},
		},
		TotalAPICalls: 1, TotalProcessedBytes: 128,
	}
	item.Report.CodeInvestigation = &domain.CodeInvestigation{
		Version: domain.CodeEvidenceVersion, Status: domain.CodeInvestigationComplete, Complete: true,
		Deployment:  &domain.DeploymentEvidence{Status: domain.DeploymentComplete, RepositoryID: "dam", CommitSHA: strings.Repeat("c", 40)},
		Matches:     []domain.CodeMatch{{AnchorID: "anchor-safe", File: "internal/payment/client.go", MatchLine: 87, Snippet: "private source must stay local", ChangedSincePrevious: true}},
		DiffChecked: true, ChangedFiles: []string{"internal/payment/client.go"},
	}
	card, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if !strings.Contains(value, "Trace 调查证据") || !strings.Contains(value, "dam-server") || !strings.Contains(value, "TRACE") ||
		!strings.Contains(value, "检索锚点") || !strings.Contains(value, "internal/payment/client.go:87") || !strings.Contains(value, "不代表根因") {
		t.Fatalf("Trace evidence was not rendered: %s", value)
	}
	if strings.Contains(value, "trace-12345678") {
		t.Fatalf("raw TraceID leaked into card: %s", value)
	}
	if !strings.Contains(value, "代码证据") || !strings.Contains(value, "dam") || !strings.Contains(value, "与上一部署版本有文件变更") || strings.Contains(value, "private source must stay local") {
		t.Fatalf("code evidence projection is invalid: %s", value)
	}
}

func TestEvidenceViewHasBackAndFollowUpActions(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	card, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &document); err != nil {
		t.Fatal(err)
	}
	actions := collectButtonActions(t, document)
	sort.Strings(actions)
	want := []string{"expand_window", "rerun", "view_report"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("evidence actions=%v want %v", actions, want)
	}
	if !strings.Contains(payload, `payment\\_timeout`) || !strings.Contains(payload, "pod-1") {
		t.Fatalf("evidence aggregates are missing: %s", payload)
	}
}

func TestEvidenceCardRendersCountOnlyScopeWithoutFakeDimensions(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	for index := range item.Report.Evidence {
		item.Report.Evidence[index].TemplateID = domain.ErrorCountTemplateID
		item.Report.Evidence[index].TopError = ""
		item.Report.Evidence[index].TopErrorCount = 0
		item.Report.Evidence[index].ErrorPatterns = nil
		item.Report.Evidence[index].Instances = nil
	}
	card, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"分析范围：仅错误计数", "错误类型：本模板不适用", "实例分布：本模板不适用"} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("count-only card lacks %q: %s", expected, payload)
		}
	}
	if strings.Contains(payload, "Top 错误") || strings.Contains(payload, `payment\_timeout`) || strings.Contains(payload, "pod-1") {
		t.Fatalf("count-only card leaked dimensions: %s", payload)
	}
}

func TestReportCardRendersGovernedAISummaryAndEscapesText(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	item.Report.Summary = &domain.ReportSummary{
		Status: domain.SummaryGenerated, Mode: domain.SummaryModeMock,
		Phenomenon: "payment_timeout *增长*", PossibleCause: "发布候选 <v2>",
		EvidenceNotes: []domain.SummaryEvidenceNote{{Statement: "current 比 baseline 高", EvidenceIDs: []string{"ev_current"}}},
	}
	card := renderReportCard(item)
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "AI 证据摘要（Mock）") || !strings.Contains(payload, `payment\\_timeout`) || !strings.Contains(payload, `\\*增长\\*`) || !strings.Contains(payload, "发布候选") {
		t.Fatalf("governed summary missing or unescaped: %s", payload)
	}
}

func TestRendererOmitsLastErrorAndEscapesUntrustedMarkdown(t *testing.T) {
	item := cardInvestigation(domain.StatusFailed)
	item.LastError = "TOP_SECRET_LAST_ERROR raw SQL: * | select * from log"
	card, err := renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliveryFailed, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "TOP_SECRET_LAST_ERROR") || strings.Contains(payload, "select * from log") {
		t.Fatalf("internal failure detail leaked into card: %s", payload)
	}

	item = cardInvestigation(domain.StatusSucceeded)
	item.Report.Findings[0].Statement = "**admin** [click](javascript:alert(1)) <script>"
	card, err = renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliverySucceeded, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err = marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "**admin**") || strings.Contains(payload, "<script>") {
		t.Fatalf("untrusted markdown was not escaped: %s", payload)
	}
}

func TestNeedsReviewDeliveryAndActionExplainManualRerunWithoutLeakingError(t *testing.T) {
	item := cardInvestigation(domain.StatusNeedsReview)
	item.LastError = "TOP_SECRET_PROVIDER_FAILURE select * from raw_logs"

	for _, render := range []struct {
		name string
		card func() (cardDocument, error)
	}{
		{name: "delivery", card: func() (cardDocument, error) {
			return renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliveryNeedsReview, Investigation: item})
		}},
		{name: "action", card: func() (cardDocument, error) {
			return renderActionCard(domain.ActionResult{View: domain.ActionViewNeedsReviewCard, Investigation: item})
		}},
	} {
		t.Run(render.name, func(t *testing.T) {
			card, err := render.card()
			if err != nil {
				t.Fatal(err)
			}
			payload, err := marshalCard(card)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload, "上一次只读查询可能已执行但结果未落盘；系统没有自动重试；确认潜在重复查询成本后可新建调查") {
				t.Fatalf("recovery contract is missing: %s", payload)
			}
			if strings.Contains(payload, "TOP_SECRET_PROVIDER_FAILURE") || strings.Contains(payload, "raw_logs") {
				t.Fatalf("internal recovery detail leaked into card: %s", payload)
			}
			var document map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &document); err != nil {
				t.Fatal(err)
			}
			actions := collectButtonActions(t, document)
			if len(actions) != 1 || actions[0] != string(domain.ActionRerunWithCostAck) {
				t.Fatalf("needs-review actions=%v want [rerun_with_cost_ack]", actions)
			}
		})
	}
}

func TestCancelledUnknownOutcomeRequiresExplicitCostAcknowledgement(t *testing.T) {
	item := cardInvestigation(domain.StatusCancelled)
	item.LastError = domain.CancelReasonExternalQueryOutcomeUnknown
	card, err := renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliveryCancelled, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "该只读查询可能已经到达云端") || !strings.Contains(payload, "确认潜在重复查询成本") {
		t.Fatalf("cancelled unknown-outcome warning is missing: %s", payload)
	}
	if strings.Contains(payload, domain.CancelReasonExternalQueryOutcomeUnknown) {
		t.Fatalf("stable internal reason code leaked into card: %s", payload)
	}
	var document map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &document); err != nil {
		t.Fatal(err)
	}
	actions := collectButtonActions(t, document)
	if len(actions) != 1 || actions[0] != string(domain.ActionRerunWithCostAck) {
		t.Fatalf("cancelled unknown-outcome actions=%v", actions)
	}
}

func TestRendererBoundsExternalCollectionsAndCardSize(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	long := strings.Repeat("外部日志字段*[]<>", 5000)
	item.Report.Findings = make([]domain.Finding, 100)
	item.Report.Recommendations = make([]domain.Recommendation, 100)
	item.Report.Evidence = make([]domain.Evidence, 100)
	for index := range item.Report.Findings {
		item.Report.Findings[index] = domain.Finding{Statement: long, Confidence: 1, Conclusive: true}
		item.Report.Recommendations[index] = domain.Recommendation{Statement: long}
		item.Report.Evidence[index] = domain.Evidence{
			ID: "ev", Name: long, Complete: true, TopError: long,
			ErrorPatterns: repeatedBuckets(long, 100), Instances: repeatedBuckets(long, 100),
		}
	}
	card, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= maxCardBytes {
		t.Fatalf("card exceeds limit: %d", len(payload))
	}
}

func TestReportCardKeepsInstanceDistributionWhenFindingsAreBounded(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	item.Report.Findings = make([]domain.Finding, 0, 8)
	for index := 0; index < 7; index++ {
		item.Report.Findings = append(item.Report.Findings, domain.Finding{
			Code: "candidate", Statement: fmt.Sprintf("候选错误模式 %d", index), Confidence: .5,
		})
	}
	item.Report.Findings = append(item.Report.Findings, domain.Finding{
		Code: "instance_distribution", Statement: "INSTANCE_DISTRIBUTION_MUST_REMAIN", Confidence: .95, Conclusive: true,
	})
	card, err := renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliverySucceeded, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "INSTANCE\\\\_DISTRIBUTION\\\\_MUST\\\\_REMAIN") {
		t.Fatalf("bounded report dropped the instance finding: %s", payload)
	}
}

func TestRendererShowsBoundedCauseSupportAndCounterEvidence(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	end := item.Request.EndTime
	change := domain.ChangeEvent{
		ID: "chg_release_1", ResourceID: "order-service-prod", Kind: domain.ChangeKindRelease,
		StartedAt: end.Add(-40 * time.Minute), CompletedAt: end.Add(-31 * time.Minute),
		FromVersion: "v1", ToVersion: "v2", Owner: "team_*ops*", Summary: "release [candidate] <unsafe>",
		AffectedInstances: []string{"pod-1"}, AffectedInstancesComplete: true,
	}
	support := domain.EvidenceLedgerEntry{
		ID: "test_support", HypothesisID: "hyp_release", Code: "affected_instance_concentration",
		Role: domain.EvidenceTestSupport, Result: domain.EvidenceTestPass, Weight: .3,
		Statement: "pod-1 覆盖当前错误的 66.7%", EvidenceIDs: []string{"ev_current", "ev_baseline"}, ChangeEventIDs: []string{change.ID},
	}
	counter := domain.EvidenceLedgerEntry{
		ID: "test_counter", HypothesisID: "hyp_release", Code: "no_instance_overlap",
		Role: domain.EvidenceTestCounter, Result: domain.EvidenceTestFail, Weight: .4,
		Statement: "未发现完整影响范围与实例分布零交集", EvidenceIDs: []string{"ev_current"}, ChangeEventIDs: []string{change.ID},
	}
	unknownSupport := domain.EvidenceLedgerEntry{
		ID: "test_unknown", HypothesisID: "hyp_release", Code: "baseline_shift",
		Role: domain.EvidenceTestSupport, Result: domain.EvidenceTestUnknown, Weight: .1,
		Statement: "基线实例 Top-K 未穷尽", EvidenceIDs: []string{"ev_baseline"}, ChangeEventIDs: []string{change.ID},
	}
	counterFound := domain.EvidenceLedgerEntry{
		ID: "test_counter_found", HypothesisID: "hyp_release", Code: "confounding_changes",
		Role: domain.EvidenceTestCounter, Result: domain.EvidenceTestPass, Weight: .1,
		Statement: "同一窗口存在多个候选变更", EvidenceIDs: []string{"ev_current"}, ChangeEventIDs: []string{change.ID},
	}
	item.Report.CauseAnalysis = &domain.CauseAnalysis{
		Status: domain.CauseAnalysisInconclusive, SourceVersion: "changes-v1", Changes: []domain.ChangeEvent{change},
		Ledger: []domain.EvidenceLedgerEntry{support, counter, unknownSupport, counterFound},
		Hypotheses: []domain.CauseHypothesis{{
			ID: "hyp_release", Code: "change_correlation", Statement: "v2 发布与错误突增存在可验证关联",
			Verdict: domain.CauseVerdictInconclusive, Confidence: .35, ConfidenceMethod: domain.CauseConfidenceMethod,
			SupportEntryIDs: []string{support.ID, unknownSupport.ID}, CounterEntryIDs: []string{counter.ID, counterFound.ID}, Limitations: []string{"correlation is not causation"},
		}},
	}

	reportCard, err := renderDeliveryCard(domain.DeliveryJob{Kind: domain.DeliverySucceeded, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	reportPayload, err := marshalCard(reportCard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reportPayload, "变更关联候选") || !strings.Contains(reportPayload, "关联候选不等于根因确认") {
		t.Fatalf("report card lacks bounded cause summary: %s", reportPayload)
	}

	evidenceCard, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	evidencePayload, err := marshalCard(evidenceCard)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"治理变更", "支持测试", "反证测试", "未知", "基线实例 Top-K 未穷尽", "同一窗口存在多个候选变更", "关联候选不等于因果证明"} {
		if !strings.Contains(evidencePayload, expected) {
			t.Fatalf("evidence card lacks %q: %s", expected, evidencePayload)
		}
	}
	if strings.Contains(evidencePayload, "team_*ops*") || strings.Contains(evidencePayload, "<unsafe>") {
		t.Fatalf("change metadata bypassed Markdown escaping: %s", evidencePayload)
	}
}

func TestRendererShowsBoundedCrossSignalTimelineAndCausalLimit(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	start := item.Report.Evidence[0].StartTime
	end := item.Report.Evidence[0].EndTime
	item.Report.IncidentTimeline = &domain.IncidentTimeline{
		Status: domain.TimelineComplete, MethodVersion: domain.OperationalSignalTimelineVersion,
		SourceVersion: "mock-operational-v1", SourceComplete: true,
		Items: []domain.IncidentTimelineItem{
			{
				ID: "tl-change", Kind: domain.TimelineItemChange, Code: "RELEASE",
				StartedAt: start.Add(-5 * time.Minute), CompletedAt: start.Add(-4 * time.Minute),
				Statement: "RELEASE 变更 chg-safe 在观察窗口内完成。",
			},
			{
				ID: "tl-metric", Kind: domain.TimelineItemMetric, Code: "ERROR_RATE",
				StartedAt: start, CompletedAt: end, Anomalous: true,
				Statement: "指标错误率从 2.0% 变化到 12.0%，达到异常阈值。",
			},
			{
				ID: "tl-trace", Kind: domain.TimelineItemTrace, Code: "LATENCY_P95",
				StartedAt: start, CompletedAt: end, Anomalous: true,
				Statement: "Trace P95 延迟从 120ms 变化到 420ms，达到异常阈值。",
			},
		},
	}
	reportPayload, err := marshalCard(renderReportCard(item))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"跨信号时间线", "指标错误率", "Trace P95", "时间相关不等于因果证明"} {
		if !strings.Contains(reportPayload, expected) {
			t.Fatalf("report card lacks %q: %s", expected, reportPayload)
		}
	}
	evidenceCard, err := renderActionCard(domain.ActionResult{View: domain.ActionViewEvidenceCard, Investigation: item})
	if err != nil {
		t.Fatal(err)
	}
	evidencePayload, err := marshalCard(evidenceCard)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"跨信号证据时间线", "达到异常阈值", "不包含原始 Span、TraceID 或指标标签"} {
		if !strings.Contains(evidencePayload, expected) {
			t.Fatalf("evidence card lacks %q: %s", expected, evidencePayload)
		}
	}
}

func TestReportCardRendersBoundedRunbookGuidanceAsPlainText(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	updatedAt := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	item.Report.RunbookGuidance = &domain.RunbookGuidance{
		Status:     domain.RunbookGuidanceComplete,
		DataSource: domain.RunbookGuidanceSourceSyntheticMock,
		Items: []domain.RunbookGuidanceItem{
			{
				Title: "核查 *支付* 依赖", Revision: "rev_[1]", Owner: "team_<ops>", UpdatedAt: updatedAt,
				Steps: []domain.RunbookStep{
					{Kind: domain.RunbookStepVerify, Instruction: "核对 payment_timeout 聚合"},
					{Kind: domain.RunbookStepObserve, Instruction: "观察 P95 延迟"},
					{Kind: domain.RunbookStepEscalate, Instruction: "联系支付值班团队"},
					{Kind: domain.RunbookStepVerify, Instruction: "STEP_FOUR_MUST_NOT_RENDER"},
				},
			},
			{Title: "第二份 SOP", Revision: "rev-2", Owner: "team-two", UpdatedAt: updatedAt, Steps: []domain.RunbookStep{{Kind: domain.RunbookStepObserve, Instruction: "观察依赖恢复"}}},
			{Title: "THIRD_ITEM_MUST_NOT_RENDER", Revision: "rev-3", Owner: "team-three", UpdatedAt: updatedAt, Steps: []domain.RunbookStep{{Kind: domain.RunbookStepVerify, Instruction: "不可见"}}},
		},
	}

	card := renderReportCard(item)
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	content := markdownContents(card)
	for _, expected := range []string{
		"受控 SOP 参考（Mock）", "核查 \\*支付\\* 依赖", "rev\\_\\[1\\]", "team\\_\\<ops\\>",
		"2026-08-24 16:00:00", "【核对】", "【观察】", "【升级联系】", "第二份 SOP",
		"仅供人工核查，不会自动执行处置",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("runbook card lacks %q: %s", expected, content)
		}
	}
	for _, forbidden := range []string{"STEP_FOUR_MUST_NOT_RENDER", "THIRD_ITEM_MUST_NOT_RENDER"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("runbook limit leaked %q: %s", forbidden, content)
		}
	}
	recommendationIndex := strings.Index(content, "**建议 1：**")
	runbookIndex := strings.Index(content, "**受控 SOP 参考（Mock）：**")
	if recommendationIndex < 0 || runbookIndex <= recommendationIndex {
		t.Fatalf("runbook guidance must follow deterministic recommendations: %s", content)
	}
	if len(payload) >= maxCardBytes {
		t.Fatalf("card exceeds limit: %d", len(payload))
	}
	var document map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &document); err != nil {
		t.Fatal(err)
	}
	actions := collectButtonActions(t, document)
	sort.Strings(actions)
	want := []string{"expand_window", "rerun", "view_evidence"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("runbook introduced an action: got %v want %v", actions, want)
	}
}

func TestReportCardTruncatesRunbookText(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	longTitle := strings.Repeat("标题", maxStatementRunes) + "TITLE_TAIL_MUST_NOT_RENDER"
	longInstruction := strings.Repeat("核查", maxStatementRunes) + "STEP_TAIL_MUST_NOT_RENDER"
	item.Report.RunbookGuidance = &domain.RunbookGuidance{
		Status:     domain.RunbookGuidanceComplete,
		DataSource: domain.RunbookGuidanceSourceSyntheticMock,
		Items: []domain.RunbookGuidanceItem{{
			Title: longTitle, Revision: strings.Repeat("r", maxIdentifierRunes+20) + "REVISION_TAIL_MUST_NOT_RENDER",
			Owner:     strings.Repeat("o", maxAggregateRunes+20) + "OWNER_TAIL_MUST_NOT_RENDER",
			UpdatedAt: time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC),
			Steps:     []domain.RunbookStep{{Kind: domain.RunbookStepVerify, Instruction: longInstruction}},
		}},
	}
	card := renderReportCard(item)
	payload, err := marshalCard(card)
	if err != nil {
		t.Fatal(err)
	}
	content := markdownContents(card)
	for _, forbidden := range []string{"TITLE_TAIL_MUST_NOT_RENDER", "STEP_TAIL_MUST_NOT_RENDER", "REVISION_TAIL_MUST_NOT_RENDER", "OWNER_TAIL_MUST_NOT_RENDER"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("runbook text was not truncated: %q", forbidden)
		}
	}
	if !strings.Contains(content, "…") || len(payload) >= maxCardBytes {
		t.Fatalf("runbook truncation/card-size guard is missing: bytes=%d", len(payload))
	}
}

func TestReportCardUsesFixedRunbookStatusMessages(t *testing.T) {
	tests := []struct {
		name     string
		status   domain.RunbookGuidanceStatus
		expected string
		mustOmit bool
	}{
		{name: "no match", status: domain.RunbookGuidanceNoMatch, expected: "当前受控目录未匹配到适用条目；这不代表企业不存在相关 SOP"},
		{name: "inconclusive", status: domain.RunbookGuidanceInconclusive, expected: "当前目录结果不完整，暂不展示 SOP 条目；不能据此推断没有相关 SOP"},
		{name: "unavailable", status: domain.RunbookGuidanceUnavailable, expected: "当前不可用；既有调查结论和建议不受影响"},
		{name: "skipped", status: domain.RunbookGuidanceSkippedNoTrigger, mustOmit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := cardInvestigation(domain.StatusSucceeded)
			item.Report.RunbookGuidance = &domain.RunbookGuidance{
				Status:        test.status,
				DataSource:    domain.RunbookGuidanceSourceSyntheticMock,
				MissingInputs: []string{"TOP_SECRET_PROVIDER_DETAIL"},
				Items:         []domain.RunbookGuidanceItem{{Title: "TOP_SECRET_PROVIDER_TITLE"}},
			}
			card := renderReportCard(item)
			if _, err := marshalCard(card); err != nil {
				t.Fatal(err)
			}
			content := markdownContents(card)
			if test.mustOmit {
				if strings.Contains(content, "受控 SOP 参考") {
					t.Fatalf("skipped runbook guidance was rendered: %s", content)
				}
				return
			}
			if !strings.Contains(content, test.expected) || !strings.Contains(content, "仅供人工核查，不会自动执行处置") {
				t.Fatalf("fixed status message missing: %s", content)
			}
			if strings.Contains(content, "TOP_SECRET_PROVIDER") {
				t.Fatalf("runbook provider detail leaked: %s", content)
			}
		})
	}
}

func TestReportCardDoesNotPresentUnknownRunbookSourceAsGovernedEnterpriseKnowledge(t *testing.T) {
	item := cardInvestigation(domain.StatusSucceeded)
	item.Report.RunbookGuidance = &domain.RunbookGuidance{
		Status: domain.RunbookGuidanceComplete,
		Items: []domain.RunbookGuidanceItem{{
			Title: "UNTRUSTED_SOURCE_ITEM_MUST_NOT_RENDER",
			Steps: []domain.RunbookStep{{Instruction: "UNTRUSTED_SOURCE_STEP_MUST_NOT_RENDER"}},
		}},
	}

	content := markdownContents(renderReportCard(item))
	if !strings.Contains(content, "SOP 参考（来源未确认）") || !strings.Contains(content, "当前不可用") {
		t.Fatalf("unknown runbook source is not labeled explicitly: %s", content)
	}
	if strings.Contains(content, "**受控 SOP 参考：**") {
		t.Fatalf("unknown runbook source was presented as governed enterprise knowledge: %s", content)
	}
	if strings.Contains(content, "UNTRUSTED_SOURCE") {
		t.Fatalf("unknown runbook source content was rendered: %s", content)
	}
}

func markdownContents(card cardDocument) string {
	contents := make([]string, 0, len(card.Body.Elements))
	for _, element := range card.Body.Elements {
		if item, ok := element.(cardMarkdown); ok {
			contents = append(contents, item.Content)
		}
	}
	return strings.Join(contents, "\n")
}

func collectButtonActions(t *testing.T, value interface{}) []string {
	t.Helper()
	actions := make([]string, 0)
	var walk func(interface{})
	walk = func(current interface{}) {
		switch typed := current.(type) {
		case []interface{}:
			for _, child := range typed {
				walk(child)
			}
		case map[string]interface{}:
			if typed["tag"] == "button" {
				behaviors, ok := typed["behaviors"].([]interface{})
				if !ok || len(behaviors) != 1 {
					t.Fatalf("button lacks one callback behavior: %#v", typed)
				}
				behavior := behaviors[0].(map[string]interface{})
				if behavior["type"] != "callback" {
					t.Fatalf("unexpected behavior: %#v", behavior)
				}
				callbackValue := behavior["value"].(map[string]interface{})
				if len(callbackValue) != 2 || callbackValue["investigation_id"] != "inv_card" {
					t.Fatalf("button value is not closed: %#v", callbackValue)
				}
				actions = append(actions, callbackValue["action"].(string))
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return actions
}

func cardInvestigation(status domain.Status) domain.Investigation {
	end := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	current := domain.Evidence{
		ID: "ev_current", Name: "current", StartTime: end.Add(-30 * time.Minute), EndTime: end,
		Complete: true, ErrorCount: 120, TopError: "payment_timeout", TopErrorCount: 90,
		ProcessedBytes: 1024, ErrorPatterns: []domain.CountBucket{{Label: "payment_timeout", Count: 90}},
		Instances: []domain.CountBucket{{Label: "pod-1", Count: 80}},
	}
	baseline := current
	baseline.ID = "ev_baseline"
	baseline.Name = "baseline"
	baseline.StartTime = end.Add(-60 * time.Minute)
	baseline.EndTime = end.Add(-30 * time.Minute)
	baseline.ErrorCount = 20
	baseline.TopErrorCount = 5
	return domain.Investigation{
		ID: "inv_card", Status: status,
		Request: domain.InvestigationRequest{
			Service: "order-service", Environment: "prod",
			StartTime: end.Add(-30 * time.Minute), EndTime: end,
		},
		Report: &domain.Report{
			InvestigationID: "inv_card", Outcome: "spike_detected",
			Findings:        []domain.Finding{{Statement: "错误日志显著增长。", Confidence: .95, Conclusive: true, EvidenceIDs: []string{"ev_current", "ev_baseline"}}},
			Recommendations: []domain.Recommendation{{Statement: "检查 payment 依赖。", EvidenceIDs: []string{"ev_current"}}},
			Evidence:        []domain.Evidence{current, baseline}, GeneratedAt: end,
		},
	}
}

func repeatedBuckets(label string, count int) []domain.CountBucket {
	values := make([]domain.CountBucket, count)
	for index := range values {
		values[index] = domain.CountBucket{Label: label, Count: int64(index + 1)}
	}
	return values
}
