package slsmock

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

var mockSchemaFetchedAt = time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)

// Backend exposes the existing deterministic Executor fixture through the
// governed SLSBackend boundary used by application/query.Gateway.
type Backend struct {
	currentStart time.Time
	currentEnd   time.Time
	executor     Executor

	statsMu sync.Mutex
	stats   Stats
}

// Stats is a point-in-time copy of local mock-provider activity. APICalls
// counts the four simulated SLS requests represented by every successful
// fixed-template execution.
type Stats struct {
	SchemaCalls      int `json:"schema_calls"`
	ExecuteCalls     int `json:"execute_calls"`
	ProviderAPICalls int `json:"provider_api_calls"`
}

// NewBackend binds the fixture to one exact current window and its immediately
// preceding, equally-sized baseline window.
func NewBackend(currentStart, currentEnd time.Time) (*Backend, error) {
	currentStart = currentStart.UTC()
	currentEnd = currentEnd.UTC()
	if currentStart.IsZero() || currentEnd.IsZero() || !currentEnd.After(currentStart) {
		return nil, errors.New("mock current window must have non-zero start and end with end after start")
	}
	return &Backend{
		currentStart: currentStart,
		currentEnd:   currentEnd,
	}, nil
}

// GetSchema returns the complete fixed index shape required by the error
// analysis template. No provider or network call is performed.
func (b *Backend) GetSchema(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	b.recordSchemaCall()
	if err := ctx.Err(); err != nil {
		return domain.IndexSchema{}, err
	}
	if err := validateFixedResource(resource); err != nil {
		return domain.IndexSchema{}, err
	}
	return domain.IndexSchema{
		Fingerprint: "mock-schema-v2",
		FetchedAt:   mockSchemaFetchedAt,
		Fields: map[string]domain.IndexField{
			"service":       {Type: "text", DocValue: true},
			"env":           {Type: "text", DocValue: true},
			"level":         {Type: "text", DocValue: true},
			"error_message": {Type: "text", DocValue: true},
			"pod_name":      {Type: "text", DocValue: true},
		},
	}, nil
}

// Execute accepts only the two windows bound at construction. This prevents a
// malformed local request from silently receiving a plausible fixture for an
// unrelated time range.
func (b *Backend) Execute(ctx context.Context, query domain.ApprovedQuery) (domain.QueryResult, error) {
	b.recordExecuteCall()
	if err := ctx.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	if err := validateFixedResource(query.Resource); err != nil {
		return domain.QueryResult{}, err
	}

	name, ok := b.windowName(query.StartTime, query.EndTime)
	if !ok {
		return domain.QueryResult{}, fmt.Errorf(
			"unsupported mock query window [%s,%s); expected current [%s,%s) or baseline [%s,%s)",
			query.StartTime.UTC().Format(time.RFC3339Nano),
			query.EndTime.UTC().Format(time.RFC3339Nano),
			b.currentStart.Format(time.RFC3339Nano),
			b.currentEnd.Format(time.RFC3339Nano),
			b.baselineStart().Format(time.RFC3339Nano),
			b.currentStart.Format(time.RFC3339Nano),
		)
	}

	result, err := b.executor.Execute(ctx, domain.QuerySpec{
		Name:        name,
		TemplateID:  query.TemplateID,
		Service:     query.Resource.Service,
		Environment: query.Resource.Environment,
		StartTime:   query.StartTime.UTC(),
		EndTime:     query.EndTime.UTC(),
	})
	if err != nil {
		return domain.QueryResult{}, err
	}
	b.recordProviderAPICalls(result.APICalls)
	return result, nil
}

// Stats returns an immutable snapshot safe to read while workers are active.
func (b *Backend) Stats() Stats {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	return b.stats
}

func (b *Backend) recordSchemaCall() {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	b.stats.SchemaCalls++
}

func (b *Backend) recordExecuteCall() {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	b.stats.ExecuteCalls++
}

func (b *Backend) recordProviderAPICalls(calls int) {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	b.stats.ProviderAPICalls += calls
}

func (b *Backend) windowName(start, end time.Time) (string, bool) {
	start = start.UTC()
	end = end.UTC()
	if start.Equal(b.currentStart) && end.Equal(b.currentEnd) {
		return "current", true
	}
	if start.Equal(b.baselineStart()) && end.Equal(b.currentStart) {
		return "baseline", true
	}
	return "", false
}

func (b *Backend) baselineStart() time.Time {
	return b.currentStart.Add(-b.currentEnd.Sub(b.currentStart))
}

func validateFixedResource(resource domain.LogResource) error {
	if !reflect.DeepEqual(resource, fixedResource()) {
		return fmt.Errorf("mock backend rejects resource %q: %w", resource.ID, ports.ErrQueryDenied)
	}
	return nil
}

var _ ports.SLSBackend = (*Backend)(nil)
