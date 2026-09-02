package volcark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestIntentParserCallsResponsesAPIWithLogicalCapabilitiesOnly(t *testing.T) {
	var received responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing authorization")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write([]byte(`{"id":"resp_intent","model":"model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"intent\":\"error_spike\",\"service\":\"dam-server\",\"environment\":\"test\",\"duration_seconds\":1800,\"confidence\":0.96}"}]}],"usage":{"input_tokens":40,"output_tokens":20,"total_tokens":60}}`))
	}))
	defer server.Close()
	parser, err := newIntentParser("test-key", "model", server.URL, &http.Client{Timeout: time.Second}, 16*1024, 512, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := parser.Parse(context.Background(), domain.IntentProviderInput{
		Problem: "测试环境错误是否增加",
		Capabilities: []domain.InvestigationCapability{{
			Service: "dam-server", Environment: "test", Intent: domain.IntentErrorSpike, TemplateID: domain.ErrorCountTemplateID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Store || received.Thinking.Type != "disabled" || received.Text.Format.Name != "governed_investigation_intent" || !received.Text.Format.Strict {
		t.Fatalf("unsafe intent request: %#v", received)
	}
	parts := strings.SplitN(received.Input, "受治理输入 JSON：", 2)
	if len(parts) != 2 {
		t.Fatalf("governed input marker missing: %s", received.Input)
	}
	for _, forbidden := range []string{"tech-center-sha", "2016-hyper", "logstore", "project", "test-key"} {
		if strings.Contains(strings.ToLower(parts[1]), strings.ToLower(forbidden)) {
			t.Fatalf("intent input leaked %q", forbidden)
		}
	}
	if result.Draft.Intent != domain.IntentErrorSpike || result.Draft.DurationSeconds != 1800 || result.TotalTokens != 60 || result.RequestID != "resp_intent" {
		t.Fatalf("unexpected intent result: %#v", result)
	}
}

func TestIntentParserRejectsUnknownFieldsAndProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var received responsesRequest
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(received.Input, "provider-reject-case") {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte("secret provider diagnostic"))
			return
		}
		_, _ = writer.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"intent\":\"unknown\",\"service\":null,\"environment\":null,\"duration_seconds\":null,\"confidence\":1,\"spl\":\"*\"}"}]}]}`))
	}))
	defer server.Close()
	parser, err := newIntentParser("secret-key", "model", server.URL, &http.Client{Timeout: time.Second}, 16*1024, 512, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(context.Background(), domain.IntentProviderInput{Problem: "x"}); err == nil {
		t.Fatal("unknown provider field was accepted")
	}
	_, err = parser.Parse(context.Background(), domain.IntentProviderInput{Problem: "provider-reject-case"})
	if err == nil || strings.Contains(err.Error(), "secret provider diagnostic") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}
