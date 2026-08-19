package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"logagent/internal/adapters/changecatalog"
	"logagent/internal/config"
	"logagent/internal/domain"
)

func TestBuildChangeSourceDefaultsToExplicitlyDisabled(t *testing.T) {
	source, err := buildChangeSource(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	set, err := source.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-service-prod",
		StartTime:  time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 19, 10, 30, 0, 0, time.UTC),
		Limit:      domain.MaxChangeEvents,
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.Complete || set.ReasonCode != changecatalog.ReasonSourceDisabled || len(set.Events) != 0 {
		t.Fatalf("disabled source fabricated change evidence: %#v", set)
	}
}

func TestBuildChangeSourceLoadsConfiguredCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changes.json")
	payload := []byte(`{
  "version": "test-v1",
  "events": [{
    "id": "chg_release_v2",
    "resource_id": "order-service-prod",
    "kind": "RELEASE",
    "started_at": "2026-08-19T09:55:00Z",
    "completed_at": "2026-08-19T09:58:00Z",
    "from_version": "v1",
    "to_version": "v2",
    "owner": "order-team",
    "summary": "release v2",
    "affected_instances": ["order-pod-a"],
    "affected_instances_complete": true
  }]
}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	source, err := buildChangeSource(config.Config{ChangeCatalogPath: path})
	if err != nil {
		t.Fatal(err)
	}
	set, err := source.List(context.Background(), domain.ChangeQuery{
		ResourceID: "order-service-prod",
		StartTime:  time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 8, 19, 10, 30, 0, 0, time.UTC),
		Limit:      domain.MaxChangeEvents,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Complete || set.SourceVersion != "test-v1" || len(set.Events) != 1 || set.Events[0].ID != "chg_release_v2" {
		t.Fatalf("configured source mismatch: %#v", set)
	}
}
