package eino

import (
	"context"
	"fmt"
	"sort"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	missingConclusiveSpike       = "conclusive_error_spike"
	missingCompleteEvidence      = "complete_current_baseline_evidence"
	missingGovernedResource      = "governed_resource_identity"
	missingSignalSourceDisabled  = "operational_signal_source_disabled"
	missingSignalSourceAvailable = "operational_signal_source_available"
	missingValidSignalSet        = "valid_operational_signal_set"
	missingCompleteSignalSet     = "complete_operational_signal_set"
	missingUntruncatedSignalSet  = "untruncated_operational_signal_set"
	missingMetricCoverage        = "metric_signal_coverage"
	missingTraceCoverage         = "trace_signal_coverage"
)

func enrichIncidentTimeline(ctx context.Context, output graphOutput, source ports.OperationalSignalSource) (graphOutput, error) {
	if len(output.Evidence) > 0 && output.Evidence[0].TemplateID == domain.ErrorCountTemplateID {
		output.Report.IncidentTimeline = newIncidentTimeline(domain.TimelineInconclusive, []string{"dimensional_event_evidence"})
		return output, nil
	}
	if source == nil {
		return output, nil
	}
	if !hasConclusiveSpike(output.Report) {
		status := domain.TimelineSkippedNoSpike
		missing := []string(nil)
		if output.Report.Outcome == "data_insufficient" {
			status = domain.TimelineInconclusive
			missing = []string{missingConclusiveSpike}
		}
		output.Report.IncidentTimeline = newIncidentTimeline(status, missing)
		return output, nil
	}

	current, baseline, ok := currentAndBaseline(output.Evidence)
	if !ok || !evidenceComplete(current) || !evidenceComplete(baseline) {
		output.Report.IncidentTimeline = newIncidentTimeline(domain.TimelineInconclusive, []string{missingCompleteEvidence})
		return output, nil
	}
	if current.ResourceID == "" || current.ResourceID != baseline.ResourceID {
		output.Report.IncidentTimeline = newIncidentTimeline(domain.TimelineUnavailable, []string{missingGovernedResource})
		return output, nil
	}

	query := domain.OperationalSignalQuery{
		ResourceID: current.ResourceID,
		StartTime:  baseline.StartTime,
		EndTime:    current.EndTime,
		Limit:      domain.MaxOperationalSignals,
	}
	set, err := source.List(ctx, query)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return output, contextErr
		}
		output.Report.IncidentTimeline = newIncidentTimeline(domain.TimelineUnavailable, []string{missingSignalSourceAvailable})
		return output, nil
	}
	if set.ReasonCode == domain.OperationalSignalReasonDisabled {
		timeline := newIncidentTimeline(domain.TimelineUnavailable, []string{missingSignalSourceDisabled})
		timeline.SourceVersion = safeOperationalSignalSourceVersion(set.SourceVersion)
		output.Report.IncidentTimeline = timeline
		return output, nil
	}
	if err := validateOperationalSignalSet(set, query); err != nil {
		timeline := newIncidentTimeline(domain.TimelineUnavailable, []string{missingValidSignalSet})
		timeline.SourceVersion = safeOperationalSignalSourceVersion(set.SourceVersion)
		output.Report.IncidentTimeline = timeline
		return output, nil
	}

	timeline := buildIncidentTimeline(output.Report, current, baseline, set)
	output.Report.IncidentTimeline = &timeline
	return output, nil
}

func newIncidentTimeline(status domain.TimelineStatus, missing []string) *domain.IncidentTimeline {
	return &domain.IncidentTimeline{
		Status:        status,
		MethodVersion: domain.OperationalSignalTimelineVersion,
		MissingInputs: append([]string(nil), missing...),
	}
}

func validateOperationalSignalSet(set domain.OperationalSignalSet, query domain.OperationalSignalQuery) error {
	if err := domain.ValidateOperationalSignalQuery(query); err != nil {
		return err
	}
	if err := domain.ValidateOperationalSignalSourceVersion(set.SourceVersion); err != nil {
		return err
	}
	if !domain.ValidateOperationalSignalReason(set.ReasonCode) || set.ReasonCode == domain.OperationalSignalReasonDisabled {
		return fmt.Errorf("unsupported operational signal reason %q", set.ReasonCode)
	}
	if set.Complete && set.Truncated {
		return fmt.Errorf("complete operational signal set cannot be truncated")
	}
	if set.Complete && set.ReasonCode != "" {
		return fmt.Errorf("complete operational signal set cannot have a reason")
	}
	if !set.Complete && set.ReasonCode == "" {
		return fmt.Errorf("incomplete operational signal set requires a reason")
	}
	if set.Truncated && set.ReasonCode != domain.OperationalSignalReasonTruncated {
		return fmt.Errorf("truncated operational signal set has invalid reason")
	}
	if !set.Truncated && !set.Complete && set.ReasonCode != domain.OperationalSignalReasonIncomplete {
		return fmt.Errorf("incomplete operational signal set has invalid reason")
	}
	if len(set.Observations) > query.Limit || len(set.Observations) > domain.MaxOperationalSignals {
		return fmt.Errorf("operational signal set exceeds limit")
	}
	seen := make(map[string]struct{}, len(set.Observations))
	for _, observation := range set.Observations {
		if err := domain.ValidateOperationalSignalObservation(observation, query); err != nil {
			return err
		}
		if _, duplicate := seen[observation.ID]; duplicate {
			return fmt.Errorf("duplicate operational signal %q", observation.ID)
		}
		seen[observation.ID] = struct{}{}
	}
	return nil
}

