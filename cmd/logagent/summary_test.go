package main

import (
	"testing"
	"time"

	"logagent/internal/adapters/sqlite"
	"logagent/internal/config"
)

func TestBuildSummaryServiceModes(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC) }
	for _, test := range []struct {
		name    string
		mode    string
		wantNil bool
	}{
		{name: "disabled", mode: "disabled", wantNil: true},
		{name: "mock", mode: "mock", wantNil: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := sqlite.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service, err := buildSummaryService(config.Config{
				LLM:      config.LLMConfig{Mode: test.mode, Timeout: time.Second},
				LLMQuota: config.LLMQuotaConfig{Window: time.Hour, MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: 4096},
			}, store, now)
			if err != nil {
				t.Fatal(err)
			}
			if (service == nil) != test.wantNil {
				t.Fatalf("service nil=%v want %v", service == nil, test.wantNil)
			}
		})
	}
}

func TestBuildSummaryServiceRejectsUnsafeVolcengineEndpoint(t *testing.T) {
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = buildSummaryService(config.Config{LLM: config.LLMConfig{
		Mode: "volcengine", APIKey: "test-key", Model: "endpoint", BaseURL: "http://insecure.example", Timeout: time.Second,
	}, LLMQuota: config.LLMQuotaConfig{Window: time.Hour, MaxRequests: 10, MaxTokens: 40960, ReservedTokensPerRequest: 4096}}, store, time.Now)
	if err == nil {
		t.Fatal("want insecure Volcengine endpoint error")
	}
}
