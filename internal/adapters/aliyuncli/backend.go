// Package aliyuncli is the only package allowed to invoke Alibaba Cloud CLI.
package aliyuncli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ids"
	"logagent/internal/ports"
)

type Config struct {
	CLIPath        string
	Profile        string
	RequestTimeout time.Duration
	MaxOutputBytes int64
}

type Backend struct {
	runner         commandRunner
	requestTimeout time.Duration
	now            func() time.Time
	newExecutionID func() (string, error)
}

var safeProfileName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func New(config Config) (*Backend, error) {
	if config.RequestTimeout <= 0 {
		return nil, errors.New("SLS CLI request timeout must be positive")
	}
	if !safeProfileName.MatchString(config.Profile) {
		return nil, errors.New("SLS CLI Profile must contain 1-64 safe characters")
	}
	path := strings.TrimSpace(config.CLIPath)
	if path == "" {
		var err error
		path, err = exec.LookPath("aliyun")
		if err != nil {
			return nil, errors.New("locate aliyun CLI executable")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("resolve aliyun CLI executable")
	}
	if resolved, err := exec.LookPath(absolute); err != nil || resolved == "" {
		return nil, errors.New("aliyun CLI executable is not available")
	}
	maxOutput := config.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = defaultMaxOutputBytes
	}
	if maxOutput < 64*1024 || maxOutput > 16*1024*1024 {
		return nil, errors.New("SLS CLI output limit must be between 64 KiB and 16 MiB")
	}
	return &Backend{
		runner:         &processRunner{executable: absolute, profile: config.Profile, maxOutput: maxOutput},
		requestTimeout: config.RequestTimeout,
		now:            time.Now,
		newExecutionID: func() (string, error) { return ids.New("slscli") },
	}, nil
}

func (b *Backend) GetSchema(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	if err := validateResourceLocation(resource); err != nil {
		return domain.IndexSchema{}, err
	}
	if _, err := b.getLogStore(ctx, resource); err != nil {
		return domain.IndexSchema{}, err
	}
	return b.getIndex(ctx, resource)
}

func (b *Backend) Execute(ctx context.Context, query domain.ApprovedQuery) (domain.QueryResult, error) {
	if err := validateApprovedQuery(query); err != nil {
		return domain.QueryResult{}, err
	}
	countQuery, patternQuery, instanceQuery, err := compileQueries(query.Resource)
	if err != nil {
		return domain.QueryResult{}, err
	}
	expressions := []struct {
		operation  string
		expression string
	}{
		{operation: "error-count-before", expression: countQuery},
		{operation: "error-patterns", expression: patternQuery},
		{operation: "error-instances", expression: instanceQuery},
		{operation: "error-count-after", expression: countQuery},
	}
	responses := make([]queryResponse, 0, len(expressions))
	for _, item := range expressions {
		response, callErr := b.getLogs(ctx, query, item.operation, item.expression)
		responses = append(responses, response)
		partial, combineErr := combineResponses(responses...)
		if callErr != nil {
			return partial, callErr
		}
		if combineErr != nil {
			return partial, combineErr
		}
	}
	return mapAnalysis(responses[0], responses[1], responses[2], responses[3])
}

type ResourceCheck struct {
	ResourceID        string `json:"resource_id"`
	Project           string `json:"project"`
	LogStore          string `json:"logstore"`
	SchemaFingerprint string `json:"schema_fingerprint"`
	IndexedFields     int    `json:"indexed_fields"`
	LogStoreMode      string `json:"logstore_mode"`
}

func (b *Backend) CheckResources(ctx context.Context, resources []domain.LogResource) ([]ResourceCheck, error) {
	checks := make([]ResourceCheck, 0, len(resources))
	for _, resource := range resources {
		if err := validateResourceLocation(resource); err != nil {
			return nil, err
		}
		var project map[string]json.RawMessage
		if err := b.runJSON(ctx, "get-project", []string{
			"sls", "get-project", "--project", resource.Project,
			"--endpoint", resource.Endpoint, "--log-level", "ERROR",
		}, &project); err != nil {
			return nil, fmt.Errorf("check project for resource %q: %w", resource.ID, err)
		}
		if firstString(project, "projectName", "project_name", "name") != resource.Project {
			return nil, fmt.Errorf("configured project for resource %q was not returned", resource.ID)
		}
		logstore, err := b.getLogStore(ctx, resource)
		if err != nil {
			return nil, err
		}
		schema, err := b.getIndex(ctx, resource)
		if err != nil {
			return nil, err
		}
		if err := validateResourceSchema(resource, schema); err != nil {
			return nil, err
		}
		checks = append(checks, ResourceCheck{
			ResourceID: resource.ID, Project: resource.Project, LogStore: resource.LogStore,
			SchemaFingerprint: schema.Fingerprint, IndexedFields: len(schema.Fields), LogStoreMode: logstore.Mode,
		})
	}
	return checks, nil
}

