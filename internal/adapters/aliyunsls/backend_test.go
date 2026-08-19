package aliyunsls

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"

	"logagent/internal/domain"
)

type fakeReader struct {
	projectExists  bool
	logstore       *sls.LogStore
	index          *sls.Index
	responses      []*sls.GetLogsResponse
	err            error
	requests       []*sls.GetLogRequest
	projectsCalled int
	storesCalled   int
	indexCalled    int
	delayOnCall    int
	delay          time.Duration
	queryErrOnCall int
	queryErr       error
}

func (f *fakeReader) CheckProjectExist(_ string) (bool, error) {
	f.projectsCalled++
	return f.projectExists, f.err
}

func (f *fakeReader) GetLogStore(_, _ string) (*sls.LogStore, error) {
	f.storesCalled++
	return f.logstore, f.err
}

func (f *fakeReader) GetIndex(_, _ string) (*sls.Index, error) {
	f.indexCalled++
	return f.index, f.err
}

func (f *fakeReader) GetLogsV2(_, _ string, request *sls.GetLogRequest) (*sls.GetLogsResponse, error) {
	f.requests = append(f.requests, request)
	if f.delayOnCall == len(f.requests) {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.queryErrOnCall == len(f.requests) {
		return nil, f.queryErr
	}
	if len(f.responses) < len(f.requests) {
		return nil, errors.New("missing fake response")
	}
	return f.responses[len(f.requests)-1], nil
}

func TestBackendMapsIndexSchema(t *testing.T) {
	reader := validReader()
	backend := testBackend(reader)

	schema, err := backend.GetSchema(context.Background(), testApprovedQuery().Resource)
	if err != nil {
		t.Fatal(err)
	}
	if schema.Fingerprint == "" || schema.Fields["error_type"].Type != "text" || !schema.Fields["error_type"].DocValue {
		t.Fatalf("unexpected schema: %#v", schema)
	}
}

func TestBackendRejectsQueryModeBeforeReadingIndex(t *testing.T) {
	reader := validReader()
	reader.logstore.Mode = "query"
	backend := testBackend(reader)

	_, err := backend.GetSchema(context.Background(), testApprovedQuery().Resource)
	if err == nil || !strings.Contains(err.Error(), "standard mode") {
		t.Fatalf("want standard-mode error, got %v", err)
	}
	if reader.indexCalled != 0 || len(reader.requests) != 0 {
		t.Fatalf("backend continued after incompatible LogStore mode: index=%d query=%d", reader.indexCalled, len(reader.requests))
	}
}

func TestBackendExecutesOnlyFixedAggregateQueries(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "120"}}, "Complete", `{"isAccurate":1,"limited":"100"}`),
		response("request-patterns", []map[string]string{
			{"bucket_key": "payment_timeout", "bucket_count": "90"},
			{"bucket_key": "inventory_lock", "bucket_count": "20"},
			{"bucket_key": "signature_invalid", "bucket_count": "10"},
		}, "Complete", `{"isAccurate":1,"limited":"5"}`),
		response("request-instances", []map[string]string{
			{"bucket_key": "order-pod-a", "bucket_count": "80"},
			{"bucket_key": "order-pod-b", "bucket_count": "40"},
		}, "Complete", `{"isAccurate":1,"limited":"5"}`),
		response("request-count-verification", []map[string]string{{"error_count": "120"}}, "Complete", `{"isAccurate":1,"limited":"100"}`),
	}
	backend := testBackend(reader)

	result, err := backend.Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.QueryID != "request-count,request-patterns,request-instances,request-count-verification" || result.Progress != "Complete" || result.ErrorCount != 120 || result.TopErrorCount != 90 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.ProcessedRows != 400 || result.ProcessedBytes != 4096 || result.ElapsedMillisecond != 20 {
		t.Fatalf("provider usage values were not preserved: %#v", result)
	}
	if !result.UsageKnown || !result.NanosecondOrderedKnown || !result.NanosecondOrdered || result.Truncated || result.APICalls != domain.ErrorAnalysisAPICalls {
		t.Fatalf("quality metadata was not preserved: %#v", result)
	}
	if !result.ErrorPatternsExhaustive || !result.InstancesExhaustive || len(result.ErrorPatterns) != 3 || len(result.Instances) != 2 {
		t.Fatalf("aggregate buckets were not preserved: %#v", result)
	}
	if len(reader.requests) != domain.ErrorAnalysisAPICalls {
		t.Fatalf("want %d fixed requests, got %d", domain.ErrorAnalysisAPICalls, len(reader.requests))
	}
	wantFilter := `environment:"prod" AND level:"ERROR" AND service:"order-service"`
	if reader.requests[0].Query != wantFilter+" | SELECT count(*) AS error_count LIMIT 1" {
		t.Fatalf("unexpected count query: %s", reader.requests[0].Query)
	}
	if !strings.Contains(reader.requests[1].Query, `COALESCE(NULLIF("error_type", ''), '[missing]') AS bucket_key`) || !strings.HasSuffix(reader.requests[1].Query, `ORDER BY bucket_count DESC, bucket_key ASC LIMIT 5`) {
		t.Fatalf("unexpected pattern query: %s", reader.requests[1].Query)
	}
	if !strings.Contains(reader.requests[2].Query, `COALESCE(NULLIF("pod_name", ''), '[missing]') AS bucket_key`) || !strings.HasSuffix(reader.requests[2].Query, `ORDER BY bucket_count DESC, bucket_key ASC LIMIT 5`) {
		t.Fatalf("unexpected instance query: %s", reader.requests[2].Query)
	}
	if reader.requests[3].Query != reader.requests[0].Query {
		t.Fatalf("verification count must repeat the initial count query: first=%s verification=%s", reader.requests[0].Query, reader.requests[3].Query)
	}
	if reader.requests[0].From != testApprovedQuery().StartTime.Unix() || reader.requests[0].To != testApprovedQuery().EndTime.Unix() {
		t.Fatalf("unexpected query range: %#v", reader.requests[0])
	}
	if reader.requests[0].Lines != 0 || reader.requests[1].Lines != 0 || reader.requests[2].Lines != 0 || reader.requests[3].Lines != 0 {
		t.Fatalf("SQL requests must not rely on the ignored line parameter: %#v", reader.requests)
	}
}

