package query

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const (
	PolicyVersion = "query-policy-v2"
)

type Budget struct {
	MaxWindow         time.Duration
	IngestionGrace    time.Duration
	Timeout           time.Duration
	MaxRows           int64
	MaxAPICalls       int
	MaxProcessedBytes int64
	MaxConcurrent     int
	SchemaTTL         time.Duration
}

type Gateway struct {
	catalog ports.ResourceCatalog
	backend ports.SLSBackend
	auditor ports.QueryAuditor
	budget  Budget
	limiter chan struct{}
	now     func() time.Time

	cacheMu sync.Mutex
	cache   map[string]cachedSchema
}

type cachedSchema struct {
	schema    domain.IndexSchema
	expiresAt time.Time
}

type governanceBudget struct {
	MaxWindowNanoseconds      int64 `json:"max_window_nanoseconds"`
	IngestionGraceNanoseconds int64 `json:"ingestion_grace_nanoseconds"`
	TimeoutNanoseconds        int64 `json:"timeout_nanoseconds"`
	MaxRows                   int64 `json:"max_rows"`
	MaxAPICalls               int   `json:"max_api_calls"`
	MaxProcessedBytes         int64 `json:"max_processed_bytes"`
	MaxConcurrent             int   `json:"max_concurrent"`
	SchemaTTLNanoseconds      int64 `json:"schema_ttl_nanoseconds"`
	PatternLimit              int   `json:"pattern_limit"`
	InstanceLimit             int   `json:"instance_limit"`
	ExpectedAPICalls          int   `json:"expected_api_calls"`
	ExpectedResultRows        int   `json:"expected_result_rows"`
}

type queryGovernanceIdentity struct {
	Resource          domain.LogResource `json:"resource"`
	TemplateID        string             `json:"template_id"`
	PolicyVersion     string             `json:"policy_version"`
	SchemaFingerprint string             `json:"schema_fingerprint"`
	Budget            governanceBudget   `json:"budget"`
}

func NewGateway(catalog ports.ResourceCatalog, backend ports.SLSBackend, auditor ports.QueryAuditor, budget Budget) (*Gateway, error) {
	if catalog == nil || backend == nil || auditor == nil {
		return nil, errors.New("catalog, SLS backend, and query auditor are required")
	}
	if budget.MaxWindow <= 0 || budget.Timeout <= 0 || budget.MaxRows <= 0 || budget.MaxAPICalls <= 0 || budget.MaxProcessedBytes <= 0 || budget.MaxConcurrent <= 0 || budget.SchemaTTL <= 0 {
		return nil, errors.New("all query budget values must be positive")
	}
	if budget.IngestionGrace < domain.MinimumIngestionGrace {
		return nil, fmt.Errorf("ingestion grace must be at least %s", domain.MinimumIngestionGrace)
	}
	if budget.MaxAPICalls < domain.ErrorAnalysisAPICalls {
		return nil, fmt.Errorf("max API calls must allow the %d fixed analysis requests", domain.ErrorAnalysisAPICalls)
	}
	if budget.MaxRows < int64(domain.ErrorAnalysisResultRows) {
		return nil, fmt.Errorf("max rows must allow the %d fixed analysis rows", domain.ErrorAnalysisResultRows)
	}
	return &Gateway{
		catalog: catalog,
		backend: backend,
		auditor: auditor,
		budget:  budget,
		limiter: make(chan struct{}, budget.MaxConcurrent),
		now:     time.Now,
		cache:   make(map[string]cachedSchema),
	}, nil
}

