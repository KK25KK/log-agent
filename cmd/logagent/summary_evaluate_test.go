package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"logagent/internal/evaluation"
	"logagent/internal/evaluation/summaryeval"
)

func TestExecuteSummaryEvaluationPassesAllSafetyScenarios(t *testing.T) {
	dataset, err := summaryeval.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	baseDataset, err := evaluation.LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := executeSummaryEvaluation(context.Background(), dataset, baseDataset)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != summaryeval.EvaluationPassed || report.Metrics.TotalCases != 9 || report.Metrics.PassedCases != 9 ||
		report.Metrics.ActualProviderCalls != 8 || report.Metrics.ProviderCallBudgetBreaches != 0 || report.Metrics.TotalTokens != 0 ||
		report.Metrics.ExternalNetworkCalls != 0 || report.Metrics.CredentialsRequiredCases != 0 {
		t.Fatalf("unexpected summary gate result: %#v", report.Metrics)
	}
	for _, result := range report.Cases {
		if !result.Passed || !result.ProductionOutputValid || !result.DeterministicIntegrity || !result.SummaryContractPassed || !result.InputPrivacyPassed {
			t.Fatalf("case %q failed: %#v", result.ID, result)
		}
		if result.ID == "sensitive-outbound" && result.ActualProviderCalls != 0 {
			t.Fatalf("sensitive input reached provider: %#v", result)
		}
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), syntheticSensitiveFinding) || strings.Contains(string(payload), "synthetic provider failure") {
		t.Fatal("sensitive or provider error content leaked into report")
	}
}

func TestRunSummaryEvaluateRejectsArguments(t *testing.T) {
	if err := runSummaryEvaluate([]string{"--real"}); err == nil {
		t.Fatal("summary-evaluate accepted runtime configuration")
	}
}
