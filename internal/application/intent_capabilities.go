package application

import (
	"context"
	"errors"
	"sort"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

type IntentCapabilityUnion struct {
	sources []ports.IntentCapabilitySource
}

func NewIntentCapabilityUnion(sources ...ports.IntentCapabilitySource) (*IntentCapabilityUnion, error) {
	if len(sources) == 0 {
		return nil, errors.New("at least one intent capability source is required")
	}
	for _, source := range sources {
		if source == nil {
			return nil, errors.New("intent capability source is nil")
		}
	}
	return &IntentCapabilityUnion{sources: append([]ports.IntentCapabilitySource(nil), sources...)}, nil
}

func (u *IntentCapabilityUnion) ListAllowedCapabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error) {
	seen := make(map[domain.InvestigationCapability]struct{})
	result := make([]domain.InvestigationCapability, 0)
	for _, source := range u.sources {
		capabilities, err := source.ListAllowedCapabilities(ctx, principal)
		if err != nil {
			return nil, err
		}
		for _, capability := range capabilities {
			if _, duplicate := seen[capability]; duplicate {
				continue
			}
			seen[capability] = struct{}{}
			result = append(result, capability)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Service != result[right].Service {
			return result[left].Service < result[right].Service
		}
		if result[left].Environment != result[right].Environment {
			return result[left].Environment < result[right].Environment
		}
		return result[left].Intent < result[right].Intent
	})
	return result, nil
}

var _ ports.IntentCapabilitySource = (*IntentCapabilityUnion)(nil)
