package slsmock

import (
	"context"
	"fmt"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

// Executor returns deterministic observations for offline development and CI.
type Executor struct {
	Incomplete bool
}

func (e *Executor) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	switch spec.Name {
	case "current":
		return e.result(
			spec,
			"mock-current-count-before,mock-current-patterns,mock-current-instances,mock-current-count-after",
			120,
			[]domain.CountBucket{
				{Label: "payment_timeout", Count: 90},
				{Label: "inventory_lock", Count: 20},
				{Label: "signature_invalid", Count: 10},
			},
			[]domain.CountBucket{
				{Label: "order-pod-a", Count: 80},
				{Label: "order-pod-b", Count: 30},
				{Label: "order-pod-c", Count: 10},
			},
		)
	case "baseline":
		return e.result(
			spec,
			"mock-baseline-count-before,mock-baseline-patterns,mock-baseline-instances,mock-baseline-count-after",
			20,
			[]domain.CountBucket{
				{Label: "inventory_lock", Count: 10},
				{Label: "database_timeout", Count: 5},
				{Label: "payment_timeout", Count: 5},
			},
			[]domain.CountBucket{
				{Label: "order-pod-b", Count: 10},
				{Label: "order-pod-c", Count: 10},
			},
		)
	default:
		return domain.QueryResult{}, fmt.Errorf("unsupported mock query %q", spec.Name)
	}
}

func (e *Executor) result(spec domain.QuerySpec, queryID string, errorCount int64, patterns, instances []domain.CountBucket) (domain.QueryResult, error) {
	hash, err := fingerprint.JSON(spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	progress := "Complete"
	if e.Incomplete {
		progress = "Incomplete"
	}
	topError := ""
	var topErrorCount int64
	if len(patterns) > 0 {
		topError = patterns[0].Label
		topErrorCount = patterns[0].Count
	}
	return domain.QueryResult{
		QueryID:                 queryID,
		QuerySpecHash:           hash,
		ResourceID:              "mock/" + spec.Service + "/" + spec.Environment,
		TemplateID:              spec.TemplateID,
		TemplateVersion:         "mock-v2",
		SchemaFingerprint:       "mock-schema-v2",
		PolicyVersion:           "mock-policy-v2",
		Progress:                progress,
		Complete:                !e.Incomplete,
		NanosecondOrderedKnown:  true,
		NanosecondOrdered:       true,
		UsageKnown:              true,
		ProcessedRows:           errorCount * domain.ErrorAnalysisAPICalls,
		ProcessedBytes:          errorCount * 128,
		ElapsedMillisecond:      15,
		APICalls:                domain.ErrorAnalysisAPICalls,
		ErrorCount:              errorCount,
		TopError:                topError,
		TopErrorCount:           topErrorCount,
		ErrorPatterns:           append([]domain.CountBucket(nil), patterns...),
		Instances:               append([]domain.CountBucket(nil), instances...),
		ErrorPatternsExhaustive: bucketTotal(patterns) == errorCount,
		InstancesExhaustive:     bucketTotal(instances) == errorCount,
		PatternLimit:            domain.ErrorAnalysisPatternLimit,
		InstanceLimit:           domain.ErrorAnalysisInstanceLimit,
	}, nil
}

func bucketTotal(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

var _ ports.SLSExecutor = (*Executor)(nil)
