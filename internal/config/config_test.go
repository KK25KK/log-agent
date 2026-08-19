package config

import (
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestLoadDefaultsToOfflineMock(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_MODE", "")
	t.Setenv("LOG_AGENT_SLS_CREDENTIAL_MODE", "")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.SLS.Mode != "mock" || config.SLS.MaxAPICalls != 4 || config.SLS.MaxRows != 12 || config.SLS.MaxConcurrent <= 0 {
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

func TestLoadRejectsQueryTimeoutBelowSDKRequestTimeout(t *testing.T) {
	t.Setenv("LOG_AGENT_SLS_REQUEST_TIMEOUT", "15s")
	t.Setenv("LOG_AGENT_SLS_QUERY_TIMEOUT", "5s")
	if _, err := Load(); err == nil {
		t.Fatal("want invalid query timeout error")
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
