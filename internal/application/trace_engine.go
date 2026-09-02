package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ids"
	"logagent/internal/ports"
)

type TraceEngine struct {
	executor ports.GovernedTraceExecutor
	steps    ports.TraceQueryStepStore
	now      func() time.Time
}

func NewTraceEngine(executor ports.GovernedTraceExecutor, steps ports.TraceQueryStepStore, now func() time.Time) (*TraceEngine, error) {
	if executor == nil || steps == nil || now == nil {
		return nil, errors.New("governed Trace executor, checkpoint store, and clock are required")
	}
	return &TraceEngine{executor: executor, steps: steps, now: now}, nil
}

func (e *TraceEngine) Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	if request.TemplateID != domain.TraceSearchTemplateID || request.TraceID == "" {
		return nil, domain.Report{}, errors.New("Trace engine requires a trace_search_v1 request")
	}
	job, ok := runJobFromContext(ctx)
	if !ok || job.InvestigationID != investigationID || job.Request.TraceID != request.TraceID {
		return nil, domain.Report{}, errors.New("Trace engine requires the active claimed job context")
	}
	plan, err := e.executor.ResolveTraceGovernance(ctx, domain.TraceSearchSpec{
		InvestigationID: investigationID, Service: request.Service, Environment: request.Environment,
		TraceID: request.TraceID, StartTime: request.StartTime, EndTime: request.EndTime, Requester: request.Requester,
	})
	if err != nil {
		return nil, domain.Report{}, err
	}
	primary, found := traceMemberByID(plan.Group.Members, plan.Group.PrimaryMemberID)
	if !found {
		return nil, domain.Report{}, errors.New("Trace plan primary member is missing")
	}
	results := make([]domain.TraceMemberResult, 0, len(plan.Group.Members))
	primaryResult, err := e.executeMember(ctx, job, plan, primary.ID)
	if err != nil {
		return nil, domain.Report{}, err
	}
	results = append(results, primaryResult)

	remaining := make([]domain.TraceResourceMember, 0, len(plan.Group.Members)-1)
	for _, member := range plan.Group.Members {
		if member.ID != primary.ID {
			remaining = append(remaining, member)
		}
	}
	parallel, err := e.executeRemaining(ctx, job, plan, remaining)
	if err != nil {
		return nil, domain.Report{}, err
	}
	results = append(results, parallel...)
	return e.buildTraceReport(investigationID, plan, results)
}

func (e *TraceEngine) executeRemaining(ctx context.Context, job domain.Job, plan domain.TracePlan, members []domain.TraceResourceMember) ([]domain.TraceMemberResult, error) {
	if len(members) == 0 {
		return nil, nil
	}
	workerCount := plan.MaxConcurrency
	if workerCount > len(members) {
		workerCount = len(members)
	}
	type task struct {
		index  int
		member domain.TraceResourceMember
	}
	tasks := make(chan task)
	results := make([]domain.TraceMemberResult, len(members))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var firstErr error
	var errMu sync.Mutex
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range tasks {
				if workCtx.Err() != nil {
					return
				}
				result, err := e.executeMember(workCtx, job, plan, task.member.ID)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
				results[task.index] = result
			}
		}()
	}
