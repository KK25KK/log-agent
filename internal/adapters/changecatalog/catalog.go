// Package changecatalog loads an immutable, administrator-owned change-event
// catalog. It is a read-only M3 adapter; catalog contents cannot select log
// resources or query text.
package changecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	// MaxListLimit is the hard upper bound for one change-source lookup.
	MaxListLimit = domain.MaxChangeEvents

	ReasonResultTruncated = "change_result_truncated"
	ReasonSourceDisabled  = "change_source_disabled"

	maxSourceVersionBytes = domain.MaxChangeSourceVersionBytes
	maxIdentifierBytes    = domain.MaxChangeIdentifierBytes
	maxVersionBytes       = domain.MaxChangeVersionBytes
	maxOwnerBytes         = domain.MaxChangeOwnerBytes
	maxSummaryBytes       = domain.MaxChangeSummaryBytes
	maxInstanceBytes      = domain.MaxAffectedInstanceBytes
	maxAffectedInstances  = domain.MaxAffectedInstances
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type catalogFile struct {
	Version *string        `json:"version"`
	Events  *[]eventConfig `json:"events"`
}

// Pointer fields make omission and JSON null distinguishable from deliberately
// supplied zero values. Every event field is part of the administrator-owned
// contract and must be present.
type eventConfig struct {
	ID                        *string            `json:"id"`
	ResourceID                *string            `json:"resource_id"`
	Kind                      *domain.ChangeKind `json:"kind"`
	StartedAt                 *time.Time         `json:"started_at"`
	CompletedAt               *time.Time         `json:"completed_at"`
	FromVersion               *string            `json:"from_version"`
	ToVersion                 *string            `json:"to_version"`
	Owner                     *string            `json:"owner"`
	Summary                   *string            `json:"summary"`
	AffectedInstances         *[]string          `json:"affected_instances"`
	AffectedInstancesComplete *bool              `json:"affected_instances_complete"`
}

// Catalog is immutable after Load and is safe for concurrent readers.
type Catalog struct {
	version          string
	eventsByResource map[string][]domain.ChangeEvent
}

// Load decodes exactly one versioned JSON catalog and validates every field.
func Load(path string) (*Catalog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("change catalog path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open change catalog: %w", err)
	}
	defer file.Close()

	config, err := decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode change catalog: %w", err)
	}
	catalog, err := build(config)
	if err != nil {
		return nil, fmt.Errorf("validate change catalog: %w", err)
	}
	return catalog, nil
}

func decode(reader io.Reader) (catalogFile, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var config catalogFile
	if err := decoder.Decode(&config); err != nil {
		return catalogFile{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return catalogFile{}, errors.New("catalog must contain exactly one JSON value")
		}
		return catalogFile{}, fmt.Errorf("read trailing JSON: %w", err)
	}
	return config, nil
}

func build(config catalogFile) (*Catalog, error) {
	version, err := requiredText("version", config.Version, maxSourceVersionBytes)
	if err != nil {
		return nil, err
	}
	if config.Events == nil {
		return nil, errors.New("events is required and must be an array")
	}

	catalog := &Catalog{
		version:          version,
		eventsByResource: make(map[string][]domain.ChangeEvent),
	}
	seenIDs := make(map[string]struct{}, len(*config.Events))
	for index, item := range *config.Events {
		event, eventErr := validateEvent(item)
		if eventErr != nil {
			return nil, fmt.Errorf("event %d: %w", index, eventErr)
		}
		if _, duplicate := seenIDs[event.ID]; duplicate {
			return nil, fmt.Errorf("event %d: duplicate event ID %q", index, event.ID)
		}
		seenIDs[event.ID] = struct{}{}
		catalog.eventsByResource[event.ResourceID] = append(catalog.eventsByResource[event.ResourceID], event)
	}

	for resourceID := range catalog.eventsByResource {
		events := catalog.eventsByResource[resourceID]
		sort.Slice(events, func(left, right int) bool {
			if !events[left].StartedAt.Equal(events[right].StartedAt) {
				return events[left].StartedAt.After(events[right].StartedAt)
			}
			return events[left].ID < events[right].ID
		})
		catalog.eventsByResource[resourceID] = events
	}
	return catalog, nil
}