func buildIncidentTimeline(report domain.Report, current, baseline domain.Evidence, set domain.OperationalSignalSet) domain.IncidentTimeline {
	timeline := domain.IncidentTimeline{
		Status:          domain.TimelineComplete,
		MethodVersion:   domain.OperationalSignalTimelineVersion,
		SourceVersion:   set.SourceVersion,
		SourceComplete:  set.Complete,
		SourceTruncated: set.Truncated,
		Signals:         make([]domain.TimelineSignal, 0, len(set.Observations)),
	}
	evidenceIDs := []string{current.ID, baseline.ID}
	hasMetric := false
	hasTrace := false
	observations := append([]domain.OperationalSignalObservation(nil), set.Observations...)
	sort.Slice(observations, func(left, right int) bool {
		return domain.OperationalSignalObservationLess(observations[left], observations[right])
	})
	for _, observation := range observations {
		signal := domain.TimelineSignal{
			OperationalSignalObservation: observation,
			Anomalous: domain.OperationalSignalIsAnomalous(
				observation.Code,
				observation.BaselineValue,
				observation.CurrentValue,
			),
		}
		timeline.Signals = append(timeline.Signals, signal)
		switch signal.Kind {
		case domain.OperationalSignalMetric:
			hasMetric = true
		case domain.OperationalSignalTrace:
			hasTrace = true
		}
		timeline.Items = append(timeline.Items, signalTimelineItem(signal, evidenceIDs))
	}
	if report.CauseAnalysis != nil {
		for _, change := range report.CauseAnalysis.Changes {
			timeline.Items = append(timeline.Items, changeTimelineItem(change, evidenceIDs))
		}
	}
	sortTimelineItems(timeline.Items)

	if !set.Complete {
		timeline.Status = domain.TimelineInconclusive
		timeline.MissingInputs = append(timeline.MissingInputs, missingCompleteSignalSet)
	}
	if set.Truncated {
		timeline.Status = domain.TimelineInconclusive
		timeline.MissingInputs = appendUnique(timeline.MissingInputs, missingUntruncatedSignalSet)
	}
	if !hasMetric {
		timeline.Status = domain.TimelineInconclusive
		timeline.MissingInputs = appendUnique(timeline.MissingInputs, missingMetricCoverage)
	}
	if !hasTrace {
		timeline.Status = domain.TimelineInconclusive
		timeline.MissingInputs = appendUnique(timeline.MissingInputs, missingTraceCoverage)
	}
	return timeline
}

func signalTimelineItem(signal domain.TimelineSignal, evidenceIDs []string) domain.IncidentTimelineItem {
	kind := domain.TimelineItemMetric
	if signal.Kind == domain.OperationalSignalTrace {
		kind = domain.TimelineItemTrace
	}
	return domain.IncidentTimelineItem{
		ID:          stableID("tl", domain.OperationalSignalTimelineVersion, signal.ID),
		Kind:        kind,
		Code:        string(signal.Code),
		StartedAt:   signal.StartedAt,
		CompletedAt: signal.CompletedAt,
		Statement:   domain.OperationalSignalStatement(signal),
		Anomalous:   signal.Anomalous,
		EvidenceIDs: append([]string(nil), evidenceIDs...),
		SignalIDs:   []string{signal.ID},
	}
}

func changeTimelineItem(change domain.ChangeEvent, evidenceIDs []string) domain.IncidentTimelineItem {
	return domain.IncidentTimelineItem{
		ID:             stableID("tl", domain.OperationalSignalTimelineVersion, change.ID),
		Kind:           domain.TimelineItemChange,
		Code:           string(change.Kind),
		StartedAt:      change.StartedAt,
		CompletedAt:    change.CompletedAt,
		Statement:      domain.ChangeTimelineStatement(change),
		EvidenceIDs:    append([]string(nil), evidenceIDs...),
		ChangeEventIDs: []string{change.ID},
	}
}

func sortTimelineItems(items []domain.IncidentTimelineItem) {
	sort.Slice(items, func(left, right int) bool {
		if !items[left].StartedAt.Equal(items[right].StartedAt) {
			return items[left].StartedAt.Before(items[right].StartedAt)
		}
		if !items[left].CompletedAt.Equal(items[right].CompletedAt) {
			return items[left].CompletedAt.Before(items[right].CompletedAt)
		}
		if items[left].Kind != items[right].Kind {
			return items[left].Kind < items[right].Kind
		}
		return items[left].ID < items[right].ID
	})
}

func safeOperationalSignalSourceVersion(version string) string {
	if err := domain.ValidateOperationalSignalSourceVersion(version); err != nil {
		return ""
	}
	return version
}
