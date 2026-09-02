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
	t.Setenv("LOG_AGENT_INTENT_MODE", "")
	t.Setenv("LOG_AGENT_INTENT_MODEL", "")
	t.Setenv("LOG_AGENT_CODE_MODE", "")
	t.Setenv("LOG_AGENT_CODE_CATALOG", "")
	t.Setenv("LOG_AGENT_GIT_PATH", "")
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
	if config.Intent.Mode != "disabled" || config.Intent.MaxInputRunes != 500 || config.Intent.MaxOutputBytes != 16*1024 ||
		config.Intent.MinConfidence != 0.80 || config.Intent.MaxTokens != 512 || config.Intent.ResolutionTTL != 15*time.Minute {
		t.Fatalf("unexpected intent defaults: %#v", config.Intent)
	}
	if config.IntentQuota.Window != time.Hour || config.IntentQuota.MaxRequests != 100 || config.IntentQuota.MaxTokens != 51200 || config.IntentQuota.ReservedTokensPerRequest != 512 {
		t.Fatalf("unexpected intent quota defaults: %#v", config.IntentQuota)
	}
	if config.Trace.Mode != "disabled" || config.Trace.MaxWindow != 30*time.Minute || config.Trace.MemberLimit != 50 ||
		config.Trace.GlobalLimit != 500 || config.Trace.MaxConcurrency != 2 || config.Trace.RetryIncomplete != 1 {
		t.Fatalf("unexpected Trace defaults: %#v", config.Trace)
	}
	if config.Code.Mode != "disabled" || config.Code.Timeout != 8*time.Second || config.Code.MaxCommandOutput != 512*1024 || config.Code.GitPath != "git" {
		t.Fatalf("unexpected code evidence defaults: %#v", config.Code)
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

func TestLoadAcceptsDisabledTraceRetryAndRejectsUnsafeTraceBudgets(t *testing.T) {
	t.Setenv("LOG_AGENT_TRACE_RETRY_INCOMPLETE", "0")
	loaded, err := Load()
	if err != nil || loaded.Trace.RetryIncomplete != 0 {
		t.Fatalf("zero Trace retry should be valid: %#v err=%v", loaded.Trace, err)
	}
	t.Setenv("LOG_AGENT_TRACE_MAX_CONCURRENT", "3")
	if _, err := Load(); err == nil {
		t.Fatal("want Trace concurrency budget error")
	}
}

func TestLoadRejectsUnsafeCodeEvidenceConfiguration(t *testing.T) {
	t.Setenv("LOG_AGENT_CODE_MODE", "shell")
	if _, err := Load(); err == nil {
		t.Fatal("want unsupported code mode error")
	}
	t.Setenv("LOG_AGENT_CODE_MODE", "localgit")
	t.Setenv("LOG_AGENT_GIT_MAX_OUTPUT_BYTES", "1024")
	if _, err := Load(); err == nil {
		t.Fatal("want unsafe Git output limit error")
	}
}

func TestLoadRequiresIntentCredentialsOnlyWhenEnabled(t *testing.T) {
	t.Setenv("LOG_AGENT_INTENT_MODE", "volcengine")
	t.Setenv("ARK_API_KEY", "")
	t.Setenv("LOG_AGENT_INTENT_MODEL", "")
	t.Setenv("LOG_AGENT_ARK_MODEL", "")
	if _, err := Load(); err == nil {
		t.Fatal("want missing intent credentials error")
	}
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("LOG_AGENT_INTENT_MODEL", "intent-model")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Intent.Mode != "volcengine" || loaded.Intent.Model != "intent-model" {
		t.Fatalf("unexpected intent config: %#v", loaded.Intent)
	}
}

func TestLoadRejectsUnsafeIntentConfiguration(t *testing.T) {
	t.Setenv("LOG_AGENT_INTENT_MIN_CONFIDENCE", "1.1")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid intent confidence error")
	}
	t.Setenv("LOG_AGENT_INTENT_MIN_CONFIDENCE", "0.8")
	t.Setenv("LOG_AGENT_INTENT_QUOTA_MAX_TOKENS", "100")
	t.Setenv("LOG_AGENT_INTENT_QUOTA_RESERVED_TOKENS", "200")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid intent quota reservation error")
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