type logStoreInfo struct {
	Name string
	Mode string
}

func (b *Backend) getLogStore(ctx context.Context, resource domain.LogResource) (logStoreInfo, error) {
	var payload map[string]json.RawMessage
	err := b.runJSON(ctx, "get-log-store", []string{
		"sls", "get-log-store", "--project", resource.Project, "--logstore", resource.LogStore,
		"--endpoint", resource.Endpoint, "--log-level", "ERROR",
	}, &payload)
	if err != nil {
		return logStoreInfo{}, fmt.Errorf("get LogStore for resource %q: %w", resource.ID, err)
	}
	name := firstString(payload, "logstoreName", "logstore_name", "name")
	if name != resource.LogStore {
		return logStoreInfo{}, fmt.Errorf("configured LogStore for resource %q was not returned", resource.ID)
	}
	mode := firstString(payload, "mode")
	if mode == "" {
		mode = "standard"
	}
	if !strings.EqualFold(mode, "standard") {
		return logStoreInfo{}, fmt.Errorf("LogStore for resource %q must use standard mode for SQL analysis", resource.ID)
	}
	return logStoreInfo{Name: resource.LogStore, Mode: strings.ToLower(mode)}, nil
}

func (b *Backend) getIndex(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	var payload struct {
		Keys map[string]map[string]json.RawMessage `json:"keys"`
	}
	err := b.runJSON(ctx, "get-index", []string{
		"sls", "get-index", "--project", resource.Project, "--logstore", resource.LogStore,
		"--endpoint", resource.Endpoint, "--log-level", "ERROR",
	}, &payload)
	if err != nil {
		return domain.IndexSchema{}, fmt.Errorf("get index for resource %q: %w", resource.ID, err)
	}
	if payload.Keys == nil {
		return domain.IndexSchema{}, errors.New("SLS CLI returned an empty index")
	}
	fields := make(map[string]domain.IndexField, len(payload.Keys))
	for name, item := range payload.Keys {
		fieldType := strings.ToLower(firstString(item, "type"))
		docValue, _ := firstBool(item, "doc_value", "docValue")
		fields[name] = domain.IndexField{Type: fieldType, DocValue: docValue}
	}
	fingerprintValue, err := fingerprint.JSON(fields)
	if err != nil {
		return domain.IndexSchema{}, fmt.Errorf("fingerprint SLS index: %w", err)
	}
	return domain.IndexSchema{Fingerprint: fingerprintValue, Fields: fields, FetchedAt: b.now().UTC()}, nil
}

type queryResponse struct {
	ExecutionID          string
	ProviderRequestID    string
	Logs                 []map[string]string
	Progress             string
	ProcessedRows        int64
	ProcessedBytes       int64
	ElapsedMillisecond   int64
	UsageKnown           bool
	NanosecondOrderKnown bool
	NanosecondOrdered    bool
}

