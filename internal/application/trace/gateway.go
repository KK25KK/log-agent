// Package trace implements the governed TraceID query boundary.
package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const PolicyVersion = domain.TracePolicyVersion

var (
	traceIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,256}$`)
	traceSecretPattern = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9._~+/=-]{8,}|LTAI[A-Za-z0-9]{12,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}|\b(?:\d{1,3}\.){3}\d{1,3}\b)`)
	traceURLPattern    = regexp.MustCompile(`(?i)https?://[^\s]+`)
	traceSafeLabel     = regexp.MustCompile(`^[\p{L}\p{N}._:/ -]{0,128}$`)
)

type Budget struct {
	MaxWindow         time.Duration
	IngestionGrace    time.Duration
	Timeout           time.Duration
	MemberLimit       int
	GlobalLimit       int
	MaxProcessedBytes int64
	MaxConcurrency    int
	RetryIncomplete   int
}

type Gateway struct {
	catalog ports.TraceResourceCatalog
	backend ports.TraceBackend
	auditor ports.TraceAuditor
	budget  Budget
	now     func() time.Time
}

func NewGateway(catalog ports.TraceResourceCatalog, backend ports.TraceBackend, auditor ports.TraceAuditor, budget Budget, now func() time.Time) (*Gateway, error) {
	if catalog == nil || backend == nil || auditor == nil || now == nil {
		return nil, errors.New("Trace catalog, backend, auditor, and clock are required")
	}
	if budget.MaxWindow <= 0 || budget.MaxWindow > 30*time.Minute || budget.IngestionGrace < domain.MinimumIngestionGrace ||
		budget.Timeout <= 0 || budget.MemberLimit <= 0 || budget.MemberLimit > domain.TraceDefaultMemberLimit ||
		budget.GlobalLimit <= 0 || budget.GlobalLimit > domain.TraceDefaultGlobalLimit || budget.MaxProcessedBytes <= 0 ||
		budget.MaxConcurrency <= 0 || budget.MaxConcurrency > domain.TraceDefaultConcurrency || budget.RetryIncomplete < 0 || budget.RetryIncomplete > 1 {
		return nil, errors.New("Trace query budget is invalid")
	}
	return &Gateway{catalog: catalog, backend: backend, auditor: auditor, budget: budget, now: now}, nil
}

func (g *Gateway) ResolveTraceGovernance(ctx context.Context, spec domain.TraceSearchSpec) (domain.TracePlan, error) {
	if err := validateSpec(spec, g.budget, g.now().UTC()); err != nil {
		g.record(ctx, domain.TraceAudit{
			InvestigationID: spec.InvestigationID, Principal: spec.Requester, TemplateID: domain.TraceSearchTemplateID,
			TraceIDFingerprint: TraceIDFingerprint(spec.TraceID), Outcome: "DENIED", ReasonCode: stableDenyReason(err), OccurredAt: g.now().UTC(),
		})
		return domain.TracePlan{}, err
	}
	group, err := g.catalog.ResolveTraceGroup(ctx, spec.Service, spec.Environment)
	if err != nil {
		g.record(ctx, domain.TraceAudit{InvestigationID: spec.InvestigationID, Principal: spec.Requester, TemplateID: domain.TraceSearchTemplateID,
			TraceIDFingerprint: TraceIDFingerprint(spec.TraceID), Outcome: "DENIED", ReasonCode: "trace_group_not_found", OccurredAt: g.now().UTC()})
		return domain.TracePlan{}, fmt.Errorf("%w: Trace resource group is unavailable", ports.ErrQueryDenied)
	}
	if !g.catalog.AllowedTraceGroup(ctx, spec.Requester, group.ID) {
		g.record(ctx, domain.TraceAudit{InvestigationID: spec.InvestigationID, Principal: spec.Requester, GroupID: group.ID,
			TemplateID: domain.TraceSearchTemplateID, TraceIDFingerprint: TraceIDFingerprint(spec.TraceID), Outcome: "DENIED", ReasonCode: "trace_acl_denied", OccurredAt: g.now().UTC()})
		return domain.TracePlan{}, fmt.Errorf("%w: Trace resource group is not authorized", ports.ErrQueryDenied)
	}
	if group.TemplateVersion != domain.TraceSearchTemplateVersion || len(group.Members) == 0 || len(group.Members) > domain.TraceMaximumMembers {
		return domain.TracePlan{}, fmt.Errorf("%w: Trace resource group contract is invalid", ports.ErrQueryDenied)
	}
	fingerprintValue, err := fingerprint.JSON(struct {
		Group              domain.TraceResourceGroup `json:"group"`
		Policy             string                    `json:"policy"`
		TraceIDFingerprint string                    `json:"trace_id_fingerprint"`
		MemberLimit        int                       `json:"member_limit"`
		GlobalLimit        int                       `json:"global_limit"`
		MaxProcessedBytes  int64                     `json:"max_processed_bytes"`
		MaxConcurrency     int                       `json:"max_concurrency"`
		RetryIncomplete    int                       `json:"retry_incomplete"`
	}{group, PolicyVersion, TraceIDFingerprint(spec.TraceID), g.budget.MemberLimit, g.budget.GlobalLimit, g.budget.MaxProcessedBytes, g.budget.MaxConcurrency, g.budget.RetryIncomplete})
	if err != nil {
		return domain.TracePlan{}, fmt.Errorf("fingerprint Trace governance: %w", err)
	}
	return domain.TracePlan{
		Spec: spec, Group: group, GovernanceFingerprint: fingerprintValue, TraceIDFingerprint: TraceIDFingerprint(spec.TraceID),
		MemberLimit: g.budget.MemberLimit, GlobalLimit: g.budget.GlobalLimit, MaxConcurrency: g.budget.MaxConcurrency,
		MaxProcessedBytes: g.budget.MaxProcessedBytes, RetryIncomplete: g.budget.RetryIncomplete,
	}, nil
}

