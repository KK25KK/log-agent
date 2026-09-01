package slsmock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

// Executor returns deterministic observations for offline development and CI.
type Executor struct {
	Incomplete bool
}

type mockGovernanceIdentity struct {
	Resource          domain.LogResource `json:"resource"`
	TemplateID        string             `json:"template_id"`
	PolicyVersion     string             `json:"policy_version"`
	SchemaFingerprint string             `json:"schema_fingerprint"`
	Budget            struct {
		MaxWindowNanoseconds      int64 `json:"max_window_nanoseconds"`
		IngestionGraceNanoseconds int64 `json:"ingestion_grace_nanoseconds"`
		TimeoutNanoseconds        int64 `json:"timeout_nanoseconds"`
		MaxRows                   int64 `json:"max_rows"`
		MaxAPICalls               int   `json:"max_api_calls"`
		MaxProcessedBytes         int64 `json:"max_processed_bytes"`
		MaxConcurrent             int   `json:"max_concurrent"`
		SchemaTTLNanoseconds      int64 `json:"schema_ttl_nanoseconds"`
		PatternLimit              int   `json:"pattern_limit"`
		InstanceLimit             int   `json:"instance_limit"`
		ExpectedAPICalls          int   `json:"expected_api_calls"`
		ExpectedResultRows        int   `json:"expected_result_rows"`
	} `json:"budget"`
}

// ResolveQueryGovernance returns the stable offline identity used by the mock
// executor. It deliberately excludes the time window; CheckpointExecutor binds
// this fingerprint together with the complete logical QuerySpec.
func (e *Executor) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	contract, ok := domain.QueryTemplateByID(spec.TemplateID)
	if spec.Service == "" || spec.Environment == "" || !ok {
		return "", fmt.Errorf("invalid mock query governance scope")
	}
	identity := mockGovernanceIdentity{
		Resource: domain.LogResource{
			ID: "mock/" + spec.Service + "/" + spec.Environment, CatalogVersion: "mock-catalog-v2",
			Service: spec.Service, Environment: spec.Environment, Endpoint: "mock://sls",
			Project: "mock-project", LogStore: "mock-logstore", TemplateVersion: contract.Version,
			Selectors: []domain.LogSelector{
				{Field: "service", Value: spec.Service},
				{Field: "environment", Value: spec.Environment},
			},
			ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"},
			ErrorField:    "error_type", InstanceField: "pod_name",
		},
		TemplateID: spec.TemplateID, PolicyVersion: "mock-policy-v2", SchemaFingerprint: "mock-schema-v2",
	}
	identity.Budget.MaxWindowNanoseconds = int64(2 * time.Hour)
	identity.Budget.IngestionGraceNanoseconds = int64(domain.DefaultIngestionGrace)
	identity.Budget.TimeoutNanoseconds = int64(30 * time.Second)
	identity.Budget.MaxRows = contract.ResultRows
	identity.Budget.MaxAPICalls = contract.APICalls
	identity.Budget.MaxProcessedBytes = 16 * 1024 * 1024
	identity.Budget.MaxConcurrent = 1
	identity.Budget.SchemaTTLNanoseconds = int64(time.Minute)
	identity.Budget.PatternLimit = contract.PatternLimit
	identity.Budget.InstanceLimit = contract.InstanceLimit
	identity.Budget.ExpectedAPICalls = contract.APICalls
	identity.Budget.ExpectedResultRows = int(contract.ResultRows)
	return fingerprint.JSON(identity)
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
	contract, ok := domain.QueryTemplateByID(spec.TemplateID)
	if !ok {
		return domain.QueryResult{}, fmt.Errorf("unsupported mock template %q", spec.TemplateID)
	}
	hash, err := fingerprint.JSON(spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	governanceFingerprint, err := e.ResolveQueryGovernance(context.Background(), spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	progress := "Complete"
	incompleteReason := ""
	if e.Incomplete {
		progress = "Incomplete"
		incompleteReason = "mock_incomplete_fixture"
	}
	topError := ""
	var topErrorCount int64
	if len(patterns) > 0 {
		topError = patterns[0].Label
		topErrorCount = patterns[0].Count
	}
	if !contract.Dimensional {
		queryID = strings.Split(queryID, ",")[0] + ",mock-count-after"
		patterns = nil
		instances = nil
		topError = ""
		topErrorCount = 0
	}
	return domain.QueryResult{
		QueryID:                 queryID,
		QuerySpecHash:           hash,
		ResourceID:              "mock/" + spec.Service + "/" + spec.Environment,
		TemplateID:              spec.TemplateID,
		TemplateVersion:         contract.Version,
		SchemaFingerprint:       "mock-schema-v2",
		PolicyVersion:           "mock-policy-v2",
		GovernanceFingerprint:   governanceFingerprint,
		Progress:                progress,
		Complete:                !e.Incomplete,
		IncompleteReason:        incompleteReason,
		NanosecondOrderedKnown:  true,
		NanosecondOrdered:       true,
		UsageKnown:              true,
		ProcessedRows:           errorCount * int64(contract.APICalls),
		ProcessedBytes:          errorCount * 128,
		ElapsedMillisecond:      15,
		APICalls:                contract.APICalls,
		ErrorCount:              errorCount,
		TopError:                topError,
		TopErrorCount:           topErrorCount,
		ErrorPatterns:           append([]domain.CountBucket(nil), patterns...),
		Instances:               append([]domain.CountBucket(nil), instances...),
		ErrorPatternsExhaustive: contract.Dimensional && bucketTotal(patterns) == errorCount,
		InstancesExhaustive:     contract.Dimensional && bucketTotal(instances) == errorCount,
		PatternLimit:            contract.PatternLimit,
		InstanceLimit:           contract.InstanceLimit,
	}, nil
}

func bucketTotal(buckets []domain.CountBucket) int64 {
	var total int64
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

var (
	_ ports.SLSExecutor             = (*Executor)(nil)
	_ ports.QueryGovernanceResolver = (*Executor)(nil)
)