sendLoop:
	for index, member := range members {
		select {
		case tasks <- task{index: index, member: member}:
		case <-workCtx.Done():
			break sendLoop
		}
	}
	close(tasks)
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func (e *TraceEngine) executeMember(ctx context.Context, job domain.Job, plan domain.TracePlan, memberID string) (domain.TraceMemberResult, error) {
	inputHash, err := fingerprint.JSON(struct {
		InvestigationID       string `json:"investigation_id"`
		MemberID              string `json:"member_id"`
		GovernanceFingerprint string `json:"governance_fingerprint"`
		TraceIDFingerprint    string `json:"trace_id_fingerprint"`
		StartUnixNano         int64  `json:"start_unix_nano"`
		EndUnixNano           int64  `json:"end_unix_nano"`
	}{job.InvestigationID, memberID, plan.GovernanceFingerprint, plan.TraceIDFingerprint, plan.Spec.StartTime.UnixNano(), plan.Spec.EndTime.UnixNano()})
	if err != nil {
		return domain.TraceMemberResult{}, fmt.Errorf("fingerprint Trace checkpoint: %w", err)
	}
	decision, err := e.steps.PrepareTraceQueryStep(ctx, job, memberID, inputHash, e.now().UTC())
	if err != nil {
		if errors.Is(err, ports.ErrExternalOutcomeUnknown) {
			return domain.TraceMemberResult{}, err
		}
		return domain.TraceMemberResult{}, fmt.Errorf("prepare Trace checkpoint: %w", err)
	}
	if decision.Action == domain.QueryStepReuse {
		if decision.Result == nil || !validTraceCheckpointResult(plan, memberID, *decision.Result) {
			return domain.TraceMemberResult{}, fmt.Errorf("%w: cached Trace result is invalid", ports.ErrExternalOutcomeUnknown)
		}
		return cloneTraceMemberResult(*decision.Result), nil
	}
	if decision.Action != domain.QueryStepExecute || decision.Result != nil {
		return domain.TraceMemberResult{}, errors.New("Trace checkpoint returned an invalid decision")
	}
	result, executeErr := e.executor.ExecuteTraceMember(ctx, plan, memberID)
	if executeErr != nil {
		if ctx.Err() != nil {
			return domain.TraceMemberResult{}, ctx.Err()
		}
		if errors.Is(executeErr, ports.ErrInvalidQuerySchema) || errors.Is(executeErr, ports.ErrQueryDenied) || errors.Is(executeErr, ports.ErrQueryBudgetExceeded) {
			if result.QueryID == "" {
				result.QueryID = "trace-preflight:" + memberID
			}
			if result.QuerySpecHash == "" {
				result.QuerySpecHash = inputHash
			}
			if err := e.steps.CompleteTraceQueryStep(ctx, job, memberID, inputHash, result, e.now().UTC()); err != nil {
				return domain.TraceMemberResult{}, fmt.Errorf("%w: persist incomplete Trace checkpoint", ports.ErrExternalOutcomeUnknown)
			}
			return result, nil
		}
		return domain.TraceMemberResult{}, fmt.Errorf("%w: Trace member %s", ports.ErrExternalOutcomeUnknown, memberID)
	}
	if !validTraceCheckpointResult(plan, memberID, result) {
		return domain.TraceMemberResult{}, fmt.Errorf("%w: Trace member returned invalid normalized evidence", ports.ErrExternalOutcomeUnknown)
	}
	if err := e.steps.CompleteTraceQueryStep(ctx, job, memberID, inputHash, result, e.now().UTC()); err != nil {
		return domain.TraceMemberResult{}, fmt.Errorf("%w: persist Trace member result", ports.ErrExternalOutcomeUnknown)
	}
	return cloneTraceMemberResult(result), nil
}

