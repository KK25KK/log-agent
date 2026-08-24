package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateRunbookQuery(t *testing.T) {
	valid := validRunbookQuery()
	tests := []struct {
		name   string
		mutate func(*RunbookQuery)
	}{
		{name: "missing resource", mutate: func(query *RunbookQuery) { query.ResourceID = "" }},
		{name: "wrong fixed limit", mutate: func(query *RunbookQuery) { query.Limit-- }},
		{name: "missing codes", mutate: func(query *RunbookQuery) { query.RecommendationCodes = nil }},
		{name: "too many codes", mutate: func(query *RunbookQuery) {
			query.RecommendationCodes = []string{"a", "b", "c", "d", "e"}
		}},
		{name: "unsorted codes", mutate: func(query *RunbookQuery) {
			query.RecommendationCodes = []string{"inspect_hot_instance", "compare_recent_changes"}
		}},
		{name: "duplicate codes", mutate: func(query *RunbookQuery) {
			query.RecommendationCodes = []string{"compare_recent_changes", "compare_recent_changes"}
		}},
		{name: "invalid code", mutate: func(query *RunbookQuery) {
			query.RecommendationCodes = []string{"compare recent changes"}
		}},
	}
	if err := ValidateRunbookQuery(valid); err != nil {
		t.Fatalf("valid query rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRunbookQuery(valid)
			test.mutate(&candidate)
			if err := ValidateRunbookQuery(candidate); err == nil {
				t.Fatalf("invalid query accepted: %#v", candidate)
			}
		})
	}
}

func TestValidateRunbookSourceVersionAndIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "stable source version", value: "catalog-mock-v1", valid: true},
		{name: "surrounding whitespace", value: " catalog-mock-v1", valid: false},
		{name: "control", value: "catalog\nv1", valid: false},
		{name: "URL", value: "https://catalog", valid: false},
		{name: "invalid UTF-8", value: string([]byte{0xff}), valid: false},
		{name: "too long", value: strings.Repeat("a", MaxRunbookSourceVersionRunes+1), valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRunbookSourceVersion(test.value)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateRunbookSourceVersion(%q) error=%v, valid=%t", test.value, err, test.valid)
			}
		})
	}
	if err := ValidateRunbookIdentifier("entry.v1-20260824"); err != nil {
		t.Fatalf("valid identifier rejected: %v", err)
	}
}

func TestValidateRunbookGuidanceDataSource(t *testing.T) {
	for _, source := range []RunbookGuidanceDataSource{
		RunbookGuidanceSourceSyntheticMock,
		RunbookGuidanceSourceEnterpriseGoverned,
	} {
		if !ValidateRunbookGuidanceDataSource(source) {
			t.Fatalf("trusted assembly data source %q was rejected", source)
		}
	}
	for _, source := range []RunbookGuidanceDataSource{"", "MOCK", "PROVIDER_REPORTED_REAL"} {
		if ValidateRunbookGuidanceDataSource(source) {
			t.Fatalf("untrusted data source %q was accepted", source)
		}
	}
}

func TestCanonicalRunbookStep(t *testing.T) {
	tests := []struct {
		code            RunbookStepCode
		wantKind        RunbookStepKind
		wantInstruction string
	}{
		{
			code:            RunbookStepCodeVerifyErrorPattern,
			wantKind:        RunbookStepVerify,
			wantInstruction: "核对主要错误模式是否与告警现象一致。",
		},
		{
			code:            RunbookStepCodeObserveHotInstance,
			wantKind:        RunbookStepObserve,
			wantInstruction: "观察高频实例的错误占比与延迟变化。",
		},
		{
			code:            RunbookStepCodeEscalateServiceOwner,
			wantKind:        RunbookStepEscalate,
			wantInstruction: "联系对应服务值班负责人确认依赖健康状态。",
		},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			kind, instruction, ok := CanonicalRunbookStep(test.code)
			if !ok || kind != test.wantKind || instruction != test.wantInstruction {
				t.Fatalf("CanonicalRunbookStep(%q)=(%q, %q, %t), want (%q, %q, true)", test.code, kind, instruction, ok, test.wantKind, test.wantInstruction)
			}
			step := RunbookStep{ID: "step-1", Code: test.code, Kind: kind, Instruction: instruction}
			if err := ValidateRunbookStep(step); err != nil {
				t.Fatalf("canonical step rejected: %v", err)
			}
		})
	}
	if kind, instruction, ok := CanonicalRunbookStep("PROVIDER_AUTHORED"); ok || kind != "" || instruction != "" {
		t.Fatalf("unknown code returned canonical content: (%q, %q, %t)", kind, instruction, ok)
	}
}

