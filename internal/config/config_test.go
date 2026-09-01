package config

import (
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestLoadDefaultsToOfflineMock(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_MODE", "")
	t.Setenv("LOG_AGENT_SLS_CLI_PROFILE", "")
	t.Setenv("LOG_AGENT_LLM_MODE", "")
	t.Setenv("ARK_API_KEY", "")
	t.Setenv("LOG_AGENT_ARK_MODEL", "")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.SLS.Mode != "mock" || config.SLS.CLIProfile != "default" || config.SLS.MaxAPICalls != 4 || config.SLS.MaxRows != 12 || config.SLS.MaxConcurrent <= 0 {
		t.Fatalf("unexpected defaults: %#v", config.SLS)
	}
	if config.SLS.QueryTimeout != 45*time.Second || config.Delivery.MaxAttempts != 5 || config.Delivery.Lease <= config.Delivery.SendTimeout {
		t.Fatalf("unexpected M2 defaults: sls=%#v delivery=%#v", config.SLS, config.Delivery)
	}
	if config.SLS.IngestionGrace != domain.DefaultIngestionGrace {
		t.Fatalf("unexpected ingestion grace: %s", config.SLS.IngestionGrace)
	}
	if config.ChangeCatalogPath != "" {
		t.Fatalf("change catalog must be disabled by default: %q", config.ChangeCatalogPath)
	}
	if config.LLM.Mode != "mock" || config.LLM.Timeout != 12*time.Second || config.LLM.APIKey != "" {
		t.Fatalf("unexpected offline LLM defaults: %#v", config.LLM)
	}
	if config.LLMQuota.Window != time.Hour || config.LLMQuota.MaxRequests != 100 || config.LLMQuota.MaxTokens != 409600 || config.LLMQuota.ReservedTokensPerRequest != 4096 {
		t.Fatalf("unexpected LLM quota defaults: %#v", config.LLMQuota)
	}
	if config.Quota.Window != time.Hour || config.Quota.MaxObservations != 100 || config.Quota.MaxAPICalls != 400 ||
		config.Quota.ReservedBytesPerObservation != config.SLS.MaxProcessedBytes || config.Quota.MaxProcessedBytes <= config.Quota.ReservedBytesPerObservation {
		t.Fatalf("unexpected tenant quota defaults: %#v", config.Quota)
	}
	if config.Web.Address != "127.0.0.1:8080" || config.Web.DatabasePath != "./data/web-pilot.db" ||
		config.Web.AppID != "local-web" || config.Web.TenantKey != "local-pilot" || config.Web.UserID != "operator" {
		t.Fatalf("unexpected local Web defaults: %#v", config.Web)
	}
}

func TestLoadRequiresVolcengineCredentialsOnlyWhenEnabled(t *testing.T) {
	t.Setenv("LOG_AGENT_LLM_MODE", "volcengine")
	t.Setenv("ARK_API_KEY", "")
	t.Setenv("LOG_AGENT_ARK_MODEL", "")
	if _, err := Load(); err == nil {
		t.Fatal("want missing Volcengine credentials error")
	}
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("LOG_AGENT_ARK_MODEL", "doubao-endpoint")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.Mode != "volcengine" || loaded.LLM.Model != "doubao-endpoint" {
		t.Fatalf("unexpected Volcengine config: %#v", loaded.LLM)
	}
}

func TestLoadRejectsQuotaReservationAboveWindowBudget(t *testing.T) {
	t.Setenv("LOG_AGENT_TENANT_QUOTA_MAX_PROCESSED_BYTES", "1024")
	t.Setenv("LOG_AGENT_TENANT_QUOTA_RESERVED_BYTES", "2048")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid tenant quota reservation error")
	}
}

func TestLoadRejectsLLMQuotaReservationAboveWindowBudget(t *testing.T) {
	t.Setenv("LOG_AGENT_LLM_QUOTA_MAX_TOKENS", "1024")
	t.Setenv("LOG_AGENT_LLM_QUOTA_RESERVED_TOKENS", "2048")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid LLM quota reservation error")
	}
}

func TestLoadRejectsUnsafeSLSConfiguration(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_MODE", "aliyun")
	t.Setenv("LOG_AGENT_SLS_MAX_PROCESSED_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid processed-byte budget error")
	}
}

func TestLoadRejectsUnsafeIngestionGrace(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_INGESTION_GRACE", "1s")
	if _, err := Load(); err == nil {
		t.Fatal("want unsafe ingestion grace error")
	}
}

func TestLoadRejectsQueryTimeoutBelowCLIRequestTimeout(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_REQUEST_TIMEOUT", "15s")
	t.Setenv("LOG_AGENT_SLS_QUERY_TIMEOUT", "5s")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid query timeout error")
	}
}

func TestLoadRejectsUnsafeCLIOutputLimit(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_CLI_MAX_OUTPUT_BYTES", "1024")
	if _, err := Load(); err == nil {
		t.Fatal("want unsafe CLI output limit error")
	}
}

func TestLoadReadsOptionalChangeCatalogPath(t *testing.T) {
	t.Setenv("LOG_AGENT_CHANGE_CATALOG", `.\config\change-catalog.json`)

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ChangeCatalogPath != `.\config\change-catalog.json` {
		t.Fatalf("change catalog path=%q", loaded.ChangeCatalogPath)
	}
}