func (b *Backend) getLogs(ctx context.Context, query domain.ApprovedQuery, operation, expression string) (queryResponse, error) {
	executionID, err := b.newExecutionID()
	if err != nil {
		return queryResponse{}, err
	}
	response := queryResponse{ExecutionID: executionID}
	var payload struct {
		Logs              []map[string]json.RawMessage `json:"logs"`
		Meta              map[string]json.RawMessage   `json:"meta"`
		RequestID         string                       `json:"requestId"`
		ProviderRequestID string                       `json:"request_id"`
	}
	err = b.runJSON(ctx, operation, []string{
		"sls", "get-logs-v2",
		"--project", query.Resource.Project,
		"--logstore", query.Resource.LogStore,
		"--from", strconv.FormatInt(query.StartTime.Unix(), 10),
		"--to", strconv.FormatInt(query.EndTime.Unix(), 10),
		"--query", expression,
		"--line", "0",
		"--is-accurate", "true",
		"--endpoint", query.Resource.Endpoint,
		"--log-level", "ERROR",
	}, &payload)
	if err != nil {
		return response, err
	}
	response.ProviderRequestID = safeProviderID(payload.RequestID)
	if response.ProviderRequestID == "" {
		response.ProviderRequestID = safeProviderID(payload.ProviderRequestID)
	}
	response.Progress = firstString(payload.Meta, "progress")
	var rowsOK, bytesOK, elapsedOK bool
	response.ProcessedRows, rowsOK = firstInt64(payload.Meta, "processedRows", "processed_rows")
	response.ProcessedBytes, bytesOK = firstInt64(payload.Meta, "processedBytes", "processed_bytes")
	response.ElapsedMillisecond, elapsedOK = firstInt64(payload.Meta, "elapsedMillisecond", "elapsed_millisecond")
	response.UsageKnown = rowsOK && bytesOK && elapsedOK
	response.NanosecondOrdered, response.NanosecondOrderKnown = firstBool(payload.Meta, "isAccurate", "is_accurate")
	response.Logs = make([]map[string]string, 0, len(payload.Logs))
	for rowIndex, rawRow := range payload.Logs {
		row := make(map[string]string, len(rawRow))
		for key, rawValue := range rawRow {
			value, err := scalarString(rawValue)
			if err != nil {
				return response, fmt.Errorf("SLS CLI query row %d contains a non-scalar value", rowIndex)
			}
			row[key] = value
		}
		response.Logs = append(response.Logs, row)
	}
	return response, nil
}

func (b *Backend) runJSON(ctx context.Context, operation string, args []string, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, b.requestTimeout)
	defer cancel()
	payload, err := b.runner.Run(callCtx, args...)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode aliyun CLI %s response", operation)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode aliyun CLI %s response: trailing data", operation)
	}
	return nil
}

var safeProviderToken = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func safeProviderID(value string) string {
	if safeProviderToken.MatchString(value) {
		return value
	}
	return ""
}

func firstString(values map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		raw, ok := values[name]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	return ""
}

func firstInt64(values map[string]json.RawMessage, names ...string) (int64, bool) {
	for _, name := range names {
		raw, ok := values[name]
		if !ok {
			continue
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			value, err := strconv.ParseInt(string(number), 10, 64)
			return value, err == nil && value >= 0
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			return value, err == nil && value >= 0
		}
		return 0, false
	}
	return 0, false
}

func firstBool(values map[string]json.RawMessage, names ...string) (bool, bool) {
	for _, name := range names {
		raw, ok := values[name]
		if !ok {
			continue
		}
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			return value, true
		}
		var number json.Number
		if json.Unmarshal(raw, &number) == nil {
			if number == "0" {
				return false, true
			}
			if number == "1" {
				return true, true
			}
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "0", "false":
				return false, true
			case "1", "true":
				return true, true
			}
		}
		return false, false
	}
	return false, false
}

func scalarString(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return string(number), nil
	}
	return "", errors.New("not a string or number")
}

var safeFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	safeProjectName  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	safeLogStoreName = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,61}[a-z0-9]$`)
)

func validateApprovedQuery(query domain.ApprovedQuery) error {
	if query.TemplateID != domain.ErrorAnalysisTemplateID {
		return fmt.Errorf("unsupported approved template %q", query.TemplateID)
	}
	if query.Resource.TemplateVersion != domain.ErrorAnalysisTemplateVersion {
		return fmt.Errorf("unsupported resource template version %q", query.Resource.TemplateVersion)
	}
	if err := validateResourceLocation(query.Resource); err != nil {
		return err
	}
	if !query.EndTime.After(query.StartTime) || query.MaxRows < int64(domain.ErrorAnalysisResultRows) || query.MaxAPICalls < domain.ErrorAnalysisAPICalls {
		return errors.New("approved query has invalid time or limit values")
	}
	if query.PatternLimit != domain.ErrorAnalysisPatternLimit || query.InstanceLimit != domain.ErrorAnalysisInstanceLimit || query.ExpectedAPICalls != domain.ErrorAnalysisAPICalls {
		return errors.New("approved query does not match the fixed analysis template")
	}
	return nil
}

func compileQueries(resource domain.LogResource) (string, string, string, error) {
	if !safeFieldName.MatchString(resource.ErrorField) {
		return "", "", "", fmt.Errorf("unsafe error field %q", resource.ErrorField)
	}
	if !safeFieldName.MatchString(resource.InstanceField) {
		return "", "", "", fmt.Errorf("unsafe instance field %q", resource.InstanceField)
	}
	selectors := append(append([]domain.LogSelector(nil), resource.Selectors...), resource.ErrorSelector)
	sort.Slice(selectors, func(i, j int) bool { return selectors[i].Field < selectors[j].Field })
	filters := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if !safeFieldName.MatchString(selector.Field) {
			return "", "", "", fmt.Errorf("unsafe selector field %q", selector.Field)
		}
		filters = append(filters, selector.Field+`:"`+escapeQueryValue(selector.Value)+`"`)
	}
	filter := "*"
	if len(filters) > 0 {
		filter = strings.Join(filters, " AND ")
	}
	countQuery := filter + " | SELECT count(*) AS error_count LIMIT 1"
	patternExpression := bucketExpression(resource.ErrorField)
	patternQuery := fmt.Sprintf("%s | SELECT %s AS bucket_key, count(*) AS bucket_count GROUP BY %s ORDER BY bucket_count DESC, bucket_key ASC LIMIT %d", filter, patternExpression, patternExpression, domain.ErrorAnalysisPatternLimit)
	instanceExpression := bucketExpression(resource.InstanceField)
	instanceQuery := fmt.Sprintf("%s | SELECT %s AS bucket_key, count(*) AS bucket_count GROUP BY %s ORDER BY bucket_count DESC, bucket_key ASC LIMIT %d", filter, instanceExpression, instanceExpression, domain.ErrorAnalysisInstanceLimit)
	return countQuery, patternQuery, instanceQuery, nil
}

func bucketExpression(field string) string {
	return `COALESCE(NULLIF("` + field + `", ''), '[missing]')`
}

func escapeQueryValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `*`, `\*`, `?`, `\?`).Replace(value)
}

func mapAnalysis(countBefore, patterns, instances, countAfter queryResponse) (domain.QueryResult, error) {
	result, err := combineResponses(countBefore, patterns, instances, countAfter)
	if err != nil {
		return result, err
	}
	if len(countBefore.Logs) != 1 || len(countAfter.Logs) != 1 {
		return result, errors.New("error-count query must return exactly one row")
	}
	before, err := parseCount(countBefore.Logs[0], "error_count")
	if err != nil {
		return result, fmt.Errorf("parse initial error-count response: %w", err)
	}
	after, err := parseCount(countAfter.Logs[0], "error_count")
	if err != nil {
		return result, fmt.Errorf("parse verification error-count response: %w", err)
	}
	errorPatterns, patternTotal, err := parseBuckets(patterns.Logs, domain.ErrorAnalysisPatternLimit)
	if err != nil {
		return result, fmt.Errorf("parse error-pattern response: %w", err)
	}
	instanceBuckets, instanceTotal, err := parseBuckets(instances.Logs, domain.ErrorAnalysisInstanceLimit)
	if err != nil {
		return result, fmt.Errorf("parse instance-distribution response: %w", err)
	}
	errorCount := max(before, after)
	if patternTotal > errorCount || instanceTotal > errorCount {
		return result, errors.New("aggregate bucket counts exceed total error count")
	}
	if errorCount > 0 && (len(errorPatterns) == 0 || len(instanceBuckets) == 0) {
		return result, errors.New("non-zero error count requires pattern and instance buckets")
	}
	result.ErrorCount = errorCount
	result.ErrorPatterns = errorPatterns
	result.Instances = instanceBuckets
	stable := before == after
	result.ErrorPatternsExhaustive = stable && patternTotal == errorCount
	result.InstancesExhaustive = stable && instanceTotal == errorCount
	result.PatternLimit = domain.ErrorAnalysisPatternLimit
	result.InstanceLimit = domain.ErrorAnalysisInstanceLimit
	if len(errorPatterns) > 0 {
		result.TopError = errorPatterns[0].Label
		result.TopErrorCount = errorPatterns[0].Count
	}
	if !stable {
		result.Progress = "Incomplete"
		result.IncompleteReason = "visible error count changed during aggregate queries"
	}
	return result, nil
}

func parseBuckets(rows []map[string]string, limit int) ([]domain.CountBucket, int64, error) {
	if len(rows) > limit {
		return nil, 0, fmt.Errorf("query returned %d rows, limit is %d", len(rows), limit)
	}
	buckets := make([]domain.CountBucket, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	var total int64
	for index, row := range rows {
		label, exists := row["bucket_key"]
		if !exists || strings.TrimSpace(label) == "" {
			return nil, 0, fmt.Errorf("row %d is missing bucket_key", index)
		}
		if _, duplicate := seen[label]; duplicate {
			return nil, 0, fmt.Errorf("row %d duplicates a previous bucket", index)
		}
		count, err := parseCount(row, "bucket_count")
		if err != nil || count == 0 {
			return nil, 0, fmt.Errorf("row %d has invalid bucket_count", index)
		}
		if total > math.MaxInt64-count {
			return nil, 0, errors.New("bucket counts overflow int64")
		}
		total += count
		seen[label] = struct{}{}
		buckets = append(buckets, domain.CountBucket{Label: label, Count: count})
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].Count == buckets[j].Count {
			return buckets[i].Label < buckets[j].Label
		}
		return buckets[i].Count > buckets[j].Count
	})
	return buckets, total, nil
}

func parseCount(row map[string]string, field string) (int64, error) {
	value, exists := row[field]
	if !exists {
		return 0, fmt.Errorf("field %q is missing", field)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("field %q is not a non-negative integer", field)
	}
	return parsed, nil
}

func combineResponses(responses ...queryResponse) (domain.QueryResult, error) {
	result := domain.QueryResult{Progress: "Complete", NanosecondOrderedKnown: true, NanosecondOrdered: true, UsageKnown: true, APICalls: len(responses)}
	executionIDs := make([]string, 0, len(responses))
	providerIDs := make([]string, 0, len(responses))
	for _, response := range responses {
		if response.ExecutionID == "" {
			return result, errors.New("SLS CLI execution ID is missing")
		}
		executionIDs = append(executionIDs, response.ExecutionID)
		if response.ProviderRequestID != "" {
			providerIDs = append(providerIDs, response.ProviderRequestID)
		}
		if !strings.EqualFold(response.Progress, "complete") {
			result.Progress = "Incomplete"
		}
		result.UsageKnown = result.UsageKnown && response.UsageKnown
		result.NanosecondOrderedKnown = result.NanosecondOrderedKnown && response.NanosecondOrderKnown
		result.NanosecondOrdered = result.NanosecondOrdered && response.NanosecondOrdered
		var err error
		result.ProcessedRows, err = addNonNegative(result.ProcessedRows, response.ProcessedRows)
		if err != nil {
			return result, err
		}
		result.ProcessedBytes, err = addNonNegative(result.ProcessedBytes, response.ProcessedBytes)
		if err != nil {
			return result, err
		}
		result.ElapsedMillisecond, err = addNonNegative(result.ElapsedMillisecond, response.ElapsedMillisecond)
		if err != nil {
			return result, err
		}
	}
	result.QueryID = strings.Join(executionIDs, ",")
	result.ProviderRequestID = strings.Join(providerIDs, ",")
	return result, nil
}

func addNonNegative(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("non-negative int64 overflow")
	}
	return left + right, nil
}

func validateResourceLocation(resource domain.LogResource) error {
	if resource.ID == "" || resource.Project == "" || resource.LogStore == "" {
		return errors.New("SLS resource identity is incomplete")
	}
	if !safeProjectName.MatchString(resource.Project) || !safeLogStoreName.MatchString(resource.LogStore) {
		return errors.New("SLS Project or LogStore name is invalid")
	}
	parsed, err := url.Parse(resource.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("SLS endpoint must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("SLS endpoint must contain only an HTTPS scheme and host")
	}
	return nil
}

func validateResourceSchema(resource domain.LogResource, schema domain.IndexSchema) error {
	selectors := append(append([]domain.LogSelector(nil), resource.Selectors...), resource.ErrorSelector)
	for _, selector := range selectors {
		if _, exists := schema.Fields[selector.Field]; !exists {
			return fmt.Errorf("resource %q selector field %q is not indexed", resource.ID, selector.Field)
		}
	}
	for name, label := range map[string]string{resource.ErrorField: "error", resource.InstanceField: "instance"} {
		field, exists := schema.Fields[name]
		if !exists || field.Type != "text" || !field.DocValue {
			return fmt.Errorf("resource %q %s field %q must be indexed text with statistics enabled", resource.ID, label, name)
		}
	}
	return nil
}

var _ ports.SLSBackend = (*Backend)(nil)
