package aliyuncli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const defaultMaxOutputBytes = 4 * 1024 * 1024

type commandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type processRunner struct {
	executable string
	profile    string
	maxOutput  int64
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - int64(b.buffer.Len())
	if remaining > 0 {
		if int64(len(value)) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buffer.Write(value)
	}
	if int64(original) > remaining {
		b.overflow = true
	}
	return original, nil
}

func (r *processRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, r.executable, args...)
	command.Env = cliEnvironment(os.Environ(), r.profile)
	stdout := &boundedBuffer{limit: r.maxOutput}
	stderr := &boundedBuffer{limit: 64 * 1024}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("aliyun CLI output exceeds configured limit")
	}
	if err != nil {
		return nil, sanitizeCommandError(args, stderr.buffer.String(), err)
	}
	return stdout.buffer.Bytes(), nil
}

var blockedCLIEnvironment = map[string]struct{}{
	"ACCESS_KEY_ID":                                    {},
	"ACCESS_KEY_SECRET":                                {},
	"SECURITY_TOKEN":                                   {},
	"ALIBABA_CLOUD_ACCESS_KEY_ID":                      {},
	"ALIBABA_CLOUD_ACCESS_KEY_SECRET":                  {},
	"ALIBABA_CLOUD_SECURITY_TOKEN":                     {},
	"ALIBABACLOUD_ACCESS_KEY_ID":                       {},
	"ALIBABACLOUD_ACCESS_KEY_SECRET":                   {},
	"ALIBABACLOUD_SECURITY_TOKEN":                      {},
	"ALICLOUD_ACCESS_KEY_ID":                           {},
	"ALICLOUD_ACCESS_KEY_SECRET":                       {},
	"ALICLOUD_SECURITY_TOKEN":                          {},
	"ALIBABA_CLOUD_BEARER_TOKEN":                       {},
	"ALIBABA_CLOUD_BEARER_TOKEN_HEADER_KEY":            {},
	"ALIBABA_CLOUD_CONNECT_TIMEOUT":                    {},
	"ALIBABA_CLOUD_CREDENTIALS_URI":                    {},
	"ALIBABA_CLOUD_ENDPOINT":                           {},
	"ALIBABA_CLOUD_ENDPOINT_TYPE":                      {},
	"ALIBABA_CLOUD_IGNORE_PROFILE":                     {},
	"ALIBABA_CLOUD_OIDC_TOKEN_FILE":                    {},
	"ALIBABA_CLOUD_PROFILE":                            {},
	"ALIBABA_CLOUD_PROFILE_MODE":                       {},
	"ALIBABA_CLOUD_READ_TIMEOUT":                       {},
	"ALIBABA_CLOUD_REGION_ID":                          {},
	"ALIBABA_CLOUD_RETRY_COUNT":                        {},
	"ALIBABA_CLOUD_STS_ENDPOINT":                       {},
	"ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL":            {},
	"ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL_ENABLE_PRE": {},
	"ALIBABACLOUD_OIDC_TOKEN_FILE":                     {},
	"ALIBABACLOUD_REGION_ID":                           {},
	"ALICLOUD_REGION_ID":                               {},
	"DEBUG":                                            {},
	"REGION":                                           {},
	"REGION_ID":                                        {},
}

func cliEnvironment(source []string, profile string) []string {
	result := make([]string, 0, len(source)+2)
	for _, item := range source {
		name := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			name = item[:index]
		}
		if _, blocked := blockedCLIEnvironment[strings.ToUpper(name)]; blocked {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"ALIBABA_CLOUD_PROFILE="+profile,
		"ALIBABA_CLOUD_CLI_PLUGIN_AUTO_INSTALL=false",
	)
}

var safeCLICode = regexp.MustCompile(`(?i)\b(TokenExpired|InvalidSecurityToken|Forbidden|Unauthorized|AccessDenied|InvalidAccessKeyId|LogStoreNotExist|ProjectNotExist|InvalidQuery|ServerBusy|Throttling|ProfileNotFound|InvalidParameter)\b`)

func sanitizeCommandError(args []string, stderr string, source error) error {
	operation := "unknown"
	if len(args) >= 2 {
		operation = args[1]
	}
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(source, &exitError) {
		exitCode = exitError.ExitCode()
	}
	code := "CommandFailed"
	if match := safeCLICode.FindString(stderr); match != "" {
		code = match
	}
	return fmt.Errorf("aliyun CLI %s failed code=%s exit=%s", operation, code, strconv.Itoa(exitCode))
}
