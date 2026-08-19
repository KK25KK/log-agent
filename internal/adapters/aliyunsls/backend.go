// Package aliyunsls is the only package allowed to import the Alibaba SLS SDK.
package aliyunsls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
	"logagent/internal/ports"
)

const userAgent = "logagent-query-foundation/0.2"

type Config struct {
	CredentialMode  string
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string
	ECSRAMRoleName  string
	RequestTimeout  time.Duration
}

type Backend struct {
	newReader readerFactory
	now       func() time.Time
}

type reader interface {
	CheckProjectExist(name string) (bool, error)
	GetLogStore(project, logstore string) (*sls.LogStore, error)
	GetIndex(project, logstore string) (*sls.Index, error)
	GetLogsV2(project, logstore string, request *sls.GetLogRequest) (*sls.GetLogsResponse, error)
}

type readerFactory func(endpoint string) reader

func init() {
	// The application owns retry and cost semantics. SDK retries use an internal
	// background context and hide physical attempts, so they are disabled before
	// any client can be created. The architecture test ensures this is the only
	// package importing the SDK.
	sls.RetryOnServerErrorEnabled = false
	sls.GlobalDebugLevel = 0
}

func New(config Config) (*Backend, error) {
	if config.RequestTimeout <= 0 {
		return nil, errors.New("SLS request timeout must be positive")
	}

	var provider sls.CredentialsProvider
	switch config.CredentialMode {
	case "static":
		if config.AccessKeyID == "" || config.AccessKeySecret == "" {
			return nil, errors.New("static SLS credentials require AccessKey ID and secret")
		}
		provider = sls.NewStaticCredentialsProvider(config.AccessKeyID, config.AccessKeySecret, config.SecurityToken)
	case "ecs_ram_role":
		var err error
		provider, err = newECSRAMRoleProvider(config.ECSRAMRoleName, config.RequestTimeout)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported SLS credential mode %q", config.CredentialMode)
	}

	return &Backend{
		newReader: func(endpoint string) reader {
			client := sls.CreateNormalInterfaceV2(endpoint, provider)
			client.SetHTTPClient(&http.Client{Timeout: config.RequestTimeout})
			// GET metadata operations retry transport errors even when server-error
			// retries are disabled globally. Bound that SDK-owned background retry
			// context to the same configured request budget instead of its 90s default.
			client.SetRetryTimeout(config.RequestTimeout)
			client.SetUserAgent(userAgent)
			return client
		},
		now: time.Now,
	}, nil
}

