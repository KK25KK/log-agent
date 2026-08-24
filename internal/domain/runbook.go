package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RunbookGuidanceVersion  = "governed-runbook-guidance-v1"
	MaxRunbookEntries       = 4
	MaxRunbookStepsPerEntry = 5

	MaxRunbookSourceVersionRunes = 64
	MaxRunbookIdentifierRunes    = 128
	MaxRunbookTitleRunes         = 120
	MaxRunbookOwnerRunes         = 80
	MaxRunbookInstructionRunes   = 240
)

const (
	RunbookReasonDisabled   = "runbook_source_disabled"
	RunbookReasonIncomplete = "runbook_source_incomplete"
	RunbookReasonTruncated  = "runbook_result_truncated"
)

var (
	runbookIdentifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	runbookFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runbookDomainPattern      = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,63}(?::\d+)?(?:\b|/)`)
	runbookIPv4Pattern        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?(?:\b|/)`)
	runbookLocalhostPattern   = regexp.MustCompile(`(?i)\blocalhost(?::\d+)?(?:\b|/)`)
	runbookSchemePattern      = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,31}:`)
	runbookOrderedListPattern = regexp.MustCompile(`^\s*\d+[.)]\s+`)
	runbookEmphasisPattern    = regexp.MustCompile(`(^|[[:space:]])_[^_\r\n]+_([[:space:]]|$|[。,.，])`)
	runbookShellWordPattern   = regexp.MustCompile(`(?i)\b(?:bash|sh|zsh|powershell|pwsh|cmd(?:\.exe)?|curl|wget|kubectl|helm|ansible|terraform|systemctl|sudo|rm|python\d*|node|perl|ruby|php|ssh|scp)\b`)
	runbookSQLWordPattern     = regexp.MustCompile(`(?i)\b(?:select|insert|update|delete|drop|truncate|alter|exec|execute)\b`)
	runbookDangerWordPattern  = regexp.MustCompile(`(?i)\b(?:restart|reboot|rollback|shutdown|start|stop|disable|enable|delete|remove|destroy|purge|kill|terminate|deploy|scale|execute|apply|write|modify|patch|drain|evict)\b`)
)

var runbookDangerousChineseWords = []string{
	"重启",
	"重啓",
	"重新启动",
	"重载",
	"回滚",
	"删除",
	"清空",
	"停机",
	"停止",
	"启动",
	"关闭服务",
	"开启服务",
	"禁用",
	"启用",
	"下线",
	"终止进程",
	"杀死",
	"销毁",
	"扩容",
	"缩容",
	"执行",
	"执行命令",
	"运行命令",
	"运行脚本",
	"调用接口",
	"发送请求",
	"修改配置",
	"更新配置",
	"调整配置",
	"改写配置",
	"写入配置",
	"发布变更",
	"部署",
	"切流",
	"摘除",
	"封禁",
}

type RunbookQuery struct {
	ResourceID          string   `json:"resource_id"`
	RecommendationCodes []string `json:"recommendation_codes"`
	Limit               int      `json:"limit"`
}

type RunbookSet struct {
	SourceVersion string         `json:"source_version,omitempty"`
	Entries       []RunbookEntry `json:"entries,omitempty"`
	Complete      bool           `json:"complete"`
	Truncated     bool           `json:"truncated"`
	ReasonCode    string         `json:"reason_code,omitempty"`
}

type RunbookEntry struct {
	ID                         string        `json:"id"`
	Revision                   string        `json:"revision"`
	ResourceID                 string        `json:"resource_id"`
	Title                      string        `json:"title"`
	OwnerTeam                  string        `json:"owner_team"`
	UpdatedAt                  time.Time     `json:"updated_at"`
	MatchedRecommendationCodes []string      `json:"matched_recommendation_codes"`
	Steps                      []RunbookStep `json:"steps"`
}

type RunbookStepKind string

const (
	RunbookStepVerify   RunbookStepKind = "VERIFY"
	RunbookStepObserve  RunbookStepKind = "OBSERVE"
	RunbookStepEscalate RunbookStepKind = "ESCALATE"
)

type RunbookStepCode string

const (
	RunbookStepCodeVerifyErrorPattern   RunbookStepCode = "VERIFY_ERROR_PATTERN"
	RunbookStepCodeObserveHotInstance   RunbookStepCode = "OBSERVE_HOT_INSTANCE"
	RunbookStepCodeEscalateServiceOwner RunbookStepCode = "ESCALATE_SERVICE_OWNER"
)

type RunbookStep struct {
	ID          string          `json:"id"`
	Code        RunbookStepCode `json:"code"`
	Kind        RunbookStepKind `json:"kind"`
	Instruction string          `json:"instruction"`
}

type RunbookGuidanceStatus string

const (
	RunbookGuidanceComplete         RunbookGuidanceStatus = "COMPLETE"
	RunbookGuidanceNoMatch          RunbookGuidanceStatus = "NO_MATCH"
	RunbookGuidanceInconclusive     RunbookGuidanceStatus = "INCONCLUSIVE"
	RunbookGuidanceUnavailable      RunbookGuidanceStatus = "UNAVAILABLE"
	RunbookGuidanceSkippedNoTrigger RunbookGuidanceStatus = "SKIPPED_NO_TRIGGER"
)

type RunbookGuidanceDataSource string

const (
	RunbookGuidanceSourceSyntheticMock      RunbookGuidanceDataSource = "SYNTHETIC_MOCK"
	RunbookGuidanceSourceEnterpriseGoverned RunbookGuidanceDataSource = "ENTERPRISE_GOVERNED"
)

type RunbookExecutionMode string

const RunbookExecutionHumanReviewOnly RunbookExecutionMode = "HUMAN_REVIEW_ONLY"

type RunbookGuidanceItem struct {
	EntryID             string               `json:"entry_id"`
	Revision            string               `json:"revision"`
	Fingerprint         string               `json:"fingerprint"`
	Title               string               `json:"title"`
	Owner               string               `json:"owner"`
	UpdatedAt           time.Time            `json:"updated_at"`
	RecommendationCodes []string             `json:"recommendation_codes"`
	EvidenceIDs         []string             `json:"evidence_ids"`
	Steps               []RunbookStep        `json:"steps"`
	ExecutionMode       RunbookExecutionMode `json:"execution_mode"`
}

type RunbookGuidance struct {
	Status          RunbookGuidanceStatus     `json:"status"`
	DataSource      RunbookGuidanceDataSource `json:"data_source"`
	MethodVersion   string                    `json:"method_version"`
	SourceVersion   string                    `json:"source_version,omitempty"`
	SourceComplete  bool                      `json:"source_complete"`
	SourceTruncated bool                      `json:"source_truncated"`
	Items           []RunbookGuidanceItem     `json:"items,omitempty"`
	MissingInputs   []string                  `json:"missing_inputs,omitempty"`
}

func ValidateRunbookGuidanceDataSource(source RunbookGuidanceDataSource) bool {
	switch source {
	case RunbookGuidanceSourceSyntheticMock, RunbookGuidanceSourceEnterpriseGoverned:
		return true
	default:
		return false
	}
}

// ValidateRunbookQuery validates the complete source lookup contract. The
// caller sorts and deduplicates recommendation codes before crossing the port.
func ValidateRunbookQuery(query RunbookQuery) error {
	if err := ValidateResourceID(query.ResourceID); err != nil {
		return fmt.Errorf("invalid runbook query: %w", err)
	}
	if query.Limit != MaxRunbookEntries {
		return fmt.Errorf("runbook query limit must equal %d", MaxRunbookEntries)
	}
	if err := validateRunbookCodes("runbook query", query.RecommendationCodes); err != nil {
		return err
	}
	return nil
}

func ValidateRunbookSourceVersion(version string) error {
	return validateRunbookIdentifier("runbook source version", version, MaxRunbookSourceVersionRunes)
}

// ValidateRunbookSet treats every adapter result as untrusted. Entries must be
// in stable identity order and have unique IDs so callers never depend on a
// provider's incidental result ordering.
func ValidateRunbookSet(set RunbookSet, query RunbookQuery) error {
	if err := ValidateRunbookQuery(query); err != nil {
		return err
	}
	if !ValidateRunbookReason(set.ReasonCode) {
		return fmt.Errorf("unsupported runbook reason %q", set.ReasonCode)
	}
	if set.SourceVersion == "" {
		if set.ReasonCode != RunbookReasonDisabled {
			return errors.New("runbook source version is required")
		}
	} else if err := ValidateRunbookSourceVersion(set.SourceVersion); err != nil {
		return err
	}
	if err := validateRunbookSetState(set); err != nil {
		return err
	}
	if len(set.Entries) > query.Limit || len(set.Entries) > MaxRunbookEntries {
		return fmt.Errorf("runbook set has more than %d entries", MaxRunbookEntries)
	}
	seen := make(map[string]struct{}, len(set.Entries))
	for index, entry := range set.Entries {
		if err := ValidateRunbookEntry(entry, query); err != nil {
			return fmt.Errorf("runbook entry %d is invalid: %w", index, err)
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return fmt.Errorf("runbook set contains duplicate entry ID %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if index > 0 && !RunbookEntryLess(set.Entries[index-1], entry) {
			return errors.New("runbook entries must be in stable order")
		}
	}
	return nil
}

func ValidateRunbookEntry(entry RunbookEntry, query RunbookQuery) error {
	if err := ValidateRunbookQuery(query); err != nil {
		return err
	}
	if err := validateRunbookEntryContent(entry); err != nil {
		return err
	}
	if entry.ResourceID != query.ResourceID {
		return errors.New("runbook entry resource does not match query")
	}
	allowed := make(map[string]struct{}, len(query.RecommendationCodes))
	for _, code := range query.RecommendationCodes {
		allowed[code] = struct{}{}
	}
	for _, code := range entry.MatchedRecommendationCodes {
		if _, ok := allowed[code]; !ok {
			return fmt.Errorf("runbook entry recommendation code %q is outside the query", code)
		}
	}
	return nil
}

func ValidateRunbookStep(step RunbookStep) error {
	if err := validateRunbookIdentifier("runbook step ID", step.ID, MaxRunbookIdentifierRunes); err != nil {
		return err
	}
	canonicalKind, canonicalInstruction, ok := CanonicalRunbookStep(step.Code)
	if !ok {
		return fmt.Errorf("unsupported runbook step code %q", step.Code)
	}
	if step.Kind != canonicalKind {
		return fmt.Errorf("runbook step code %q requires kind %q", step.Code, canonicalKind)
	}
	if step.Instruction != canonicalInstruction {
		return fmt.Errorf("runbook step code %q requires its canonical instruction", step.Code)
	}
	return validateRunbookSafeText("runbook step instruction", step.Instruction, MaxRunbookInstructionRunes)
}

// CanonicalRunbookStep returns the locally governed, immutable kind and
// instruction for a closed step code. Adapters may select a code, but cannot
// supply executable or provider-authored free text through a runbook step.
func CanonicalRunbookStep(code RunbookStepCode) (RunbookStepKind, string, bool) {
	switch code {
	case RunbookStepCodeVerifyErrorPattern:
		return RunbookStepVerify, "核对主要错误模式是否与告警现象一致。", true
	case RunbookStepCodeObserveHotInstance:
		return RunbookStepObserve, "观察高频实例的错误占比与延迟变化。", true
	case RunbookStepCodeEscalateServiceOwner:
		return RunbookStepEscalate, "联系对应服务值班负责人确认依赖健康状态。", true
	default:
		return "", "", false
	}
}

func ValidateRunbookIdentifier(value string) error {
	return validateRunbookIdentifier("runbook identifier", value, MaxRunbookIdentifierRunes)
}

func ValidateRunbookReason(reason string) bool {
	switch reason {
	case "", RunbookReasonDisabled, RunbookReasonIncomplete, RunbookReasonTruncated:
		return true
	default:
		return false
	}
}

func ValidateRunbookGuidanceItem(item RunbookGuidanceItem) error {
	if err := validateRunbookIdentifier("runbook guidance entry ID", item.EntryID, MaxRunbookIdentifierRunes); err != nil {
		return err
	}
	if err := validateRunbookIdentifier("runbook guidance revision", item.Revision, MaxRunbookIdentifierRunes); err != nil {
		return err
	}
	if !runbookFingerprintPattern.MatchString(item.Fingerprint) {
		return errors.New("runbook guidance fingerprint is invalid")
	}
	if err := validateRunbookSafeText("runbook guidance title", item.Title, MaxRunbookTitleRunes); err != nil {
		return err
	}
	if err := validateRunbookSafeText("runbook guidance owner", item.Owner, MaxRunbookOwnerRunes); err != nil {
		return err
	}
	if err := validateRunbookTime("runbook guidance update time", item.UpdatedAt); err != nil {
		return err
	}
	if err := validateRunbookCodes("runbook guidance", item.RecommendationCodes); err != nil {
		return err
	}
	if err := validateRunbookEvidenceIDs(item.EvidenceIDs); err != nil {
		return err
	}
	if err := validateRunbookSteps(item.Steps); err != nil {
		return err
	}
	if item.ExecutionMode != RunbookExecutionHumanReviewOnly {
		return errors.New("runbook guidance execution mode must be HUMAN_REVIEW_ONLY")
	}
	return nil
}

func RunbookEntryLess(left, right RunbookEntry) bool {
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	return left.Revision < right.Revision
}

func RunbookGuidanceItemLess(left, right RunbookGuidanceItem) bool {
	if left.EntryID != right.EntryID {
		return left.EntryID < right.EntryID
	}
	return left.Revision < right.Revision
}

// RunbookEntryFingerprint hashes only the normalized, provider-neutral entry
// content. The guidance method version is included to make future canonical
// form changes explicit rather than silently changing the meaning of a hash.
func RunbookEntryFingerprint(entry RunbookEntry) (string, error) {
	if err := validateRunbookEntryContent(entry); err != nil {
		return "", err
	}
	payload := struct {
		MethodVersion              string        `json:"method_version"`
		ID                         string        `json:"id"`
		Revision                   string        `json:"revision"`
		ResourceID                 string        `json:"resource_id"`
		Title                      string        `json:"title"`
		OwnerTeam                  string        `json:"owner_team"`
		UpdatedAt                  string        `json:"updated_at"`
		MatchedRecommendationCodes []string      `json:"matched_recommendation_codes"`
		Steps                      []RunbookStep `json:"steps"`
	}{
		MethodVersion:              RunbookGuidanceVersion,
		ID:                         entry.ID,
		Revision:                   entry.Revision,
		ResourceID:                 entry.ResourceID,
		Title:                      entry.Title,
		OwnerTeam:                  entry.OwnerTeam,
		UpdatedAt:                  entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
		MatchedRecommendationCodes: entry.MatchedRecommendationCodes,
		Steps:                      entry.Steps,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal runbook fingerprint payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateRunbookEntryContent(entry RunbookEntry) error {
	if err := validateRunbookIdentifier("runbook entry ID", entry.ID, MaxRunbookIdentifierRunes); err != nil {
		return err
	}
	if err := validateRunbookIdentifier("runbook entry revision", entry.Revision, MaxRunbookIdentifierRunes); err != nil {
		return err
	}
	if err := ValidateResourceID(entry.ResourceID); err != nil {
		return err
	}
	if err := validateRunbookSafeText("runbook title", entry.Title, MaxRunbookTitleRunes); err != nil {
		return err
	}
	if err := validateRunbookSafeText("runbook owner team", entry.OwnerTeam, MaxRunbookOwnerRunes); err != nil {
		return err
	}
	if err := validateRunbookTime("runbook update time", entry.UpdatedAt); err != nil {
		return err
	}
	if err := validateRunbookCodes("runbook entry", entry.MatchedRecommendationCodes); err != nil {
		return err
	}
	return validateRunbookSteps(entry.Steps)
}

func validateRunbookSetState(set RunbookSet) error {
	if set.Complete && set.Truncated {
		return errors.New("complete runbook set cannot be truncated")
	}
	if set.Complete && set.ReasonCode != "" {
		return errors.New("complete runbook set cannot have a reason")
	}
	if !set.Complete && set.ReasonCode == "" {
		return errors.New("incomplete runbook set requires a reason")
	}
	if set.Truncated && set.ReasonCode != RunbookReasonTruncated {
		return errors.New("truncated runbook set has invalid reason")
	}
	if !set.Truncated && !set.Complete && set.ReasonCode != RunbookReasonIncomplete && set.ReasonCode != RunbookReasonDisabled {
		return errors.New("incomplete runbook set has invalid reason")
	}
	if set.ReasonCode == RunbookReasonDisabled && len(set.Entries) != 0 {
		return errors.New("disabled runbook set cannot contain entries")
	}
	return nil
}

func validateRunbookCodes(name string, codes []string) error {
	if len(codes) == 0 || len(codes) > MaxRunbookEntries {
		return fmt.Errorf("%s recommendation codes must contain between 1 and %d items", name, MaxRunbookEntries)
	}
	if !sort.StringsAreSorted(codes) {
		return fmt.Errorf("%s recommendation codes must be in stable order", name)
	}
	for index, code := range codes {
		if err := validateRunbookIdentifier("recommendation code", code, MaxRunbookIdentifierRunes); err != nil {
			return err
		}
		if index > 0 && codes[index-1] == code {
			return fmt.Errorf("%s contains duplicate recommendation code %q", name, code)
		}
	}
	return nil
}

func validateRunbookEvidenceIDs(evidenceIDs []string) error {
	if len(evidenceIDs) == 0 {
		return errors.New("runbook guidance requires evidence IDs")
	}
	if !sort.StringsAreSorted(evidenceIDs) {
		return errors.New("runbook guidance evidence IDs must be in stable order")
	}
	for index, evidenceID := range evidenceIDs {
		if err := validateRunbookIdentifier("runbook guidance evidence ID", evidenceID, MaxRunbookIdentifierRunes); err != nil {
			return err
		}
		if index > 0 && evidenceIDs[index-1] == evidenceID {
			return fmt.Errorf("runbook guidance contains duplicate evidence ID %q", evidenceID)
		}
	}
	return nil
}

func validateRunbookSteps(steps []RunbookStep) error {
	if len(steps) == 0 || len(steps) > MaxRunbookStepsPerEntry {
		return fmt.Errorf("runbook steps must contain between 1 and %d items", MaxRunbookStepsPerEntry)
	}
	seen := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if err := ValidateRunbookStep(step); err != nil {
			return err
		}
		if _, duplicate := seen[step.ID]; duplicate {
			return fmt.Errorf("runbook contains duplicate step ID %q", step.ID)
		}
		seen[step.ID] = struct{}{}
	}
	return nil
}

func validateRunbookIdentifier(name, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and cannot have surrounding whitespace", name)
	}
	if utf8.RuneCountInString(value) > maxRunes || !runbookIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func validateRunbookSafeText(name, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and cannot have surrounding whitespace", name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d runes", name, maxRunes)
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
		return fmt.Errorf("%s contains control or format characters", name)
	}
	if containsRunbookURL(value) {
		return fmt.Errorf("%s contains a URL or URI", name)
	}
	if containsRunbookMarkdown(value) {
		return fmt.Errorf("%s contains Markdown", name)
	}
	if containsRunbookShellOrSQL(value) {
		return fmt.Errorf("%s contains a command or script", name)
	}
	if containsRunbookDangerousAction(value) {
		return fmt.Errorf("%s contains a dangerous execution action", name)
	}
	return nil
}

func containsRunbookURL(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"://", "www.", "mailto:", "tel:", "data:", "javascript:", "file:"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return runbookDomainPattern.MatchString(value) || runbookIPv4Pattern.MatchString(value) || runbookLocalhostPattern.MatchString(value) || runbookSchemePattern.MatchString(value) || strings.Contains(value, "::")
}

func containsRunbookMarkdown(value string) bool {
	trimmed := strings.TrimSpace(value)
	if strings.ContainsAny(value, "`[]*~") || runbookEmphasisPattern.MatchString(value) {
		return true
	}
	if runbookOrderedListPattern.MatchString(value) {
		return true
	}
	for _, prefix := range []string{"#", ">", "- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func validateRunbookTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	if _, err := value.MarshalJSON(); err != nil {
		return fmt.Errorf("%s is not JSON serializable: %w", name, err)
	}
	return nil
}

func containsRunbookShellOrSQL(value string) bool {
	for _, marker := range []string{"&&", "||", "$(", "${", ";", "|", ">", "<"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return runbookShellWordPattern.MatchString(value) || runbookSQLWordPattern.MatchString(value)
}

func containsRunbookDangerousAction(value string) bool {
	if runbookDangerWordPattern.MatchString(value) {
		return true
	}
	for _, word := range runbookDangerousChineseWords {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}
