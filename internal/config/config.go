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
	CredentialMode    string
	AccessKeyID       string
	AccessKeySecret   string
	SecurityToken     string
	ECSRAMRoleName    string
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
			Mode:            valueOrDefault("LOG_AGENT_SLS_MODE", "mock"),
			CatalogPath:     valueOrDefault("LOG_AGENT_SLS_CATALOG", "./config/sls-resources.json"),
			CredentialMode:  valueOrDefault("LOG_AGENT_SLS_CREDENTIAL_MODE", "static"),
			AccessKeyID:     os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID"),
			AccessKeySecret: os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET"),
			SecurityToken:   os.Getenv("ALIBABA_CLOUD_SECURITY_TOKEN"),
			ECSRAMRoleName:  os.Getenv("LOG_AGENT_SLS_ECS_RAM_ROLE_NAME"),
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
	if config.SLS.CredentialMode != "static" && config.SLS.CredentialMode != "ecs_ram_role" {
		return Config{}, fmt.Errorf("LOG_AGENT_SLS_CREDENTIAL_MODE must be static or ecs_ram_role")
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