func (g *Gateway) Execute(ctx context.Context, spec domain.QuerySpec) (domain.QueryResult, error) {
	approved, runCtx, cleanup, err := g.resolveApproved(ctx, spec)
	if err != nil {
		return domain.QueryResult{}, err
	}
	defer cleanup()

	if err := g.audit(runCtx, spec, approved, domain.QueryAudit{Outcome: "STARTED"}); err != nil {
		return domain.QueryResult{}, fmt.Errorf("persist query start audit: %w", err)
	}

	result, queryErr := g.backend.Execute(runCtx, approved)
	// Some provider SDK methods cannot observe the caller context while an HTTP
	// request is in flight. A provider may therefore return a seemingly valid
	// result after our application deadline. Re-check the governed context here
	// so a late response can never become conclusive evidence.
	if queryErr == nil {
		if err := runCtx.Err(); err != nil {
			queryErr = fmt.Errorf("query context ended before the provider result was accepted: %w", err)
		}
	}
	if queryErr != nil {
		auditErr := g.auditTerminal(spec, approved, result, "FAILED", safeFailureReason(queryErr))
		if auditErr != nil {
			return domain.QueryResult{}, fmt.Errorf("execute SLS query: %v; persist terminal audit: %w", queryErr, auditErr)
		}
		return domain.QueryResult{}, fmt.Errorf("execute SLS query: %w", queryErr)
	}

	result = g.normalize(approved, result)
	outcome := "SUCCEEDED"
	if !result.Complete || result.Truncated {
		outcome = "INCOMPLETE"
	}
	if err := g.auditTerminal(spec, approved, result, outcome, result.IncompleteReason); err != nil {
		return domain.QueryResult{}, fmt.Errorf("persist terminal query audit: %w", err)
	}
	return result, nil
}

// ResolveQueryGovernance performs the same policy, catalog, ACL, budget and
// schema resolution as Execute, but stops before the log-reading backend call.
// Checkpoint reuse binds this identity to the logical QuerySpec.
func (g *Gateway) ResolveQueryGovernance(ctx context.Context, spec domain.QuerySpec) (string, error) {
	approved, _, cleanup, err := g.resolveApproved(ctx, spec)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if !validFingerprint(approved.GovernanceFingerprint) {
		return "", g.deny(ctx, spec, approved.Resource, "governance_fingerprint", fmt.Errorf("%w: resolved governance fingerprint is invalid", ports.ErrQueryDenied))
	}
	return approved.GovernanceFingerprint, nil
}

// resolveApproved owns the complete pre-provider governance path. Its caller
// must invoke cleanup so the concurrency slot and timeout remain active for the
// whole provider call in Execute, while ResolveQueryGovernance releases them
// immediately after schema identity has been resolved.
func (g *Gateway) resolveApproved(ctx context.Context, spec domain.QuerySpec) (domain.ApprovedQuery, context.Context, func(), error) {
	if err := validateSpec(spec); err != nil {
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, domain.LogResource{}, "invalid_request", fmt.Errorf("%w: %v", ports.ErrQueryDenied, err))
	}

	resource, err := g.catalog.Resolve(ctx, spec.Service, spec.Environment)
	if err != nil {
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, domain.LogResource{}, "unknown_resource", fmt.Errorf("%w: %v", ports.ErrQueryDenied, err))
	}
	if !g.catalog.Allowed(ctx, spec.Requester, resource.ID) {
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "acl_denied", ports.ErrQueryDenied)
	}
	if err := g.preflight(spec); err != nil {
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "preflight_budget", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, g.budget.Timeout)
	if err := g.acquire(runCtx); err != nil {
		cancel()
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "concurrency_budget", fmt.Errorf("%w: %v", ports.ErrQueryBudgetExceeded, err))
	}
	cleanup := func() {
		g.release()
		cancel()
	}

	schema, err := g.schema(runCtx, resource)
	if err != nil {
		cleanup()
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "schema_unavailable", fmt.Errorf("%w: %v", ports.ErrInvalidQuerySchema, err))
	}
	if err := validateSchema(resource, schema); err != nil {
		cleanup()
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "schema_invalid", err)
	}

	approved, err := g.approve(spec, resource, schema)
	if err != nil {
		cleanup()
		return domain.ApprovedQuery{}, nil, nil, g.deny(ctx, spec, resource, "governance_fingerprint", fmt.Errorf("%w: %v", ports.ErrQueryDenied, err))
	}
	return approved, runCtx, cleanup, nil
}

func validateSpec(spec domain.QuerySpec) error {
	if spec.InvestigationID == "" || spec.Name == "" || spec.Service == "" || spec.Environment == "" {
		return errors.New("investigation, query name, service, and environment are required")
	}
	if !spec.Requester.Complete() {
		return errors.New("trusted requester identity is required")
	}
	if spec.TemplateID != domain.ErrorAnalysisTemplateID {
		return fmt.Errorf("%w: template %q is not allowed", ports.ErrQueryDenied, spec.TemplateID)
	}
	if !spec.EndTime.After(spec.StartTime) {
		return errors.New("query end time must be after start time")
	}
	return nil
}