func TestValidateRunbookStepRejectsNonCanonicalContent(t *testing.T) {
	valid := canonicalRunbookStep("step-1", RunbookStepCodeVerifyErrorPattern)
	tests := []struct {
		name   string
		mutate func(*RunbookStep)
	}{
		{name: "missing code", mutate: func(step *RunbookStep) { step.Code = "" }},
		{name: "unknown code", mutate: func(step *RunbookStep) { step.Code = "PROVIDER_AUTHORED" }},
		{name: "mismatched kind", mutate: func(step *RunbookStep) { step.Kind = RunbookStepEscalate }},
		{name: "provider-authored benign text", mutate: func(step *RunbookStep) { step.Instruction = "核对依赖健康状态。" }},
		{name: "chmod disguised as verify", mutate: func(step *RunbookStep) { step.Instruction = "人工核对 chmod 000 /srv/order。" }},
		{name: "scale to zero disguised as verify", mutate: func(step *RunbookStep) { step.Instruction = "人工核对并将副本数设为零。" }},
		{name: "URL", mutate: func(step *RunbookStep) { step.Instruction = "访问 https://example.invalid 查看详情。" }},
		{name: "Markdown", mutate: func(step *RunbookStep) { step.Instruction = "查看[排障文档](kb)。" }},
		{name: "invalid UTF-8", mutate: func(step *RunbookStep) { step.Instruction = string([]byte{0xff}) }},
		{name: "invalid ID", mutate: func(step *RunbookStep) { step.ID = "bad id" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := ValidateRunbookStep(candidate); err == nil {
				t.Fatalf("non-canonical step accepted: %#v", candidate)
			}
		})
	}
}

func TestValidateRunbookEntry(t *testing.T) {
	query := validRunbookQuery()
	valid := validRunbookEntry("rb-order-errors")
	tests := []struct {
		name   string
		mutate func(*RunbookEntry)
	}{
		{name: "invalid ID", mutate: func(entry *RunbookEntry) { entry.ID = "bad id" }},
		{name: "invalid revision", mutate: func(entry *RunbookEntry) { entry.Revision = "" }},
		{name: "wrong resource", mutate: func(entry *RunbookEntry) { entry.ResourceID = "mock/other/prod" }},
		{name: "unsafe title", mutate: func(entry *RunbookEntry) { entry.Title = "重启订单服务" }},
		{name: "unsafe owner", mutate: func(entry *RunbookEntry) { entry.OwnerTeam = "https://owner.invalid" }},
		{name: "missing update time", mutate: func(entry *RunbookEntry) { entry.UpdatedAt = time.Time{} }},
		{name: "unserializable update time", mutate: func(entry *RunbookEntry) { entry.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{name: "missing codes", mutate: func(entry *RunbookEntry) { entry.MatchedRecommendationCodes = nil }},
		{name: "unsorted codes", mutate: func(entry *RunbookEntry) {
			entry.MatchedRecommendationCodes = []string{"inspect_hot_instance", "compare_recent_changes"}
		}},
		{name: "duplicate codes", mutate: func(entry *RunbookEntry) {
			entry.MatchedRecommendationCodes = []string{"compare_recent_changes", "compare_recent_changes"}
		}},
		{name: "code outside query", mutate: func(entry *RunbookEntry) {
			entry.MatchedRecommendationCodes = []string{"invented_action"}
		}},
		{name: "missing steps", mutate: func(entry *RunbookEntry) { entry.Steps = nil }},
		{name: "too many steps", mutate: func(entry *RunbookEntry) {
			entry.Steps = make([]RunbookStep, MaxRunbookStepsPerEntry+1)
			for index := range entry.Steps {
				entry.Steps[index] = canonicalRunbookStep("step-"+string(rune('a'+index)), RunbookStepCodeVerifyErrorPattern)
			}
		}},
		{name: "duplicate step IDs", mutate: func(entry *RunbookEntry) {
			entry.Steps = append(entry.Steps, entry.Steps[0])
		}},
	}
	if err := ValidateRunbookEntry(valid, query); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRunbookEntry(valid)
			test.mutate(&candidate)
			if err := ValidateRunbookEntry(candidate, query); err == nil {
				t.Fatalf("invalid entry accepted: %#v", candidate)
			}
		})
	}
}

