package command

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"logagent/internal/domain"
)

const maxWindow = 24 * time.Hour

var ErrUsage = errors.New("usage: /investigate <service> <environment> <duration>")

// ParseInvestigation parses the deliberately strict M0 command syntax.
func ParseInvestigation(text string, now time.Time) (domain.InvestigationRequest, error) {
	return ParseInvestigationWithGrace(text, now, domain.DefaultIngestionGrace)
}

// ParseInvestigationWithGrace anchors the requested duration at an ingestion
// watermark instead of the message timestamp, so normal near-real-time SLS
// indexing delay cannot silently turn a provisional window into a conclusion.
func ParseInvestigationWithGrace(text string, now time.Time, ingestionGrace time.Duration) (domain.InvestigationRequest, error) {
	if ingestionGrace < domain.MinimumIngestionGrace {
		return domain.InvestigationRequest{}, fmt.Errorf("ingestion grace must be at least %s", domain.MinimumIngestionGrace)
	}
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 4 || fields[0] != "/investigate" {
		return domain.InvestigationRequest{}, ErrUsage
	}

	service := strings.TrimSpace(fields[1])
	environment := strings.TrimSpace(fields[2])
	if service == "" || environment == "" {
		return domain.InvestigationRequest{}, ErrUsage
	}

	window, err := time.ParseDuration(fields[3])
	if err != nil || window <= 0 || window > maxWindow {
		return domain.InvestigationRequest{}, fmt.Errorf("duration must be greater than zero and at most %s: %w", maxWindow, ErrUsage)
	}

	end := now.UTC().Add(-ingestionGrace).Truncate(time.Second)
	return domain.InvestigationRequest{
		Service:     service,
		Environment: environment,
		StartTime:   end.Add(-window),
		EndTime:     end,
	}, nil
}
