package domain

import (
	"math"
	"testing"
	"time"
)

func TestValidateOperationalSignalObservation(t *testing.T) {
	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	query := OperationalSignalQuery{ResourceID: "resource/order/prod", StartTime: start, EndTime: start.Add(time.Hour), Limit: MaxOperationalSignals}
	valid := OperationalSignalObservation{
		ID: "metric-errors", ResourceID: query.ResourceID, Kind: OperationalSignalMetric,
		Code: OperationalSignalErrorRate, StartedAt: start.Add(30 * time.Minute), CompletedAt: start.Add(time.Hour),
		BaselineValue: 0.02, CurrentValue: 0.12, Unit: OperationalSignalRatio,
	}
	if err := ValidateOperationalSignalObservation(valid, query); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*OperationalSignalObservation)
	}{
		{name: "unknown kind", mutate: func(value *OperationalSignalObservation) { value.Kind = "RAW_SPAN" }},
		{name: "wrong resource", mutate: func(value *OperationalSignalObservation) { value.ResourceID = "other" }},
		{name: "outside range", mutate: func(value *OperationalSignalObservation) { value.CompletedAt = query.EndTime.Add(time.Second) }},
		{name: "NaN", mutate: func(value *OperationalSignalObservation) { value.CurrentValue = math.NaN() }},
		{name: "ratio over one", mutate: func(value *OperationalSignalObservation) { value.CurrentValue = 1.1 }},
		{name: "wrong unit", mutate: func(value *OperationalSignalObservation) { value.Unit = OperationalSignalMillisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := ValidateOperationalSignalObservation(candidate, query); err == nil {
				t.Fatalf("invalid observation accepted: %#v", candidate)
			}
		})
	}
}

func TestOperationalSignalAnomalyRules(t *testing.T) {
	tests := []struct {
		name              string
		code              OperationalSignalCode
		baseline, current float64
		want              bool
	}{
		{name: "error anomaly", code: OperationalSignalErrorRate, baseline: .02, current: .12, want: true},
		{name: "error absolute threshold", code: OperationalSignalErrorRate, baseline: .04, current: .08, want: false},
		{name: "latency anomaly", code: OperationalSignalLatencyP95, baseline: 120, current: 420, want: true},
		{name: "latency ratio only", code: OperationalSignalLatencyP95, baseline: 40, current: 90, want: false},
		{name: "zero baseline", code: OperationalSignalErrorRate, baseline: 0, current: .05, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OperationalSignalIsAnomalous(test.code, test.baseline, test.current); got != test.want {
				t.Fatalf("OperationalSignalIsAnomalous()=%t, want %t", got, test.want)
			}
		})
	}
}
