package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"logagent/internal/domain"
)

type Config struct {
	FeishuAppID       string
	FeishuAppSecret   string
	DatabasePath      string
	WorkerID          string
	WorkerPoll        time.Duration
	WorkerLease       time.Duration
	ChangeCatalogPath string
	Delivery          DeliveryConfig
	SLS               SLSConfig
	Quota             QuotaConfig
	LLM               LLMConfig
	LLMQuota          LLMQuotaConfig
	SmokePrincipal    SmokePrincipal
}

type DeliveryConfig struct {
	WorkerID    string
	Poll        time.Duration
	Lease       time.Duration
	SendTimeout time.Duration
	MaxAttempts int
	RetryBase   time.Duration
}

type SLSConfig struct {
	Mode              string
	CatalogPath       string
	CLIPath           string
	CLIProfile        string
	CLIMaxOutputBytes int64
	RequestTimeout    time.Duration
	QueryTimeout      time.Duration
	MaxWindow         time.Duration
	IngestionGrace    time.Duration
	MaxRows           int64
	MaxAPICalls       int
	MaxProcessedBytes int64
	MaxConcurrent     int
	SchemaTTL         time.Duration
}

type QuotaConfig struct {
	Window                      time.Duration
	MaxObservations             int64
	MaxAPICalls                 int64
	MaxProcessedBytes           int64
	ReservedBytesPerObservation int64
}