func (b *Backend) GetSchema(ctx context.Context, resource domain.LogResource) (domain.IndexSchema, error) {
	if err := validateResourceLocation(resource); err != nil {
		return domain.IndexSchema{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.IndexSchema{}, err
	}
	client := b.newReader(resource.Endpoint)
	logstore, err := client.GetLogStore(resource.Project, resource.LogStore)
	if err != nil {
		safeErr, _ := safeProviderError("get-logstore", err)
		return domain.IndexSchema{}, fmt.Errorf("get LogStore for resource %q: %w", resource.ID, safeErr)
	}
	if err := validateLogStore(resource, logstore); err != nil {
		return domain.IndexSchema{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.IndexSchema{}, err
	}
	index, err := client.GetIndex(resource.Project, resource.LogStore)
	if err != nil {
		safeErr, _ := safeProviderError("get-index", err)
		return domain.IndexSchema{}, fmt.Errorf("get index for resource %q: %w", resource.ID, safeErr)
	}
	return b.mapSchema(index)
}

func (b *Backend) Execute(ctx context.Context, query domain.ApprovedQuery) (domain.QueryResult, error) {
	if err := validateApprovedQuery(query); err != nil {
		return domain.QueryResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.QueryResult{}, err
	}

	countQuery, patternQuery, instanceQuery, err := compileQueries(query.Resource)
	if err != nil {
		return domain.QueryResult{}, err
	}
	client := b.newReader(query.Resource.Endpoint)
	expressions := []struct {
		operation  string
		expression string
	}{
		{operation: "error-count-before", expression: countQuery},
		{operation: "error-patterns", expression: patternQuery},
		{operation: "error-instances", expression: instanceQuery},
		{operation: "error-count-after", expression: countQuery},
	}
	responses := make([]*sls.GetLogsResponse, 0, len(expressions))
	for index, item := range expressions {
		response, callErr := client.GetLogsV2(
			query.Resource.Project,
			query.Resource.LogStore,
			newRequest(query, item.expression),
		)
		if callErr != nil {
			safeErr, providerRequestID := safeProviderError(item.operation, callErr)
			partial, _ := combineResponses(responses...)
			partial.APICalls = index + 1
			partial.QueryID = joinRequestIDs(partial.QueryID, providerRequestID)
			return partial, safeErr
		}
		responses = append(responses, response)
		partial, combineErr := combineResponses(responses...)
		if combineErr != nil {
			return partial, combineErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return partial, ctxErr
		}
	}

	result, err := mapAnalysis(responses[0], responses[1], responses[2], responses[3])
	// GetLogsV2 does not accept a caller context. If the application deadline
	// elapsed while an SDK call was in flight, preserve the observation for
	// audit but never return it as a successful result.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

type ResourceCheck struct {
	ResourceID        string `json:"resource_id"`
	Project           string `json:"project"`
	LogStore          string `json:"logstore"`
	SchemaFingerprint string `json:"schema_fingerprint"`
	IndexedFields     int    `json:"indexed_fields"`
	LogStoreMode      string `json:"logstore_mode"`
}

// CheckResources verifies only resource metadata. It does not query log rows.
func (b *Backend) CheckResources(ctx context.Context, resources []domain.LogResource) ([]ResourceCheck, error) {
	checks := make([]ResourceCheck, 0, len(resources))
	for _, resource := range resources {
		check, err := b.checkResource(ctx, resource)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (b *Backend) checkResource(ctx context.Context, resource domain.LogResource) (ResourceCheck, error) {
	if err := validateResourceLocation(resource); err != nil {
		return ResourceCheck{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResourceCheck{}, err
	}
	client := b.newReader(resource.Endpoint)
	exists, err := client.CheckProjectExist(resource.Project)
	if err != nil {
		safeErr, _ := safeProviderError("check-project", err)
		return ResourceCheck{}, fmt.Errorf("check project for resource %q: %w", resource.ID, safeErr)
	}
	if !exists {
		return ResourceCheck{}, fmt.Errorf("configured project for resource %q is not accessible", resource.ID)
	}
	if err := ctx.Err(); err != nil {
		return ResourceCheck{}, err
	}
	logstore, err := client.GetLogStore(resource.Project, resource.LogStore)
	if err != nil {
		safeErr, _ := safeProviderError("get-logstore", err)
		return ResourceCheck{}, fmt.Errorf("get LogStore for resource %q: %w", resource.ID, safeErr)
	}
	if err := validateLogStore(resource, logstore); err != nil {
		return ResourceCheck{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResourceCheck{}, err
	}
	index, err := client.GetIndex(resource.Project, resource.LogStore)
	if err != nil {
		safeErr, _ := safeProviderError("get-index", err)
		return ResourceCheck{}, fmt.Errorf("get index for resource %q: %w", resource.ID, safeErr)
	}
	schema, err := b.mapSchema(index)
	if err != nil {
		return ResourceCheck{}, err
	}
	if err := validateResourceSchema(resource, schema); err != nil {
		return ResourceCheck{}, err
	}
	mode := logstore.Mode
	if mode == "" {
		mode = "standard"
	}
	return ResourceCheck{
		ResourceID: resource.ID, Project: resource.Project, LogStore: resource.LogStore,
		SchemaFingerprint: schema.Fingerprint, IndexedFields: len(schema.Fields), LogStoreMode: mode,
	}, nil
}

func (b *Backend) mapSchema(index *sls.Index) (domain.IndexSchema, error) {
	if index == nil {
		return domain.IndexSchema{}, errors.New("SLS returned an empty index")
	}
	fields := make(map[string]domain.IndexField, len(index.Keys))
	for name, key := range index.Keys {
		fields[name] = domain.IndexField{Type: strings.ToLower(key.Type), DocValue: key.DocValue}
	}
	fingerprintValue, err := fingerprint.JSON(fields)
	if err != nil {
		return domain.IndexSchema{}, fmt.Errorf("fingerprint SLS index: %w", err)
	}
	return domain.IndexSchema{Fingerprint: fingerprintValue, Fields: fields, FetchedAt: b.now().UTC()}, nil
}

func newRequest(query domain.ApprovedQuery, expression string) *sls.GetLogRequest {
	nanosecondOrdered := true
	return &sls.GetLogRequest{
		From: query.StartTime.Unix(),
		To:   query.EndTime.Unix(),
		// SLS ignores line/offset for SQL analysis. The fixed SQL templates own
		// their limits explicitly.
		Lines:      0,
		Query:      expression,
		IsAccurate: &nanosecondOrdered,
	}
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
	selectors := make([]domain.LogSelector, 0, len(resource.Selectors)+1)
	selectors = append(selectors, resource.Selectors...)
	selectors = append(selectors, resource.ErrorSelector)
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
	patternQuery := fmt.Sprintf(
		"%s | SELECT %s AS bucket_key, count(*) AS bucket_count GROUP BY %s ORDER BY bucket_count DESC, bucket_key ASC LIMIT %d",
		filter, patternExpression, patternExpression, domain.ErrorAnalysisPatternLimit,
	)
	instanceExpression := bucketExpression(resource.InstanceField)
	instanceQuery := fmt.Sprintf(
		"%s | SELECT %s AS bucket_key, count(*) AS bucket_count GROUP BY %s ORDER BY bucket_count DESC, bucket_key ASC LIMIT %d",
		filter, instanceExpression, instanceExpression, domain.ErrorAnalysisInstanceLimit,
	)
	return countQuery, patternQuery, instanceQuery, nil
}

func bucketExpression(field string) string {
	return `COALESCE(NULLIF("` + field + `", ''), '[missing]')`
}

func escapeQueryValue(value string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`*`, `\*`,
		`?`, `\?`,
	).Replace(value)
}

func mapAnalysis(countBeforeResponse, patternResponse, instanceResponse, countAfterResponse *sls.GetLogsResponse) (domain.QueryResult, error) {
	result, err := combineResponses(countBeforeResponse, patternResponse, instanceResponse, countAfterResponse)
	if err != nil {
		return result, err
	}
	if requestID(countBeforeResponse) == "" || requestID(patternResponse) == "" || requestID(instanceResponse) == "" || requestID(countAfterResponse) == "" {
		return result, errors.New("SLS response is missing provider request ID")
	}
	if len(countBeforeResponse.Logs) != 1 {
		return result, fmt.Errorf("initial error-count query returned %d rows, want 1", len(countBeforeResponse.Logs))
	}
	if len(countAfterResponse.Logs) != 1 {
		return result, fmt.Errorf("verification error-count query returned %d rows, want 1", len(countAfterResponse.Logs))
	}
	countBefore, err := parseCount(countBeforeResponse.Logs[0], "error_count")
	if err != nil {
		return result, fmt.Errorf("parse initial error-count response: %w", err)
	}
	countAfter, err := parseCount(countAfterResponse.Logs[0], "error_count")
	if err != nil {
		return result, fmt.Errorf("parse verification error-count response: %w", err)
	}
	patterns, patternTotal, err := parseBuckets(patternResponse.Logs, domain.ErrorAnalysisPatternLimit)
	if err != nil {
		return result, fmt.Errorf("parse error-pattern response: %w", err)
	}
	instances, instanceTotal, err := parseBuckets(instanceResponse.Logs, domain.ErrorAnalysisInstanceLimit)
	if err != nil {
		return result, fmt.Errorf("parse instance-distribution response: %w", err)
	}
	errorCount := countAfter
	if countBefore > errorCount {
		errorCount = countBefore
	}
	if patternTotal > errorCount {
		return result, errors.New("error-pattern counts exceed total error count")
	}
	if instanceTotal > errorCount {
		return result, errors.New("instance counts exceed total error count")
	}
	if errorCount > 0 && (len(patterns) == 0 || len(instances) == 0) {
		return result, errors.New("non-zero error count requires pattern and instance buckets")
	}

	result.ErrorCount = errorCount
	result.ErrorPatterns = patterns
	result.Instances = instances
	stableSnapshot := countBefore == countAfter
	result.ErrorPatternsExhaustive = stableSnapshot && patternTotal == errorCount
	result.InstancesExhaustive = stableSnapshot && instanceTotal == errorCount
	result.PatternLimit = domain.ErrorAnalysisPatternLimit
	result.InstanceLimit = domain.ErrorAnalysisInstanceLimit
	if len(patterns) > 0 {
		result.TopError = patterns[0].Label
		result.TopErrorCount = patterns[0].Count
	}
	if !stableSnapshot {
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

func combineResponses(responses ...*sls.GetLogsResponse) (domain.QueryResult, error) {
	if len(responses) == 0 {
		return domain.QueryResult{}, nil
	}
	result := domain.QueryResult{
		Progress:               "Complete",
		NanosecondOrderedKnown: true,
		NanosecondOrdered:      true,
		UsageKnown:             true,
		APICalls:               len(responses),
	}
	requestIDs := make([]string, 0, len(responses))
	for _, response := range responses {
		if response == nil {
			return result, errors.New("SLS returned an empty query response")
		}
		summary := summarizeResponse(response)
		requestIDs = append(requestIDs, summary.QueryID)
		if !response.IsComplete() {
			result.Progress = "Incomplete"
		}
		result.NanosecondOrderedKnown = result.NanosecondOrderedKnown && summary.NanosecondOrderedKnown
		result.NanosecondOrdered = result.NanosecondOrdered && summary.NanosecondOrdered
		result.UsageKnown = result.UsageKnown && summary.UsageKnown
		var err error
		result.ProcessedRows, err = addNonNegative(result.ProcessedRows, summary.ProcessedRows)
		if err != nil {
			return result, fmt.Errorf("sum processed rows: %w", err)
		}
		result.ProcessedBytes, err = addNonNegative(result.ProcessedBytes, summary.ProcessedBytes)
		if err != nil {
			return result, fmt.Errorf("sum processed bytes: %w", err)
		}
		result.ElapsedMillisecond, err = addNonNegative(result.ElapsedMillisecond, summary.ElapsedMillisecond)
		if err != nil {
			return result, fmt.Errorf("sum elapsed milliseconds: %w", err)
		}
	}
	result.QueryID = joinRequestIDs(requestIDs...)
	return result, nil
}

func addNonNegative(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("non-negative int64 overflow")
	}
	return left + right, nil
}

func summarizeResponse(response *sls.GetLogsResponse) domain.QueryResult {
	if response == nil {
		return domain.QueryResult{}
	}
	processedRows, rowsOK := headerInt64(response.Header, sls.ProcessedRows)
	processedBytes, bytesOK := headerInt64(response.Header, sls.ProcessedBytes)
	elapsed, elapsedOK := headerInt64(response.Header, sls.ElapsedMillisecond)
	nanosecondOrderedKnown, nanosecondOrdered := nanosecondOrderInfo(response.Contents)
	return domain.QueryResult{
		QueryID:                requestID(response),
		Progress:               response.Progress,
		NanosecondOrderedKnown: nanosecondOrderedKnown,
		NanosecondOrdered:      nanosecondOrdered,
		UsageKnown:             rowsOK && bytesOK && elapsedOK,
		ProcessedRows:          processedRows,
		ProcessedBytes:         processedBytes,
		ElapsedMillisecond:     elapsed,
	}
}

func nanosecondOrderInfo(contents string) (known, ordered bool) {
	if contents == "" {
		return false, false
	}
	var info struct {
		IsAccurate *int64 `json:"isAccurate"`
	}
	if err := json.Unmarshal([]byte(contents), &info); err != nil {
		return false, false
	}
	return info.IsAccurate != nil, info.IsAccurate != nil && *info.IsAccurate == 1
}

func parseCount(row map[string]string, field string) (int64, error) {
	raw, exists := row[field]
	if !exists {
		return 0, fmt.Errorf("field %q is missing", field)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("field %q is not a non-negative integer", field)
	}
	return value, nil
}

func headerInt64(header http.Header, name string) (int64, bool) {
	if header == nil {
		return 0, false
	}
	raw := header.Get(name)
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= 0
}

func requestID(response *sls.GetLogsResponse) string {
	if response == nil || response.Header == nil {
		return ""
	}
	return response.Header.Get(sls.RequestIDHeader)
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
	errorField, exists := schema.Fields[resource.ErrorField]
	if !exists || errorField.Type != "text" || !errorField.DocValue {
		return fmt.Errorf("resource %q error field %q must be indexed text with statistics enabled", resource.ID, resource.ErrorField)
	}
	instanceField, exists := schema.Fields[resource.InstanceField]
	if !exists || instanceField.Type != "text" || !instanceField.DocValue {
		return fmt.Errorf("resource %q instance field %q must be indexed text with statistics enabled", resource.ID, resource.InstanceField)
	}
	return nil
}

func validateLogStore(resource domain.LogResource, logstore *sls.LogStore) error {
	if logstore == nil || logstore.Name != resource.LogStore {
		return fmt.Errorf("configured LogStore for resource %q was not returned", resource.ID)
	}
	if logstore.Mode != "" && !strings.EqualFold(logstore.Mode, "standard") {
		return fmt.Errorf("LogStore for resource %q must use standard mode for SQL analysis", resource.ID)
	}
	return nil
}

func joinRequestIDs(values ...string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return strings.Join(result, ",")
}

var _ ports.SLSBackend = (*Backend)(nil)
