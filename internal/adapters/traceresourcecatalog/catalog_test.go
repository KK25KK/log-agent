package traceresourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"logagent/internal/domain"
)

func testGroup() domain.TraceResourceGroup {
	return domain.TraceResourceGroup{
		ID: "dam-trace-test", CatalogVersion: "trace-test-v1", Service: "dam-server", Environment: "test",
		TemplateVersion: domain.TraceSearchTemplateVersion, PrimaryMemberID: "dam-server",
		Members: []domain.TraceResourceMember{{
			ID: "dam-server", Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha",
			LogStore: "2016-hyper-dam-file", TraceMode: domain.TraceQueryFullText,
			EnvironmentMode: domain.TraceEnvironmentField, EnvironmentField: "env",
			MessageField: "msg", LevelField: "level", EventTimeField: "__time__", NanosecondTimeField: "__time_ns_part__",
		}},
	}
}

func TestDAMExampleCatalogContainsEightGovernedMembers(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "trace-resources.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	groups := catalog.Groups()
	if len(groups) != 1 || len(groups[0].Members) != 8 || groups[0].PrimaryMemberID != "dam-server" {
		t.Fatalf("unexpected DAM example catalog: %#v", groups)
	}
}

func TestCatalogResolvesOnlyAuthorizedLogicalCapability(t *testing.T) {
	principal := domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"}
	catalog, err := New([]domain.TraceResourceGroup{testGroup()}, map[domain.Principal][]string{principal: {"dam-trace-test"}})
	if err != nil {
		t.Fatal(err)
	}
	group, err := catalog.ResolveTraceGroup(context.Background(), "dam-server", "test")
	if err != nil || group.PrimaryMemberID != "dam-server" || !catalog.AllowedTraceGroup(context.Background(), principal, group.ID) {
		t.Fatalf("unexpected resolution: group=%#v err=%v", group, err)
	}
	capabilities, err := catalog.ListAllowedCapabilities(context.Background(), principal)
	if err != nil || len(capabilities) != 1 || capabilities[0].TemplateID != domain.TraceSearchTemplateID {
		t.Fatalf("unexpected capabilities: %#v err=%v", capabilities, err)
	}
}

func TestCatalogRejectsUnsafeAndAmbiguousConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.TraceResourceGroup)
	}{
		{name: "missing primary", mutate: func(group *domain.TraceResourceGroup) { group.PrimaryMemberID = "missing" }},
		{name: "unsafe endpoint", mutate: func(group *domain.TraceResourceGroup) { group.Members[0].Endpoint = "http://unsafe" }},
		{name: "fulltext with field", mutate: func(group *domain.TraceResourceGroup) { group.Members[0].TraceField = "trace_id" }},
		{name: "unsafe field", mutate: func(group *domain.TraceResourceGroup) { group.Members[0].MessageField = "msg | select" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			group := testGroup()
			test.mutate(&group)
			if _, err := New([]domain.TraceResourceGroup{group}, map[domain.Principal][]string{}); err == nil {
				t.Fatal("unsafe Trace catalog was accepted")
			}
		})
	}
}

func TestLoadUsesStrictJSONAndDoesNotAcceptCatalogVersionFromFileGroup(t *testing.T) {
	input := `{"version":"v1","groups":[],"bindings":[],"unknown":true}`
	file := t.TempDir() + "/trace.json"
	if err := osWriteFile(file, []byte(input)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("strict JSON was not enforced: %v", err)
	}
}

func osWriteFile(path string, value []byte) error {
	return os.WriteFile(path, value, 0o600)
}