func (g *Gateway) preflight(spec domain.QuerySpec) error {
	if spec.EndTime.Sub(spec.StartTime) > g.budget.MaxWindow {
		return fmt.Errorf("%w: window exceeds %s", ports.ErrQueryBudgetExceeded, g.budget.MaxWindow)
	}
	watermark := g.now().UTC().Add(-g.budget.IngestionGrace)
	if spec.EndTime.After(watermark) {
		return fmt.Errorf("%w: window end has not crossed the configured ingestion watermark", ports.ErrQueryBudgetExceeded)
	}
	if g.budget.MaxRows < int64(domain.ErrorAnalysisResultRows) {
		return fmt.Errorf("%w: fixed template requires %d aggregate rows", ports.ErrQueryBudgetExceeded, domain.ErrorAnalysisResultRows)
	}
	if g.budget.MaxAPICalls < domain.ErrorAnalysisAPICalls {
		return fmt.Errorf("%w: fixed template requires %d calls", ports.ErrQueryBudgetExceeded, domain.ErrorAnalysisAPICalls)
	}
	return nil
}

func (g *Gateway) acquire(ctx context.Context) error {
	select {
	case g.limiter <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gateway) release() {
	<-g.limiter
}

func (g *Gateway) schema(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	now := g.now().UTC()
	g.cacheMu.Lock()
	cached, exists := g.cache[resource.ID]
	g.cacheMu.Unlock()
	if exists && now.Before(cached.expiresAt) {
		return cached.schema, nil
	}

	schema, err := g.backend.GetSchema(ctx, resource)
	if err != nil {
		return domain.IndexSchema{}, err
	}
	if schema.Fingerprint == "" {
		return domain.IndexSchema{}, errors.New("schema fingerprint is required")
	}
	g.cacheMu.Lock()
	g.cache[resource.ID] = cachedSchema{schema: schema, expiresAt: now.Add(g.budget.SchemaTTL)}
	g.cacheMu.Unlock()
	return schema, nil
}

func validateSchema(resource domain.LogResource, schema domain.IndexSchema) error {
	selectors := append(append([]domain.LogSelector(nil), resource.Selectors...), resource.ErrorSelector)
	for _, selector := range selectors {
		if _, exists := schema.Fields[selector.Field]; !exists {
			return fmt.Errorf("%w: selector field %q is not indexed", ports.ErrInvalidQuerySchema, selector.Field)
		}
	}
	errorField, exists := schema.Fields[resource.ErrorField]
	if !exists {
		return fmt.Errorf("%w: error field %q is not indexed", ports.ErrInvalidQuerySchema, resource.ErrorField)
	}
	if errorField.Type != "text" || !errorField.DocValue {
		return fmt.Errorf("%w: error field %q must be text with statistics enabled", ports.ErrInvalidQuerySchema, resource.ErrorField)
	}
	instanceField, exists := schema.Fields[resource.InstanceField]
	if !exists {
		return fmt.Errorf("%w: instance field %q is not indexed", ports.ErrInvalidQuerySchema, resource.InstanceField)
	}
	if instanceField.Type != "text" || !instanceField.DocValue {
		return fmt.Errorf("%w: instance field %q must be text with statistics enabled", ports.ErrInvalidQuerySchema, resource.InstanceField)
	}
	return nil
}

func (g *Gateway) approve(spec domain.QuerySpec, resource domain.LogResource, schema domain.IndexSchema) (domain.ApprovedQuery, error) {
	approved := domain.ApprovedQuery{
		Resource:          resource,
		TemplateID:        spec.TemplateID,
		PolicyVersion:     PolicyVersion,
		SchemaFingerprint: schema.Fingerprint,
		StartTime:         spec.StartTime.UTC(),
		EndTime:           spec.EndTime.UTC(),
		MaxRows:           g.budget.MaxRows,
		MaxAPICalls:       g.budget.MaxAPICalls,
		PatternLimit:      domain.ErrorAnalysisPatternLimit,
		InstanceLimit:     domain.ErrorAnalysisInstanceLimit,
		ExpectedAPICalls:  domain.ErrorAnalysisAPICalls,
	}
	governanceFingerprint, err := queryGovernanceFingerprint(resource, spec.TemplateID, PolicyVersion, schema.Fingerprint, g.budget)
	if err != nil {
		return domain.ApprovedQuery{}, fmt.Errorf("fingerprint query governance: %w", err)
	}
	approved.GovernanceFingerprint = governanceFingerprint
	hash, err := fingerprint.JSON(approved)
	if err != nil {
		return domain.ApprovedQuery{}, fmt.Errorf("fingerprint approved query: %w", err)
	}
	approved.SpecHash = hash
	return approved, nil
}

func (g *Gateway) normalize(approved domain.ApprovedQuery, result domain.QueryResult) domain.QueryResult {
	result.QuerySpecHash = approved.SpecHash
	result.ResourceID = approved.Resource.ID
	result.TemplateID = approved.TemplateID
	result.TemplateVersion = approved.Resource.TemplateVersion
	result.SchemaFingerprint = approved.SchemaFingerprint
	result.PolicyVersion = approved.PolicyVersion
	result.GovernanceFingerprint = approved.GovernanceFingerprint

	result.ErrorPatterns = append([]domain.CountBucket(nil), result.ErrorPatterns...)
	result.Instances = append([]domain.CountBucket(nil), result.Instances...)
	result.ErrorPatterns, result.Redacted = redactBuckets(result.ErrorPatterns, result.Redacted)
	result.Instances, result.Redacted = redactBuckets(result.Instances, result.Redacted)
	if len(result.ErrorPatterns) > 0 {
		result.TopError = result.ErrorPatterns[0].Label
		result.TopErrorCount = result.ErrorPatterns[0].Count
	} else {
		redacted, changed := redactLabel(result.TopError)
		result.TopError = redacted
		result.Redacted = result.Redacted || changed
	}

	complete := strings.EqualFold(result.Progress, "complete") && !result.Truncated
	if !result.UsageKnown {
		complete = false
		result.IncompleteReason = appendReason(result.IncompleteReason, "provider usage metadata is unavailable")
	}
	if result.APICalls != approved.ExpectedAPICalls {
		complete = false
		result.IncompleteReason = appendReason(result.IncompleteReason, fmt.Sprintf("expected %d API calls, got %d", approved.ExpectedAPICalls, result.APICalls))
	}
	if result.APICalls > approved.MaxAPICalls {
		complete = false
		result.Truncated = true
		result.IncompleteReason = appendReason(result.IncompleteReason, "API call budget exceeded")
	}
	if result.ProcessedBytes > g.budget.MaxProcessedBytes {
		complete = false
		result.Truncated = true
		result.IncompleteReason = appendReason(result.IncompleteReason, "processed-byte budget exceeded")
	}
	if !strings.EqualFold(result.Progress, "complete") {
		result.IncompleteReason = appendReason(result.IncompleteReason, "provider progress is "+result.Progress)
	}
	result.Complete = complete
	return result
}

func queryGovernanceFingerprint(resource domain.LogResource, templateID, policyVersion, schemaFingerprint string, budget Budget) (string, error) {
	identity := queryGovernanceIdentity{
		Resource:          resource,
		TemplateID:        templateID,
		PolicyVersion:     policyVersion,
		SchemaFingerprint: schemaFingerprint,
		Budget: governanceBudget{
			MaxWindowNanoseconds:      int64(budget.MaxWindow),
			IngestionGraceNanoseconds: int64(budget.IngestionGrace),
			TimeoutNanoseconds:        int64(budget.Timeout),
			MaxRows:                   budget.MaxRows,
			MaxAPICalls:               budget.MaxAPICalls,
			MaxProcessedBytes:         budget.MaxProcessedBytes,
			MaxConcurrent:             budget.MaxConcurrent,
			SchemaTTLNanoseconds:      int64(budget.SchemaTTL),
			PatternLimit:              domain.ErrorAnalysisPatternLimit,
			InstanceLimit:             domain.ErrorAnalysisInstanceLimit,
			ExpectedAPICalls:          domain.ErrorAnalysisAPICalls,
			ExpectedResultRows:        domain.ErrorAnalysisResultRows,
		},
	}
	return fingerprint.JSON(identity)
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func redactBuckets(buckets []domain.CountBucket, alreadyRedacted bool) ([]domain.CountBucket, bool) {
	redacted := alreadyRedacted
	for index := range buckets {
		label, changed := redactLabel(buckets[index].Label)
		buckets[index].Label = label
		buckets[index].Redacted = buckets[index].Redacted || changed
		redacted = redacted || buckets[index].Redacted
	}
	return buckets, redacted
}

func appendReason(current, reason string) string {
	if current == "" {
		return reason
	}
	return current + "; " + reason
}

func safeFailureReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, ports.ErrTenantQuotaExceeded) {
		return "tenant_query_quota_exceeded"
	}
	return "provider_error"
}

