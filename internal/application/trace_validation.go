package application

import (
	"errors"
	"fmt"
	"sort"

	"logagent/internal/domain"
)

func validateTraceEvidence(item domain.Evidence) error {
	if item.TraceMember == nil {
		return errors.New("Trace member projection is missing")
	}
	trace := *item.TraceMember
	if trace.QueryID != item.QueryID || trace.QuerySpecHash != item.QuerySpecHash || trace.GroupID != item.ResourceID ||
		trace.TemplateID != item.TemplateID || trace.TemplateVersion != item.TemplateVersion ||
		trace.SchemaFingerprint != item.SchemaFingerprint || trace.PolicyVersion != item.PolicyVersion ||
		trace.GovernanceFingerprint != item.GovernanceFingerprint || !trace.StartTime.Equal(item.StartTime) || !trace.EndTime.Equal(item.EndTime) ||
		trace.Progress != item.Progress || trace.Complete != item.Complete || trace.Truncated != item.Truncated ||
		trace.ProcessedRows != item.ProcessedRows || trace.ProcessedBytes != item.ProcessedBytes ||
		trace.ElapsedMillisecond != item.ElapsedMillisecond || trace.APICalls != item.APICalls ||
		trace.NanosecondOrderedKnown != item.NanosecondOrderedKnown || trace.NanosecondOrdered != item.NanosecondOrdered ||
		trace.UsageKnown != item.UsageKnown || trace.IncompleteReason != item.IncompleteReason {
		return errors.New("Trace member projection does not match Evidence envelope")
	}
	if item.ErrorCount != 0 || item.TopError != "" || item.TopErrorCount != 0 || len(item.ErrorPatterns) != 0 || len(item.Instances) != 0 ||
		item.PatternLimit != 0 || item.InstanceLimit != 0 || item.ErrorPatternsExhaustive || item.InstancesExhaustive {
		return errors.New("Trace evidence contains aggregate claims")
	}
	if trace.MemberID == "" || trace.TraceIDFingerprint == "" || len(trace.TraceIDFingerprint) != 64 || len(trace.Events) > domain.TraceDefaultMemberLimit {
		return errors.New("Trace member identity or bounds are invalid")
	}
	seenEvents := make(map[string]struct{}, len(trace.Events))
	for _, event := range trace.Events {
		if event.ID == "" || event.MemberID != trace.MemberID || event.Message == "" || len(event.MessageFingerprint) != 64 {
			return errors.New("Trace event identity or projection is invalid")
		}
		if event.SortQuality != domain.TraceSortNanosecond && event.SortQuality != domain.TraceSortSecond && event.SortQuality != domain.TraceSortUnknown {
			return errors.New("Trace event sort quality is invalid")
		}
		if event.SortQuality != domain.TraceSortUnknown && event.EventTime.IsZero() {
			return errors.New("Trace event claims time quality without an event time")
		}
		if _, duplicate := seenEvents[event.ID]; duplicate {
			return errors.New("Trace member contains duplicate event IDs")
		}
		seenEvents[event.ID] = struct{}{}
	}
	switch trace.Status {
	case domain.TraceMemberComplete:
		if !trace.Complete || trace.ZeroHit || trace.Truncated || len(trace.Events) == 0 || trace.IncompleteReason != "" {
			return errors.New("complete Trace member has inconsistent state")
		}
	case domain.TraceMemberZeroHit:
		if !trace.Complete || !trace.ZeroHit || trace.Truncated || len(trace.Events) != 0 || trace.IncompleteReason != "" {
			return errors.New("zero-hit Trace member has inconsistent state")
		}
	case domain.TraceMemberIncomplete, domain.TraceMemberTruncated, domain.TraceMemberInvalidSchema, domain.TraceMemberFailed:
		if trace.Complete || trace.IncompleteReason == "" {
			return errors.New("incomplete Trace member has inconsistent state")
		}
	default:
		return fmt.Errorf("Trace member has unsupported status %q", trace.Status)
	}
	return nil
}