func TestValidateRunbookSet(t *testing.T) {
	query := validRunbookQuery()
	valid := RunbookSet{
		SourceVersion: "catalog-mock-v1",
		Entries: []RunbookEntry{
			validRunbookEntry("rb-a"),
			validRunbookEntry("rb-b"),
		},
		Complete: true,
	}
	if err := ValidateRunbookSet(valid, query); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}
	validNoMatch := RunbookSet{SourceVersion: "catalog-mock-v1", Complete: true}
	if err := ValidateRunbookSet(validNoMatch, query); err != nil {
		t.Fatalf("valid no-match set rejected: %v", err)
	}
	validIncomplete := RunbookSet{SourceVersion: "catalog-mock-v1", Complete: false, ReasonCode: RunbookReasonIncomplete}
	if err := ValidateRunbookSet(validIncomplete, query); err != nil {
		t.Fatalf("valid incomplete set rejected: %v", err)
	}
	validTruncated := RunbookSet{SourceVersion: "catalog-mock-v1", Complete: false, Truncated: true, ReasonCode: RunbookReasonTruncated}
	if err := ValidateRunbookSet(validTruncated, query); err != nil {
		t.Fatalf("valid truncated set rejected: %v", err)
	}
	validDisabled := RunbookSet{Complete: false, ReasonCode: RunbookReasonDisabled}
	if err := ValidateRunbookSet(validDisabled, query); err != nil {
		t.Fatalf("valid disabled set rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RunbookSet)
	}{
		{name: "missing source version", mutate: func(set *RunbookSet) { set.SourceVersion = "" }},
		{name: "complete and truncated", mutate: func(set *RunbookSet) { set.Truncated = true }},
		{name: "complete with reason", mutate: func(set *RunbookSet) { set.ReasonCode = RunbookReasonIncomplete }},
		{name: "incomplete without reason", mutate: func(set *RunbookSet) { set.Complete = false }},
		{name: "unknown reason", mutate: func(set *RunbookSet) {
			set.Complete = false
			set.ReasonCode = "provider_error"
		}},
		{name: "truncated wrong reason", mutate: func(set *RunbookSet) {
			set.Complete = false
			set.Truncated = true
			set.ReasonCode = RunbookReasonIncomplete
		}},
		{name: "duplicate entry ID", mutate: func(set *RunbookSet) {
			set.Entries[1].ID = set.Entries[0].ID
		}},
		{name: "unstable entry order", mutate: func(set *RunbookSet) {
			set.Entries[0], set.Entries[1] = set.Entries[1], set.Entries[0]
		}},
		{name: "disabled with entries", mutate: func(set *RunbookSet) {
			set.SourceVersion = ""
			set.Complete = false
			set.ReasonCode = RunbookReasonDisabled
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRunbookSet(valid)
			test.mutate(&candidate)
			if err := ValidateRunbookSet(candidate, query); err == nil {
				t.Fatalf("invalid set accepted: %#v", candidate)
			}
		})
	}
}