func validateEvent(config eventConfig) (domain.ChangeEvent, error) {
	id, err := requiredIdentifier("id", config.ID)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	resourceID, err := requiredText("resource_id", config.ResourceID, maxIdentifierBytes)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	if err := domain.ValidateResourceID(resourceID); err != nil {
		return domain.ChangeEvent{}, err
	}
	if config.Kind == nil {
		return domain.ChangeEvent{}, errors.New("kind is required")
	}
	if *config.Kind != domain.ChangeKindRelease && *config.Kind != domain.ChangeKindConfig {
		return domain.ChangeEvent{}, fmt.Errorf("kind %q must be RELEASE or CONFIG", *config.Kind)
	}
	startedAt, completedAt, err := validateInterval(config.StartedAt, config.CompletedAt)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	fromVersion, err := optionalText("from_version", config.FromVersion, maxVersionBytes)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	toVersion, err := optionalText("to_version", config.ToVersion, maxVersionBytes)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	if *config.Kind == domain.ChangeKindRelease && toVersion == "" {
		return domain.ChangeEvent{}, errors.New("to_version is required for a RELEASE event")
	}
	if fromVersion != "" && toVersion != "" && fromVersion == toVersion {
		return domain.ChangeEvent{}, errors.New("from_version and to_version must differ")
	}
	owner, err := requiredText("owner", config.Owner, maxOwnerBytes)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	summary, err := requiredText("summary", config.Summary, maxSummaryBytes)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	instances, err := validateInstances(config.AffectedInstances)
	if err != nil {
		return domain.ChangeEvent{}, err
	}
	if config.AffectedInstancesComplete == nil {
		return domain.ChangeEvent{}, errors.New("affected_instances_complete is required")
	}

	event := domain.ChangeEvent{
		ID:                        id,
		ResourceID:                resourceID,
		Kind:                      *config.Kind,
		StartedAt:                 startedAt,
		CompletedAt:               completedAt,
		FromVersion:               fromVersion,
		ToVersion:                 toVersion,
		Owner:                     owner,
		Summary:                   summary,
		AffectedInstances:         instances,
		AffectedInstancesComplete: *config.AffectedInstancesComplete,
	}
	if err := domain.ValidateChangeEvent(event); err != nil {
		return domain.ChangeEvent{}, err
	}
	return event, nil
}

func validateInterval(started, completed *time.Time) (time.Time, time.Time, error) {
	if started == nil || started.IsZero() {
		return time.Time{}, time.Time{}, errors.New("started_at is required and must be non-zero")
	}
	if completed == nil || completed.IsZero() {
		return time.Time{}, time.Time{}, errors.New("completed_at is required and must be non-zero")
	}
	if !started.Before(*completed) {
		return time.Time{}, time.Time{}, errors.New("completed_at must be later than started_at")
	}
	return started.UTC(), completed.UTC(), nil
}

func validateInstances(values *[]string) ([]string, error) {
	if values == nil {
		return nil, errors.New("affected_instances is required and must be an array")
	}
	if len(*values) > maxAffectedInstances {
		return nil, fmt.Errorf("affected_instances exceeds %d entries", maxAffectedInstances)
	}

	instances := make([]string, len(*values))
	seen := make(map[string]struct{}, len(*values))
	for index, value := range *values {
		validated, err := validateText(fmt.Sprintf("affected_instances[%d]", index), value, maxInstanceBytes)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[validated]; duplicate {
			return nil, fmt.Errorf("affected_instances contains duplicate %q", validated)
		}
		seen[validated] = struct{}{}
		instances[index] = validated
	}
	return instances, nil
}

