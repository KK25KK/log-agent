package changecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestCatalogListFiltersOverlapsAndSorts(t *testing.T) {
	config := validCatalog()
	config.Events = eventSlicePointer([]eventConfig{
		validEventWith("release-newer", "order-prod", "2026-08-19T10:20:00Z", "2026-08-19T10:25:00Z"),
		validEventWith("config-tie-b", "order-prod", "2026-08-19T10:10:00Z", "2026-08-19T10:15:00Z"),
		validEventWith("config-tie-a", "order-prod", "2026-08-19T10:10:00Z", "2026-08-19T10:12:00Z"),
		validEventWith("spans-window-start", "order-prod", "2026-08-19T09:55:00Z", "2026-08-19T10:05:00Z"),
		validEventWith("ends-at-window-start", "order-prod", "2026-08-19T09:55:00Z", "2026-08-19T10:00:00Z"),
		validEventWith("starts-at-window-end", "order-prod", "2026-08-19T10:30:00Z", "2026-08-19T10:31:00Z"),
		validEventWith("other-resource", "payment-prod", "2026-08-19T10:20:00Z", "2026-08-19T10:25:00Z"),
	})
	catalog := loadTestCatalog(t, config)

	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceVersion != "2026-08-19.1" || !result.Complete || result.Truncated || result.ReasonCode != "" {
		t.Fatalf("unexpected result metadata: %#v", result)
	}
	wantIDs := []string{"release-newer", "config-tie-a", "config-tie-b", "spans-window-start"}
	if len(result.Events) != len(wantIDs) {
		t.Fatalf("event count=%d, want %d: %#v", len(result.Events), len(wantIDs), result.Events)
	}
	for index, wantID := range wantIDs {
		if result.Events[index].ID != wantID {
			t.Fatalf("event[%d].ID=%q, want %q", index, result.Events[index].ID, wantID)
		}
		if result.Events[index].StartedAt.Location() != time.UTC || result.Events[index].CompletedAt.Location() != time.UTC {
			t.Fatalf("event timestamps were not normalized to UTC: %#v", result.Events[index])
		}
	}
}

func TestCatalogListReturnsDefensiveCopy(t *testing.T) {
	catalog := loadTestCatalog(t, validCatalog())
	query := domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T09:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T11:00:00Z"),
		Limit:      MaxListLimit,
	}

	first, err := catalog.List(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	first.Events[0].AffectedInstances[0] = "mutated"
	first.Events[0].ID = "mutated"

	second, err := catalog.List(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if second.Events[0].ID != "release-20260819-001" || second.Events[0].AffectedInstances[0] != "order-7d9f" {
		t.Fatalf("catalog was mutated through returned data: %#v", second.Events[0])
	}
}

func TestCatalogListTruncatesWithoutClaimingCompleteness(t *testing.T) {
	config := validCatalog()
	config.Events = eventSlicePointer([]eventConfig{
		validEventWith("change-3", "order-prod", "2026-08-19T10:20:00Z", "2026-08-19T10:21:00Z"),
		validEventWith("change-2", "order-prod", "2026-08-19T10:10:00Z", "2026-08-19T10:11:00Z"),
		validEventWith("change-1", "order-prod", "2026-08-19T10:00:00Z", "2026-08-19T10:01:00Z"),
	})
	catalog := loadTestCatalog(t, config)

	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T09:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T11:00:00Z"),
		Limit:      2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].ID != "change-3" || result.Events[1].ID != "change-2" {
		t.Fatalf("unexpected bounded events: %#v", result.Events)
	}
	if result.Complete || !result.Truncated || result.ReasonCode != ReasonResultTruncated {
		t.Fatalf("truncated result claimed completeness: %#v", result)
	}
}

func TestCatalogSupportsOpaqueMockStyleResourceIDs(t *testing.T) {
	config := validCatalog()
	event := validEvent()
	event.ResourceID = stringPointer("mock/order-service/prod")
	config.Events = eventSlicePointer([]eventConfig{event})
	catalog := loadTestCatalog(t, config)

	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "mock/order-service/prod",
		StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].ResourceID != "mock/order-service/prod" {
		t.Fatalf("opaque resource identity was not preserved: %#v", result)
	}
}

func TestCatalogListNoMatchesIsComplete(t *testing.T) {
	catalog := loadTestCatalog(t, validCatalog())
	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "unknown-prod",
		StartTime:  mustTime(t, "2026-08-19T09:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T11:00:00Z"),
		Limit:      MaxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Truncated || result.ReasonCode != "" || len(result.Events) != 0 {
		t.Fatalf("unexpected no-match result: %#v", result)
	}
}

