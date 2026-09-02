package aliyuncli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestSearchTraceUsesFixedExactPhraseAndConfiguredProjection(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"data":[{"__time__":"1788330000","__time_ns_part__":"7","level":"error","msg":"Bearer abcdefghijklmnop failed"}],"meta":{"progress":"Complete","processedRows":1,"processedBytes":200,"elapsedMillisecond":4,"isAccurate":true}}`),
	}}
	backend := &Backend{runner: runner, requestTimeout: time.Second, now: time.Now, newExecutionID: func() (string, error) { return "trace_exec_1", nil }}
	query := approvedTraceTestQuery()
	result, err := backend.SearchTrace(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionID != "trace_exec_1" || result.APICalls != 1 || len(result.Events) != 1 || result.Events[0].Message == "" {
		t.Fatalf("unexpected Trace result: %#v", result)
	}
	args := strings.Join(runner.args[0], " ")
	if !strings.Contains(args, `--query #"trace-12345678" and env: "test"`) || !strings.Contains(args, "--line 50") {
		t.Fatalf("fixed Trace expression missing: %s", args)
	}
	for _, forbidden := range []string{"SELECT", "project-from-user", "logstore-from-user"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("unsafe Trace query argument %q: %s", forbidden, args)
		}
	}
}

func TestSearchTraceRetriesOnlyExplicitIncompleteOnce(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"data":[],"meta":{"progress":"Incomplete","processedRows":10,"processedBytes":100,"elapsedMillisecond":3,"isAccurate":true}}`),
		[]byte(`{"data":[],"meta":{"progress":"Complete","processedRows":20,"processedBytes":200,"elapsedMillisecond":4,"isAccurate":true}}`),
	}}
	backend := &Backend{runner: runner, requestTimeout: time.Second, now: time.Now, newExecutionID: func() (string, error) { return "trace_exec_2", nil }}
	query := approvedTraceTestQuery()
	query.RetryIncomplete = 1
	result, err := backend.SearchTrace(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.APICalls != 2 || result.Progress != "Complete" || result.ProcessedBytes != 300 || len(runner.args) != 2 {
		t.Fatalf("unexpected retry accounting: %#v calls=%d", result, len(runner.args))
	}
}

func TestMapTraceRowRejectsNonScalarConfiguredField(t *testing.T) {
	member := approvedTraceTestQuery().Member
	_, err := mapTraceRow(map[string]json.RawMessage{"msg": json.RawMessage(`{"nested":true}`)}, member)
	if err == nil {
		t.Fatal("non-scalar Trace field was accepted")
	}
}

func TestGetTraceIndexAcceptsFullTextOnlyIndex(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{"line":{"token":[" ","#"]},"index_mode":"v2"}`)}}
	backend := &Backend{runner: runner, requestTimeout: time.Second, now: time.Now}
	schema, err := backend.getTraceIndex(context.Background(), domain.LogResource{
		ID: "dam-video", Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha", LogStore: "2904-hyper-dam-consume-video-file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !schema.FullText || schema.Fingerprint == "" || len(schema.Fields) != 0 {
		t.Fatalf("unexpected full-text-only schema: %#v", schema)
	}
}

func approvedTraceTestQuery() domain.ApprovedTraceQuery {
	return domain.ApprovedTraceQuery{
		Spec: domain.TraceSearchSpec{
			InvestigationID: "inv-trace", Service: "dam-server", Environment: "test", TraceID: "trace-12345678",
			StartTime: time.Unix(1788329400, 0).UTC(), EndTime: time.Unix(1788330000, 0).UTC(),
			Requester: domain.Principal{AppID: "app", TenantKey: "tenant", UserID: "user"},
		},
		GroupID: "dam-trace-test", Member: domain.TraceResourceMember{
			ID: "dam-server", Endpoint: "https://cn-shanghai.log.aliyuncs.com", Project: "tech-center-sha", LogStore: "2016-hyper-dam-file",
			TraceMode: domain.TraceQueryFullText, EnvironmentMode: domain.TraceEnvironmentField, EnvironmentField: "env",
			MessageField: "msg", LevelField: "level", EventTimeField: "__time__", NanosecondTimeField: "__time_ns_part__",
		},
		GovernanceFingerprint: strings.Repeat("a", 64), TraceIDFingerprint: strings.Repeat("b", 64), MemberLimit: 50,
	}
}
