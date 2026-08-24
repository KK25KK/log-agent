package runbookmock

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestDynamicSourceReturnsOnlyActuallyMatchedCodes(t *testing.T) {
	source := New()
	query := validQuery("mock/order-service/prod", []string{
		"compare_recent_changes",
		"inspect_hot_instance",
		"inspect_top_error_pattern",
	})

	set, err := source.Lookup(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.ValidateRunbookSet(set, query); err != nil {
		t.Fatalf("mock set does not satisfy domain contract: %v", err)
	}
	if !set.Complete || set.Truncated || set.ReasonCode != "" || set.SourceVersion != SourceVersion || len(set.Entries) != 1 {
		t.Fatalf("unexpected set: %#v", set)
	}
	entry := set.Entries[0]
	wantCodes := []string{"inspect_hot_instance", "inspect_top_error_pattern"}
	if entry.ID != "rb_error_spike_triage" || entry.Revision != "r1" || entry.ResourceID != query.ResourceID || entry.OwnerTeam != "order-oncall" || !entry.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected entry identity: %#v", entry)
	}
	if !reflect.DeepEqual(entry.MatchedRecommendationCodes, wantCodes) {
		t.Fatalf("matched codes=%v, want %v", entry.MatchedRecommendationCodes, wantCodes)
	}
	wantStepCodes := []domain.RunbookStepCode{
		domain.RunbookStepCodeVerifyErrorPattern,
		domain.RunbookStepCodeObserveHotInstance,
		domain.RunbookStepCodeEscalateServiceOwner,
	}
	if len(entry.Steps) != len(wantStepCodes) {
		t.Fatalf("step count=%d, want %d", len(entry.Steps), len(wantStepCodes))
	}
	for index, wantCode := range wantStepCodes {
		wantKind, wantInstruction, ok := domain.CanonicalRunbookStep(wantCode)
		if !ok {
			t.Fatalf("missing canonical template for %q", wantCode)
		}
		step := entry.Steps[index]
		if step.Code != wantCode || step.Kind != wantKind || step.Instruction != wantInstruction {
			t.Fatalf("step[%d]=%#v, want code=%q kind=%q instruction=%q", index, step, wantCode, wantKind, wantInstruction)
		}
	}
	if source.Stats().LookupCalls != 1 {
		t.Fatalf("lookup calls=%d, want 1", source.Stats().LookupCalls)
	}
}

func TestSourceReturnsCompleteEmptySetWhenNoCodeMatches(t *testing.T) {
	source := New()
	query := validQuery("mock/order-service/prod", []string{"compare_recent_changes"})

	set, err := source.Lookup(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if !set.Complete || set.Truncated || set.ReasonCode != "" || len(set.Entries) != 0 {
		t.Fatalf("unexpected no-match set: %#v", set)
	}
	if err := domain.ValidateRunbookSet(set, query); err != nil {
		t.Fatalf("complete no-match set is invalid: %v", err)
	}
}

func TestSourceDoesNotAddAnUnrequestedSupportedCode(t *testing.T) {
	tests := []string{"inspect_hot_instance", "inspect_top_error_pattern"}
	for _, code := range tests {
		t.Run(code, func(t *testing.T) {
			query := validQuery("mock/order-service/prod", []string{code})
			set, err := New().Lookup(context.Background(), query)
			if err != nil {
				t.Fatal(err)
			}
			if len(set.Entries) != 1 || !reflect.DeepEqual(set.Entries[0].MatchedRecommendationCodes, []string{code}) {
				t.Fatalf("unexpected matched codes for %q: %#v", code, set.Entries)
			}
		})
	}
}

func TestNewIncidentRequiresExactValidResource(t *testing.T) {
	if _, err := NewIncident("bad resource"); err == nil {
		t.Fatal("expected invalid fixture resource to fail closed")
	}
	source, err := NewIncident("mock/order-service/prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Lookup(context.Background(), validQuery("mock/order-service/prod", []string{"inspect_top_error_pattern"})); err != nil {
		t.Fatalf("exact resource was rejected: %v", err)
	}
	if _, err := source.Lookup(context.Background(), validQuery("mock/payment-service/prod", []string{"inspect_top_error_pattern"})); err == nil {
		t.Fatal("expected mismatched fixture resource to fail closed")
	}
	if source.Stats().LookupCalls != 1 {
		t.Fatalf("failed lookup changed accepted-call stats: %#v", source.Stats())
	}
}

func TestSourceHonorsCancellationAndRejectsInvalidQuery(t *testing.T) {
	source := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Lookup(ctx, validQuery("mock/order-service/prod", []string{"inspect_hot_instance"})); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lookup() error=%v, want context.Canceled", err)
	}

	invalid := validQuery("mock/order-service/prod", []string{"inspect_hot_instance"})
	invalid.ResourceID = "bad resource"
	if _, err := source.Lookup(context.Background(), invalid); err == nil {
		t.Fatal("expected invalid query to fail closed")
	}
	if source.Stats().LookupCalls != 0 {
		t.Fatalf("rejected lookup changed stats: %#v", source.Stats())
	}
}

func TestSourceReturnsDefensiveCopies(t *testing.T) {
	source := New()
	query := validQuery("mock/order-service/prod", []string{"inspect_hot_instance", "inspect_top_error_pattern"})
	first, err := source.Lookup(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	first.Entries[0].MatchedRecommendationCodes[0] = "mutated"
	first.Entries[0].Steps[0].Code = "MUTATED"
	first.Entries[0].Steps[0].Instruction = "mutated"

	second, err := source.Lookup(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	_, wantInstruction, _ := domain.CanonicalRunbookStep(domain.RunbookStepCodeVerifyErrorPattern)
	if second.Entries[0].MatchedRecommendationCodes[0] != "inspect_hot_instance" ||
		second.Entries[0].Steps[0].Code != domain.RunbookStepCodeVerifyErrorPattern ||
		second.Entries[0].Steps[0].Instruction != wantInstruction {
		t.Fatalf("source leaked returned mutation: %#v", second.Entries[0])
	}
}

func TestFixedUpdateTimeIsUTC(t *testing.T) {
	if updatedAt.Location() != time.UTC {
		t.Fatalf("updatedAt location=%v, want UTC", updatedAt.Location())
	}
}

func validQuery(resourceID string, codes []string) domain.RunbookQuery {
	return domain.RunbookQuery{
		ResourceID:          resourceID,
		RecommendationCodes: codes,
		Limit:               domain.MaxRunbookEntries,
	}
}
