package aliyuncli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

type fakeRunner struct {
	outputs [][]byte
	errors  []error
	args    [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.args = append(f.args, append([]string(nil), args...))
	index := len(f.args) - 1
	if index < len(f.errors) && f.errors[index] != nil {
		return nil, f.errors[index]
	}
	if index >= len(f.outputs) {
		return nil, errors.New("missing fake CLI output")
	}
	return f.outputs[index], nil
}

func TestBackendMapsCLIIndexSchema(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"logstoreName":"logstore","mode":"standard"}`),
		[]byte(`{"keys":{"service":{"type":"text"},"environment":{"type":"text"},"level":{"type":"text"},"error_type":{"type":"text","doc_value":true},"pod_name":{"type":"text","docValue":true}}}`),
	}}
	backend := testBackend(runner)
	schema, err := backend.GetSchema(context.Background(), testApprovedQuery().Resource)
	if err != nil {
		t.Fatal(err)
	}
	if schema.Fingerprint == "" || !schema.Fields["error_type"].DocValue || !schema.Fields["pod_name"].DocValue {
		t.Fatalf("unexpected schema: %#v", schema)
	}
	if len(runner.args) != 2 || runner.args[0][1] != "get-log-store" || runner.args[1][1] != "get-index" {
		t.Fatalf("unexpected metadata calls: %#v", runner.args)
	}
}

func TestBackendExecutesOnlyFixedAggregateCLIQueries(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		queryOutput(`[{"error_count":"120"}]`),
		queryOutput(`[{"bucket_key":"payment_timeout","bucket_count":"90"},{"bucket_key":"inventory_lock","bucket_count":"30"}]`),
		queryOutput(`[{"bucket_key":"order-pod-a","bucket_count":"80"},{"bucket_key":"order-pod-b","bucket_count":"40"}]`),
		queryOutput(`[{"error_count":"120"}]`),
	}}
	backend := testBackend(runner)
	result, err := backend.Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryID != "exec-1,exec-2,exec-3,exec-4" || result.ProviderRequestID != "" {
		t.Fatalf("execution/provider identity was mislabeled: %#v", result)
	}
	if result.Progress != "Complete" || !result.UsageKnown || !result.NanosecondOrderedKnown || !result.NanosecondOrdered {
		t.Fatalf("quality metadata was not preserved: %#v", result)
	}
	if result.ProcessedRows != 400 || result.ProcessedBytes != 4096 || result.ElapsedMillisecond != 20 || result.APICalls != 4 {
		t.Fatalf("usage was not accumulated: %#v", result)
	}
	if result.ErrorCount != 120 || result.TopError != "payment_timeout" || result.TopErrorCount != 90 {
		t.Fatalf("unexpected aggregate result: %#v", result)
	}
	if len(runner.args) != domain.ErrorAnalysisAPICalls {
		t.Fatalf("CLI calls=%d, want %d", len(runner.args), domain.ErrorAnalysisAPICalls)
	}
	for _, args := range runner.args {
		if args[0] != "sls" || args[1] != "get-logs-v2" || argument(args, "--project") != "project" || argument(args, "--logstore") != "logstore" {
			t.Fatalf("unexpected CLI arguments: %#v", args)
		}
		if argument(args, "--endpoint") != "https://cn-hangzhou.log.aliyuncs.com" || argument(args, "--is-accurate") != "true" || argument(args, "--line") != "0" {
			t.Fatalf("missing fixed CLI guardrails: %#v", args)
		}
	}
	wantFilter := `environment:"prod" AND level:"ERROR" AND service:"order-service"`
	if argument(runner.args[0], "--query") != wantFilter+" | SELECT count(*) AS error_count LIMIT 1" {
		t.Fatalf("unexpected count query: %s", argument(runner.args[0], "--query"))
	}
	if argument(runner.args[3], "--query") != argument(runner.args[0], "--query") {
		t.Fatal("verification query did not repeat initial count")
	}
}

func TestBackendFailsClosedWhenCLIUsageMetadataIsMissing(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"logs":[{"error_count":"1"}],"meta":{"progress":"Complete","isAccurate":true}}`),
	}}
	result, err := testBackend(runner).Execute(context.Background(), testApprovedQuery())
	if err == nil {
		t.Fatal("want missing later fake output error")
	}
	if result.UsageKnown || result.QueryID != "exec-1,exec-2" || result.APICalls != 2 {
		t.Fatalf("partial fail-closed metadata was lost: %#v", result)
	}
}

