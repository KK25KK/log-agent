// Package runbookmock provides deterministic, human-review-only runbook
// entries for the credential-free SOP guidance acceptance path.
package runbookmock

import (
	"context"
	"errors"
	"sync"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const SourceVersion = "mock-runbook-v1"

var updatedAt = time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

type Stats struct {
	LookupCalls int `json:"lookup_calls"`
}

type Source struct {
	mu            sync.Mutex
	exactResource string
	lookupCalls   int
}

// New returns a dynamic offline source for the normal Mock worker. It accepts
// any valid governed resource identity and performs no I/O.
func New() *Source {
	return &Source{}
}

// NewIncident returns an offline source that accepts only the supplied
// governed resource identity.
func NewIncident(resourceID string) (*Source, error) {
	if err := domain.ValidateResourceID(resourceID); err != nil {
		return nil, err
	}
	return &Source{exactResource: resourceID}, nil
}

func (source *Source) Lookup(ctx context.Context, query domain.RunbookQuery) (domain.RunbookSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.RunbookSet{}, err
	}
	if err := domain.ValidateRunbookQuery(query); err != nil {
		return domain.RunbookSet{}, err
	}
	if source.exactResource != "" && query.ResourceID != source.exactResource {
		return domain.RunbookSet{}, errors.New("mock runbook resource does not match governed fixture")
	}

	source.mu.Lock()
	source.lookupCalls++
	source.mu.Unlock()

	set := buildSet(query)
	if err := domain.ValidateRunbookSet(set, query); err != nil {
		return domain.RunbookSet{}, err
	}
	return cloneSet(set), nil
}

func (source *Source) Stats() Stats {
	source.mu.Lock()
	defer source.mu.Unlock()
	return Stats{LookupCalls: source.lookupCalls}
}

func buildSet(query domain.RunbookQuery) domain.RunbookSet {
	matchedCodes := supportedCodes(query.RecommendationCodes)
	set := domain.RunbookSet{
		SourceVersion: SourceVersion,
		Complete:      true,
	}
	if len(matchedCodes) == 0 {
		return set
	}
	set.Entries = []domain.RunbookEntry{{
		ID:                         "rb_error_spike_triage",
		Revision:                   "r1",
		ResourceID:                 query.ResourceID,
		Title:                      "错误突增人工核查",
		OwnerTeam:                  "order-oncall",
		UpdatedAt:                  updatedAt,
		MatchedRecommendationCodes: matchedCodes,
		Steps: []domain.RunbookStep{
			governedStep("verify-error-pattern", domain.RunbookStepCodeVerifyErrorPattern),
			governedStep("observe-hot-instance", domain.RunbookStepCodeObserveHotInstance),
			governedStep("escalate-service-owner", domain.RunbookStepCodeEscalateServiceOwner),
		},
	}}
	return set
}

func governedStep(id string, code domain.RunbookStepCode) domain.RunbookStep {
	kind, instruction, _ := domain.CanonicalRunbookStep(code)
	return domain.RunbookStep{ID: id, Code: code, Kind: kind, Instruction: instruction}
}

func supportedCodes(codes []string) []string {
	matched := make([]string, 0, 2)
	for _, code := range codes {
		switch code {
		case "inspect_hot_instance", "inspect_top_error_pattern":
			matched = append(matched, code)
		}
	}
	return matched
}

func cloneSet(set domain.RunbookSet) domain.RunbookSet {
	set.Entries = append([]domain.RunbookEntry(nil), set.Entries...)
	for index := range set.Entries {
		set.Entries[index].MatchedRecommendationCodes = append([]string(nil), set.Entries[index].MatchedRecommendationCodes...)
		set.Entries[index].Steps = append([]domain.RunbookStep(nil), set.Entries[index].Steps...)
	}
	return set
}

var _ ports.RunbookSource = (*Source)(nil)