func (g *Gateway) deny(ctx context.Context, spec domain.QuerySpec, resource domain.LogResource, reason string, cause error) error {
	hash, _ := fingerprint.JSON(spec)
	audit := domain.QueryAudit{
		InvestigationID: spec.InvestigationID,
		Principal:       spec.Requester,
		ResourceID:      resource.ID,
		TemplateID:      spec.TemplateID,
		TemplateVersion: resource.TemplateVersion,
		QuerySpecHash:   hash,
		PolicyVersion:   PolicyVersion,
		Outcome:         "DENIED",
		Reason:          reason,
		OccurredAt:      g.now().UTC(),
	}
	auditCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		auditCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	}
	defer cancel()
	if err := g.auditor.RecordQueryAudit(auditCtx, audit); err != nil {
		return fmt.Errorf("%v; persist denied query audit: %w", cause, err)
	}
	return cause
}

func (g *Gateway) audit(ctx context.Context, spec domain.QuerySpec, approved domain.ApprovedQuery, partial domain.QueryAudit) error {
	partial.InvestigationID = spec.InvestigationID
	partial.Principal = spec.Requester
	partial.ResourceID = approved.Resource.ID
	partial.TemplateID = approved.TemplateID
	partial.TemplateVersion = approved.Resource.TemplateVersion
	partial.QuerySpecHash = approved.SpecHash
	partial.SchemaFingerprint = approved.SchemaFingerprint
	partial.PolicyVersion = approved.PolicyVersion
	partial.OccurredAt = g.now().UTC()
	return g.auditor.RecordQueryAudit(ctx, partial)
}