func (g *Gateway) ExecuteTraceMember(ctx context.Context, plan domain.TracePlan, memberID string) (domain.TraceMemberResult, error) {
	member, ok := findMember(plan.Group.Members, memberID)
	if !ok || plan.Group.ID == "" || plan.Spec.TraceID == "" || plan.GovernanceFingerprint == "" {
		return domain.TraceMemberResult{}, fmt.Errorf("%w: approved Trace plan is invalid", ports.ErrQueryDenied)
	}
	base := domain.TraceMemberResult{
		GroupID: plan.Group.ID, MemberID: member.ID, TemplateID: domain.TraceSearchTemplateID,
		TemplateVersion: domain.TraceSearchTemplateVersion, PolicyVersion: PolicyVersion,
		GovernanceFingerprint: plan.GovernanceFingerprint, TraceIDFingerprint: plan.TraceIDFingerprint,
		StartTime: plan.Spec.StartTime, EndTime: plan.Spec.EndTime,
	}
	schema, err := g.backend.GetTraceSchema(ctx, member)
	if err != nil {
		base.Status = domain.TraceMemberInvalidSchema
		base.IncompleteReason = "trace_schema_unavailable"
		g.recordMember(ctx, plan, base, "INCOMPLETE", base.IncompleteReason, "")
		return base, fmt.Errorf("%w: Trace member schema unavailable", ports.ErrInvalidQuerySchema)
	}
	if err := validateMemberSchema(member, schema); err != nil {
		base.SchemaFingerprint = schema.Fingerprint
		base.Status = domain.TraceMemberInvalidSchema
		base.IncompleteReason = "trace_schema_incompatible"
		g.recordMember(ctx, plan, base, "INCOMPLETE", base.IncompleteReason, "")
		return base, fmt.Errorf("%w: Trace member schema incompatible", ports.ErrInvalidQuerySchema)
	}
	queryHash, err := fingerprint.JSON(struct {
		InvestigationID       string                     `json:"investigation_id"`
		GroupID               string                     `json:"group_id"`
		Member                domain.TraceResourceMember `json:"member"`
		TraceIDFingerprint    string                     `json:"trace_id_fingerprint"`
		StartTime             time.Time                  `json:"start_time"`
		EndTime               time.Time                  `json:"end_time"`
		SchemaFingerprint     string                     `json:"schema_fingerprint"`
		GovernanceFingerprint string                     `json:"governance_fingerprint"`
		Limit                 int                        `json:"limit"`
	}{plan.Spec.InvestigationID, plan.Group.ID, member, plan.TraceIDFingerprint, plan.Spec.StartTime, plan.Spec.EndTime, schema.Fingerprint, plan.GovernanceFingerprint, plan.MemberLimit})
	if err != nil {
		return base, fmt.Errorf("fingerprint Trace query: %w", err)
	}
	base.QuerySpecHash = queryHash
	base.SchemaFingerprint = schema.Fingerprint
	g.recordMember(ctx, plan, base, "STARTED", "", "")

	approved := domain.ApprovedTraceQuery{
		Spec: plan.Spec, GroupID: plan.Group.ID, Member: member, GovernanceFingerprint: plan.GovernanceFingerprint,
		TraceIDFingerprint: plan.TraceIDFingerprint, MemberLimit: plan.MemberLimit, RetryIncomplete: plan.RetryIncomplete,
	}
	callCtx, cancel := context.WithTimeout(ctx, g.budget.Timeout)
	defer cancel()
	providerResult, err := g.backend.SearchTrace(callCtx, approved)
	base.QueryID = providerResult.ExecutionID
	if err != nil {
		base.Status = domain.TraceMemberOutcomeUnknown
		base.IncompleteReason = "trace_provider_outcome_unknown"
		g.recordMember(ctx, plan, base, "OUTCOME_UNKNOWN", base.IncompleteReason, providerResult.ProviderRequestID)
		return base, err
	}
	result := g.normalizeMember(plan, member, schema, queryHash, providerResult)
	outcome := "SUCCEEDED"
	if !result.Complete {
		outcome = "INCOMPLETE"
	}
	g.recordMember(ctx, plan, result, outcome, result.IncompleteReason, providerResult.ProviderRequestID)
	return result, nil
}

