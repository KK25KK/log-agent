package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ids"
	"logagent/internal/ports"
)

const IntentQuotaPolicyVersion = "intent-quota-v1"

var (
	intentSecretPattern    = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9._~+/=-]{8,}|LTAI[A-Za-z0-9]{12,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}|\b(?:\d{1,3}\.){3}\d{1,3}\b)`)
	intentInjectionPattern = regexp.MustCompile(`(?i)(忽略.{0,12}(规则|指令|限制)|ignore.{0,12}(instruction|rule)|执行.{0,8}(SPL|SQL|shell|命令)|execute.{0,8}(SPL|SQL|shell)|\bcurl\b|\bkubectl\b|\brm\s+-)`)
	intentTraceIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,256}$`)
)

type IntentPolicy struct {
	MaxInputRunes  int
	MinConfidence  float64
	MaxWindow      time.Duration
	IngestionGrace time.Duration
	ResolutionTTL  time.Duration
	Provider       string
	Model          string
	PromptHash     string
}

type IntentResolutionService struct {
	store        ports.IntentResolutionStore
	parser       ports.InvestigationIntentParser
	capabilities ports.IntentCapabilitySource
	intake       *Intake
	policy       IntentPolicy
	quotaStore   ports.IntentQuotaStore
	quotaPolicy  domain.IntentQuotaPolicy
	now          func() time.Time
}

type IntentServiceOption func(*IntentResolutionService) error

func WithIntentClock(now func() time.Time) IntentServiceOption {
	return func(service *IntentResolutionService) error {
		if now == nil {
			return errors.New("intent clock is required")
		}
		service.now = now
		return nil
	}
}

func WithIntentQuota(store ports.IntentQuotaStore, policy domain.IntentQuotaPolicy) IntentServiceOption {
	return func(service *IntentResolutionService) error {
		if store == nil || validateIntentQuotaPolicy(policy) != nil {
			return errors.New("intent quota store and policy are required")
		}
		service.quotaStore = store
		service.quotaPolicy = policy
		return nil
	}
}

