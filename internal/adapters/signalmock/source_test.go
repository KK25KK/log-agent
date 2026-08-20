package signalmock

import (
	"context"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestSourceRequiresExactGovernedQueryAndReturnsCopy(t *testing.T) {
	currentStart := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	currentEnd := currentStart.Add(30 * time.Minute)
	source, err := NewIncident("mock/order-service/prod", currentStart, currentEnd)
	if err != nil {
		t.Fatal(err)
	}
	query := domain.OperationalSignalQuery{
		ResourceID: "mock/order-service/prod",
		StartTime:  currentStart.Add(-30 * time.Minute), EndTime: currentEnd,
		Limit: domain.MaxOperationalSignals,
	}
	first, err := source.List(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	first.Observations[0].ID = "mutated"
	second, err := source.List(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if second.Observations[0].ID != "metric_error_rate" || source.Stats().ListCalls != 2 {
		t.Fatalf("mock source leaked mutation or stats mismatch: %#v %#v", second, source.Stats())
	}
	query.EndTime = query.EndTime.Add(time.Second)
	if _, err := source.List(context.Background(), query); err == nil {
		t.Fatal("expected mismatched query to fail closed")
	}
}

func TestDynamicSourceSupportsNormalMockWorkerScope(t *testing.T) {
	start := time.Date(2026, 1, 2, 9, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	source := New()
	set, err := source.List(context.Background(), domain.OperationalSignalQuery{
		ResourceID: "mock/order-service/prod", StartTime: start, EndTime: end, Limit: domain.MaxOperationalSignals,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Complete || len(set.Observations) != 2 || !set.Observations[0].StartedAt.Equal(start.Add(30*time.Minute)) || !set.Observations[1].CompletedAt.Equal(end) {
		t.Fatalf("unexpected dynamic mock set: %#v", set)
	}
}