func TestCatalogListRejectsInvalidQuery(t *testing.T) {
	valid := domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	}
	tests := []struct {
		name   string
		mutate func(*domain.ChangeQuery)
		want   string
	}{
		{name: "missing resource", mutate: func(query *domain.ChangeQuery) { query.ResourceID = "" }, want: "resource ID"},
		{name: "unsafe resource", mutate: func(query *domain.ChangeQuery) { query.ResourceID = "order prod" }, want: "resource ID"},
		{name: "zero start", mutate: func(query *domain.ChangeQuery) { query.StartTime = time.Time{} }, want: "start_time"},
		{name: "zero end", mutate: func(query *domain.ChangeQuery) { query.EndTime = time.Time{} }, want: "end_time"},
		{name: "equal times", mutate: func(query *domain.ChangeQuery) { query.EndTime = query.StartTime }, want: "later"},
		{name: "reversed times", mutate: func(query *domain.ChangeQuery) { query.EndTime = query.StartTime.Add(-time.Second) }, want: "later"},
		{name: "zero limit", mutate: func(query *domain.ChangeQuery) { query.Limit = 0 }, want: "limit"},
		{name: "limit over hard maximum", mutate: func(query *domain.ChangeQuery) { query.Limit = MaxListLimit + 1 }, want: "limit"},
	}
	catalog := loadTestCatalog(t, validCatalog())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			_, err := catalog.List(context.Background(), query)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("List() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogAndDisabledHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query := domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	}

	catalog := loadTestCatalog(t, validCatalog())
	if _, err := catalog.List(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("Catalog.List() error=%v, want context.Canceled", err)
	}
	if _, err := NewDisabled().List(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("Disabled.List() error=%v, want context.Canceled", err)
	}
}