func NewIntentResolutionService(
	store ports.IntentResolutionStore,
	parser ports.InvestigationIntentParser,
	capabilities ports.IntentCapabilitySource,
	intake *Intake,
	policy IntentPolicy,
	options ...IntentServiceOption,
) (*IntentResolutionService, error) {
	if store == nil || parser == nil || capabilities == nil || intake == nil {
		return nil, errors.New("intent store, parser, capability source, and intake are required")
	}
	if policy.MaxInputRunes <= 0 || policy.MinConfidence <= 0 || policy.MinConfidence > 1 || policy.MaxWindow <= 0 ||
		policy.IngestionGrace < domain.MinimumIngestionGrace || policy.ResolutionTTL <= 0 ||
		!safeIntentCode(policy.Provider, 1, 64) || !safeIntentCode(policy.Model, 1, 160) || !validIntentHash(policy.PromptHash) {
		return nil, errors.New("intent policy is invalid")
	}
	service := &IntentResolutionService{
		store: store, parser: parser, capabilities: capabilities, intake: intake,
		policy: policy, now: time.Now,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("intent service option is nil")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *IntentResolutionService) Resolve(ctx context.Context, inbound domain.InboundMessage, rawProblem string) (domain.IntentResolution, bool, error) {
	if err := validateIntentInbound(inbound); err != nil {
		return domain.IntentResolution{}, false, err
	}
	problem, rejected, err := normalizeProblemStatement(rawProblem, s.policy.MaxInputRunes)
	if err != nil {
		return domain.IntentResolution{}, false, err
	}
	resolutionID, err := ids.New("intent")
	if err != nil {
		return domain.IntentResolution{}, false, err
	}
	now := s.now().UTC()
	initial := domain.IntentResolution{
		ID: resolutionID, Principal: principalFromInbound(inbound), SourceMessageID: inbound.MessageID,
		Problem: problem, Status: domain.IntentResolutionParsing,
		Provider: s.policy.Provider, Model: s.policy.Model,
		PromptVersion: domain.IntentPromptVersion, PromptFingerprint: s.policy.PromptHash,
		CreatedAt: now, ExpiresAt: now.Add(s.policy.ResolutionTTL),
	}
	stored, created, err := s.store.BeginIntentResolution(ctx, initial)
	if err != nil || !created {
		return stored, created, err
	}
	if rejected {
		stored.Status = domain.IntentResolutionRejected
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "prompt_injection_rejected"
		return s.complete(ctx, stored, created)
	}

	capabilities, err := s.capabilities.ListAllowedCapabilities(ctx, stored.Principal)
	if err != nil {
		stored.Status = domain.IntentResolutionFallback
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "capability_lookup_failed"
		return s.complete(ctx, stored, created)
	}
	if len(capabilities) == 0 {
		stored.Status = domain.IntentResolutionRejected
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "no_allowed_capability"
		return s.complete(ctx, stored, created)
	}
	reservation, quotaOK := s.reserveIntentQuota(ctx, stored)
	if !quotaOK {
		stored.Status = domain.IntentResolutionFallback
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "intent_quota_unavailable"
		return s.complete(ctx, stored, created)
	}

	result, err := s.parser.Parse(ctx, domain.IntentProviderInput{Problem: stored.Problem.Text, Capabilities: capabilities})
	if err != nil {
		s.settleIntentQuota(reservation, domain.QuotaUnknown, 0, 0, reservation.ReservedTokens, "intent_external_outcome_unknown")
		stored.Status = domain.IntentResolutionOutcomeUnknown
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "intent_provider_outcome_unknown"
		return s.complete(context.WithoutCancel(ctx), stored, created)
	}
	if !validIntentTokenUsage(result) {
		s.settleIntentQuota(reservation, domain.QuotaUnknown, 0, 0, reservation.ReservedTokens, "intent_token_usage_invalid")
		stored.Status = domain.IntentResolutionFallback
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "intent_token_usage_invalid"
		return s.complete(ctx, stored, created)
	}
	if err := s.settleIntentQuota(reservation, domain.QuotaSettled, result.InputTokens, result.OutputTokens, result.TotalTokens, "intent_succeeded"); err != nil {
		stored.Status = domain.IntentResolutionFallback
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "intent_quota_settlement_failed"
		return s.complete(ctx, stored, created)
	}
	if s.quotaStore != nil && result.TotalTokens > reservation.ReservedTokens {
		stored.Status = domain.IntentResolutionFallback
		stored.Intent = domain.IntentUnknown
		stored.ReasonCode = "intent_token_reservation_exceeded"
		return s.complete(ctx, stored, created)
	}
	applyIntentProviderResult(&stored, result)
	s.resolveDraft(&stored, capabilities)
	return s.complete(ctx, stored, created)
}

type intentQuotaIdentity struct {
	TenantID     string `json:"tenant_id"`
	ResolutionID string `json:"resolution_id"`
	Prompt       string `json:"prompt_version"`
}

func (s *IntentResolutionService) reserveIntentQuota(ctx context.Context, resolution domain.IntentResolution) (domain.IntentQuotaReservation, bool) {
	if s.quotaStore == nil {
		return domain.IntentQuotaReservation{}, true
	}
	now := s.now().UTC()
	tenantID := domain.TrustedTenantID(resolution.Principal)
	usageKey, err := fingerprint.JSON(intentQuotaIdentity{
		TenantID: tenantID, ResolutionID: resolution.ID, Prompt: domain.IntentPromptVersion,
	})
	if err != nil {
		return domain.IntentQuotaReservation{}, false
	}
	windowStart := now.Truncate(s.quotaPolicy.Window)
	reservation := domain.IntentQuotaReservation{
		UsageKey: usageKey, TenantID: tenantID, ResolutionID: resolution.ID,
		PromptVersion: domain.IntentPromptVersion, WindowStart: windowStart, WindowEnd: windowStart.Add(s.quotaPolicy.Window),
		ReservedTokens: s.quotaPolicy.ReservedTokensPerRequest, Status: domain.QuotaReserved,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.quotaStore.ReserveIntentQuota(ctx, reservation, s.quotaPolicy); err != nil {
		return domain.IntentQuotaReservation{}, false
	}
	return reservation, true
}

func (s *IntentResolutionService) settleIntentQuota(
	reservation domain.IntentQuotaReservation,
	status domain.QuotaReservationStatus,
	inputTokens, outputTokens, totalTokens int64,
	reasonCode string,
) error {
	if s.quotaStore == nil {
		return nil
	}
	settleCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.quotaStore.SettleIntentQuota(
		settleCtx, reservation.UsageKey, status, inputTokens, outputTokens, totalTokens, reasonCode, s.now().UTC(),
	)
}

func validateIntentQuotaPolicy(policy domain.IntentQuotaPolicy) error {
	if policy.Version != IntentQuotaPolicyVersion || policy.Window < time.Minute || policy.Window > 24*time.Hour ||
		policy.MaxRequests <= 0 || policy.MaxTokens <= 0 || policy.ReservedTokensPerRequest <= 0 ||
		policy.ReservedTokensPerRequest > policy.MaxTokens {
		return errors.New("intent quota policy is invalid")
	}
	return nil
}

func validIntentTokenUsage(result domain.IntentProviderResult) bool {
	const maxInt64 = int64(^uint64(0) >> 1)
	return result.InputTokens >= 0 && result.OutputTokens >= 0 && result.TotalTokens >= 0 &&
		result.InputTokens <= maxInt64-result.OutputTokens && result.TotalTokens >= result.InputTokens+result.OutputTokens
}

func (s *IntentResolutionService) Confirm(ctx context.Context, resolutionID string, principal domain.Principal, chatID string) (string, bool, error) {
	if !principal.Complete() || chatID == "" {
		return "", false, ports.ErrIntentForbidden
	}
	resolution, err := s.store.GetIntentResolution(ctx, resolutionID)
	if err != nil {
		return "", false, err
	}
	if resolution.Principal != principal {
		return "", false, ports.ErrIntentForbidden
	}
	if resolution.InvestigationID != "" {
		return resolution.InvestigationID, false, nil
	}
	now := s.now().UTC()
	if !now.Before(resolution.ExpiresAt) {
		return "", false, ports.ErrIntentExpired
	}
	if resolution.Status != domain.IntentResolutionResolved {
		return "", false, ports.ErrIntentInvalid
	}
	switch resolution.Intent {
	case domain.IntentErrorSpike:
		if resolution.TemplateID != domain.ErrorCountTemplateID || resolution.TraceID != "" {
			return "", false, ports.ErrIntentInvalid
		}
	case domain.IntentTraceSearch:
		if resolution.TemplateID != domain.TraceSearchTemplateID || !intentTraceIDPattern.MatchString(resolution.TraceID) ||
			resolution.TraceIDFingerprint != traceIDFingerprint(resolution.TraceID) || resolution.TraceIDHint != traceIDHint(resolution.TraceID) {
			return "", false, ports.ErrIntentInvalid
		}
	default:
		return "", false, ports.ErrIntentInvalid
	}
	capabilities, err := s.capabilities.ListAllowedCapabilities(ctx, principal)
	if err != nil || !containsIntentCapability(capabilities, resolution) {
		return "", false, ports.ErrIntentForbidden
	}
	if resolution.DurationSeconds <= 0 || resolution.DurationSeconds > int64(s.policy.MaxWindow/time.Second) {
		return "", false, ports.ErrIntentInvalid
	}
	window := time.Duration(resolution.DurationSeconds) * time.Second
	end := now.Add(-s.policy.IngestionGrace)
	request := domain.InvestigationRequest{
		Service: resolution.Service, Environment: resolution.Environment,
		TemplateID: resolution.TemplateID, StartTime: end.Add(-window), EndTime: end,
		Problem: &resolution.Problem, IntentResolutionID: resolution.ID, TraceID: resolution.TraceID,
	}
	inbound := domain.InboundMessage{
		AppID: principal.AppID, TenantKey: principal.TenantKey, UserID: principal.UserID,
		MessageID: resolution.SourceMessageID, ReplyToMessageID: resolution.SourceMessageID,
		ChatID: chatID, Text: resolution.Problem.Text, ReceivedAt: now,
	}
	investigationID, created, err := s.intake.Accept(ctx, inbound, request)
	if err != nil {
		return "", false, err
	}
	confirmedID, marked, err := s.store.ConfirmIntentResolution(ctx, resolution.ID, principal, investigationID, now)
	if err != nil {
		return "", false, err
	}
	if confirmedID != investigationID {
		return confirmedID, false, nil
	}
	return investigationID, created && marked, nil
}

func (s *IntentResolutionService) Get(ctx context.Context, resolutionID string, principal domain.Principal) (domain.IntentResolution, error) {
	resolution, err := s.store.GetIntentResolution(ctx, resolutionID)
	if err != nil {
		return domain.IntentResolution{}, err
	}
	if !principal.Complete() || resolution.Principal != principal {
		return domain.IntentResolution{}, ports.ErrIntentForbidden
	}
	return resolution, nil
}

func (s *IntentResolutionService) Capabilities(ctx context.Context, principal domain.Principal) ([]domain.InvestigationCapability, error) {
	if !principal.Complete() {
		return nil, ports.ErrIntentForbidden
	}
	return s.capabilities.ListAllowedCapabilities(ctx, principal)
}

func (s *IntentResolutionService) complete(ctx context.Context, resolution domain.IntentResolution, created bool) (domain.IntentResolution, bool, error) {
	if err := s.store.CompleteIntentResolution(ctx, resolution); err != nil {
		return domain.IntentResolution{}, false, err
	}
	return resolution, created, nil
}

func (s *IntentResolutionService) resolveDraft(resolution *domain.IntentResolution, capabilities []domain.InvestigationCapability) {
	if !validProviderMetadata(*resolution) {
		resolution.Status = domain.IntentResolutionFallback
		resolution.Intent = domain.IntentUnknown
		clearIntentScope(resolution)
		resolution.ReasonCode = "intent_provider_metadata_invalid"
		return
	}
	switch resolution.Intent {
	case domain.IntentUnknown:
		clearIntentScope(resolution)
		resolution.Status = domain.IntentResolutionUnknown
		resolution.ReasonCode = "unsupported_intent"
	case domain.IntentTraceSearch:
		if (resolution.Service != "" && !safeIntentCode(resolution.Service, 1, 64)) ||
			(resolution.Environment != "" && !safeIntentCode(resolution.Environment, 1, 64)) {
			clearIntentScope(resolution)
			resolution.Intent = domain.IntentUnknown
			resolution.Status = domain.IntentResolutionFallback
			resolution.ReasonCode = "intent_fields_unsafe"
			return
		}
		if resolution.Service == "" || resolution.Environment == "" || resolution.DurationSeconds <= 0 || resolution.TraceID == "" {
			resolution.Status = domain.IntentResolutionIncomplete
			resolution.ReasonCode = "intent_fields_incomplete"
			return
		}
		if !intentTraceIDPattern.MatchString(resolution.TraceID) {
			clearIntentScope(resolution)
			resolution.Status = domain.IntentResolutionRejected
			resolution.ReasonCode = "trace_id_rejected"
			return
		}
		if resolution.DurationSeconds > int64((30*time.Minute)/time.Second) {
			resolution.Status = domain.IntentResolutionRejected
			resolution.ReasonCode = "trace_window_rejected"
			return
		}
		if resolution.Confidence < s.policy.MinConfidence {
			resolution.Status = domain.IntentResolutionIncomplete
			resolution.ReasonCode = "intent_confidence_below_policy"
			return
		}
		capability, ok := matchingIntentCapability(capabilities, resolution.Intent, resolution.Service, resolution.Environment)
		if !ok || capability.TemplateID != domain.TraceSearchTemplateID {
			resolution.Status = domain.IntentResolutionRejected
			resolution.ReasonCode = "intent_scope_forbidden"
			return
		}
		resolution.TemplateID = capability.TemplateID
		resolution.TraceIDFingerprint = traceIDFingerprint(resolution.TraceID)
		resolution.TraceIDHint = traceIDHint(resolution.TraceID)
		resolution.Status = domain.IntentResolutionResolved
		resolution.ReasonCode = ""
	case domain.IntentErrorSpike:
		if (resolution.Service != "" && !safeIntentCode(resolution.Service, 1, 64)) ||
			(resolution.Environment != "" && !safeIntentCode(resolution.Environment, 1, 64)) {
			clearIntentScope(resolution)
			resolution.Intent = domain.IntentUnknown
			resolution.Status = domain.IntentResolutionFallback
			resolution.ReasonCode = "intent_fields_unsafe"
			return
		}
		if resolution.DurationSeconds > int64(s.policy.MaxWindow/time.Second) {
			resolution.Status = domain.IntentResolutionRejected
			resolution.ReasonCode = "intent_window_rejected"
			return
		}
		if resolution.Service == "" || resolution.Environment == "" || resolution.DurationSeconds <= 0 {
			resolution.Status = domain.IntentResolutionIncomplete
			resolution.ReasonCode = "intent_fields_incomplete"
			return
		}
		if resolution.Confidence < s.policy.MinConfidence {
			resolution.Status = domain.IntentResolutionIncomplete
			resolution.ReasonCode = "intent_confidence_below_policy"
			return
		}
		capability, ok := matchingIntentCapability(capabilities, resolution.Intent, resolution.Service, resolution.Environment)
		if !ok || capability.TemplateID != domain.ErrorCountTemplateID {
			resolution.Status = domain.IntentResolutionRejected
			resolution.ReasonCode = "intent_scope_forbidden"
			return
		}
		resolution.TemplateID = capability.TemplateID
		resolution.Status = domain.IntentResolutionResolved
		resolution.ReasonCode = ""
	default:
		resolution.Status = domain.IntentResolutionRejected
		resolution.Intent = domain.IntentUnknown
		clearIntentScope(resolution)
		resolution.ReasonCode = "intent_kind_rejected"
	}
}

func normalizeProblemStatement(raw string, maxRunes int) (domain.ProblemStatement, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return domain.ProblemStatement{}, false, ports.ErrIntentInvalid
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return domain.ProblemStatement{}, false, ports.ErrIntentInvalid
		}
	}
	rejected := intentInjectionPattern.MatchString(value)
	redacted := intentSecretPattern.ReplaceAllString(value, "[REDACTED]")
	digest := sha256.Sum256([]byte(redacted))
	return domain.ProblemStatement{
		Text: redacted, Fingerprint: hex.EncodeToString(digest[:]), Redacted: redacted != value,
	}, rejected, nil
}

