package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"logagent/internal/adapters/localweb"
	"logagent/internal/adapters/sqlite"
	"logagent/internal/application"
	"logagent/internal/config"
	"logagent/internal/domain"
)

func TestLocalWebTraversesMockAgentAndDeliveryChain(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_MODE", "mock")
	t.Setenv("LOG_AGENT_LLM_MODE", "mock")
	t.Setenv("ARK_API_KEY", "")
	t.Setenv("LOG_AGENT_ARK_MODEL", "")
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	worker, err := buildInvestigationWorker(ctx, loaded, store)
	if err != nil {
		t.Fatal(err)
	}
	intake := application.NewIntake(store)
	actions, err := application.NewActionService(store, intake, loaded.SLS.MaxWindow)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := localweb.NewSender(loaded.Web.AppID)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := application.NewDeliveryWorker(store, sender, "web-e2e-delivery", time.Minute, time.Second, 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	address := "127.0.0.1:8080"
	adapter, err := localweb.NewServer(localweb.Options{
		Address: address,
		Principal: domain.Principal{
			AppID: loaded.Web.AppID, TenantKey: loaded.Web.TenantKey, UserID: loaded.Web.UserID,
		},
		ChatID: loaded.Web.ChatID, IngestionGrace: loaded.SLS.IngestionGrace, MaxWindow: loaded.SLS.MaxWindow,
		SLSMode: loaded.SLS.Mode, LLMMode: loaded.LLM.Mode,
	}, store, intake, actions, sender)
	if err != nil {
		t.Fatal(err)
	}
	csrf := webTestCSRF(t, adapter.Handler(), address)
	response := webTestPost(t, adapter.Handler(), address, "/api/investigations", csrf, map[string]string{
		"request_id": "web-e2e-request-1234", "service": "order-service", "environment": "prod",
		"duration": "30m", "template_id": domain.ErrorAnalysisTemplateID,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		Investigation localweb.InvestigationView `json:"investigation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if ran, err := delivery.RunOne(ctx); err != nil || !ran {
		t.Fatalf("queued delivery: ran=%v err=%v", ran, err)
	}
	if ran, err := worker.RunOne(ctx); err != nil || !ran {
		t.Fatalf("investigation worker: ran=%v err=%v", ran, err)
	}
	for index := 0; index < 2; index++ {
		if ran, err := delivery.RunOne(ctx); err != nil || !ran {
			t.Fatalf("post-worker delivery %d: ran=%v err=%v", index, ran, err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "http://"+address+"/api/investigations/"+accepted.Investigation.ID, nil)
	request.Host = address
	response = httptest.NewRecorder()
	adapter.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
	var view localweb.InvestigationView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Status != domain.StatusSucceeded || view.Report == nil || len(view.Report.Evidence) != 2 {
		t.Fatalf("unexpected completed view: %#v", view)
	}
	if view.Report.Summary == nil || view.Report.Summary.Mode != domain.SummaryModeMock {
		t.Fatalf("mock summary did not traverse Worker: %#v", view.Report.Summary)
	}
	if !view.Delivery.CardReady || view.Delivery.Kind != domain.DeliverySucceeded {
		t.Fatalf("terminal delivery projection missing: %#v", view.Delivery)
	}
}

func TestWebCommandRejectsArgumentsBeforeStarting(t *testing.T) {
	if err := run([]string{"web", "unexpected"}); err == nil {
		t.Fatal("web command accepted unexpected arguments")
	}
}

func webTestCSRF(t *testing.T, handler http.Handler, address string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://"+address+"/", nil)
	request.Host = address
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	match := regexp.MustCompile(`name="log-agent-csrf" content="([a-f0-9]+)"`).FindStringSubmatch(response.Body.String())
	if len(match) != 2 {
		t.Fatal("CSRF token missing")
	}
	return match[1]
}

func webTestPost(t *testing.T, handler http.Handler, address, path, csrf string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://"+address+path, bytes.NewReader(body))
	request.Host = address
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Log-Agent-CSRF", csrf)
	request.Header.Set("Origin", "http://"+address)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
