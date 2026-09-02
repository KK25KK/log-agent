package intentmock

import (
	"context"
	"testing"

	"logagent/internal/domain"
)

func TestParserKeepsMissingScopeOrWindowIncomplete(t *testing.T) {
	capabilities := []domain.InvestigationCapability{{
		Service: "dam-server", Environment: "test", Intent: domain.IntentErrorSpike, TemplateID: domain.ErrorCountTemplateID,
	}}
	tests := []struct {
		problem         string
		wantService     string
		wantEnvironment string
		wantDuration    int64
	}{
		{problem: "最近错误有没有增加"},
		{problem: "DAM 测试环境错误有没有增加", wantService: "dam-server", wantEnvironment: "test"},
		{problem: "DAM 测试环境最近半小时错误有没有增加", wantService: "dam-server", wantEnvironment: "test", wantDuration: 1800},
	}
	for _, test := range tests {
		result, err := New().Parse(context.Background(), domain.IntentProviderInput{Problem: test.problem, Capabilities: capabilities})
		if err != nil {
			t.Fatal(err)
		}
		if result.Draft.Intent != domain.IntentErrorSpike || result.Draft.Service != test.wantService ||
			result.Draft.Environment != test.wantEnvironment || result.Draft.DurationSeconds != test.wantDuration {
			t.Fatalf("problem %q produced %#v", test.problem, result.Draft)
		}
	}
}

func TestParserNeverDowngradesTraceToErrorSpike(t *testing.T) {
	result, err := New().Parse(context.Background(), domain.IntentProviderInput{
		Problem: "DAM 测试环境查 Trace abc12345 看错误为什么增加",
		Capabilities: []domain.InvestigationCapability{{
			Service: "dam-server", Environment: "test", Intent: domain.IntentErrorSpike, TemplateID: domain.ErrorCountTemplateID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft.Intent != domain.IntentUnknown {
		t.Fatalf("trace request was downgraded: %#v", result.Draft)
	}
}