func TestBackendPreservesProviderIDOnlyWhenCLIExposesIt(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		queryOutputWithRequestID(`[{"error_count":"1"}]`, "provider-1"),
		queryOutputWithRequestID(`[{"bucket_key":"timeout","bucket_count":"1"}]`, "provider-2"),
		queryOutputWithRequestID(`[{"bucket_key":"pod-a","bucket_count":"1"}]`, "provider-3"),
		queryOutputWithRequestID(`[{"error_count":"1"}]`, "provider-4"),
	}}
	result, err := testBackend(runner).Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestID != "provider-1,provider-2,provider-3,provider-4" {
		t.Fatalf("provider IDs were not preserved: %#v", result)
	}
}

func TestBackendDowngradesWhenVisibleCountChanges(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		queryOutput(`[{"error_count":"1"}]`),
		queryOutput(`[{"bucket_key":"timeout","bucket_count":"1"}]`),
		queryOutput(`[{"bucket_key":"pod-a","bucket_count":"1"}]`),
		queryOutput(`[{"error_count":"2"}]`),
	}}
	result, err := testBackend(runner).Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.Progress != "Incomplete" || result.ErrorCount != 2 || result.ErrorPatternsExhaustive || result.InstancesExhaustive {
		t.Fatalf("changing visible set was accepted: %#v", result)
	}
}

func TestBackendRejectsUnsafeEndpointBeforeCLICall(t *testing.T) {
	runner := &fakeRunner{}
	query := testApprovedQuery()
	query.Resource.Endpoint = "https://example.com/path?query=1"
	if _, err := testBackend(runner).Execute(context.Background(), query); err == nil {
		t.Fatal("want unsafe endpoint error")
	}
	if len(runner.args) != 0 {
		t.Fatalf("CLI called with unsafe endpoint: %#v", runner.args)
	}
}

func TestBackendEscapesCatalogSelectorValues(t *testing.T) {
	resource := testApprovedQuery().Resource
	resource.Selectors = []domain.LogSelector{{Field: "service", Value: `order" OR *`}}
	countQuery, _, _, err := compileQueries(resource)
	if err != nil {
		t.Fatal(err)
	}
	if countQuery != `level:"ERROR" AND service:"order\" OR \*" | SELECT count(*) AS error_count LIMIT 1` {
		t.Fatalf("selector was not escaped: %s", countQuery)
	}
}