func validateIntentInbound(inbound domain.InboundMessage) error {
	if inbound.AppID == "" || inbound.TenantKey == "" || inbound.UserID == "" || inbound.MessageID == "" || inbound.ChatID == "" {
		return ports.ErrIntentInvalid
	}
	return nil
}

func principalFromInbound(inbound domain.InboundMessage) domain.Principal {
	return domain.Principal{AppID: inbound.AppID, TenantKey: inbound.TenantKey, UserID: inbound.UserID}
}

func applyIntentProviderResult(resolution *domain.IntentResolution, result domain.IntentProviderResult) {
	resolution.Intent = result.Draft.Intent
	resolution.Service = result.Draft.Service
	resolution.Environment = result.Draft.Environment
	resolution.DurationSeconds = result.Draft.DurationSeconds
	resolution.TraceID = result.Draft.TraceID
	resolution.Confidence = result.Draft.Confidence
	resolution.Provider = result.Provider
	resolution.Model = result.Model
	resolution.RequestID = result.RequestID
	resolution.PromptVersion = result.PromptVersion
	resolution.PromptFingerprint = result.PromptFingerprint
	resolution.InputTokens = result.InputTokens
	resolution.OutputTokens = result.OutputTokens
	resolution.TotalTokens = result.TotalTokens
	resolution.LatencyMillis = result.LatencyMillis
}

