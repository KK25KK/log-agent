// Package signalmock provides deterministic metric and Trace aggregates for
// the credential-free cross-signal timeline acceptance path.
package signalmock

import (
	"context"
	"errors"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const SourceVersion = "mock-operational-v1"

type Stats struct {
	ListCalls int `json:"list_calls"`
}

type Source struct {
	mu        sync.Mutex
	query     *domain.OperationalSignalQuery
	set       domain.OperationalSignalSet
	listCalls int
}

// New returns a dynamic offline source for the normal mock worker and demo.
// It derives the current half of any valid governed query and performs no I/O.
func New() *Source {
	return &Source{}
}

// NewIncident returns one metric error-rate observation and one Trace-latency
// observation over the supplied current window. The expected source query also
// includes the immediately preceding equal-length baseline window.
func NewIncident(resourceID string, currentStart, currentEnd time.Time) (*Source, error) {
	if currentStart.IsZero() || !currentStart.Before(currentEnd) {
		return nil, errors.New("mock operational current window is invalid")
	}
	duration := currentEnd.Sub(currentStart)
	query := domain.OperationalSignalQuery{
		ResourceID: resourceID,
		StartTime:  currentStart.Add(-duration),
		EndTime:    currentEnd,
		Limit:      domain.MaxOperationalSignals,
	}
	if err := domain.ValidateOperationalSignalQuery(query); err != nil {
		return nil, err
	}
	set := incidentSet(resourceID, currentStart, currentEnd)
	for _, observation := range set.Observations {
		if err := domain.ValidateOperationalSignalObservation(observation, query); err != nil {
			return nil, err
		}
	}
	return &Source{query: &query, set: set}, nil
}

func (source *Source) List(ctx context.Context, query domain.OperationalSignalQuery) (domain.OperationalSignalSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.OperationalSignalSet{}, err
	}
	if err := domain.ValidateOperationalSignalQuery(query); err != nil {
		return domain.OperationalSignalSet{}, err
	}
	if source.query != nil && query != *source.query {
		return domain.OperationalSignalSet{}, errors.New("mock operational signal query does not match governed fixture")
	}
	source.mu.Lock()
	source.listCalls++
	set := source.set
	if source.query == nil {
		currentStart := query.StartTime.Add(query.EndTime.Sub(query.StartTime) / 2)
		set = incidentSet(query.ResourceID, currentStart, query.EndTime)
	}
	set = cloneSet(set)
	source.mu.Unlock()
	return set, nil
}

func (source *Source) Stats() Stats {
	source.mu.Lock()
	defer source.mu.Unlock()
	return Stats{ListCalls: source.listCalls}
}

func cloneSet(set domain.OperationalSignalSet) domain.OperationalSignalSet {
	set.Observations = append([]domain.OperationalSignalObservation(nil), set.Observations...)
	return set
}

func incidentSet(resourceID string, currentStart, currentEnd time.Time) domain.OperationalSignalSet {
	return domain.OperationalSignalSet{
		SourceVersion: SourceVersion,
		Complete:      true,
		Observations: []domain.OperationalSignalObservation{
			{
				ID: "metric_error_rate", ResourceID: resourceID,
				Kind: domain.OperationalSignalMetric, Code: domain.OperationalSignalErrorRate,
				StartedAt: currentStart, CompletedAt: currentEnd,
				BaselineValue: 0.02, CurrentValue: 0.12, Unit: domain.OperationalSignalRatio,
			},
			{
				ID: "trace_latency_p95", ResourceID: resourceID,
				Kind: domain.OperationalSignalTrace, Code: domain.OperationalSignalLatencyP95,
				StartedAt: currentStart, CompletedAt: currentEnd,
				BaselineValue: 120, CurrentValue: 420, Unit: domain.OperationalSignalMillisecond,
			},
		},
	}
}

var _ ports.OperationalSignalSource = (*Source)(nil)