func TestBackendTreatsTopFiveAsBoundedTemplateNotTruncation(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "100"}}, "Complete", `{"isAccurate":0}`),
		response("request-patterns", []map[string]string{
			{"bucket_key": "a", "bucket_count": "20"},
			{"bucket_key": "b", "bucket_count": "15"},
			{"bucket_key": "c", "bucket_count": "10"},
			{"bucket_key": "d", "bucket_count": "5"},
			{"bucket_key": "e", "bucket_count": "5"},
		}, "Complete", `{"isAccurate":0,"limited":"5"}`),
		response("request-instances", []map[string]string{
			{"bucket_key": "pod-a", "bucket_count": "20"},
			{"bucket_key": "pod-b", "bucket_count": "15"},
			{"bucket_key": "pod-c", "bucket_count": "10"},
			{"bucket_key": "pod-d", "bucket_count": "5"},
			{"bucket_key": "pod-e", "bucket_count": "5"},
		}, "Complete", `{"isAccurate":0,"limited":"5"}`),
		response("request-count-verification", []map[string]string{{"error_count": "100"}}, "Complete", `{"isAccurate":0}`),
	}

	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated || result.ErrorPatternsExhaustive || result.InstancesExhaustive {
		t.Fatalf("bounded Top5 was confused with transport truncation or exhaustive data: %#v", result)
	}
	if !result.NanosecondOrderedKnown || result.NanosecondOrdered {
		t.Fatalf("isAccurate metadata was not mapped as nanosecond ordering: %#v", result)
	}
}