func TestDisabledReturnsExplicitIncompleteResult(t *testing.T) {
	result, err := NewDisabled().List(context.Background(), domain.ChangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Truncated || result.ReasonCode != ReasonSourceDisabled || len(result.Events) != 0 {
		t.Fatalf("unexpected disabled result: %#v", result)
	}
}

func TestCatalogRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown top-level field",
			content: `{"version":"v1","events":[],"unexpected":true}`,
			want:    "unknown field",
		},
		{
			name: "unknown event field",
			content: `{"version":"v1","events":[{` +
				`"id":"change-1","resource_id":"order-prod","kind":"RELEASE",` +
				`"started_at":"2026-08-19T10:00:00Z","completed_at":"2026-08-19T10:01:00Z",` +
				`"from_version":"v1","to_version":"v2","owner":"team","summary":"release",` +
				`"affected_instances":[],"affected_instances_complete":true,"unexpected":true}]}`,
			want: "unknown field",
		},
		{
			name:    "trailing JSON",
			content: `{"version":"v1","events":[]} {}`,
			want:    "exactly one JSON value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeRawCatalog(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsInvalidTopLevelConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config catalogFile
		want   string
	}{
		{name: "missing version", config: catalogFile{Events: eventSlicePointer(nil)}, want: "version"},
		{name: "blank version", config: catalogFile{Version: stringPointer(" "), Events: eventSlicePointer(nil)}, want: "version"},
		{name: "long version", config: catalogFile{Version: stringPointer(strings.Repeat("v", maxSourceVersionBytes+1)), Events: eventSlicePointer(nil)}, want: "exceeds"},
		{name: "control in version", config: catalogFile{Version: stringPointer("v1\n"), Events: eventSlicePointer(nil)}, want: "whitespace"},
		{name: "missing events", config: catalogFile{Version: stringPointer("v1")}, want: "events"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeCatalog(t, test.config))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogRejectsInvalidEventConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*eventConfig)
		want   string
	}{
		{name: "missing id", mutate: func(event *eventConfig) { event.ID = nil }, want: "id is required"},
		{name: "id with whitespace", mutate: func(event *eventConfig) { event.ID = stringPointer(" change-1") }, want: "whitespace"},
		{name: "invalid id", mutate: func(event *eventConfig) { event.ID = stringPointer("change/1") }, want: "must match"},
		{name: "long id", mutate: func(event *eventConfig) { event.ID = stringPointer(strings.Repeat("a", maxIdentifierBytes+1)) }, want: "exceeds"},
		{name: "missing resource", mutate: func(event *eventConfig) { event.ResourceID = nil }, want: "resource_id"},
		{name: "invalid resource", mutate: func(event *eventConfig) { event.ResourceID = stringPointer("order prod") }, want: "resource ID"},
		{name: "missing kind", mutate: func(event *eventConfig) { event.Kind = nil }, want: "kind is required"},
		{name: "invalid kind", mutate: func(event *eventConfig) { event.Kind = kindPointer(domain.ChangeKind("HOST")) }, want: "RELEASE or CONFIG"},
		{name: "missing start", mutate: func(event *eventConfig) { event.StartedAt = nil }, want: "started_at"},
		{name: "zero start", mutate: func(event *eventConfig) { event.StartedAt = timePointer(time.Time{}) }, want: "started_at"},
		{name: "missing completion", mutate: func(event *eventConfig) { event.CompletedAt = nil }, want: "completed_at"},
		{name: "zero duration", mutate: func(event *eventConfig) { event.CompletedAt = timePointer(*event.StartedAt) }, want: "later"},
		{name: "reversed interval", mutate: func(event *eventConfig) { event.CompletedAt = timePointer(event.StartedAt.Add(-time.Second)) }, want: "later"},
		{name: "missing release to version", mutate: func(event *eventConfig) { event.ToVersion = nil }, want: "to_version"},
		{name: "unchanged version", mutate: func(event *eventConfig) { event.ToVersion = stringPointer(*event.FromVersion) }, want: "must differ"},
		{name: "long from version", mutate: func(event *eventConfig) { event.FromVersion = stringPointer(strings.Repeat("v", maxVersionBytes+1)) }, want: "exceeds"},
		{name: "missing owner", mutate: func(event *eventConfig) { event.Owner = nil }, want: "owner"},
		{name: "long owner", mutate: func(event *eventConfig) { event.Owner = stringPointer(strings.Repeat("o", maxOwnerBytes+1)) }, want: "exceeds"},
		{name: "missing summary", mutate: func(event *eventConfig) { event.Summary = nil }, want: "summary"},
		{name: "summary control", mutate: func(event *eventConfig) { event.Summary = stringPointer("release\nunsafe") }, want: "control"},
		{name: "long summary", mutate: func(event *eventConfig) { event.Summary = stringPointer(strings.Repeat("s", maxSummaryBytes+1)) }, want: "exceeds"},
		{name: "missing affected instances", mutate: func(event *eventConfig) { event.AffectedInstances = nil }, want: "affected_instances"},
		{name: "too many affected instances", mutate: func(event *eventConfig) {
			values := make([]string, maxAffectedInstances+1)
			for index := range values {
				values[index] = "pod-" + strings.Repeat("x", index+1)
			}
			event.AffectedInstances = stringSlicePointer(values)
		}, want: "exceeds"},
		{name: "blank affected instance", mutate: func(event *eventConfig) { event.AffectedInstances = stringSlicePointer([]string{""}) }, want: "non-empty"},
		{name: "long affected instance", mutate: func(event *eventConfig) {
			event.AffectedInstances = stringSlicePointer([]string{strings.Repeat("p", maxInstanceBytes+1)})
		}, want: "exceeds"},
		{name: "duplicate affected instance", mutate: func(event *eventConfig) { event.AffectedInstances = stringSlicePointer([]string{"pod-a", "pod-a"}) }, want: "duplicate"},
		{name: "missing completeness marker", mutate: func(event *eventConfig) { event.AffectedInstancesComplete = nil }, want: "affected_instances_complete"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validEvent()
			test.mutate(&event)
			config := validCatalog()
			config.Events = eventSlicePointer([]eventConfig{event})
			_, err := Load(writeCatalog(t, config))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCatalogAppliesKindSpecificVersionRules(t *testing.T) {
	tests := []struct {
		name     string
		kind     domain.ChangeKind
		from     *string
		to       *string
		wantErr  string
		wantFrom string
		wantTo   string
	}{
		{name: "first release may omit from", kind: domain.ChangeKindRelease, to: stringPointer("v1"), wantTo: "v1"},
		{name: "release requires to", kind: domain.ChangeKindRelease, from: stringPointer("v1"), wantErr: "to_version"},
		{name: "config may omit both", kind: domain.ChangeKindConfig},
		{name: "config may contain only to", kind: domain.ChangeKindConfig, to: stringPointer("sha256:new"), wantTo: "sha256:new"},
		{name: "config may contain only from", kind: domain.ChangeKindConfig, from: stringPointer("sha256:old"), wantFrom: "sha256:old"},
		{name: "config versions must differ when both exist", kind: domain.ChangeKindConfig, from: stringPointer("same"), to: stringPointer("same"), wantErr: "must differ"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validEvent()
			event.Kind = kindPointer(test.kind)
			event.FromVersion = test.from
			event.ToVersion = test.to
			config := validCatalog()
			config.Events = eventSlicePointer([]eventConfig{event})
			catalog, err := Load(writeCatalog(t, config))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Load() error=%v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := catalog.List(context.Background(), domain.ChangeQuery{
				ResourceID: "order-prod",
				StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
				EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
				Limit:      MaxListLimit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Events) != 1 || result.Events[0].FromVersion != test.wantFrom || result.Events[0].ToVersion != test.wantTo {
				t.Fatalf("unexpected version projection: %#v", result.Events)
			}
		})
	}
}

func TestCatalogRejectsDuplicateEventID(t *testing.T) {
	config := validCatalog()
	duplicate := validEvent()
	duplicate.ResourceID = stringPointer("payment-prod")
	config.Events = eventSlicePointer([]eventConfig{validEvent(), duplicate})

	_, err := Load(writeCatalog(t, config))
	if err == nil || !strings.Contains(err.Error(), "duplicate event ID") {
		t.Fatalf("Load() error=%v, want duplicate event ID", err)
	}
}

func TestCatalogAllowsEmptyEventArray(t *testing.T) {
	config := validCatalog()
	config.Events = eventSlicePointer([]eventConfig{})
	catalog := loadTestCatalog(t, config)

	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-prod",
		StartTime:  mustTime(t, "2026-08-19T10:00:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Events) != 0 {
		t.Fatalf("unexpected empty-catalog result: %#v", result)
	}
}

func TestLoadRejectsMissingPath(t *testing.T) {
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("Load() error=%v, want path validation", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "open change catalog") {
		t.Fatalf("Load() error=%v, want open error", err)
	}
}

func TestShippedExampleCatalogLoads(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "change-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-service-prod",
		StartTime:  mustTime(t, "2026-08-19T09:30:00Z"),
		EndTime:    mustTime(t, "2026-08-19T10:30:00Z"),
		Limit:      MaxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.SourceVersion == "" || len(result.Events) != 1 || result.Events[0].ResourceID != "order-service-prod" {
		t.Fatalf("example catalog is not usable: %#v", result)
	}
}

func validCatalog() catalogFile {
	return catalogFile{
		Version: stringPointer("2026-08-19.1"),
		Events:  eventSlicePointer([]eventConfig{validEvent()}),
	}
}

func validEvent() eventConfig {
	return eventConfig{
		ID:                        stringPointer("release-20260819-001"),
		ResourceID:                stringPointer("order-prod"),
		Kind:                      kindPointer(domain.ChangeKindRelease),
		StartedAt:                 timePointer(mustParseTime("2026-08-19T10:12:00Z")),
		CompletedAt:               timePointer(mustParseTime("2026-08-19T10:15:00Z")),
		FromVersion:               stringPointer("order-service:v41"),
		ToVersion:                 stringPointer("order-service:v42"),
		Owner:                     stringPointer("payments-oncall"),
		Summary:                   stringPointer("Deploy order-service v42"),
		AffectedInstances:         stringSlicePointer([]string{"order-7d9f", "order-8a1c"}),
		AffectedInstancesComplete: boolPointer(true),
	}
}

func validEventWith(id, resourceID, startedAt, completedAt string) eventConfig {
	event := validEvent()
	event.ID = stringPointer(id)
	event.ResourceID = stringPointer(resourceID)
	event.StartedAt = timePointer(mustParseTime(startedAt))
	event.CompletedAt = timePointer(mustParseTime(completedAt))
	return event
}

func loadTestCatalog(t *testing.T, config catalogFile) *Catalog {
	t.Helper()
	catalog, err := Load(writeCatalog(t, config))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func writeCatalog(t *testing.T, config catalogFile) string {
	t.Helper()
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return writeRawCatalog(t, string(payload))
}

func writeRawCatalog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "changes.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustParseTime(raw string) time.Time {
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		panic(err)
	}
	return value
}

func stringPointer(value string) *string {
	return &value
}

func stringSlicePointer(value []string) *[]string {
	return &value
}

func eventSlicePointer(value []eventConfig) *[]eventConfig {
	return &value
}

func kindPointer(value domain.ChangeKind) *domain.ChangeKind {
	return &value
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}
