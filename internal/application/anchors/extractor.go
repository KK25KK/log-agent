// Package anchors extracts bounded code-search keys from already-redacted
// runtime events. It performs no I/O and never produces a causal conclusion.
package anchors

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

var (
	goFramePattern     = regexp.MustCompile(`(?m)^\s*([^\s]+\.go):(\d+)(?:\s+\+0x[0-9a-fA-F]+)?\s*$`)
	javaFramePattern   = regexp.MustCompile(`(?m)^\s*at\s+([A-Za-z_$][A-Za-z0-9_.$]*)\(([^():\s]+\.java):(\d+)\)\s*$`)
	pythonFramePattern = regexp.MustCompile(`(?m)^\s*File\s+["']([^"']+\.py)["'],\s+line\s+(\d+),\s+in\s+([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	goSymbolPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+(?:\.\(\*?[A-Za-z_][A-Za-z0-9_]*\))?\.[A-Za-z_][A-Za-z0-9_]*$`)
	errorTypePattern   = regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9]*(?:Error|Exception)|[a-z][a-z0-9]*(?:_[a-z0-9]+)*(?:_error|_exception|_timeout|_failed))\b`)
	errorTextPattern   = regexp.MustCompile(`(?i)(?:error|err|failed|failure|panic|exception)\s*(?:=|:)\s*([^;\r\n]+)`)
	routePattern       = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(/[A-Za-z0-9_./:{}*-]{1,120})$`)
	safePathPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\.(?:go|java|py)$`)
	safeSymbolPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.$/()\*-]{0,191}$`)
	drivePathPattern   = regexp.MustCompile(`^[A-Za-z]:/`)
)

type candidate struct {
	kind     domain.RuntimeAnchorKind
	value    string
	file     string
	line     int
	symbol   string
	priority int
}

// Extract enriches cloned events and returns one exact, bounded anchor set.
// complete means the upstream runtime evidence is complete; it does not mean
// the extracted anchors establish a root cause.
func Extract(source []domain.TraceEvent, complete bool) ([]domain.TraceEvent, domain.RuntimeAnchorSet) {
	events := make([]domain.TraceEvent, len(source))
	set := domain.RuntimeAnchorSet{
		Version: domain.RuntimeAnchorVersion, SourceEventCount: len(source), Limit: domain.RuntimeAnchorGlobalLimit,
		Anchors: make([]domain.RuntimeAnchor, 0),
	}
	seen := make(map[string]struct{})
	for index, event := range source {
		event.Anchors = nil
		candidates := extractCandidates(event)
		if len(candidates) > domain.RuntimeAnchorPerEventLimit {
			set.DroppedCount += len(candidates) - domain.RuntimeAnchorPerEventLimit
			candidates = candidates[:domain.RuntimeAnchorPerEventLimit]
		}
		for _, item := range candidates {
			anchor, ok := makeAnchor(event, item)
			if !ok {
				set.DroppedCount++
				continue
			}
			if _, duplicate := seen[anchor.Fingerprint]; duplicate {
				set.DroppedCount++
				continue
			}
			if len(set.Anchors) >= domain.RuntimeAnchorGlobalLimit {
				set.DroppedCount++
				continue
			}
			seen[anchor.Fingerprint] = struct{}{}
			event.Anchors = append(event.Anchors, anchor)
			set.Anchors = append(set.Anchors, anchor)
		}
		if len(event.Anchors) > 0 {
			set.AnchoredEventCount++
		}
		events[index] = event
	}
	sort.SliceStable(set.Anchors, func(left, right int) bool {
		if set.Anchors[left].Kind != set.Anchors[right].Kind {
			return set.Anchors[left].Kind < set.Anchors[right].Kind
		}
		return set.Anchors[left].Fingerprint < set.Anchors[right].Fingerprint
	})
	switch {
	case !complete || set.DroppedCount > 0:
		set.Status = domain.RuntimeAnchorsPartial
	case len(set.Anchors) == 0:
		set.Status = domain.RuntimeAnchorsNone
	default:
		set.Status = domain.RuntimeAnchorsComplete
	}
	return events, set
}