func validateTraceInvestigation(trace *domain.TraceInvestigation, evidence map[string]domain.Evidence, report domain.Report) error {
	hasTraceEvidence := false
	for _, item := range evidence {
		hasTraceEvidence = hasTraceEvidence || item.TemplateID == domain.TraceSearchTemplateID
	}
	if trace == nil {
		if hasTraceEvidence {
			return errors.New("Trace evidence exists without a Trace investigation projection")
		}
		return nil
	}
	if !hasTraceEvidence || trace.GroupID == "" || trace.TemplateID != domain.TraceSearchTemplateID ||
		trace.TemplateVersion != domain.TraceSearchTemplateVersion || trace.PolicyVersion != domain.TracePolicyVersion ||
		len(trace.GovernanceFingerprint) != 64 || len(trace.TraceIDFingerprint) != 64 || !trace.StartTime.Before(trace.EndTime) ||
		len(trace.Members) != len(evidence) || len(trace.Events) > domain.TraceDefaultGlobalLimit {
		return errors.New("Trace investigation envelope is invalid")
	}
	if report.CauseAnalysis != nil || report.IncidentTimeline != nil || report.RunbookGuidance != nil || report.Summary != nil {
		return errors.New("Trace-only report contains an unapproved enrichment")
	}
	eventIDs := make(map[string]struct{})
	memberIDs := make(map[string]struct{}, len(trace.Members))
	var totalCalls int
	var totalRows, totalBytes int64
	allComplete, allZero := true, true
	for _, member := range trace.Members {
		item, exists := evidence[member.EvidenceID]
		if !exists || item.TraceMember == nil || item.ResourceID != trace.GroupID || item.GovernanceFingerprint != trace.GovernanceFingerprint ||
			item.TraceMember.MemberID != member.MemberID || member.Status != item.TraceMember.Status || member.EventCount != len(item.TraceMember.Events) ||
			member.APICalls != item.APICalls || member.ProcessedRows != item.ProcessedRows || member.ProcessedBytes != item.ProcessedBytes ||
			member.IncompleteReason != item.IncompleteReason {
			return errors.New("Trace member summary does not bind to Evidence")
		}
		if _, duplicate := memberIDs[member.MemberID]; duplicate {
			return errors.New("Trace investigation contains a duplicate member")
		}
		memberIDs[member.MemberID] = struct{}{}
		totalCalls += item.APICalls
		totalRows += item.ProcessedRows
		totalBytes += item.ProcessedBytes
		allComplete = allComplete && item.Complete && !item.Truncated
		allZero = allZero && item.TraceMember.ZeroHit
		for _, event := range item.TraceMember.Events {
			eventIDs[event.ID] = struct{}{}
		}
	}
	if trace.TotalAPICalls != totalCalls || trace.TotalProcessedRows != totalRows || trace.TotalProcessedBytes != totalBytes ||
		trace.Complete != allComplete {
		return errors.New("Trace investigation totals are inconsistent")
	}
	if len(eventIDs) != len(trace.Events) {
		return errors.New("Trace timeline does not cover member events exactly once")
	}
	for _, event := range trace.Events {
		if _, exists := eventIDs[event.ID]; !exists {
			return errors.New("Trace timeline references an unknown event")
		}
	}
	switch trace.Status {
	case domain.TraceInvestigationComplete:
		if !allComplete || allZero || len(trace.Events) == 0 || report.Outcome != "trace_evidence_found" {
			return errors.New("complete Trace investigation has inconsistent outcome")
		}
	case domain.TraceInvestigationZeroHit:
		if !allComplete || !allZero || len(trace.Events) != 0 || report.Outcome != "trace_zero_hit" {
			return errors.New("zero-hit Trace investigation has inconsistent outcome")
		}
	case domain.TraceInvestigationPartial:
		if allComplete || trace.Complete || report.Outcome != "trace_evidence_partial" {
			return errors.New("partial Trace investigation has inconsistent outcome")
		}
	default:
		return errors.New("Trace investigation status is invalid")
	}
	if !sort.SliceIsSorted(trace.Events, func(left, right int) bool {
		if trace.Events[left].EventTime.IsZero() != trace.Events[right].EventTime.IsZero() {
			return !trace.Events[left].EventTime.IsZero()
		}
		if !trace.Events[left].EventTime.Equal(trace.Events[right].EventTime) {
			return trace.Events[left].EventTime.Before(trace.Events[right].EventTime)
		}
		if trace.Events[left].MemberID != trace.Events[right].MemberID {
			return trace.Events[left].MemberID < trace.Events[right].MemberID
		}
		return trace.Events[left].ID < trace.Events[right].ID
	}) {
		return errors.New("Trace timeline order is unstable")
	}
	return nil
}
