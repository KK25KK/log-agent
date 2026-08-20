package summarymock

import (
	"context"
	"reflect"
	"testing"

	"logagent/internal/domain"
)

func TestSummarizerIsDeterministicAndReferenceOnly(t *testing.T) {
	input := domain.SummaryInput{
		Findings:        []domain.SummaryInputFinding{{Statement: "错误突增", EvidenceIDs: []string{"ev-current"}}},
		Evidence:        []domain.SummaryInputEvidence{{ID: "ev-current", Name: "current", Complete: true, ErrorCount: 120}},
		Recommendations: []domain.SummaryInputRecommendation{{Code: "inspect", Statement: "检查", EvidenceIDs: []string{"ev-current"}}},
	}
	first, err := New().Summarize(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New().Summarize(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Mode != domain.SummaryModeMock || first.Draft.RecommendationCodes[0] != "inspect" {
		t.Fatalf("unexpected mock summary: %#v %#v", first, second)
	}
}

func TestSummarizerHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Summarize(ctx, domain.SummaryInput{}); err == nil {
		t.Fatal("want cancelled context error")
	}
}
