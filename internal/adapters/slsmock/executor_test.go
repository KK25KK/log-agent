package slsmock

import (
	"context"
	"reflect"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestExecutorReturnsDeterministicM2Aggregates(t *testing.T) {
	executor := &Executor{}
	currentSpec := mockSpec("current")
	first, err := executor.Execute(context.Background(), currentSpec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), currentSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("mock result changed between runs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.ErrorCount != 120 || first.TopError != "payment_timeout" || first.TopErrorCount != 90 {
		t.Fatalf("unexpected current summary: %#v", first)
	}
	if first.APICalls != domain.ErrorAnalysisAPICalls || !first.ErrorPatternsExhaustive || !first.InstancesExhaustive {
		t.Fatalf("unexpected fixed-template metadata: %#v", first)
	}
	if len(first.ErrorPatterns) != 3 || first.ErrorPatterns[2].Label != "signature_invalid" {
		t.Fatalf("new-pattern fixture is missing: %#v", first.ErrorPatterns)
	}
	if len(first.Instances) != 3 || first.Instances[0].Count != 80 {
		t.Fatalf("instance distribution fixture is missing: %#v", first.Instances)
	}
}

func TestExecutorPreservesIncompleteFixture(t *testing.T) {
	result, err := (&Executor{Incomplete: true}).Execute(context.Background(), mockSpec("baseline"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Progress != "Incomplete" {
		t.Fatalf("incomplete fixture became complete: %#v", result)
	}
}

func mockSpec(name string) domain.QuerySpec {
	end := time.Date(2026, 8, 18, 10, 30, 0, 0, time.UTC)
	return domain.QuerySpec{
		InvestigationID: "inv_mock",
		Name:            name,
		TemplateID:      domain.ErrorAnalysisTemplateID,
		Service:         "order-service",
		Environment:     "prod",
		StartTime:       end.Add(-30 * time.Minute),
		EndTime:         end,
	}
}
