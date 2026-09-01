package application

import (
	"errors"
	"fmt"

	"logagent/internal/domain"
)

var allowedTimelineMissingInputs = map[string]struct{}{
	"dimensional_event_evidence":          {},
	"conclusive_error_spike":              {},
	"complete_current_baseline_evidence":  {},
	"governed_resource_identity":          {},
	"operational_signal_source_disabled":  {},
	"operational_signal_source_available": {},
	"valid_operational_signal_set":        {},
	"complete_operational_signal_set":     {},
	"untruncated_operational_signal_set":  {},
	"metric_signal_coverage":              {},
	"trace_signal_coverage":               {},
}

func validateIncidentTimeline(timeline *domain.IncidentTimeline, evidence map[string]domain.Evidence, cause *domain.CauseAnalysis) error {
	if timeline == nil {
		return nil
	}
	if timeline.MethodVersion != domain.OperationalSignalTimelineVersion {
		return fmt.Errorf("unsupported method version %q", timeline.MethodVersion)
	}
	if err := validateTimelineStatus(timeline); err != nil {
		return err
	}
	if len(timeline.Signals) > domain.MaxOperationalSignals || len(timeline.Items) > domain.MaxIncidentTimelineItems {
		return errors.New("incident timeline exceeds bounded size")
	}
	if err := validateTimelineMissingInputs(timeline.MissingInputs); err != nil {
		return err
	}
	if timeline.SourceComplete && timeline.SourceTruncated {
		return errors.New("complete timeline source cannot be truncated")
	}
	if timeline.Status == domain.TimelineComplete {
		if !timeline.SourceComplete || timeline.SourceTruncated || len(timeline.MissingInputs) != 0 {
			return errors.New("complete timeline has incomplete source metadata")
		}
	}
	if timeline.Status == domain.TimelineInconclusive && len(timeline.MissingInputs) == 0 {
		return errors.New("inconclusive timeline requires a missing input")
	}
	if timeline.Status == domain.TimelineUnavailable && len(timeline.MissingInputs) == 0 {
		return errors.New("unavailable timeline requires a missing input")
	}
	if timeline.Status == domain.TimelineSkippedNoSpike {
		if timeline.SourceVersion != "" || timeline.SourceComplete || timeline.SourceTruncated || len(timeline.Signals) != 0 || len(timeline.Items) != 0 || len(timeline.MissingInputs) != 0 {
			return errors.New("skipped timeline contains source output")
		}
		return nil
	}
	if timeline.Status == domain.TimelineUnavailable {
		if timeline.SourceComplete || timeline.SourceTruncated || len(timeline.Signals) != 0 || len(timeline.Items) != 0 {
			return errors.New("unavailable timeline contains source output")
		}
		if timeline.SourceVersion != "" {
			if err := domain.ValidateOperationalSignalSourceVersion(timeline.SourceVersion); err != nil {
				return err
			}
		}
		return nil
	}
	if countOnlyEvidence(evidence) {
		if timeline.Status != domain.TimelineInconclusive || !sameIDSet(timeline.MissingInputs, []string{"dimensional_event_evidence"}) ||
			timeline.SourceVersion != "" || timeline.SourceComplete || timeline.SourceTruncated || len(timeline.Signals) != 0 || len(timeline.Items) != 0 {
			return errors.New("count-only incident timeline must remain source-free and inconclusive")
		}
		return nil
	}

	current, baseline, err := causeObservationPair(evidence)
	if err != nil {
		return err
	}
	query := domain.OperationalSignalQuery{
		ResourceID: current.ResourceID,
		StartTime:  baseline.StartTime,
		EndTime:    current.EndTime,
		Limit:      domain.MaxOperationalSignals,
	}
	if err := domain.ValidateOperationalSignalQuery(query); err != nil {
		return err
	}
	if len(timeline.Signals) > 0 || timeline.SourceVersion != "" {
		if err := domain.ValidateOperationalSignalSourceVersion(timeline.SourceVersion); err != nil {
			return err
		}
	}

	signals, hasMetric, hasTrace, err := validateTimelineSignals(timeline.Signals, query)
	if err != nil {
		return err
	}
	if timeline.Status == domain.TimelineComplete && (!hasMetric || !hasTrace) {
		return errors.New("complete timeline requires metric and Trace coverage")
	}
	if timeline.Status == domain.TimelineInconclusive && (len(timeline.Signals) > 0 || timeline.SourceVersion != "") {
		expectedMissing := make([]string, 0, 4)
		if !timeline.SourceComplete {
			expectedMissing = append(expectedMissing, "complete_operational_signal_set")
		}
		if timeline.SourceTruncated {
			expectedMissing = append(expectedMissing, "untruncated_operational_signal_set")
		}
		if !hasMetric {
			expectedMissing = append(expectedMissing, "metric_signal_coverage")
		}
		if !hasTrace {
			expectedMissing = append(expectedMissing, "trace_signal_coverage")
		}
		if !sameIDSet(timeline.MissingInputs, expectedMissing) {
			return errors.New("inconclusive timeline missing inputs do not match source coverage")
		}
	}
	changes := timelineChanges(cause)
	if len(timeline.Items) != len(signals)+len(changes) {
		return errors.New("incident timeline item coverage is incomplete")
	}
	return validateTimelineItems(timeline.Items, signals, changes, []string{current.ID, baseline.ID})
}