func (e *TraceEngine) buildTraceReport(investigationID string, plan domain.TracePlan, results []domain.TraceMemberResult) ([]domain.Evidence, domain.Report, error) {
	if len(results) != len(plan.Group.Members) {
		return nil, domain.Report{}, errors.New("Trace result does not cover every configured member")
	}
	results = applyGlobalTraceBudget(results, plan.GlobalLimit, plan.MaxProcessedBytes)
	evidence := make([]domain.Evidence, 0, len(results))
	timeline := domain.TraceInvestigation{
		GroupID: plan.Group.ID, TemplateID: domain.TraceSearchTemplateID, TemplateVersion: domain.TraceSearchTemplateVersion,
		PolicyVersion: domain.TracePolicyVersion, GovernanceFingerprint: plan.GovernanceFingerprint,
		TraceIDFingerprint: plan.TraceIDFingerprint, StartTime: plan.Spec.StartTime, EndTime: plan.Spec.EndTime,
		Members: make([]domain.TraceMemberSummary, 0, len(results)), Events: make([]domain.TraceEvent, 0),
	}
	allComplete, allZero := true, true
	evidenceIDs := make([]string, 0, len(results))
	for _, result := range results {
		evidenceID, err := ids.New("ev")
		if err != nil {
			return nil, domain.Report{}, err
		}
		copyResult := cloneTraceMemberResult(result)
		evidence = append(evidence, domain.Evidence{
			ID: evidenceID, QueryID: result.QueryID, QuerySpecHash: result.QuerySpecHash, ResourceID: plan.Group.ID,
			TemplateID: domain.TraceSearchTemplateID, TemplateVersion: domain.TraceSearchTemplateVersion,
			SchemaFingerprint: result.SchemaFingerprint, PolicyVersion: domain.TracePolicyVersion,
			GovernanceFingerprint: plan.GovernanceFingerprint, Name: "trace." + result.MemberID,
			StartTime: result.StartTime, EndTime: result.EndTime, Progress: result.Progress,
			Complete: result.Complete, Truncated: result.Truncated, NanosecondOrderedKnown: result.NanosecondOrderedKnown,
			NanosecondOrdered: result.NanosecondOrdered, UsageKnown: result.UsageKnown,
			IncompleteReason: result.IncompleteReason, ProcessedRows: result.ProcessedRows,
			ProcessedBytes: result.ProcessedBytes, ElapsedMillisecond: result.ElapsedMillisecond,
			APICalls: result.APICalls, TraceMember: &copyResult,
		})
		evidenceIDs = append(evidenceIDs, evidenceID)
		timeline.Members = append(timeline.Members, domain.TraceMemberSummary{
			MemberID: result.MemberID, EvidenceID: evidenceID, Status: result.Status, EventCount: len(result.Events),
			APICalls: result.APICalls, ProcessedRows: result.ProcessedRows, ProcessedBytes: result.ProcessedBytes,
			IncompleteReason: result.IncompleteReason,
		})
		timeline.Events = append(timeline.Events, cloneTraceEvents(result.Events)...)
		timeline.TotalAPICalls += result.APICalls
		timeline.TotalProcessedRows += result.ProcessedRows
		timeline.TotalProcessedBytes += result.ProcessedBytes
		allComplete = allComplete && result.Complete && !result.Truncated
		allZero = allZero && result.ZeroHit
	}
	sort.SliceStable(timeline.Members, func(left, right int) bool { return timeline.Members[left].MemberID < timeline.Members[right].MemberID })
	sort.SliceStable(timeline.Events, func(left, right int) bool {
		if timeline.Events[left].EventTime.IsZero() != timeline.Events[right].EventTime.IsZero() {
			return !timeline.Events[left].EventTime.IsZero()
		}
		if !timeline.Events[left].EventTime.Equal(timeline.Events[right].EventTime) {
			return timeline.Events[left].EventTime.Before(timeline.Events[right].EventTime)
		}
		if timeline.Events[left].MemberID != timeline.Events[right].MemberID {
			return timeline.Events[left].MemberID < timeline.Events[right].MemberID
		}
		return timeline.Events[left].ID < timeline.Events[right].ID
	})
	var outcome, code, statement string
	confidence := 1.0
	conclusive := true
	switch {
	case !allComplete:
		timeline.Status = domain.TraceInvestigationPartial
		outcome, code, statement, confidence, conclusive = "trace_evidence_partial", "trace_evidence_partial",
			fmt.Sprintf("Trace 证据不完整：%d/%d 个成员返回，已保留 %d 条脱敏事件。", completeTraceMembers(results), len(results), len(timeline.Events)), 0.5, false
	case allZero:
		timeline.Status, timeline.Complete = domain.TraceInvestigationZeroHit, true
		outcome, code, statement = "trace_zero_hit", "trace_zero_hit", "所有已配置日志成员查询完成，但当前窗口没有命中 Trace 事件。"
	default:
		timeline.Status, timeline.Complete = domain.TraceInvestigationComplete, true
		outcome, code, statement = "trace_evidence_found", "trace_evidence_found", fmt.Sprintf("已从 %d 个日志成员构建 %d 条脱敏 Trace 事件。", len(results), len(timeline.Events))
	}
	report := domain.Report{
		InvestigationID: investigationID, Outcome: outcome,
		Findings: []domain.Finding{{Code: code, Statement: statement, Confidence: confidence, Conclusive: conclusive, EvidenceIDs: evidenceIDs}},
		Evidence: evidence, TraceInvestigation: &timeline, GeneratedAt: e.now().UTC(),
	}
	return evidence, report, nil
}