func TestBackendRejectsMalformedAggregateBuckets(t *testing.T) {
	validCount := response("count", []map[string]string{{"error_count": "5"}}, "Complete", `{"isAccurate":1}`)
	validInstances := response("instances", []map[string]string{{"bucket_key": "pod-a", "bucket_count": "5"}}, "Complete", `{"isAccurate":1}`)
	tests := []struct {
		name string
		rows []map[string]string
	}{
		{name: "missing label", rows: []map[string]string{{"bucket_count": "5"}}},
		{name: "zero count", rows: []map[string]string{{"bucket_key": "timeout", "bucket_count": "0"}}},
		{name: "duplicate label", rows: []map[string]string{{"bucket_key": "timeout", "bucket_count": "3"}, {"bucket_key": "timeout", "bucket_count": "2"}}},
		{name: "sum above total", rows: []map[string]string{{"bucket_key": "timeout", "bucket_count": "6"}}},
		{name: "over row limit", rows: []map[string]string{
			{"bucket_key": "a", "bucket_count": "1"}, {"bucket_key": "b", "bucket_count": "1"},
			{"bucket_key": "c", "bucket_count": "1"}, {"bucket_key": "d", "bucket_count": "1"},
			{"bucket_key": "e", "bucket_count": "1"}, {"bucket_key": "f", "bucket_count": "1"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			patterns := response("patterns", test.rows, "Complete", `{"isAccurate":1}`)
			if _, err := mapAnalysis(validCount, patterns, validInstances, validCount); err == nil {
				t.Fatal("want malformed aggregate error")
			}
		})
	}
}

func TestBackendDoesNotLeakSensitiveBucketLabelsInValidationErrors(t *testing.T) {
	secret := "customer@example.com"
	_, _, err := parseBuckets([]map[string]string{
		{"bucket_key": secret, "bucket_count": "1"},
		{"bucket_key": secret, "bucket_count": "1"},
	}, domain.ErrorAnalysisPatternLimit)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe bucket error: %v", err)
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

func TestBackendPreservesIncompleteProviderProgress(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "12"}}, "Incomplete", `{"isAccurate":1}`),
		response("request-patterns", []map[string]string{{"bucket_key": "timeout", "bucket_count": "12"}}, "Complete", `{"isAccurate":1}`),
		response("request-instances", []map[string]string{{"bucket_key": "pod-a", "bucket_count": "12"}}, "Complete", `{"isAccurate":1}`),
		response("request-count-verification", []map[string]string{{"error_count": "12"}}, "Complete", `{"isAccurate":1}`),
	}
	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.Progress != "Incomplete" {
		t.Fatalf("incomplete progress was lost: %#v", result)
	}
}

func TestBackendDowngradesWhenVisibleCountChangesAcrossAggregates(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "100"}}, "Complete", `{"isAccurate":1}`),
		response("request-patterns", []map[string]string{{"bucket_key": "timeout", "bucket_count": "100"}}, "Complete", `{"isAccurate":1}`),
		response("request-instances", []map[string]string{{"bucket_key": "pod-a", "bucket_count": "100"}}, "Complete", `{"isAccurate":1}`),
		response("request-count-verification", []map[string]string{{"error_count": "200"}}, "Complete", `{"isAccurate":1}`),
	}

	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err != nil {
		t.Fatal(err)
	}
	if result.Progress != "Incomplete" || result.ErrorCount != 200 || result.ErrorPatternsExhaustive || result.InstancesExhaustive {
		t.Fatalf("changing visible set was accepted as a stable snapshot: %#v", result)
	}
	if !strings.Contains(result.IncompleteReason, "changed") || result.APICalls != domain.ErrorAnalysisAPICalls {
		t.Fatalf("snapshot downgrade lacks audit metadata: %#v", result)
	}
}

func TestBackendRejectsSecondResponseThatArrivesAfterDeadline(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "12"}}, "Complete", `{"isAccurate":1}`),
		response("request-patterns", []map[string]string{{"bucket_key": "timeout", "bucket_count": "12"}}, "Complete", `{"isAccurate":1}`),
	}
	reader.delayOnCall = 2
	reader.delay = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result, err := testBackend(reader).Execute(ctx, testApprovedQuery())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline failure, got result=%#v err=%v", result, err)
	}
	if result.Complete {
		t.Fatalf("late SDK response became complete: %#v", result)
	}
}