type LLMConfig struct {
	Mode    string
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

type LLMQuotaConfig struct {
	Window                   time.Duration
	MaxRequests              int64
	MaxTokens                int64
	ReservedTokensPerRequest int64
}

type SmokePrincipal struct {
	AppID     string
	TenantKey string
	UserID    string
}

func Load() (Config, error) {
	config := Config{
		FeishuAppID:       os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:   os.Getenv("FEISHU_APP_SECRET"),
		DatabasePath:      valueOrDefault("LOG_AGENT_DB_PATH", "./data/logagent.db"),
		WorkerID:          valueOrDefault("LOG_AGENT_WORKER_ID", "worker-local"),
		ChangeCatalogPath: os.Getenv("LOG_AGENT_CHANGE_CATALOG"),
		Delivery: DeliveryConfig{
			WorkerID: valueOrDefault("LOG_AGENT_DELIVERY_WORKER_ID", "feishu-delivery-local"),
		},
		SLS: SLSConfig{
			Mode:        valueOrDefault("LOG_AGENT_SLS_MODE", "mock"),
			CatalogPath: valueOrDefault("LOG_AGENT_SLS_CATALOG", "./config/sls-resources.json"),
			CLIPath:     os.Getenv("LOG_AGENT_SLS_CLI_PATH"),
			CLIProfile:  valueOrDefault("LOG_AGENT_SLS_CLI_PROFILE", "default"),
		},
		LLM: LLMConfig{
			Mode:    valueOrDefault("LOG_AGENT_LLM_MODE", "mock"),
			APIKey:  os.Getenv("ARK_API_KEY"),
			Model:   os.Getenv("LOG_AGENT_ARK_MODEL"),
			BaseURL: valueOrDefault("LOG_AGENT_ARK_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3"),
		},
		SmokePrincipal: SmokePrincipal{
			AppID:     os.Getenv("LOG_AGENT_SMOKE_APP_ID"),
			TenantKey: os.Getenv("LOG_AGENT_SMOKE_TENANT_KEY"),
			UserID:    os.Getenv("LOG_AGENT_SMOKE_USER_ID"),
		},
	}
	if config.SLS.Mode != "mock" && config.SLS.Mode != "aliyun" {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_MODE must be mock or aliyun")
	}
	if config.LLM.Mode != "disabled" && config.LLM.Mode != "mock" && config.LLM.Mode != "volcengine" {
		return Config{}, fmt.Errorf("LOG_AGENT_LLM_MODE must be disabled, mock, or volcengine")
	}
	var err error
	config.WorkerPoll, err = durationOrDefault("LOG_AGENT_POLL_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}
	config.WorkerLease, err = durationOrDefault("LOG_AGENT_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	if config.WorkerLease < 10*time.Second {
		return Config{}, fmt.Errorf("LOG_AGENT_LEASE_DURATION must be at least 10s")
	}
	config.Delivery.Poll, err = durationOrDefault("LOG_AGENT_DELIVERY_POLL_INTERVAL", 500*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	config.Delivery.Lease, err = durationOrDefault("LOG_AGENT_DELIVERY_LEASE_DURATION", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.Delivery.SendTimeout, err = durationOrDefault("LOG_AGENT_DELIVERY_SEND_TIMEOUT", 8*time.Second)
	if err != nil {
		return Config{}, err
	}
	if config.Delivery.Lease <= config.Delivery.SendTimeout {
		return Config{}, fmt.Errorf("LOG_AGENT_DELIVERY_LEASE_DURATION must exceed LOG_AGENT_DELIVERY_SEND_TIMEOUT")
	}
	config.Delivery.MaxAttempts, err = intOrDefault("LOG_AGENT_DELIVERY_MAX_ATTEMPTS", 5)
	if err != nil {
		return Config{}, err
	}
	config.Delivery.RetryBase, err = durationOrDefault("LOG_AGENT_DELIVERY_RETRY_BASE", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.SLS.RequestTimeout, err = durationOrDefault("LOG_AGENT_SLS_REQUEST_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.SLS.QueryTimeout, err = durationOrDefault("LOG_AGENT_SLS_QUERY_TIMEOUT", 45*time.Second)
	if err != nil {
		return Config{}, err
	}
	if config.SLS.QueryTimeout < config.SLS.RequestTimeout {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_QUERY_TIMEOUT must be at least LOG_AGENT_SLS_REQUEST_TIMEOUT")
	}
	config.SLS.CLIMaxOutputBytes, err = int64OrDefault("LOG_AGENT_SLS_CLI_MAX_OUTPUT_BYTES", 4*1024*1024)
	if err != nil {
		return Config{}, err
	}
	if config.SLS.CLIMaxOutputBytes < 64*1024 || config.SLS.CLIMaxOutputBytes > 16*1024*1024 {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_CLI_MAX_OUTPUT_BYTES must be between 65536 and 16777216")
	}
	config.SLS.MaxWindow, err = durationOrDefault("LOG_AGENT_SLS_MAX_WINDOW", 2*time.Hour)
	if err != nil {
		return Config{}, err
	}
	config.SLS.IngestionGrace, err = durationOrDefault("LOG_AGENT_SLS_INGESTION_GRACE", domain.DefaultIngestionGrace)
	if err != nil {
		return Config{}, err
	}
	if config.SLS.IngestionGrace < domain.MinimumIngestionGrace {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_INGESTION_GRACE must be at least %s", domain.MinimumIngestionGrace)
	}
	config.SLS.SchemaTTL, err = durationOrDefault("LOG_AGENT_SLS_SCHEMA_TTL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	config.SLS.MaxRows, err = int64OrDefault("LOG_AGENT_SLS_MAX_ROWS", int64(domain.ErrorAnalysisResultRows))
	if err != nil {
		return Config{}, err
	}
	config.SLS.MaxProcessedBytes, err = int64OrDefault("LOG_AGENT_SLS_MAX_PROCESSED_BYTES", 256*1024*1024)
	if err != nil {
		return Config{}, err
	}
	config.SLS.MaxAPICalls, err = intOrDefault("LOG_AGENT_SLS_MAX_API_CALLS", domain.ErrorAnalysisAPICalls)
	if err != nil {
		return Config{}, err
	}
	config.SLS.MaxConcurrent, err = intOrDefault("LOG_AGENT_SLS_MAX_CONCURRENT", 2)
	if err != nil {
		return Config{}, err
	}
	config.Quota.Window, err = durationOrDefault("LOG_AGENT_TENANT_QUOTA_WINDOW", time.Hour)
	if err != nil {
		return Config{}, err
	}
	if config.Quota.Window < time.Minute || config.Quota.Window > 24*time.Hour {
		return Config{}, fmt.Errorf("LOG_AGENT_TENANT_QUOTA_WINDOW must be between 1m and 24h")
	}
	config.Quota.MaxObservations, err = int64OrDefault("LOG_AGENT_TENANT_QUOTA_MAX_OBSERVATIONS", 100)
	if err != nil {
		return Config{}, err
	}
	config.Quota.MaxAPICalls, err = int64OrDefault("LOG_AGENT_TENANT_QUOTA_MAX_API_CALLS", 400)
	if err != nil {
		return Config{}, err
	}
	config.Quota.MaxProcessedBytes, err = int64OrDefault("LOG_AGENT_TENANT_QUOTA_MAX_PROCESSED_BYTES", 8*1024*1024*1024)
	if err != nil {
		return Config{}, err
	}
	config.Quota.ReservedBytesPerObservation, err = int64OrDefault("LOG_AGENT_TENANT_QUOTA_RESERVED_BYTES", config.SLS.MaxProcessedBytes)
	if err != nil {
		return Config{}, err
	}
	if config.Quota.ReservedBytesPerObservation > config.Quota.MaxProcessedBytes {
		return Config{}, fmt.Errorf("LOG_AGENT_TENANT_QUOTA_RESERVED_BYTES cannot exceed LOG_AGENT_TENANT_QUOTA_MAX_PROCESSED_BYTES")
	}
	config.LLM.Timeout, err = durationOrDefault("LOG_AGENT_LLM_TIMEOUT", 12*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.LLMQuota.Window, err = durationOrDefault("LOG_AGENT_LLM_QUOTA_WINDOW", time.Hour)
	if err != nil {
		return Config{}, err
	}
	if config.LLMQuota.Window < time.Minute || config.LLMQuota.Window > 24*time.Hour {
		return Config{}, fmt.Errorf("LOG_AGENT_LLM_QUOTA_WINDOW must be between 1m and 24h")
	}
	config.LLMQuota.MaxRequests, err = int64OrDefault("LOG_AGENT_LLM_QUOTA_MAX_REQUESTS", 100)
	if err != nil {
		return Config{}, err
	}
	config.LLMQuota.MaxTokens, err = int64OrDefault("LOG_AGENT_LLM_QUOTA_MAX_TOKENS", 409600)
	if err != nil {
		return Config{}, err
	}
	config.LLMQuota.ReservedTokensPerRequest, err = int64OrDefault("LOG_AGENT_LLM_QUOTA_RESERVED_TOKENS", 4096)
	if err != nil {
		return Config{}, err
	}
	if config.LLMQuota.ReservedTokensPerRequest > config.LLMQuota.MaxTokens {
		return Config{}, fmt.Errorf("LOG_AGENT_LLM_QUOTA_RESERVED_TOKENS cannot exceed LOG_AGENT_LLM_QUOTA_MAX_TOKENS")
	}
	if config.LLM.Mode == "volcengine" && (config.LLM.APIKey == "" || config.LLM.Model == "") {
		return Config{}, fmt.Errorf("ARK_API_KEY and LOG_AGENT_ARK_MODEL are required when LOG_AGENT_LLM_MODE=volcengine")
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationOrDefault(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func intOrDefault(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func int64OrDefault(name string, fallback int64) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}
