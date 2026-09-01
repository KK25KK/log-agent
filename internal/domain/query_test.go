package domain

import "testing"

func TestQueryTemplateContracts(t *testing.T) {
	analysis, ok := QueryTemplateByID(ErrorAnalysisTemplateID)
	if !ok || analysis.Version != ErrorAnalysisTemplateVersion || !analysis.Dimensional || analysis.APICalls != 4 || analysis.ResultRows != 12 {
		t.Fatalf("unexpected analysis contract: %#v", analysis)
	}
	count, ok := QueryTemplateByVersion(ErrorCountTemplateVersion)
	if !ok || count.ID != ErrorCountTemplateID || count.Dimensional || count.APICalls != 2 || count.ResultRows != 2 {
		t.Fatalf("unexpected count contract: %#v", count)
	}
	if EffectiveQueryTemplateID("") != ErrorAnalysisTemplateID {
		t.Fatal("empty persisted template must preserve analysis behavior")
	}
}