func TestBackendChecksMetadataWithoutLogQuery(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"projectName":"project"}`),
		[]byte(`{"logstoreName":"logstore","mode":"standard"}`),
		[]byte(`{"keys":{"service":{"type":"text"},"environment":{"type":"text"},"level":{"type":"text"},"error_type":{"type":"text","doc_value":true},"pod_name":{"type":"text","doc_value":true}}}`),
	}}
	checks, err := testBackend(runner).CheckResources(context.Background(), []domain.LogResource{testApprovedQuery().Resource})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].IndexedFields != 5 || checks[0].LogStoreMode != "standard" {
		t.Fatalf("unexpected checks: %#v", checks)
	}
	for _, args := range runner.args {
		if len(args) > 1 && args[1] == "get-logs-v2" {
			t.Fatalf("sls-check read log rows: %#v", args)
		}
	}
}

func TestCLIEnvironmentForcesProfileAndRemovesCredentialOverrides(t *testing.T) {
	environment := cliEnvironment([]string{
		"PATH=C:\\tools",
		"ALIBABA_CLOUD_ACCESS_KEY_ID=secret-id",
		"ALIBABA_CLOUD_ACCESS_KEY_SECRET=secret-value",
		"ALIBABA_CLOUD_SECURITY_TOKEN=secret-token",
		"ALICLOUD_ACCESS_KEY_ID=alias-secret",
		"ALIBABA_CLOUD_CREDENTIALS_URI=http://127.0.0.1/credentials",
		"ALIBABA_CLOUD_PROFILE_MODE=Anonymous",
		"ALIBABA_CLOUD_REGION_ID=cn-unsafe",
		"ALIBABA_CLOUD_ENDPOINT=https://unsafe.example.com",
		"ALIBABA_CLOUD_PROFILE=other",
		"DEBUG=true",
	}, "default")
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"secret-id", "secret-value", "secret-token", "alias-secret", "127.0.0.1", "Anonymous", "cn-unsafe", "unsafe.example.com", "PROFILE=other", "DEBUG=true"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("unsafe environment escaped: %s", joined)
		}
	}
	if !strings.Contains(joined, "ALIBABA_CLOUD_PROFILE=default") || !strings.Contains(joined, "ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL=false") {
		t.Fatalf("fixed CLI environment missing: %s", joined)
	}
}

func TestSanitizeCommandErrorDoesNotLeakProviderBodyOrQuery(t *testing.T) {
	err := sanitizeCommandError([]string{"sls", "get-logs-v2", "--query", "secret query"}, `{"Code":"InvalidQuery","Message":"customer@example.com secret query"}`, errors.New("exit"))
	if strings.Contains(err.Error(), "customer") || strings.Contains(err.Error(), "secret query") || !strings.Contains(err.Error(), "InvalidQuery") {
		t.Fatalf("unsafe CLI error: %v", err)
	}
}

func TestNewRejectsUnsafeProfileBeforeExecutableLookup(t *testing.T) {
	_, err := New(Config{CLIPath: "does-not-exist", Profile: "../../profile", RequestTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "Profile") {
		t.Fatalf("want unsafe Profile error, got %v", err)
	}
}

func TestNewAcceptsExplicitResolvedExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := New(Config{CLIPath: executable, Profile: "default", RequestTimeout: time.Second})
	if err != nil || backend == nil {
		t.Fatalf("resolve explicit executable: backend=%#v err=%v", backend, err)
	}
}

func queryOutput(logs string) []byte {
	return []byte(`{"logs":` + logs + `,"meta":{"progress":"Complete","processedRows":100,"processedBytes":1024,"elapsedMillisecond":5,"isAccurate":true}}`)
}

func queryOutputWithRequestID(logs, requestID string) []byte {
	return []byte(`{"logs":` + logs + `,"meta":{"progress":"Complete","processed_rows":"100","processed_bytes":"1024","elapsed_millisecond":"5","is_accurate":1},"requestId":"` + requestID + `"}`)
}

func argument(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func testBackend(runner commandRunner) *Backend {
	next := 0
	return &Backend{
		runner: runner, requestTimeout: time.Second,
		now: func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC) },
		newExecutionID: func() (string, error) {
			next++
			return "exec-" + strconv.Itoa(next), nil
		},
	}
}

func testApprovedQuery() domain.ApprovedQuery {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	return domain.ApprovedQuery{
		SpecHash: "hash", TemplateID: domain.ErrorAnalysisTemplateID, PolicyVersion: "policy-v2", SchemaFingerprint: "schema-v2",
		StartTime: start, EndTime: start.Add(30 * time.Minute), MaxRows: domain.ErrorAnalysisResultRows, MaxAPICalls: domain.ErrorAnalysisAPICalls,
		PatternLimit: domain.ErrorAnalysisPatternLimit, InstanceLimit: domain.ErrorAnalysisInstanceLimit, ExpectedAPICalls: domain.ErrorAnalysisAPICalls,
		Resource: domain.LogResource{
			ID: "order-prod", Endpoint: "https://cn-hangzhou.log.aliyuncs.com", Project: "project", LogStore: "logstore",
			TemplateVersion: domain.ErrorAnalysisTemplateVersion, ErrorField: "error_type", InstanceField: "pod_name",
			Selectors:     []domain.LogSelector{{Field: "service", Value: "order-service"}, {Field: "environment", Value: "prod"}},
			ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"},
		},
	}
}
