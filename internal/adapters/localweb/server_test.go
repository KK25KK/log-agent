package localweb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"logagent/internal/adapters/localweb"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/domain"
)

const testAddress = "127.0.0.1:8080"

var csrfPattern = regexp.MustCompile(`name="log-agent-csrf" content="([a-f0-9]+)"`)

func TestValidateLoopbackAddress(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		if err := localweb.ValidateLoopbackAddress(address); err != nil {
			t.Fatalf("safe address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "192.168.1.2:8080", "localhost:8080", "127.0.0.1:0", "127.0.0.1"} {
		if err := localweb.ValidateLoopbackAddress(address); err == nil {
			t.Fatalf("unsafe address %q accepted", address)
		}
	}
}

func TestServerUsesFixedIdentityAndDurableActionBinding(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	intake := application.NewIntake(store)
	actions, err := application.NewActionService(store, intake, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := localweb.NewSender("local-web")
	if err != nil {
		t.Fatal(err)
	}
	server, err := localweb.NewServer(localweb.Options{
		Address:   testAddress,
		Principal: domain.Principal{AppID: "local-web", TenantKey: "local-pilot", UserID: "operator"},
		ChatID:    "local-console", IngestionGrace: domain.DefaultIngestionGrace, MaxWindow: 2 * time.Hour,
		SLSMode: "mock", LLMMode: "mock",
	}, store, intake, actions, sender)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := application.NewDeliveryWorker(store, sender, "delivery-web", time.Minute, time.Second, 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	csrf := readCSRF(t, server.Handler())
	css := get(t, server.Handler(), "/app.css")
	if css.Code != http.StatusOK || !strings.Contains(css.Body.String(), "[hidden]{display:none!important}") {
		t.Fatalf("hidden-state CSS guard missing: status=%d", css.Code)
	}
	favicon := get(t, server.Handler(), "/favicon.ico")
	if favicon.Code != http.StatusNoContent {
		t.Fatalf("favicon status=%d", favicon.Code)
	}
	payload := map[string]string{
		"request_id": "12345678-test-request", "service": "order-service", "environment": "prod",
		"duration": "30m", "template_id": domain.ErrorAnalysisTemplateID,
	}
	first := postJSON(t, server.Handler(), "/api/investigations", csrf, payload)
	if first.Code != http.StatusCreated {
		t.Fatalf("first submit status=%d body=%s", first.Code, first.Body.String())
	}
	var created struct {
		Created       bool                       `json:"created"`
		Investigation localweb.InvestigationView `json:"investigation"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Investigation.ID == "" {
		t.Fatalf("unexpected create response: %#v", created)
	}
	duplicate := postJSON(t, server.Handler(), "/api/investigations", csrf, payload)
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"created":false`) {
		t.Fatalf("duplicate was not idempotent: status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	persisted, err := store.GetInvestigation(context.Background(), created.Investigation.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrincipal := domain.Principal{AppID: "local-web", TenantKey: "local-pilot", UserID: "operator"}
	if persisted.Request.Requester != wantPrincipal {
		t.Fatalf("requester was not derived from fixed identity: %#v", persisted.Request.Requester)
	}
	if ran, err := delivery.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("deliver queued projection: ran=%v err=%v", ran, err)
	}

	view := get(t, server.Handler(), "/api/investigations/"+created.Investigation.ID)
	if view.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", view.Code, view.Body.String())
	}
	for _, forbidden := range []string{`"requester"`, `"last_error"`, `"project"`, `"logstore"`, `"query_id"`, `"query_spec_hash"`} {
		if strings.Contains(strings.ToLower(view.Body.String()), forbidden) {
			t.Fatalf("public projection leaked %s: %s", forbidden, view.Body.String())
		}
	}
	action := postJSON(t, server.Handler(), "/api/investigations/"+created.Investigation.ID+"/actions", csrf, map[string]string{
		"request_id": "12345678-cancel-action", "action": string(domain.ActionCancel),
	})
	if action.Code != http.StatusOK || !strings.Contains(action.Body.String(), `"status":"CANCELLED"`) {
		t.Fatalf("cancel action failed: status=%d body=%s", action.Code, action.Body.String())
	}
}

func TestServerRejectsCSRFIdentitySpoofAndHostRebinding(t *testing.T) {
	store, _ := sqlite.Open(":memory:")
	defer store.Close()
	intake := application.NewIntake(store)
	actions, _ := application.NewActionService(store, intake, time.Hour)
	sender, _ := localweb.NewSender("local-web")
	server, err := localweb.NewServer(localweb.Options{
		Address:   testAddress,
		Principal: domain.Principal{AppID: "local-web", TenantKey: "local-pilot", UserID: "operator"},
		ChatID:    "local-console", IngestionGrace: domain.DefaultIngestionGrace, MaxWindow: time.Hour,
		SLSMode: "mock", LLMMode: "mock",
	}, store, intake, actions, sender)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"request_id":"12345678-request","service":"order-service","environment":"prod","duration":"30m","principal":{"user_id":"attacker"}}`)
	request := httptest.NewRequest(http.MethodPost, "http://"+testAddress+"/api/investigations", bytes.NewReader(body))
	request.Host = testAddress
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", response.Code, response.Body.String())
	}

	csrf := readCSRF(t, server.Handler())
	response = postRaw(server.Handler(), testAddress, "/api/investigations", csrf, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("identity field was accepted: status=%d body=%s", response.Code, response.Body.String())
	}
	tooWide := []byte(`{"request_id":"12345678-wide-window","service":"order-service","environment":"prod","duration":"2h"}`)
	response = postRaw(server.Handler(), testAddress, "/api/investigations", csrf, tooWide)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "window_exceeds_policy") {
		t.Fatalf("over-policy window was accepted: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://attacker.invalid/api/meta", nil)
	request.Host = "attacker.invalid"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("host rebinding was accepted: status=%d", response.Code)
	}
}

func readCSRF(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := get(t, handler, "/")
	match := csrfPattern.FindStringSubmatch(response.Body.String())
	if len(match) != 2 {
		t.Fatalf("CSRF token not found in page")
	}
	return match[1]
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://"+testAddress+path, nil)
	request.Host = testAddress
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func postJSON(t *testing.T, handler http.Handler, path, csrf string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(handler, testAddress, path, csrf, body)
}

func postRaw(handler http.Handler, host, path, csrf string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://"+host+path, bytes.NewReader(body))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Log-Agent-CSRF", csrf)
	request.Header.Set("Origin", "http://"+host)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
