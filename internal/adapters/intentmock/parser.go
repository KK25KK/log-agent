package intentmock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

const (
	Provider = "intent_mock"
	Model    = "deterministic_v1"
	prompt   = "classify only error_spike or unknown from an allowlisted logical capability"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func PromptFingerprint() string {
	digest := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(digest[:])
}

func (p *Parser) Parse(ctx context.Context, input domain.IntentProviderInput) (domain.IntentProviderResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.IntentProviderResult{}, err
	}
	started := time.Now()
	draft := domain.IntentDraft{Intent: domain.IntentUnknown, Confidence: 1}
	problem := strings.ToLower(input.Problem)
	if !mentionsTrace(problem) && mentionsErrorTrend(problem) {
		draft = domain.IntentDraft{Intent: domain.IntentErrorSpike, DurationSeconds: parseDuration(problem), Confidence: 0.95}
		if capability, ok := selectCapability(problem, input.Capabilities); ok {
			draft.Service = capability.Service
			draft.Environment = capability.Environment
		}
	}
	return domain.IntentProviderResult{
		Draft: draft, Provider: Provider, Model: Model,
		PromptVersion: domain.IntentPromptVersion, PromptFingerprint: PromptFingerprint(),
		LatencyMillis: time.Since(started).Milliseconds(),
	}, nil
}

func mentionsTrace(problem string) bool {
	return strings.Contains(problem, "trace") || strings.Contains(problem, "链路") || strings.Contains(problem, "调用链")
}

func mentionsErrorTrend(problem string) bool {
	errorWord := strings.Contains(problem, "错误") || strings.Contains(problem, "异常") || strings.Contains(problem, "error")
	trendWord := strings.Contains(problem, "增加") || strings.Contains(problem, "上升") || strings.Contains(problem, "突增") ||
		strings.Contains(problem, "变多") || strings.Contains(problem, "有没有") || strings.Contains(problem, "是否")
	return errorWord && trendWord
}

func selectCapability(problem string, capabilities []domain.InvestigationCapability) (domain.InvestigationCapability, bool) {
	for _, capability := range capabilities {
		if capability.Intent != domain.IntentErrorSpike || capability.TemplateID != domain.ErrorCountTemplateID {
			continue
		}
		if serviceMentioned(problem, capability.Service) && environmentMentioned(problem, capability.Environment) {
			return capability, true
		}
	}
	return domain.InvestigationCapability{}, false
}

func serviceMentioned(problem, service string) bool {
	if strings.Contains(problem, strings.ToLower(service)) {
		return true
	}
	return service == "dam-server" && strings.Contains(problem, "dam")
}

func environmentMentioned(problem, environment string) bool {
	if strings.Contains(problem, strings.ToLower(environment)) {
		return true
	}
	switch environment {
	case "test":
		return strings.Contains(problem, "测试")
	case "prod", "production":
		return strings.Contains(problem, "生产") || strings.Contains(problem, "线上")
	case "staging", "pre":
		return strings.Contains(problem, "预发")
	default:
		return false
	}
}

func parseDuration(problem string) int64 {
	switch {
	case strings.Contains(problem, "半小时") || strings.Contains(problem, "30分钟") || strings.Contains(problem, "30 分钟"):
		return int64((30 * time.Minute) / time.Second)
	case strings.Contains(problem, "10分钟") || strings.Contains(problem, "十分钟"):
		return int64((10 * time.Minute) / time.Second)
	case strings.Contains(problem, "一小时") || strings.Contains(problem, "1小时"):
		return int64(time.Hour / time.Second)
	case strings.Contains(problem, "七天") || strings.Contains(problem, "7天"):
		return int64((7 * 24 * time.Hour) / time.Second)
	default:
		return 0
	}
}

var _ ports.InvestigationIntentParser = (*Parser)(nil)
