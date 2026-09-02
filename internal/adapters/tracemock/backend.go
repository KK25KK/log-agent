// Package tracemock provides deterministic Trace member results without network access.
package tracemock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

type Backend struct {
	mu        sync.Mutex
	calls     []string
	active    int
	maxActive int
	delay     time.Duration
}

func New() *Backend { return &Backend{} }

func NewWithDelay(delay time.Duration) *Backend { return &Backend{delay: delay} }

func (b *Backend) GetTraceSchema(ctx context.Context, member domain.TraceResourceMember) (domain.IndexSchema, error) {
	if err := ctx.Err(); err != nil {
		return domain.IndexSchema{}, err
	}
	fields := map[string]domain.IndexField{}
	for _, field := range []string{member.TraceField, member.EnvironmentField, member.MessageField, member.LevelField, member.OperationField} {
		if field != "" && !strings.HasPrefix(field, "__") {
			fields[field] = domain.IndexField{Type: "text", DocValue: true}
		}
	}
	hash, _ := fingerprint.JSON(fields)
	fullText := member.TraceMode == domain.TraceQueryFullText || member.EnvironmentMode == domain.TraceEnvironmentFullText
	return domain.IndexSchema{Fingerprint: hash, Fields: fields, FullText: fullText, FetchedAt: time.Unix(1788330000, 0).UTC()}, nil
}

func (b *Backend) SearchTrace(ctx context.Context, query domain.ApprovedTraceQuery) (domain.TraceBackendResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, query.Member.ID)
	b.active++
	if b.active > b.maxActive {
		b.maxActive = b.active
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()
	if b.delay > 0 {
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domain.TraceBackendResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.TraceBackendResult{}, err
	}
	events := []domain.TraceBackendEvent{}
	if query.Member.ID == "dam-server" {
		events = append(events, domain.TraceBackendEvent{
			EventTimeRaw: query.Spec.StartTime.Add(time.Second).Format(time.RFC3339Nano), Level: "error", Operation: "POST /dam/job",
			Message: "request accepted for trace " + query.Spec.TraceID,
		})
	} else if query.Member.ID == "dam-consume-fast" {
		events = append(events, domain.TraceBackendEvent{
			EventTimeRaw: query.Spec.StartTime.Add(2 * time.Second).Format(time.RFC3339Nano), Level: "error", Operation: "consume",
			Message: "processing failed: payment timeout for 10.0.0.1",
		})
	}
	return domain.TraceBackendResult{
		ExecutionID: fmt.Sprintf("trace-mock-%s", query.Member.ID), Progress: "Complete",
		ProcessedRows: int64(len(events)), ProcessedBytes: int64(256 + len(events)*128), ElapsedMillisecond: 3,
		UsageKnown: true, NanosecondOrderKnown: true, NanosecondOrdered: true, APICalls: 1, Events: events,
	}, nil
}

func (b *Backend) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

func (b *Backend) MaxActive() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxActive
}