func (g *Gateway) normalizeMember(plan domain.TracePlan, member domain.TraceResourceMember, schema domain.IndexSchema, queryHash string, raw domain.TraceBackendResult) domain.TraceMemberResult {
	result := domain.TraceMemberResult{
		QueryID: raw.ExecutionID, QuerySpecHash: queryHash, GroupID: plan.Group.ID, MemberID: member.ID,
		TemplateID: domain.TraceSearchTemplateID, TemplateVersion: domain.TraceSearchTemplateVersion,
		PolicyVersion: PolicyVersion, SchemaFingerprint: schema.Fingerprint, GovernanceFingerprint: plan.GovernanceFingerprint,
		TraceIDFingerprint: plan.TraceIDFingerprint, StartTime: plan.Spec.StartTime, EndTime: plan.Spec.EndTime,
		Progress: raw.Progress, ProcessedRows: raw.ProcessedRows, ProcessedBytes: raw.ProcessedBytes,
		ElapsedMillisecond: raw.ElapsedMillisecond, UsageKnown: raw.UsageKnown,
		NanosecondOrderedKnown: raw.NanosecondOrderKnown, NanosecondOrdered: raw.NanosecondOrdered, APICalls: raw.APICalls,
	}
	invalidEventTime := false
	for index, item := range raw.Events {
		if len(result.Events) >= plan.MemberLimit {
			break
		}
		event := normalizeEvent(member.ID, plan.Spec.TraceID, index, item)
		if event.EventTime.IsZero() || event.EventTime.Before(plan.Spec.StartTime) || event.EventTime.After(plan.Spec.EndTime) {
			invalidEventTime = true
		}
		result.Events = append(result.Events, event)
	}
	sortTraceEvents(result.Events)
	switch {
	case raw.ExecutionID == "" || raw.APICalls <= 0 || raw.APICalls > 1+plan.RetryIncomplete:
		result.Status, result.IncompleteReason = domain.TraceMemberIncomplete, "trace_metadata_invalid"
	case invalidEventTime:
		result.Status, result.IncompleteReason = domain.TraceMemberIncomplete, "trace_event_time_invalid"
	case !raw.UsageKnown || raw.ProcessedRows < 0 || raw.ProcessedBytes < 0 || raw.ElapsedMillisecond < 0:
		result.Status, result.IncompleteReason = domain.TraceMemberIncomplete, "trace_usage_unknown"
	case raw.ProcessedBytes > plan.MaxProcessedBytes:
		result.Status, result.IncompleteReason = domain.TraceMemberIncomplete, "trace_processed_bytes_exceeded"
	case !strings.EqualFold(raw.Progress, "Complete"):
		result.Status, result.IncompleteReason = domain.TraceMemberIncomplete, "trace_progress_incomplete"
	case len(raw.Events) >= plan.MemberLimit:
		result.Status, result.Truncated, result.IncompleteReason = domain.TraceMemberTruncated, true, "trace_member_limit_reached"
	case len(result.Events) == 0:
		result.Status, result.Complete, result.ZeroHit = domain.TraceMemberZeroHit, true, true
	default:
		result.Status, result.Complete = domain.TraceMemberComplete, true
	}
	return result
}