func extractCandidates(event domain.TraceEvent) []candidate {
	result := make([]candidate, 0, 8)
	message := strings.TrimSpace(event.Message)
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if goFrame := goFramePattern.FindStringSubmatch(trimmed); len(goFrame) == 3 {
			file := repositoryRelativePath(goFrame[1])
			lineNumber, _ := strconv.Atoi(goFrame[2])
			symbol := ""
			if index > 0 {
				previous := strings.TrimSpace(lines[index-1])
				if goSymbolPattern.MatchString(previous) {
					symbol = previous
				}
			}
			if file != "" && lineNumber > 0 && lineNumber <= 1_000_000 {
				result = append(result, candidate{kind: domain.RuntimeAnchorStackFrame, file: file, line: lineNumber, symbol: symbol, priority: 0})
				if symbol != "" {
					result = append(result, candidate{kind: domain.RuntimeAnchorSymbol, symbol: symbol, priority: 1})
				}
			}
		}
		if javaFrame := javaFramePattern.FindStringSubmatch(trimmed); len(javaFrame) == 4 {
			lineNumber, _ := strconv.Atoi(javaFrame[3])
			file := javaFrame[2]
			if lineNumber > 0 && lineNumber <= 1_000_000 {
				result = append(result, candidate{kind: domain.RuntimeAnchorStackFrame, file: file, line: lineNumber, symbol: javaFrame[1], priority: 0})
			}
		}
		if pythonFrame := pythonFramePattern.FindStringSubmatch(trimmed); len(pythonFrame) == 4 {
			lineNumber, _ := strconv.Atoi(pythonFrame[2])
			file := repositoryRelativePath(pythonFrame[1])
			if file != "" && lineNumber > 0 && lineNumber <= 1_000_000 {
				result = append(result, candidate{kind: domain.RuntimeAnchorStackFrame, file: file, line: lineNumber, symbol: pythonFrame[3], priority: 0})
			}
		}
	}
	for _, match := range errorTypePattern.FindAllString(message, -1) {
		result = append(result, candidate{kind: domain.RuntimeAnchorErrorType, value: match, priority: 2})
	}
	if match := errorTextPattern.FindStringSubmatch(message); len(match) == 2 {
		if value := stableErrorText(match[1]); value != "" {
			result = append(result, candidate{kind: domain.RuntimeAnchorErrorText, value: value, priority: 3})
		}
	}
	if route := strings.TrimSpace(event.Operation); routePattern.MatchString(route) {
		result = append(result, candidate{kind: domain.RuntimeAnchorRoute, value: route, priority: 4})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].priority != result[right].priority {
			return result[left].priority < result[right].priority
		}
		return candidateKey(result[left]) < candidateKey(result[right])
	})
	return deduplicateCandidates(result)
}

func stableErrorText(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'.,`)
	for _, separator := range []string{" for [", " trace [", " at [", " id=", " request_id="} {
		if index := strings.Index(strings.ToLower(value), separator); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
	}
	if utf8.RuneCountInString(value) < 4 || utf8.RuneCountInString(value) > 160 ||
		strings.Contains(value, "[REDACTED]") || strings.Contains(value, "[TRACE_ID]") || strings.ContainsAny(value, "\r\n\t{}[]|;$`") {
		return ""
	}
	letters := 0
	for _, character := range value {
		if unicode.IsLetter(character) {
			letters++
		}
		if unicode.IsControl(character) {
			return ""
		}
	}
	if letters < 4 {
		return ""
	}
	return value

}

func repositoryRelativePath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	if strings.Contains(value, "../") || strings.Contains(value, "/vendor/") || strings.Contains(value, "/generated/") || strings.Contains(value, "/node_modules/") {
		return ""
	}
	value = strings.TrimPrefix(value, "./")
	if strings.HasPrefix(value, "/") || drivePathPattern.MatchString(value) {
		lower := strings.ToLower(value)
		best := -1
		for _, marker := range []string{"/internal/", "/cmd/", "/pkg/", "/src/"} {
			if index := strings.Index(lower, marker); index >= 0 && (best < 0 || index < best) {
				best = index + 1
			}
		}
		if best < 0 {
			return ""
		}
		value = value[best:]
	}
	if !safePathPattern.MatchString(value) || containsForbiddenPath(value) {
		return ""
	}
	return value
}