func (g *Gateway) auditTerminal(spec domain.QuerySpec, approved domain.ApprovedQuery, result domain.QueryResult, outcome, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return g.audit(ctx, spec, approved, domain.QueryAudit{
		Outcome:           outcome,
		Reason:            reason,
		ProviderRequestID: result.QueryID,
		Progress:          result.Progress,
		Complete:          result.Complete,
		Truncated:         result.Truncated,
		ProcessedRows:     result.ProcessedRows,
		ProcessedBytes:    result.ProcessedBytes,
	})
}

var (
	emailPattern  = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	ipv4Pattern   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	jwtPattern    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	akPattern     = regexp.MustCompile(`\bLTAI[A-Za-z0-9]{12,}\b`)
)

func redactLabel(value string) (string, bool) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	replacements := []struct {
		pattern     *regexp.Regexp
		replacement string
	}{
		{emailPattern, "[REDACTED_EMAIL]"},
		{ipv4Pattern, "[REDACTED_IP]"},
		{bearerPattern, "Bearer [REDACTED_TOKEN]"},
		{jwtPattern, "[REDACTED_TOKEN]"},
		{akPattern, "[REDACTED_ACCESS_KEY]"},
	}
	for _, replacement := range replacements {
		cleaned = replacement.pattern.ReplaceAllString(cleaned, replacement.replacement)
	}
	runes := []rune(cleaned)
	if len(runes) > 200 {
		cleaned = string(runes[:200]) + "…"
	}
	return cleaned, cleaned != value
}

var (
	_ ports.SLSExecutor             = (*Gateway)(nil)
	_ ports.QueryGovernanceResolver = (*Gateway)(nil)
)