func requiredIdentifier(name string, value *string) (string, error) {
	validated, err := requiredText(name, value, maxIdentifierBytes)
	if err != nil {
		return "", err
	}
	if !identifierPattern.MatchString(validated) {
		return "", fmt.Errorf("%s %q must match %s", name, validated, identifierPattern.String())
	}
	return validated, nil
}

func requiredText(name string, value *string, maxBytes int) (string, error) {
	if value == nil {
		return "", fmt.Errorf("%s is required", name)
	}
	return validateText(name, *value, maxBytes)
}

func optionalText(name string, value *string, maxBytes int) (string, error) {
	if value == nil || *value == "" {
		return "", nil
	}
	return validateText(name, *value, maxBytes)
}

func validateText(name, value string, maxBytes int) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("%s must be non-empty and have no surrounding whitespace", name)
	}
	if len(value) > maxBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%s contains control characters", name)
	}
	return value, nil
}

// List returns the newest overlapping changes for the exact resource. Both the
// event and query intervals are half-open. Results are newest-first and bounded
// by the caller's limit, which itself cannot exceed MaxListLimit.
func (c *Catalog) List(ctx context.Context, query domain.ChangeQuery) (domain.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.ChangeSet{}, err
	}
	if err := validateQuery(query); err != nil {
		return domain.ChangeSet{}, err
	}

	matches := make([]domain.ChangeEvent, 0, query.Limit+1)
	for _, event := range c.eventsByResource[query.ResourceID] {
		if !overlaps(event.StartedAt, event.CompletedAt, query.StartTime, query.EndTime) {
			continue
		}
		matches = append(matches, cloneEvent(event))
	}

	result := domain.ChangeSet{
		SourceVersion: c.version,
		Complete:      true,
	}
	if len(matches) > query.Limit {
		matches = matches[:query.Limit]
		result.Complete = false
		result.Truncated = true
		result.ReasonCode = ReasonResultTruncated
	}
	result.Events = matches
	return result, nil
}

func validateQuery(query domain.ChangeQuery) error {
	if err := domain.ValidateResourceID(query.ResourceID); err != nil {
		return fmt.Errorf("invalid change query: %w", err)
	}
	if query.StartTime.IsZero() {
		return errors.New("invalid change query: start_time is required")
	}
	if query.EndTime.IsZero() {
		return errors.New("invalid change query: end_time is required")
	}
	if !query.StartTime.Before(query.EndTime) {
		return errors.New("invalid change query: end_time must be later than start_time")
	}
	if query.Limit < 1 || query.Limit > MaxListLimit {
		return fmt.Errorf("invalid change query: limit must be between 1 and %d", MaxListLimit)
	}
	return nil
}

func overlaps(eventStart, eventEnd, queryStart, queryEnd time.Time) bool {
	return eventStart.Before(queryEnd) && eventEnd.After(queryStart)
}

func cloneEvent(event domain.ChangeEvent) domain.ChangeEvent {
	event.AffectedInstances = append([]string(nil), event.AffectedInstances...)
	return event
}

// Disabled is the fail-closed source used when M3 change correlation has not
// been configured. It never fabricates an empty-but-complete result.
type Disabled struct{}

// DisabledSource is a descriptive alias for dependency-injection call sites.
type DisabledSource = Disabled

func NewDisabled() Disabled {
	return Disabled{}
}

func (Disabled) List(ctx context.Context, _ domain.ChangeQuery) (domain.ChangeSet, error) {
	if err := ctx.Err(); err != nil {
		return domain.ChangeSet{}, err
	}
	return domain.ChangeSet{
		Complete:   false,
		ReasonCode: ReasonSourceDisabled,
	}, nil
}

var (
	_ ports.ChangeSource = (*Catalog)(nil)
	_ ports.ChangeSource = Disabled{}
)