func countOnlyEvidence(evidence map[string]domain.Evidence) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, item := range evidence {
		if item.TemplateID != domain.ErrorCountTemplateID {
			return false
		}
	}
	return true
}

func validateTimelineStatus(timeline *domain.IncidentTimeline) error {
	switch timeline.Status {
	case domain.TimelineComplete, domain.TimelineInconclusive, domain.TimelineUnavailable, domain.TimelineSkippedNoSpike:
		return nil
	default:
		return fmt.Errorf("unsupported timeline status %q", timeline.Status)
	}
}

func validateTimelineMissingInputs(inputs []string) error {
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if _, allowed := allowedTimelineMissingInputs[input]; !allowed {
			return fmt.Errorf("unsupported timeline missing input %q", input)
		}
		if _, duplicate := seen[input]; duplicate {
			return fmt.Errorf("duplicate timeline missing input %q", input)
		}
		seen[input] = struct{}{}
	}
	return nil
}

func validateTimelineSignals(values []domain.TimelineSignal, query domain.OperationalSignalQuery) (map[string]domain.TimelineSignal, bool, bool, error) {
	signals := make(map[string]domain.TimelineSignal, len(values))
	hasMetric := false
	hasTrace := false
	for index, signal := range values {
		if err := domain.ValidateOperationalSignalObservation(signal.OperationalSignalObservation, query); err != nil {
			return nil, false, false, err
		}
		if index > 0 && domain.OperationalSignalObservationLess(signal.OperationalSignalObservation, values[index-1].OperationalSignalObservation) {
			return nil, false, false, errors.New("timeline signals are not deterministically ordered")
		}
		if _, duplicate := signals[signal.ID]; duplicate {
			return nil, false, false, fmt.Errorf("duplicate timeline signal %q", signal.ID)
		}
		expectedAnomaly := domain.OperationalSignalIsAnomalous(signal.Code, signal.BaselineValue, signal.CurrentValue)
		if signal.Anomalous != expectedAnomaly {
			return nil, false, false, fmt.Errorf("timeline signal %q has a fabricated anomaly flag", signal.ID)
		}
		signals[signal.ID] = signal
		hasMetric = hasMetric || signal.Kind == domain.OperationalSignalMetric
		hasTrace = hasTrace || signal.Kind == domain.OperationalSignalTrace
	}
	return signals, hasMetric, hasTrace, nil
}

func timelineChanges(cause *domain.CauseAnalysis) map[string]domain.ChangeEvent {
	changes := make(map[string]domain.ChangeEvent)
	if cause == nil {
		return changes
	}
	for _, change := range cause.Changes {
		changes[change.ID] = change
	}
	return changes
}

