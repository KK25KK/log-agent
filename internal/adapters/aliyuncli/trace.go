package aliyuncli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

func (b *Backend) GetTraceSchema(ctx context.Context, member domain.TraceResourceMember) (domain.IndexSchema, error) {
	resource := traceLogResource(member)
	if err := validateResourceLocation(resource); err != nil {
		return domain.IndexSchema{}, err
	}
	if _, err := b.getLogStore(ctx, resource); err != nil {
		return domain.IndexSchema{}, err
	}
	return b.getTraceIndex(ctx, resource)
}

func (b *Backend) getTraceIndex(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	endpoint, region, err := cliLocation(resource.Endpoint)
	if err != nil {
		return domain.IndexSchema{}, err
	}
	var payload struct {
		Keys map[string]map[string]json.RawMessage `json:"keys"`
		Line json.RawMessage                       `json:"line"`
	}
	if err := b.runJSON(ctx, "get-trace-index", []string{
		"sls", "get-index", "--project", resource.Project, "--logstore", resource.LogStore,
		"--region", region, "--endpoint", endpoint, "--log-level", "ERROR",
	}, &payload); err != nil {
		return domain.IndexSchema{}, fmt.Errorf("get Trace index for resource %q: %w", resource.ID, err)
	}
	fields := make(map[string]domain.IndexField, len(payload.Keys))
	for name, item := range payload.Keys {
		fieldType := strings.ToLower(firstString(item, "type"))
		docValue, _ := firstBool(item, "doc_value", "docValue")
		fields[name] = domain.IndexField{Type: fieldType, DocValue: docValue}
	}
	var lineDefinition any
	fullText := len(payload.Line) > 0 && string(payload.Line) != "null"
	if fullText {
		if err := json.Unmarshal(payload.Line, &lineDefinition); err != nil {
			return domain.IndexSchema{}, errors.New("SLS CLI returned an invalid full-text index")
		}
	}
	if len(fields) == 0 && !fullText {
		return domain.IndexSchema{}, errors.New("SLS CLI returned an empty Trace index")
	}
	fingerprintValue, err := fingerprint.JSON(struct {
		Fields   map[string]domain.IndexField `json:"fields"`
		FullText any                          `json:"full_text,omitempty"`
	}{Fields: fields, FullText: lineDefinition})
	if err != nil {
		return domain.IndexSchema{}, fmt.Errorf("fingerprint SLS Trace index: %w", err)
	}
	return domain.IndexSchema{Fingerprint: fingerprintValue, Fields: fields, FullText: fullText, FetchedAt: b.now().UTC()}, nil
}

func (b *Backend) SearchTrace(ctx context.Context, query domain.ApprovedTraceQuery) (domain.TraceBackendResult, error) {
	if err := validateApprovedTraceQuery(query); err != nil {
		return domain.TraceBackendResult{}, err
	}
	expression, err := compileTraceExpression(query.Spec.Environment, query.Spec.TraceID, query.Member)
	if err != nil {
		return domain.TraceBackendResult{}, err
	}
	executionID, err := b.newExecutionID()
	if err != nil {
		return domain.TraceBackendResult{}, err
	}
	combined := domain.TraceBackendResult{ExecutionID: executionID}
	for attempt := 0; attempt <= query.RetryIncomplete; attempt++ {
		result, callErr := b.searchTraceOnce(ctx, query, expression)
		combined.APICalls++
		combined.ProviderRequestID = result.ProviderRequestID
		combined.Progress = result.Progress
		combined.ProcessedRows += result.ProcessedRows
		combined.ProcessedBytes += result.ProcessedBytes
		combined.ElapsedMillisecond += result.ElapsedMillisecond
		combined.UsageKnown = (combined.APICalls == 1 || combined.UsageKnown) && result.UsageKnown
		combined.NanosecondOrderKnown = result.NanosecondOrderKnown
		combined.NanosecondOrdered = result.NanosecondOrdered
		combined.Events = result.Events
		if callErr != nil {
			return combined, callErr
		}
		if strings.EqualFold(result.Progress, "Complete") {
			return combined, nil
		}
	}
	return combined, nil
}