func TestRunbookEntryFingerprintIsStableAndContentBound(t *testing.T) {
	entry := validRunbookEntry("rb-order-errors")
	first, err := RunbookEntryFingerprint(entry)
	if err != nil {
		t.Fatalf("fingerprint valid entry: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("fingerprint length=%d, want 64", len(first))
	}

	equivalent := cloneRunbookEntry(entry)
	equivalent.UpdatedAt = entry.UpdatedAt.In(time.FixedZone("UTC+8", 8*60*60))
	second, err := RunbookEntryFingerprint(equivalent)
	if err != nil {
		t.Fatalf("fingerprint equivalent entry: %v", err)
	}
	if first != second {
		t.Fatalf("equivalent instants changed fingerprint: %s != %s", first, second)
	}

	changed := cloneRunbookEntry(entry)
	changed.Steps[1] = canonicalRunbookStep(changed.Steps[1].ID, RunbookStepCodeVerifyErrorPattern)
	third, err := RunbookEntryFingerprint(changed)
	if err != nil {
		t.Fatalf("fingerprint changed entry: %v", err)
	}
	if first == third {
		t.Fatal("changed content did not change fingerprint")
	}
}

func TestValidateRunbookGuidanceItem(t *testing.T) {
	entry := validRunbookEntry("rb-order-errors")
	fingerprint, err := RunbookEntryFingerprint(entry)
	if err != nil {
		t.Fatal(err)
	}
	valid := RunbookGuidanceItem{
		EntryID:             entry.ID,
		Revision:            entry.Revision,
		Fingerprint:         fingerprint,
		Title:               entry.Title,
		Owner:               entry.OwnerTeam,
		UpdatedAt:           entry.UpdatedAt,
		RecommendationCodes: append([]string(nil), entry.MatchedRecommendationCodes...),
		EvidenceIDs:         []string{"ev_baseline", "ev_current"},
		Steps:               append([]RunbookStep(nil), entry.Steps...),
		ExecutionMode:       RunbookExecutionHumanReviewOnly,
	}
	if err := ValidateRunbookGuidanceItem(valid); err != nil {
		t.Fatalf("valid guidance item rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*RunbookGuidanceItem)
	}{
		{name: "bad fingerprint", mutate: func(item *RunbookGuidanceItem) { item.Fingerprint = "abc" }},
		{name: "unsafe title", mutate: func(item *RunbookGuidanceItem) { item.Title = "https://unsafe.invalid" }},
		{name: "unserializable update time", mutate: func(item *RunbookGuidanceItem) { item.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{name: "unsorted evidence", mutate: func(item *RunbookGuidanceItem) {
			item.EvidenceIDs = []string{"ev_current", "ev_baseline"}
		}},
		{name: "duplicate evidence", mutate: func(item *RunbookGuidanceItem) {
			item.EvidenceIDs = []string{"ev_current", "ev_current"}
		}},
		{name: "non-human execution", mutate: func(item *RunbookGuidanceItem) { item.ExecutionMode = "AUTO" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.RecommendationCodes = append([]string(nil), valid.RecommendationCodes...)
			candidate.EvidenceIDs = append([]string(nil), valid.EvidenceIDs...)
			candidate.Steps = append([]RunbookStep(nil), valid.Steps...)
			test.mutate(&candidate)
			if err := ValidateRunbookGuidanceItem(candidate); err == nil {
				t.Fatalf("invalid guidance item accepted: %#v", candidate)
			}
		})
	}
}

func TestValidateRunbookReason(t *testing.T) {
	tests := []struct {
		reason string
		valid  bool
	}{
		{reason: "", valid: true},
		{reason: RunbookReasonDisabled, valid: true},
		{reason: RunbookReasonIncomplete, valid: true},
		{reason: RunbookReasonTruncated, valid: true},
		{reason: "provider_error", valid: false},
	}
	for _, test := range tests {
		if got := ValidateRunbookReason(test.reason); got != test.valid {
			t.Fatalf("ValidateRunbookReason(%q)=%t, want %t", test.reason, got, test.valid)
		}
	}
}

func TestRunbookContractHasNoFloatingPointFields(t *testing.T) {
	// The contract has no numeric score or provider-authored confidence, so NaN
	// is structurally inapplicable rather than another value to sanitize later.
	types := []reflect.Type{
		reflect.TypeOf(RunbookQuery{}),
		reflect.TypeOf(RunbookSet{}),
		reflect.TypeOf(RunbookEntry{}),
		reflect.TypeOf(RunbookStep{}),
		reflect.TypeOf(RunbookGuidance{}),
		reflect.TypeOf(RunbookGuidanceItem{}),
	}
	for _, contractType := range types {
		if containsFloatingPoint(contractType, make(map[reflect.Type]bool)) {
			t.Fatalf("runbook contract %s unexpectedly contains a floating-point field", contractType)
		}
	}
}

func validRunbookQuery() RunbookQuery {
	return RunbookQuery{
		ResourceID:          "mock/order/prod",
		RecommendationCodes: []string{"compare_recent_changes", "inspect_hot_instance"},
		Limit:               MaxRunbookEntries,
	}
}

func validRunbookEntry(id string) RunbookEntry {
	return RunbookEntry{
		ID:                         id,
		Revision:                   "rev-20260824",
		ResourceID:                 "mock/order/prod",
		Title:                      "订单错误突增核查",
		OwnerTeam:                  "订单平台值班组",
		UpdatedAt:                  time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC),
		MatchedRecommendationCodes: []string{"compare_recent_changes", "inspect_hot_instance"},
		Steps: []RunbookStep{
			canonicalRunbookStep("step-verify-error-pattern", RunbookStepCodeVerifyErrorPattern),
			canonicalRunbookStep("step-observe-hot-instance", RunbookStepCodeObserveHotInstance),
			canonicalRunbookStep("step-escalate-owner", RunbookStepCodeEscalateServiceOwner),
		},
	}
}

func canonicalRunbookStep(id string, code RunbookStepCode) RunbookStep {
	kind, instruction, ok := CanonicalRunbookStep(code)
	if !ok {
		panic("unknown test runbook step code: " + string(code))
	}
	return RunbookStep{ID: id, Code: code, Kind: kind, Instruction: instruction}
}

func cloneRunbookQuery(query RunbookQuery) RunbookQuery {
	query.RecommendationCodes = append([]string(nil), query.RecommendationCodes...)
	return query
}

func cloneRunbookEntry(entry RunbookEntry) RunbookEntry {
	entry.MatchedRecommendationCodes = append([]string(nil), entry.MatchedRecommendationCodes...)
	entry.Steps = append([]RunbookStep(nil), entry.Steps...)
	return entry
}

func cloneRunbookSet(set RunbookSet) RunbookSet {
	set.Entries = append([]RunbookEntry(nil), set.Entries...)
	for index := range set.Entries {
		set.Entries[index] = cloneRunbookEntry(set.Entries[index])
	}
	return set
}

func containsFloatingPoint(contractType reflect.Type, visited map[reflect.Type]bool) bool {
	for contractType.Kind() == reflect.Pointer || contractType.Kind() == reflect.Slice || contractType.Kind() == reflect.Array {
		contractType = contractType.Elem()
	}
	if visited[contractType] {
		return false
	}
	visited[contractType] = true
	switch contractType.Kind() {
	case reflect.Float32, reflect.Float64:
		return true
	case reflect.Struct:
		for index := 0; index < contractType.NumField(); index++ {
			if containsFloatingPoint(contractType.Field(index).Type, visited) {
				return true
			}
		}
	}
	return false
}