func validProviderMetadata(resolution domain.IntentResolution) bool {
	return safeIntentCode(resolution.Provider, 1, 64) && safeIntentCode(resolution.Model, 1, 160) &&
		resolution.PromptVersion == domain.IntentPromptVersion && validIntentHash(resolution.PromptFingerprint) &&
		safeIntentCode(resolution.RequestID, 0, 256) &&
		resolution.InputTokens >= 0 && resolution.OutputTokens >= 0 && resolution.TotalTokens >= resolution.InputTokens+resolution.OutputTokens &&
		resolution.LatencyMillis >= 0 && resolution.Confidence >= 0 && resolution.Confidence <= 1
}

func clearIntentScope(resolution *domain.IntentResolution) {
	resolution.Service = ""
	resolution.Environment = ""
	resolution.DurationSeconds = 0
	resolution.TemplateID = ""
	resolution.TraceID = ""
	resolution.TraceIDFingerprint = ""
	resolution.TraceIDHint = ""
}

func traceIDFingerprint(traceID string) string {
	digest := sha256.Sum256([]byte(traceID))
	return hex.EncodeToString(digest[:])
}

func traceIDHint(traceID string) string {
	if len(traceID) <= 12 {
		return traceID
	}
	return traceID[:8] + "…" + traceID[len(traceID)-4:]
}

func matchingIntentCapability(capabilities []domain.InvestigationCapability, intent domain.IntentKind, service, environment string) (domain.InvestigationCapability, bool) {
	for _, capability := range capabilities {
		if capability.Intent == intent && capability.Service == service && capability.Environment == environment {
			return capability, true
		}
	}
	return domain.InvestigationCapability{}, false
}

func containsIntentCapability(capabilities []domain.InvestigationCapability, resolution domain.IntentResolution) bool {
	capability, ok := matchingIntentCapability(capabilities, resolution.Intent, resolution.Service, resolution.Environment)
	return ok && capability.TemplateID == resolution.TemplateID
}

func safeIntentCode(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("_-.:/", character) {
			continue
		}
		return false
	}
	return true
}

func validIntentHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