func validateTimelineItems(items []domain.IncidentTimelineItem, signals map[string]domain.TimelineSignal, changes map[string]domain.ChangeEvent, evidenceIDs []string) error {
	seenItems := make(map[string]struct{}, len(items))
	seenSignals := make(map[string]struct{}, len(signals))
	seenChanges := make(map[string]struct{}, len(changes))
	for index, item := range items {
		if err := domain.ValidateOperationalSignalIdentifier(item.ID); err != nil {
			return err
		}
		if _, duplicate := seenItems[item.ID]; duplicate {
			return fmt.Errorf("duplicate timeline item %q", item.ID)
		}
		seenItems[item.ID] = struct{}{}
		if !sameIDSet(item.EvidenceIDs, evidenceIDs) {
			return fmt.Errorf("timeline item %q has invalid evidence references", item.ID)
		}
		if index > 0 && timelineItemLess(item, items[index-1]) {
			return errors.New("incident timeline items are not deterministically ordered")
		}
		switch item.Kind {
		case domain.TimelineItemMetric, domain.TimelineItemTrace:
			if err := validateSignalTimelineItem(item, signals, seenSignals); err != nil {
				return err
			}
		case domain.TimelineItemChange:
			if err := validateChangeTimelineItem(item, changes, seenChanges); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported timeline item kind %q", item.Kind)
		}
	}
	if len(seenSignals) != len(signals) || len(seenChanges) != len(changes) {
		return errors.New("incident timeline references do not cover all signals and changes")
	}
	return nil
}

func validateSignalTimelineItem(item domain.IncidentTimelineItem, signals map[string]domain.TimelineSignal, seen map[string]struct{}) error {
	if len(item.SignalIDs) != 1 || len(item.ChangeEventIDs) != 0 {
		return fmt.Errorf("signal timeline item %q has invalid references", item.ID)
	}
	signal, exists := signals[item.SignalIDs[0]]
	if !exists {
		return fmt.Errorf("timeline item %q references unknown signal", item.ID)
	}
	if _, duplicate := seen[signal.ID]; duplicate {
		return fmt.Errorf("timeline signal %q is referenced more than once", signal.ID)
	}
	seen[signal.ID] = struct{}{}
	expectedKind := domain.TimelineItemMetric
	if signal.Kind == domain.OperationalSignalTrace {
		expectedKind = domain.TimelineItemTrace
	}
	if item.Kind != expectedKind || item.Code != string(signal.Code) || !item.StartedAt.Equal(signal.StartedAt) || !item.CompletedAt.Equal(signal.CompletedAt) || item.Anomalous != signal.Anomalous || item.Statement != domain.OperationalSignalStatement(signal) {
		return fmt.Errorf("timeline item %q does not match its signal", item.ID)
	}
	return nil
}

func validateChangeTimelineItem(item domain.IncidentTimelineItem, changes map[string]domain.ChangeEvent, seen map[string]struct{}) error {
	if len(item.ChangeEventIDs) != 1 || len(item.SignalIDs) != 0 || item.Anomalous {
		return fmt.Errorf("change timeline item %q has invalid references", item.ID)
	}
	change, exists := changes[item.ChangeEventIDs[0]]
	if !exists {
		return fmt.Errorf("timeline item %q references unknown change", item.ID)
	}
	if _, duplicate := seen[change.ID]; duplicate {
		return fmt.Errorf("timeline change %q is referenced more than once", change.ID)
	}
	seen[change.ID] = struct{}{}
	if item.Code != string(change.Kind) || !item.StartedAt.Equal(change.StartedAt) || !item.CompletedAt.Equal(change.CompletedAt) || item.Statement != domain.ChangeTimelineStatement(change) {
		return fmt.Errorf("timeline item %q does not match its change", item.ID)
	}
	return nil
}

func timelineItemLess(left, right domain.IncidentTimelineItem) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	if !left.CompletedAt.Equal(right.CompletedAt) {
		return left.CompletedAt.Before(right.CompletedAt)
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.ID < right.ID
}
