package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	OperationalSignalTimelineVersion = "operational-signal-timeline-v1"
	MaxOperationalSignals            = 8
	MaxIncidentTimelineItems         = MaxOperationalSignals + MaxChangeEvents

	MaxOperationalSignalSourceVersionBytes = 64
	MaxOperationalSignalIdentifierBytes    = 128
)

var operationalSignalIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type OperationalSignalKind string

const (
	OperationalSignalMetric OperationalSignalKind = "METRIC"
	OperationalSignalTrace  OperationalSignalKind = "TRACE"
)

type OperationalSignalCode string

const (
	OperationalSignalErrorRate  OperationalSignalCode = "ERROR_RATE"
	OperationalSignalLatencyP95 OperationalSignalCode = "LATENCY_P95"
)

type OperationalSignalUnit string

const (
	OperationalSignalRatio       OperationalSignalUnit = "RATIO"
	OperationalSignalMillisecond OperationalSignalUnit = "MILLISECOND"
)

const (
	OperationalSignalReasonDisabled   = "operational_signal_source_disabled"
	OperationalSignalReasonIncomplete = "operational_signal_source_incomplete"
	OperationalSignalReasonTruncated  = "operational_signal_result_truncated"
)

type OperationalSignalQuery struct {
	ResourceID string    `json:"resource_id"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	Limit      int       `json:"limit"`
}

// OperationalSignalObservation is the closed, provider-neutral aggregate
// accepted from a metrics or Trace adapter. It deliberately has no labels,
// Trace IDs, span names, arbitrary attributes, or provider-authored text.
type OperationalSignalObservation struct {
	ID            string                `json:"id"`
	ResourceID    string                `json:"resource_id"`
	Kind          OperationalSignalKind `json:"kind"`
	Code          OperationalSignalCode `json:"code"`
	StartedAt     time.Time             `json:"started_at"`
	CompletedAt   time.Time             `json:"completed_at"`
	BaselineValue float64               `json:"baseline_value"`
	CurrentValue  float64               `json:"current_value"`
	Unit          OperationalSignalUnit `json:"unit"`
}

type OperationalSignalSet struct {
	SourceVersion string                         `json:"source_version,omitempty"`
	Observations  []OperationalSignalObservation `json:"observations,omitempty"`
	Complete      bool                           `json:"complete"`
	Truncated     bool                           `json:"truncated"`
	ReasonCode    string                         `json:"reason_code,omitempty"`
}

type TimelineStatus string

const (
	TimelineComplete       TimelineStatus = "COMPLETE"
	TimelineInconclusive   TimelineStatus = "INCONCLUSIVE"
	TimelineUnavailable    TimelineStatus = "UNAVAILABLE"
	TimelineSkippedNoSpike TimelineStatus = "SKIPPED_NO_SPIKE"
)

type TimelineSignal struct {
	OperationalSignalObservation
	Anomalous bool `json:"anomalous"`
}

type TimelineItemKind string

const (
	TimelineItemChange TimelineItemKind = "CHANGE"
	TimelineItemMetric TimelineItemKind = "METRIC"
	TimelineItemTrace  TimelineItemKind = "TRACE"
)

type IncidentTimelineItem struct {
	ID             string           `json:"id"`
	Kind           TimelineItemKind `json:"kind"`
	Code           string           `json:"code"`
	StartedAt      time.Time        `json:"started_at"`
	CompletedAt    time.Time        `json:"completed_at"`
	Statement      string           `json:"statement"`
	Anomalous      bool             `json:"anomalous"`
	EvidenceIDs    []string         `json:"evidence_ids"`
	ChangeEventIDs []string         `json:"change_event_ids,omitempty"`
	SignalIDs      []string         `json:"signal_ids,omitempty"`
}

type IncidentTimeline struct {
	Status          TimelineStatus         `json:"status"`
	MethodVersion   string                 `json:"method_version"`
	SourceVersion   string                 `json:"source_version,omitempty"`
	SourceComplete  bool                   `json:"source_complete"`
	SourceTruncated bool                   `json:"source_truncated"`
	Signals         []TimelineSignal       `json:"signals,omitempty"`
	Items           []IncidentTimelineItem `json:"items,omitempty"`
	MissingInputs   []string               `json:"missing_inputs,omitempty"`
}

func ValidateOperationalSignalQuery(query OperationalSignalQuery) error {
	if err := ValidateResourceID(query.ResourceID); err != nil {
		return fmt.Errorf("invalid operational signal query: %w", err)
	}
	if query.StartTime.IsZero() || query.EndTime.IsZero() || !query.StartTime.Before(query.EndTime) {
		return errors.New("invalid operational signal query time range")
	}
	if query.Limit < 1 || query.Limit > MaxOperationalSignals {
		return fmt.Errorf("operational signal limit must be between 1 and %d", MaxOperationalSignals)
	}
	return nil
}

func ValidateOperationalSignalSourceVersion(version string) error {
	return validateOperationalSignalText("operational signal source version", version, MaxOperationalSignalSourceVersionBytes)
}

func ValidateOperationalSignalObservation(observation OperationalSignalObservation, query OperationalSignalQuery) error {
	if !operationalSignalIdentifierPattern.MatchString(observation.ID) {
		return errors.New("operational signal ID is missing or invalid")
	}
	if observation.ResourceID != query.ResourceID {
		return errors.New("operational signal resource does not match query")
	}
	if observation.Kind != OperationalSignalMetric && observation.Kind != OperationalSignalTrace {
		return fmt.Errorf("unsupported operational signal kind %q", observation.Kind)
	}
	if observation.StartedAt.IsZero() || observation.CompletedAt.IsZero() || !observation.StartedAt.Before(observation.CompletedAt) {
		return errors.New("operational signal time range is invalid")
	}
	if observation.StartedAt.Before(query.StartTime) || observation.CompletedAt.After(query.EndTime) {
		return errors.New("operational signal is outside the governed query range")
	}
	if !finiteNonNegative(observation.BaselineValue) || !finiteNonNegative(observation.CurrentValue) {
		return errors.New("operational signal values must be finite and non-negative")
	}
	switch observation.Code {
	case OperationalSignalErrorRate:
		if observation.Unit != OperationalSignalRatio {
			return errors.New("error-rate signal must use RATIO")
		}
		if observation.BaselineValue > 1 || observation.CurrentValue > 1 {
			return errors.New("error-rate signal must be between zero and one")
		}
	case OperationalSignalLatencyP95:
		if observation.Unit != OperationalSignalMillisecond {
			return errors.New("latency signal must use MILLISECOND")
		}
	default:
		return fmt.Errorf("unsupported operational signal code %q", observation.Code)
	}
	return nil
}

func OperationalSignalIsAnomalous(code OperationalSignalCode, baseline, current float64) bool {
	switch code {
	case OperationalSignalErrorRate:
		return thresholdExceeded(baseline, current, 0.05)
	case OperationalSignalLatencyP95:
		return thresholdExceeded(baseline, current, 100)
	default:
		return false
	}
}

func OperationalSignalObservationLess(left, right OperationalSignalObservation) bool {
	if !left.StartedAt.Equal(right.StartedAt) {
		return left.StartedAt.Before(right.StartedAt)
	}
	if !left.CompletedAt.Equal(right.CompletedAt) {
		return left.CompletedAt.Before(right.CompletedAt)
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.ID < right.ID
}

func OperationalSignalStatement(signal TimelineSignal) string {
	status := "未达到异常阈值"
	if signal.Anomalous {
		status = "达到异常阈值"
	}
	source := "指标"
	if signal.Kind == OperationalSignalTrace {
		source = "Trace"
	}
	if signal.Code == OperationalSignalErrorRate {
		return fmt.Sprintf("%s错误率从 %.1f%% 变化到 %.1f%%，%s。", source, signal.BaselineValue*100, signal.CurrentValue*100, status)
	}
	return fmt.Sprintf("%s P95 延迟从 %.0fms 变化到 %.0fms，%s。", source, signal.BaselineValue, signal.CurrentValue, status)
}

func ChangeTimelineStatement(change ChangeEvent) string {
	return fmt.Sprintf("%s 变更 %s 在观察窗口内完成。", change.Kind, change.ID)
}

func ValidateOperationalSignalIdentifier(value string) error {
	if !operationalSignalIdentifierPattern.MatchString(value) {
		return errors.New("operational signal identifier is missing or invalid")
	}
	return nil
}

func ValidateOperationalSignalReason(reason string) bool {
	switch reason {
	case "", OperationalSignalReasonDisabled, OperationalSignalReasonIncomplete, OperationalSignalReasonTruncated:
		return true
	default:
		return false
	}
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func thresholdExceeded(baseline, current, absoluteIncrease float64) bool {
	if current-baseline < absoluteIncrease {
		return false
	}
	if baseline == 0 {
		return current >= absoluteIncrease
	}
	return current >= baseline*2
}

func validateOperationalSignalText(name, value string, maxBytes int) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and cannot have surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains control characters", name)
	}
	return nil
}
