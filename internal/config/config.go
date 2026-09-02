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
	Intent            IntentConfig
	IntentQuota       IntentQuotaConfig
	Trace             TraceConfig
	SmokePrincipal    SmokePrincipal
	Web               WebConfig
}

// WebConfig owns the fixed, server-side identity and loopback surface used by
// the local pilot console. It is intentionally separate from Feishu settings.
type WebConfig struct {
	Address      string
	DatabasePath string
	AppID        string
	TenantKey    string
	UserID       string
	ChatID       string
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

type IntentConfig struct {
	Mode           string
	APIKey         string
	Model          string
	BaseURL        string
	Timeout        time.Duration
	MaxInputRunes  int
	MaxOutputBytes int64
	MinConfidence  float64
	MaxTokens      int
	ResolutionTTL  time.Duration
}

type IntentQuotaConfig struct {
	Window                   time.Duration
	MaxRequests              int64
	MaxTokens                int64
	ReservedTokensPerRequest int64
}

type TraceConfig struct {
	Mode              string
	CatalogPath       string
	MaxWindow         time.Duration
	IngestionGrace    time.Duration
	QueryTimeout      time.Duration
	MemberLimit       int
	GlobalLimit       int
	MaxProcessedBytes int64
	MaxConcurrency    int
	RetryIncomplete   int
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
		Intent: IntentConfig{
			Mode:    valueOrDefault("LOG_AGENT_INTENT_MODE", "disabled"),
			APIKey:  os.Getenv("ARK_API_KEY"),
			Model:   valueOrDefault("LOG_AGENT_INTENT_MODEL", os.Getenv("LOG_AGENT_ARK_MODEL")),
			BaseURL: valueOrDefault("LOG_AGENT_INTENT_BASE_URL", valueOrDefault("LOG_AGENT_ARK_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3")),
		},
		Trace: TraceConfig{
			Mode:        valueOrDefault("LOG_AGENT_TRACE_MODE", "disabled"),
			CatalogPath: valueOrDefault("LOG_AGENT_TRACE_CATALOG", "./config/trace-resources.json"),
		},
		SmokePrincipal: SmokePrincipal{
			AppID:     os.Getenv("LOG_AGENT_SMOKE_APP_ID"),
			TenantKey: os.Getenv("LOG_AGENT_SMOKE_TENANT_KEY"),
			UserID:    os.Getenv("LOG_AGENT_SMOKE_USER_ID"),
		},
		Web: WebConfig{
			Address:      valueOrDefault("LOG_AGENT_WEB_ADDR", "127.0.0.1:8080"),
			DatabasePath: valueOrDefault("LOG_AGENT_WEB_DB_PATH", "./data/web-pilot.db"),
			AppID:        valueOrDefault("LOG_AGENT_WEB_APP_ID", "local-web"),
			TenantKey:    valueOrDefault("LOG_AGENT_WEB_TENANT_KEY", "local-pilot"),
			UserID:       valueOrDefault("LOG_AGENT_WEB_USER_ID", "operator"),
			ChatID:       valueOrDefault("LOG_AGENT_WEB_CHAT_ID", "local-console"),
		},
	}
	if config.SLS.Mode != "mock" && config.SLS.Mode != "aliyun" {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_MODE must be mock or aliyun")
	}
	if config.LLM.Mode != "disabled" && config.LLM.Mode != "mock" && config.LLM.Mode != "volcengine" {
		return Config{}, fmt.Errorf("LOG_AGENT_LLM_MODE must be disabled, mock, or volcengine")
	}
	if config.Intent.Mode != "disabled" && config.Intent.Mode != "mock" && config.Intent.Mode != "volcengine" {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_MODE must be disabled, mock, or volcengine")
	}
	if config.Trace.Mode != "disabled" && config.Trace.Mode != "mock" && config.Trace.Mode != "aliyun" {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_MODE must be disabled, mock, or aliyun")
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
	config.Trace.MaxWindow, err = durationOrDefault("LOG_AGENT_TRACE_MAX_WINDOW", 30*time.Minute)
	if err != nil || config.Trace.MaxWindow <= 0 || config.Trace.MaxWindow > 30*time.Minute {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_MAX_WINDOW must be between 1ns and 30m")
	}
	config.Trace.IngestionGrace, err = durationOrDefault("LOG_AGENT_TRACE_INGESTION_GRACE", domain.DefaultIngestionGrace)
	if err != nil || config.Trace.IngestionGrace < domain.MinimumIngestionGrace {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_INGESTION_GRACE must be at least %s", domain.MinimumIngestionGrace)
	}
	config.Trace.QueryTimeout, err = durationOrDefault("LOG_AGENT_TRACE_QUERY_TIMEOUT", 20*time.Second)
	if err != nil || config.Trace.QueryTimeout <= 0 {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_QUERY_TIMEOUT must be positive")
	}
	config.Trace.MemberLimit, err = intOrDefault("LOG_AGENT_TRACE_MEMBER_LIMIT", domain.TraceDefaultMemberLimit)
	if err != nil || config.Trace.MemberLimit <= 0 || config.Trace.MemberLimit > domain.TraceDefaultMemberLimit {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_MEMBER_LIMIT must be between 1 and %d", domain.TraceDefaultMemberLimit)
	}
	config.Trace.GlobalLimit, err = intOrDefault("LOG_AGENT_TRACE_GLOBAL_LIMIT", domain.TraceDefaultGlobalLimit)
	if err != nil || config.Trace.GlobalLimit <= 0 || config.Trace.GlobalLimit > domain.TraceDefaultGlobalLimit {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_GLOBAL_LIMIT must be between 1 and %d", domain.TraceDefaultGlobalLimit)
	}
	config.Trace.MaxProcessedBytes, err = int64OrDefault("LOG_AGENT_TRACE_MAX_PROCESSED_BYTES", 256*1024*1024)
	if err != nil || config.Trace.MaxProcessedBytes <= 0 {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_MAX_PROCESSED_BYTES must be positive")
	}
	config.Trace.MaxConcurrency, err = intOrDefault("LOG_AGENT_TRACE_MAX_CONCURRENT", domain.TraceDefaultConcurrency)
	if err != nil || config.Trace.MaxConcurrency <= 0 || config.Trace.MaxConcurrency > domain.TraceDefaultConcurrency {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_MAX_CONCURRENT must be between 1 and %d", domain.TraceDefaultConcurrency)
	}
	config.Trace.RetryIncomplete, err = nonNegativeIntOrDefault("LOG_AGENT_TRACE_RETRY_INCOMPLETE", 1)
	if err != nil || config.Trace.RetryIncomplete < 0 || config.Trace.RetryIncomplete > 1 {
		return Config{}, fmt.Errorf("LOG_AGENT_TRACE_RETRY_INCOMPLETE must be 0 or 1")
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
	config.Intent.Timeout, err = durationOrDefault("LOG_AGENT_INTENT_TIMEOUT", 8*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.Intent.MaxInputRunes, err = intOrDefault("LOG_AGENT_INTENT_MAX_INPUT_CHARS", 500)
	if err != nil {
		return Config{}, err
	}
	if config.Intent.MaxInputRunes < 32 || config.Intent.MaxInputRunes > 2000 {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_MAX_INPUT_CHARS must be between 32 and 2000")
	}
	config.Intent.MaxOutputBytes, err = int64OrDefault("LOG_AGENT_INTENT_MAX_OUTPUT_BYTES", 16*1024)
	if err != nil {
		return Config{}, err
	}
	if config.Intent.MaxOutputBytes < 1024 || config.Intent.MaxOutputBytes > 128*1024 {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_MAX_OUTPUT_BYTES must be between 1024 and 131072")
	}
	config.Intent.MinConfidence, err = float64OrDefault("LOG_AGENT_INTENT_MIN_CONFIDENCE", 0.80)
	if err != nil {
		return Config{}, err
	}
	if config.Intent.MinConfidence <= 0 || config.Intent.MinConfidence > 1 {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_MIN_CONFIDENCE must be in (0, 1]")
	}
	config.Intent.MaxTokens, err = intOrDefault("LOG_AGENT_INTENT_MAX_TOKENS", 512)
	if err != nil {
		return Config{}, err
	}
	if config.Intent.MaxTokens > 2048 {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_MAX_TOKENS must not exceed 2048")
	}
	config.Intent.ResolutionTTL, err = durationOrDefault("LOG_AGENT_INTENT_RESOLUTION_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	if config.Intent.ResolutionTTL < time.Minute || config.Intent.ResolutionTTL > time.Hour {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_RESOLUTION_TTL must be between 1m and 1h")
	}
	if config.Intent.Mode == "volcengine" && (config.Intent.APIKey == "" || config.Intent.Model == "") {
		return Config{}, fmt.Errorf("ARK_API_KEY and LOG_AGENT_INTENT_MODEL or LOG_AGENT_ARK_MODEL are required when LOG_AGENT_INTENT_MODE=volcengine")
	}
	config.IntentQuota.Window, err = durationOrDefault("LOG_AGENT_INTENT_QUOTA_WINDOW", time.Hour)
	if err != nil {
		return Config{}, err
	}
	if config.IntentQuota.Window < time.Minute || config.IntentQuota.Window > 24*time.Hour {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_QUOTA_WINDOW must be between 1m and 24h")
	}
	config.IntentQuota.MaxRequests, err = int64OrDefault("LOG_AGENT_INTENT_QUOTA_MAX_REQUESTS", 100)
	if err != nil {
		return Config{}, err
	}
	config.IntentQuota.MaxTokens, err = int64OrDefault("LOG_AGENT_INTENT_QUOTA_MAX_TOKENS", 51200)
	if err != nil {
		return Config{}, err
	}
	config.IntentQuota.ReservedTokensPerRequest, err = int64OrDefault("LOG_AGENT_INTENT_QUOTA_RESERVED_TOKENS", 512)
	if err != nil {
		return Config{}, err
	}
	if config.IntentQuota.ReservedTokensPerRequest > config.IntentQuota.MaxTokens {
		return Config{}, fmt.Errorf("LOG_AGENT_INTENT_QUOTA_RESERVED_TOKENS cannot exceed LOG_AGENT_INTENT_QUOTA_MAX_TOKENS")
	}
	if config.Web.DatabasePath == "" || config.Web.AppID == "" || config.Web.TenantKey == "" || config.Web.UserID == "" || config.Web.ChatID == "" {
		return Config{}, fmt.Errorf("local Web database path and fixed identity must be set")
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

func nonNegativeIntOrDefault(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
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

func float64OrDefault(name string, fallback float64) (float64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	return parsed, nil
}