func TestBackendConfigurationRejectsMissingCredentials(t *testing.T) {
	_, err := New(Config{CredentialMode: "static", RequestTimeout: time.Second})
	if err == nil {
		t.Fatal("want missing credential error")
	}
}

func TestSDKServerRetryIsDisabled(t *testing.T) {
	if sls.RetryOnServerErrorEnabled {
		t.Fatal("SDK server retry must remain disabled so physical attempts stay auditable")
	}
}

func TestBackendBoundsSDKRetryWindow(t *testing.T) {
	const timeout = 137 * time.Millisecond
	backend, err := New(Config{
		CredentialMode:  "static",
		AccessKeyID:     "test-access-key-id",
		AccessKeySecret: "test-access-key-secret",
		RequestTimeout:  timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := backend.newReader("https://cn-hangzhou.log.aliyuncs.com").(*sls.Client)
	if !ok {
		t.Fatal("backend reader is not the pinned SDK client")
	}
	if client.RetryTimeOut != timeout {
		t.Fatalf("retry timeout = %s, want %s", client.RetryTimeOut, timeout)
	}
	if client.HTTPClient == nil || client.HTTPClient.Timeout != timeout {
		t.Fatalf("HTTP timeout is not bounded by %s", timeout)
	}
}

func TestBackendChecksConfiguredMetadataWithoutLogQuery(t *testing.T) {
	reader := validReader()
	backend := testBackend(reader)
	checks, err := backend.CheckResources(context.Background(), []domain.LogResource{testApprovedQuery().Resource})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].SchemaFingerprint == "" || checks[0].LogStoreMode != "standard" || len(reader.requests) != 0 {
		t.Fatalf("unexpected resource check: %#v requests=%d", checks, len(reader.requests))
	}
}

func TestBackendReturnsSafeProviderErrorsAndPartialUsage(t *testing.T) {
	reader := validReader()
	reader.err = &sls.Error{
		HTTPCode:  400,
		Code:      "InvalidQuery",
		Message:   `selector-secret | SELECT raw_query`,
		RequestID: "request-safe-123",
	}
	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err == nil {
		t.Fatal("want provider error")
	}
	if strings.Contains(err.Error(), "selector-secret") || strings.Contains(err.Error(), "raw_query") {
		t.Fatalf("provider message escaped adapter: %v", err)
	}
	if !strings.Contains(err.Error(), "InvalidQuery") || !strings.Contains(err.Error(), "request-safe-123") {
		t.Fatalf("safe diagnostics were lost: %v", err)
	}
	if result.APICalls != 1 || result.QueryID != "request-safe-123" {
		t.Fatalf("partial request metadata was lost: %#v", result)
	}
}

func TestBackendPreservesPartialUsageWhenThirdQueryFails(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "12"}}, "Complete", `{"isAccurate":1}`),
		response("request-patterns", []map[string]string{{"bucket_key": "timeout", "bucket_count": "12"}}, "Complete", `{"isAccurate":1}`),
	}
	reader.queryErrOnCall = 3
	reader.queryErr = &sls.Error{HTTPCode: 503, Code: "ServerBusy", Message: "secret provider body", RequestID: "request-third"}

	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err == nil || strings.Contains(err.Error(), "secret provider body") {
		t.Fatalf("want safe third-query failure, got result=%#v err=%v", result, err)
	}
	if result.APICalls != 3 || result.QueryID != "request-count,request-patterns,request-third" || result.ProcessedBytes != 2048 {
		t.Fatalf("partial third-query metadata was lost: %#v", result)
	}
}