func validateSpec(spec domain.TraceSearchSpec, budget Budget, now time.Time) error {
	if spec.InvestigationID == "" || !spec.Requester.Complete() || !traceIDPattern.MatchString(spec.TraceID) ||
		spec.Service == "" || spec.Environment == "" || spec.StartTime.IsZero() || spec.EndTime.IsZero() || !spec.StartTime.Before(spec.EndTime) {
		return fmt.Errorf("%w: Trace request is invalid", ports.ErrQueryDenied)
	}
	if spec.EndTime.Sub(spec.StartTime) > budget.MaxWindow || spec.EndTime.After(now.Add(-budget.IngestionGrace)) {
		return fmt.Errorf("%w: Trace window exceeds policy", ports.ErrQueryBudgetExceeded)
	}
	return nil
}

func validateMemberSchema(member domain.TraceResourceMember, schema domain.IndexSchema) error {
	if schema.Fingerprint == "" || schema.Fields == nil {
		return errors.New("schema metadata is incomplete")
	}
	if (member.TraceMode == domain.TraceQueryFullText || member.EnvironmentMode == domain.TraceEnvironmentFullText) && !schema.FullText {
		return errors.New("configured full-text query requires a full-text index")
	}
	required := []string{member.MessageField}
	if member.TraceMode == domain.TraceQueryField {
		required = append(required, member.TraceField)
	}
	if member.EnvironmentMode == domain.TraceEnvironmentField {
		required = append(required, member.EnvironmentField)
	}
	for _, optional := range []string{member.LevelField, member.OperationField} {
		if optional != "" {
			required = append(required, optional)
		}
	}
	for _, field := range required {
		if strings.HasPrefix(field, "__") {
			continue
		}
		if _, exists := schema.Fields[field]; !exists {
			return fmt.Errorf("required field %q is not indexed", field)
		}
	}
	return nil
}

// ValidateConfiguredMemberSchema is used by the explicit zero-log-read check
// command. It applies the same field contract as an executing Gateway.
func ValidateConfiguredMemberSchema(member domain.TraceResourceMember, schema domain.IndexSchema) error {
	return validateMemberSchema(member, schema)
}

func normalizeEvent(memberID, traceID string, index int, raw domain.TraceBackendEvent) domain.TraceEvent {
	message, redacted := redactTraceText(raw.Message, traceID, 512)
	level, levelRedacted := redactTraceText(raw.Level, traceID, 64)
	operation, operationRedacted := redactTraceText(raw.Operation, traceID, 128)
	eventTime, eventOK := parseTraceTime(raw.EventTimeRaw, raw.NanosecondRaw)
	receiveTime, _ := parseTraceTime(raw.ReceiveTimeRaw, "")
	quality := domain.TraceSortUnknown
	if eventOK {
		quality = domain.TraceSortSecond
		if raw.NanosecondRaw != "" {
			quality = domain.TraceSortNanosecond
		}
	}
	messageHash := sha256.Sum256([]byte(message))
	idHash := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", memberID, index, eventTime.UTC().Format(time.RFC3339Nano), hex.EncodeToString(messageHash[:]))))
	return domain.TraceEvent{
		ID: "tev_" + hex.EncodeToString(idHash[:12]), MemberID: memberID, EventTime: eventTime, ReceiveTime: receiveTime,
		SortQuality: quality, Level: level, Operation: operation, Message: message,
		MessageFingerprint: hex.EncodeToString(messageHash[:]), Redacted: redacted || levelRedacted || operationRedacted,
	}
}