func containsForbiddenPath(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"secret", "credential", "private_key", ".env", "id_rsa"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func deduplicateCandidates(source []candidate) []candidate {
	seen := make(map[string]struct{}, len(source))
	result := make([]candidate, 0, len(source))
	for _, item := range source {
		key := candidateKey(item)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func candidateKey(item candidate) string {
	return string(item.kind) + "|" + item.value + "|" + item.file + "|" + strconv.Itoa(item.line) + "|" + item.symbol
}

func makeAnchor(event domain.TraceEvent, item candidate) (domain.RuntimeAnchor, bool) {
	if event.ID == "" || event.MemberID == "" ||
		(item.value != "" && utf8.RuneCountInString(item.value) > 192) ||
		(item.file != "" && !safePathPattern.MatchString(item.file)) ||
		(item.symbol != "" && !safeSymbolPattern.MatchString(item.symbol)) {
		return domain.RuntimeAnchor{}, false
	}
	content := struct {
		Kind   domain.RuntimeAnchorKind `json:"kind"`
		Value  string                   `json:"value,omitempty"`
		File   string                   `json:"file,omitempty"`
		Line   int                      `json:"line,omitempty"`
		Symbol string                   `json:"symbol,omitempty"`
	}{Kind: item.kind, Value: item.value, File: item.file, Line: item.line, Symbol: item.symbol}
	contentFingerprint, err := fingerprint.JSON(content)
	if err != nil {
		return domain.RuntimeAnchor{}, false
	}
	idHash := sha256.Sum256([]byte(event.ID + "|" + contentFingerprint))
	return domain.RuntimeAnchor{
		ID: "anchor_" + hex.EncodeToString(idHash[:12]), Kind: item.kind, EventID: event.ID, MemberID: event.MemberID,
		Value: item.value, File: item.file, Line: item.line, Symbol: item.symbol, Fingerprint: contentFingerprint,
	}, true
}

// Validate verifies that a persisted anchor is exactly reproducible from its
// closed content and source event identity.
func Validate(anchor domain.RuntimeAnchor) error {
	if anchor.EventID == "" || anchor.MemberID == "" || anchor.ID == "" || anchor.Fingerprint == "" {
		return errors.New("runtime anchor identity is incomplete")
	}
	item := candidate{kind: anchor.Kind, value: anchor.Value, file: anchor.File, line: anchor.Line, symbol: anchor.Symbol}
	switch anchor.Kind {
	case domain.RuntimeAnchorErrorText:
		if anchor.Value == "" || stableErrorText(anchor.Value) != anchor.Value || anchor.File != "" || anchor.Line != 0 || anchor.Symbol != "" {
			return errors.New("error-text anchor shape is invalid")
		}
	case domain.RuntimeAnchorErrorType:
		if anchor.Value == "" || errorTypePattern.FindString(anchor.Value) != anchor.Value || anchor.File != "" || anchor.Line != 0 || anchor.Symbol != "" {
			return errors.New("error-type anchor shape is invalid")
		}
	case domain.RuntimeAnchorRoute:
		if !routePattern.MatchString(anchor.Value) || anchor.File != "" || anchor.Line != 0 || anchor.Symbol != "" {
			return errors.New("route anchor shape is invalid")
		}
	case domain.RuntimeAnchorSymbol:
		if !safeSymbolPattern.MatchString(anchor.Symbol) || anchor.Value != "" || anchor.File != "" || anchor.Line != 0 {
			return errors.New("symbol anchor shape is invalid")
		}
	case domain.RuntimeAnchorStackFrame:
		if !safePathPattern.MatchString(anchor.File) || containsForbiddenPath(anchor.File) || anchor.Line <= 0 || anchor.Line > 1_000_000 ||
			(anchor.Symbol != "" && !safeSymbolPattern.MatchString(anchor.Symbol)) || anchor.Value != "" {
			return errors.New("stack-frame anchor shape is invalid")
		}
	default:
		return errors.New("runtime anchor kind is invalid")
	}
	expected, ok := makeAnchor(domain.TraceEvent{ID: anchor.EventID, MemberID: anchor.MemberID}, item)
	if !ok || expected.ID != anchor.ID || expected.Fingerprint != anchor.Fingerprint {
		return errors.New("runtime anchor integrity check failed")
	}
	return nil
}