func TestBackendPreservesMetadataWhenAggregateParsingFails(t *testing.T) {
	reader := validReader()
	reader.responses = []*sls.GetLogsResponse{
		response("request-count", []map[string]string{{"error_count": "not-a-number"}}, "Complete", `{"isAccurate":1}`),
		response("request-patterns", []map[string]string{{"bucket_key": "timeout", "bucket_count": "8"}}, "Complete", `{"isAccurate":1}`),
		response("request-instances", []map[string]string{{"bucket_key": "pod-a", "bucket_count": "8"}}, "Complete", `{"isAccurate":1}`),
		response("request-count-verification", []map[string]string{{"error_count": "not-a-number"}}, "Complete", `{"isAccurate":1}`),
	}
	result, err := testBackend(reader).Execute(context.Background(), testApprovedQuery())
	if err == nil {
		t.Fatal("want aggregate parse error")
	}
	if result.QueryID != "request-count,request-patterns,request-instances,request-count-verification" || result.APICalls != domain.ErrorAnalysisAPICalls || result.ProcessedBytes != 4096 {
		t.Fatalf("partial response metadata was lost: %#v", result)
	}
}

func TestBackendRejectsUnsafeEndpointBeforeProviderCall(t *testing.T) {
	reader := validReader()
	backend := testBackend(reader)
	query := testApprovedQuery()
	query.Resource.Endpoint = "https://example.com/path?query=1"

	if _, err := backend.Execute(context.Background(), query); err == nil {
		t.Fatal("want unsafe endpoint error")
	}
	if len(reader.requests) != 0 {
		t.Fatalf("provider called with unsafe endpoint: %d", len(reader.requests))
	}
}

func TestBackendResourceCheckRequiresInstanceStatistics(t *testing.T) {
	reader := validReader()
	reader.index.Keys["pod_name"] = sls.IndexKey{Type: "text", DocValue: false}

	_, err := testBackend(reader).CheckResources(context.Background(), []domain.LogResource{testApprovedQuery().Resource})
	if err == nil || !strings.Contains(err.Error(), "instance field") {
		t.Fatalf("want instance schema error, got %v", err)
	}
	if len(reader.requests) != 0 {
		t.Fatalf("resource check queried logs: %d", len(reader.requests))
	}
}

func validReader() *fakeReader {
	return &fakeReader{
		projectExists: true,
		logstore:      &sls.LogStore{Name: "logstore", Mode: "standard"},
		index: &sls.Index{Keys: map[string]sls.IndexKey{
			"service":     {Type: "text"},
			"environment": {Type: "text"},
			"level":       {Type: "text"},
			"error_type":  {Type: "text", DocValue: true},
			"pod_name":    {Type: "text", DocValue: true},
		}},
	}
}

func testBackend(fake reader) *Backend {
	return &Backend{newReader: func(string) reader { return fake }, now: func() time.Time {
		return time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	}}
}

func testApprovedQuery() domain.ApprovedQuery {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	return domain.ApprovedQuery{
		SpecHash: "hash", TemplateID: domain.ErrorAnalysisTemplateID, PolicyVersion: "policy-v2", SchemaFingerprint: "schema-v2",
		StartTime: start, EndTime: start.Add(30 * time.Minute), MaxRows: domain.ErrorAnalysisResultRows, MaxAPICalls: domain.ErrorAnalysisAPICalls,
		PatternLimit: domain.ErrorAnalysisPatternLimit, InstanceLimit: domain.ErrorAnalysisInstanceLimit, ExpectedAPICalls: domain.ErrorAnalysisAPICalls,
		Resource: domain.LogResource{
			ID: "order-prod", Endpoint: "https://cn-hangzhou.log.aliyuncs.com", Project: "project", LogStore: "logstore",
			TemplateVersion: domain.ErrorAnalysisTemplateVersion, ErrorField: "error_type", Selectors: []domain.LogSelector{
				{Field: "service", Value: "order-service"},
				{Field: "environment", Value: "prod"},
			},
			ErrorSelector: domain.LogSelector{Field: "level", Value: "ERROR"}, InstanceField: "pod_name",
		},
	}
}

func response(requestID string, logs []map[string]string, progress, contents string) *sls.GetLogsResponse {
	header := make(http.Header)
	header.Set(sls.RequestIDHeader, requestID)
	header.Set(sls.ProcessedRows, "100")
	header.Set(sls.ProcessedBytes, "1024")
	header.Set(sls.ElapsedMillisecond, "5")
	return &sls.GetLogsResponse{Progress: progress, Count: int64(len(logs)), Logs: logs, Contents: contents, Header: header}
}