func redactTraceText(value, traceID string, maxRunes int) (string, bool) {
	value = strings.TrimSpace(value)
	redacted := value
	if traceID != "" {
		redacted = strings.ReplaceAll(redacted, traceID, "[TRACE_ID]")
	}
	redacted = traceSecretPattern.ReplaceAllString(redacted, "[REDACTED]")
	redacted = traceURLPattern.ReplaceAllStringFunc(redacted, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return "[REDACTED_URL]"
		}
		return parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
	})
	changed := redacted != value
	if utf8.RuneCountInString(redacted) > maxRunes {
		runes := []rune(redacted)
		redacted = string(runes[:maxRunes]) + "…"
		changed = true
	}
	if maxRunes <= 128 && !traceSafeLabel.MatchString(redacted) {
		return "[REDACTED]", true
	}
	return redacted, changed
}

func parseTraceTime(raw, nanos string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
		seconds := number
		var nanoseconds int64
		switch len(strings.TrimPrefix(raw, "-")) {
		case 13:
			seconds, nanoseconds = number/1_000, (number%1_000)*1_000_000
		case 16:
			seconds, nanoseconds = number/1_000_000, (number%1_000_000)*1_000
		case 19:
			seconds, nanoseconds = number/1_000_000_000, number%1_000_000_000
		default:
			if nanos != "" {
				nanoseconds, _ = strconv.ParseInt(nanos, 10, 64)
			}
		}
		if nanoseconds < 0 || nanoseconds >= int64(time.Second) {
			nanoseconds = 0
		}
		return time.Unix(seconds, nanoseconds).UTC(), true
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func sortTraceEvents(events []domain.TraceEvent) {
	sort.SliceStable(events, func(left, right int) bool {
		leftTime, rightTime := events[left].EventTime, events[right].EventTime
		if leftTime.IsZero() != rightTime.IsZero() {
			return !leftTime.IsZero()
		}
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		if events[left].MemberID != events[right].MemberID {
			return events[left].MemberID < events[right].MemberID
		}
		return events[left].ID < events[right].ID
	})
}

func TraceIDFingerprint(traceID string) string {
	digest := sha256.Sum256([]byte(traceID))
	return hex.EncodeToString(digest[:])
}

func findMember(members []domain.TraceResourceMember, memberID string) (domain.TraceResourceMember, bool) {
	for _, member := range members {
		if member.ID == memberID {
			return member, true
		}
	}
	return domain.TraceResourceMember{}, false
}

func stableDenyReason(err error) string {
	if errors.Is(err, ports.ErrQueryBudgetExceeded) {
		return "trace_budget_denied"
	}
	return "trace_request_denied"
}

func (g *Gateway) recordMember(ctx context.Context, plan domain.TracePlan, result domain.TraceMemberResult, outcome, reason, providerID string) {
	g.record(ctx, domain.TraceAudit{
		InvestigationID: plan.Spec.InvestigationID, Principal: plan.Spec.Requester, GroupID: plan.Group.ID,
		MemberID: result.MemberID, TemplateID: domain.TraceSearchTemplateID, TraceIDFingerprint: plan.TraceIDFingerprint,
		GovernanceFingerprint: plan.GovernanceFingerprint, QuerySpecHash: result.QuerySpecHash,
		SchemaFingerprint: result.SchemaFingerprint, Outcome: outcome, ReasonCode: reason,
		ProviderRequestID: providerID, Progress: result.Progress, ReturnedEvents: len(result.Events), APICalls: result.APICalls,
		ProcessedRows: result.ProcessedRows, ProcessedBytes: result.ProcessedBytes,
		ElapsedMillisecond: result.ElapsedMillisecond, OccurredAt: g.now().UTC(),
	})
}

func (g *Gateway) record(ctx context.Context, audit domain.TraceAudit) {
	auditCtx := context.WithoutCancel(ctx)
	deadlineCtx, cancel := context.WithTimeout(auditCtx, 2*time.Second)
	defer cancel()
	_ = g.auditor.RecordTraceAudit(deadlineCtx, audit)
}

var _ ports.GovernedTraceExecutor = (*Gateway)(nil)
