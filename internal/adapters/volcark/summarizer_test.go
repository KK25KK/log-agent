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

func TestSummarizerCallsResponsesAPIWithGovernedInput(t *testing.T) {
	var received responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v3/responses" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing bearer authorization")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_123","model":"endpoint-model","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"phenomenon\":\"错误突增\",\"phenomenon_evidence_ids\":[\"ev-current\"],\"evidence_notes\":[{\"statement\":\"当前窗口错误数增加\",\"evidence_ids\":[\"ev-current\"]}],\"recommendation_codes\":[\"inspect\"]}"}]}],"usage":{"input_tokens":80,"output_tokens":20,"total_tokens":100}}`))
	}))
	defer server.Close()

	summarizer, err := newSummarizer("test-key", "endpoint-model", server.URL+"/api/v3/responses", &http.Client{Timeout: time.Second}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := summarizer.Summarize(context.Background(), domain.SummaryInput{
		Outcome:         "spike_detected",
		Evidence:        []domain.SummaryInputEvidence{{ID: "ev-current", Name: "current", ErrorCount: 120}},
		Recommendations: []domain.SummaryInputRecommendation{{Code: "inspect", Statement: "检查", EvidenceIDs: []string{"ev-current"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Store || received.Model != "endpoint-model" || received.MaxOutputTokens != maxOutputTokens {
		t.Fatalf("unsafe request options: %#v", received)
	}
	if received.Thinking.Type != "disabled" || received.Text.Format.Type != "json_schema" ||
		received.Text.Format.Name != "governed_log_summary" || !received.Text.Format.Strict {
		t.Fatalf("structured output contract is missing: %#v", received)
	}
	properties, ok := received.Text.Format.Schema["properties"].(map[string]any)
	if !ok || len(properties) != 5 || properties["evidence_notes"] == nil {
		t.Fatalf("unexpected structured output schema: %#v", received.Text.Format.Schema)
	}
	for _, forbidden := range []string{"project/logstore", "query-secret", "raw log", "test-key"} {
		if strings.Contains(received.Input, forbidden) {
			t.Fatalf("request leaked %q", forbidden)
		}
	}
	if result.Provider != "volcengine_ark" || result.Mode != domain.SummaryModeModel || result.TotalTokens != 100 || result.RequestID != "resp_123" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSummarizerRejectsUnknownDraftFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"phenomenon\":\"x\",\"unexpected\":true}"}]}]}`))
	}))
	defer server.Close()
	summarizer, err := newSummarizer("test-key", "model", server.URL, &http.Client{Timeout: time.Second}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summarizer.Summarize(context.Background(), domain.SummaryInput{}); err == nil {
		t.Fatal("want strict response error")
	}
}

func TestSummarizerDoesNotLeakProviderBodyOrAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte("secret provider diagnostic"))
	}))
	defer server.Close()
	summarizer, err := newSummarizer("super-secret-key", "model", server.URL, &http.Client{Timeout: time.Second}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = summarizer.Summarize(context.Background(), domain.SummaryInput{})
	if err == nil || strings.Contains(err.Error(), "secret provider diagnostic") || strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestNewRestrictsCredentialDestination(t *testing.T) {
	for _, baseURL := range []string{
		"https://example.com/api/v3",
		"https://ark.cn-beijing.volces.com/other",
		"https://ark.cn-beijing.volces.com:443/api/v3",
	} {
		if _, err := New(Config{APIKey: "test-key", Model: "model", BaseURL: baseURL, Timeout: time.Second}); err == nil {
			t.Fatalf("want unsafe destination %q to be rejected", baseURL)
		}
	}
	if _, err := New(Config{APIKey: " test-key ", Model: "model", BaseURL: DefaultBaseURL, Timeout: time.Second}); err == nil {
		t.Fatal("want padded API key to be rejected")
	}
	if _, err := New(Config{APIKey: "test-key", Model: "doubao-seed-2-0-mini-260428", BaseURL: DefaultBaseURL + "/", Timeout: time.Second}); err != nil {
		t.Fatalf("want approved endpoint to be accepted: %v", err)
	}
}
