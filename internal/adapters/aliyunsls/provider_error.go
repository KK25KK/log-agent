package aliyunsls

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sls "github.com/aliyun/aliyun-log-go-sdk"
)

var safeProviderToken = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// providerError deliberately excludes provider messages, response bodies,
// response headers, URLs, and query strings. Some SDK error implementations
// include all of those in Error(), so raw SDK errors must not cross the adapter.
type providerError struct {
	operation string
	httpCode  int
	code      string
	requestID string
	cause     error
}

func (e *providerError) Error() string {
	parts := []string{"SLS " + e.operation + " failed"}
	if e.code != "" {
		parts = append(parts, "code="+e.code)
	}
	if e.httpCode > 0 {
		parts = append(parts, fmt.Sprintf("http=%d", e.httpCode))
	}
	if e.requestID != "" {
		parts = append(parts, "request_id="+e.requestID)
	}
	return strings.Join(parts, " ")
}

func (e *providerError) Unwrap() error {
	return e.cause
}

func safeProviderError(operation string, source error) (error, string) {
	result := &providerError{operation: operation}
	if errors.Is(source, context.DeadlineExceeded) {
		result.code = "DeadlineExceeded"
		result.cause = context.DeadlineExceeded
		return result, ""
	}
	if errors.Is(source, context.Canceled) {
		result.code = "Canceled"
		result.cause = context.Canceled
		return result, ""
	}

	var serviceError *sls.Error
	if errors.As(source, &serviceError) {
		result.httpCode = int(serviceError.HTTPCode)
		result.code = safeErrorToken(serviceError.Code)
		result.requestID = safeErrorToken(serviceError.RequestID)
		return result, result.requestID
	}

	var badResponse *sls.BadResponseError
	if errors.As(source, &badResponse) {
		result.httpCode = badResponse.HTTPCode
		result.code = "BadResponse"
		result.requestID = safeErrorToken(http.Header(badResponse.RespHeader).Get(sls.RequestIDHeader))
		return result, result.requestID
	}

	result.code = "ProviderError"
	return result, ""
}

func safeErrorToken(value string) string {
	if safeProviderToken.MatchString(value) {
		return value
	}
	return ""
}