func (b *Backend) searchTraceOnce(ctx context.Context, query domain.ApprovedTraceQuery, expression string) (domain.TraceBackendResult, error) {
	var payload struct {
		Logs              []map[string]json.RawMessage `json:"logs"`
		Data              []map[string]json.RawMessage `json:"data"`
		Meta              map[string]json.RawMessage   `json:"meta"`
		RequestID         string                       `json:"requestId"`
		ProviderRequestID string                       `json:"request_id"`
	}
	endpoint, region, err := cliLocation(query.Member.Endpoint)
	if err != nil {
		return domain.TraceBackendResult{}, err
	}
	err = b.runJSON(ctx, "trace-search", []string{
		"sls", "get-logs-v2",
		"--project", query.Member.Project,
		"--logstore", query.Member.LogStore,
		"--from", strconv.FormatInt(query.Spec.StartTime.Unix(), 10),
		"--to", strconv.FormatInt(query.Spec.EndTime.Unix(), 10),
		"--query", expression,
		"--line", strconv.Itoa(query.MemberLimit),
		"--is-accurate", "true",
		"--region", region,
		"--endpoint", endpoint,
		"--log-level", "ERROR",
	}, &payload)
	if err != nil {
		return domain.TraceBackendResult{}, err
	}
	if len(payload.Logs) > 0 && len(payload.Data) > 0 {
		return domain.TraceBackendResult{}, errors.New("SLS CLI returned both logs and data rows")
	}
	rows := payload.Data
	if len(rows) == 0 {
		rows = payload.Logs
	}
	result := domain.TraceBackendResult{
		ProviderRequestID: safeProviderID(payload.RequestID),
		Progress:          firstString(payload.Meta, "progress"),
	}
	if result.ProviderRequestID == "" {
		result.ProviderRequestID = safeProviderID(payload.ProviderRequestID)
	}
	var rowsOK, bytesOK, elapsedOK bool
	result.ProcessedRows, rowsOK = firstInt64(payload.Meta, "processedRows", "processed_rows")
	result.ProcessedBytes, bytesOK = firstInt64(payload.Meta, "processedBytes", "processed_bytes")
	result.ElapsedMillisecond, elapsedOK = firstInt64(payload.Meta, "elapsedMillisecond", "elapsed_millisecond")
	result.UsageKnown = rowsOK && bytesOK && elapsedOK
	result.NanosecondOrdered, result.NanosecondOrderKnown = firstBool(payload.Meta, "isAccurate", "is_accurate")
	result.Events = make([]domain.TraceBackendEvent, 0, len(rows))
	for index, row := range rows {
		event, mapErr := mapTraceRow(row, query.Member)
		if mapErr != nil {
			return result, fmt.Errorf("SLS CLI Trace row %d contains an invalid configured field", index)
		}
		result.Events = append(result.Events, event)
	}
	return result, nil
}

func compileTraceExpression(environment, traceID string, member domain.TraceResourceMember) (string, error) {
	if !traceSafeValue(traceID, 8, 256) || !traceSafeValue(environment, 1, 64) {
		return "", errors.New("Trace query contains an unsafe value")
	}
	var traceExpression string
	switch member.TraceMode {
	case domain.TraceQueryField:
		traceExpression = member.TraceField + `: "` + traceID + `"`
	case domain.TraceQueryFullText:
		traceExpression = `#"` + traceID + `"`
	default:
		return "", errors.New("Trace query mode is unsupported")
	}
	var environmentExpression string
	switch member.EnvironmentMode {
	case domain.TraceEnvironmentField:
		environmentExpression = member.EnvironmentField + `: "` + environment + `"`
	case domain.TraceEnvironmentFullText:
		environmentExpression = `#"` + environment + `"`
	default:
		return "", errors.New("Trace environment query mode is unsupported")
	}
	return traceExpression + " and " + environmentExpression, nil
}

func validateApprovedTraceQuery(query domain.ApprovedTraceQuery) error {
	if query.Spec.InvestigationID == "" || !query.Spec.Requester.Complete() || query.GroupID == "" ||
		query.GovernanceFingerprint == "" || query.TraceIDFingerprint == "" || query.Member.ID == "" ||
		query.MemberLimit <= 0 || query.MemberLimit > domain.TraceDefaultMemberLimit ||
		query.RetryIncomplete < 0 || query.RetryIncomplete > 1 || !query.Spec.StartTime.Before(query.Spec.EndTime) {
		return errors.New("approved Trace query is invalid")
	}
	if err := validateResourceLocation(traceLogResource(query.Member)); err != nil {
		return err
	}
	return nil
}

func mapTraceRow(row map[string]json.RawMessage, member domain.TraceResourceMember) (domain.TraceBackendEvent, error) {
	read := func(field string) (string, error) {
		if field == "" {
			return "", nil
		}
		value, exists := row[field]
		if !exists {
			return "", nil
		}
		return scalarString(value)
	}
	eventTime, err := read(member.EventTimeField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	receiveTime, err := read(member.ReceiveTimeField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	nanoseconds, err := read(member.NanosecondTimeField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	level, err := read(member.LevelField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	operation, err := read(member.OperationField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	message, err := read(member.MessageField)
	if err != nil {
		return domain.TraceBackendEvent{}, err
	}
	if strings.TrimSpace(message) == "" {
		message = "[message unavailable]"
	}
	return domain.TraceBackendEvent{
		EventTimeRaw: eventTime, ReceiveTimeRaw: receiveTime, NanosecondRaw: nanoseconds,
		Level: level, Operation: operation, Message: message,
	}, nil
}

func traceSafeValue(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func traceLogResource(member domain.TraceResourceMember) domain.LogResource {
	return domain.LogResource{ID: member.ID, Endpoint: member.Endpoint, Project: member.Project, LogStore: member.LogStore}
}