func applyGlobalTraceBudget(source []domain.TraceMemberResult, globalLimit int, maxProcessedBytes int64) []domain.TraceMemberResult {
	results := make([]domain.TraceMemberResult, len(source))
	remaining := globalLimit
	var totalBytes int64
	for index, item := range source {
		results[index] = cloneTraceMemberResult(item)
		if remaining < len(results[index].Events) {
			if remaining < 0 {
				remaining = 0
			}
			results[index].Events = results[index].Events[:remaining]
			results[index].Status = domain.TraceMemberTruncated
			results[index].Complete = false
			results[index].ZeroHit = false
			results[index].Truncated = true
			results[index].IncompleteReason = "trace_global_event_limit_reached"
		}
		remaining -= len(results[index].Events)
		totalBytes += results[index].ProcessedBytes
	}
	if totalBytes > maxProcessedBytes && len(results) > 0 {
		last := &results[len(results)-1]
		last.Status = domain.TraceMemberIncomplete
		last.Complete = false
		last.ZeroHit = false
		last.IncompleteReason = "trace_global_processed_bytes_exceeded"
	}
	return results
}

func validTraceCheckpointResult(plan domain.TracePlan, memberID string, result domain.TraceMemberResult) bool {
	if result.QueryID == "" || result.QuerySpecHash == "" || result.GroupID != plan.Group.ID || result.MemberID != memberID ||
		result.TemplateID != domain.TraceSearchTemplateID || result.TemplateVersion != domain.TraceSearchTemplateVersion ||
		result.PolicyVersion != domain.TracePolicyVersion || result.GovernanceFingerprint != plan.GovernanceFingerprint ||
		result.TraceIDFingerprint != plan.TraceIDFingerprint || !result.StartTime.Equal(plan.Spec.StartTime) || !result.EndTime.Equal(plan.Spec.EndTime) ||
		result.ProcessedRows < 0 || result.ProcessedBytes < 0 || result.ElapsedMillisecond < 0 || result.APICalls < 0 ||
		len(result.Events) > plan.MemberLimit {
		return false
	}
	switch result.Status {
	case domain.TraceMemberComplete, domain.TraceMemberZeroHit:
		return result.Complete && !result.Truncated && result.IncompleteReason == ""
	case domain.TraceMemberIncomplete, domain.TraceMemberTruncated, domain.TraceMemberInvalidSchema, domain.TraceMemberFailed:
		return !result.Complete && result.IncompleteReason != ""
	default:
		return false
	}
}

func completeTraceMembers(results []domain.TraceMemberResult) int {
	count := 0
	for _, result := range results {
		if result.Complete && !result.Truncated {
			count++
		}
	}
	return count
}

func traceMemberByID(members []domain.TraceResourceMember, memberID string) (domain.TraceResourceMember, bool) {
	for _, member := range members {
		if member.ID == memberID {
			return member, true
		}
	}
	return domain.TraceResourceMember{}, false
}

func cloneTraceMemberResult(result domain.TraceMemberResult) domain.TraceMemberResult {
	result.Events = cloneTraceEvents(result.Events)
	return result
}

func cloneTraceEvents(events []domain.TraceEvent) []domain.TraceEvent {
	return append([]domain.TraceEvent(nil), events...)
}

type RoutingEngine struct {
	defaultEngine ports.InvestigationEngine
	traceEngine   ports.InvestigationEngine
}

func NewRoutingEngine(defaultEngine, traceEngine ports.InvestigationEngine) (*RoutingEngine, error) {
	if defaultEngine == nil {
		return nil, errors.New("default investigation engine is required")
	}
	return &RoutingEngine{defaultEngine: defaultEngine, traceEngine: traceEngine}, nil
}

func (e *RoutingEngine) Run(ctx context.Context, investigationID string, request domain.InvestigationRequest) ([]domain.Evidence, domain.Report, error) {
	if request.TemplateID == domain.TraceSearchTemplateID {
		if e.traceEngine == nil {
			return nil, domain.Report{}, errors.New("Trace investigation is not enabled")
		}
		return e.traceEngine.Run(ctx, investigationID, request)
	}
	return e.defaultEngine.Run(ctx, investigationID, request)
}

var _ ports.InvestigationEngine = (*TraceEngine)(nil)
var _ ports.InvestigationEngine = (*RoutingEngine)(nil)
