package evaluation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"logagent/internal/domain"
)

func TestEvaluatePassesCompleteSyntheticContract(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluateMetrics(context.Background(), dataset, successfulExecution)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != EvaluationPassed || report.Metrics.PassedCases != len(dataset.Cases) || report.Metrics.CasePassRate != 1 {
		t.Fatalf("unexpected evaluation result: %#v", report.Metrics)
	}
	metrics := report.Metrics
	if metrics.OutcomeAccuracy != 1 || metrics.FindingExactAccuracy != 1 || metrics.QueryContractAccuracy != 1 || metrics.MisleadingRate != 0 || metrics.ConclusiveRecall != 1 || metrics.EvidenceCoverage != 1 || metrics.CauseExactAccuracy != 1 || metrics.CauseVerdictAccuracy != 1 {
		t.Fatalf("unexpected exact metrics: %#v", metrics)
	}
	if metrics.LogicalSLSCalls != len(dataset.Cases)*ExpectedLogicalSLSCalls || metrics.ProviderAPICalls != len(dataset.Cases)*ExpectedProviderAPICalls || metrics.ChangeSourceCalls != 3 || metrics.CallBudgetBreaches != 0 || metrics.CostBudgetBreaches != 0 {
		t.Fatalf("unexpected usage metrics: %#v", metrics)
	}
	if report.Versions.PromptUsed || report.Versions.PromptVersion != "" || report.Versions.CauseMethod != domain.CauseConfidenceMethod {
		t.Fatalf("version boundary is misleading: %#v", report.Versions)
	}
	for _, gate := range report.Gates {
		if !gate.Passed {
			t.Fatalf("gate did not pass: %#v", gate)
		}
	}
}

func TestEvaluateRecognizesMisleadingAndGroundingGateFailures(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}

	misleading := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Findings = append(report.Findings, domain.Finding{Code: "error_spike", Conclusive: true, EvidenceIDs: []string{"ev-current", "ev-baseline"}})
		}
		return report, stats, err
	}
	report, err := evaluateMetrics(context.Background(), dataset, misleading)
	if !errors.Is(err, ErrGateFailed) || report.Status != EvaluationFailed || report.Metrics.UnexpectedConclusiveFindings != 1 || report.Metrics.MisleadingRate <= 0 {
		t.Fatalf("misleading output did not fail closed: status=%s metrics=%#v err=%v", report.Status, report.Metrics, err)
	}

	ungrounded := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Findings[0].EvidenceIDs = nil
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, ungrounded)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.EvidenceCoverage >= 1 {
		t.Fatalf("ungrounded output did not fail evidence gate: metrics=%#v err=%v", report.Metrics, err)
	}

	orphanLedger := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			hypothesis := report.CauseAnalysis.Hypotheses[0]
			changeID := evaluationCase.Expected.CauseVerdicts[0].ChangeID
			report.CauseAnalysis.Ledger = append(report.CauseAnalysis.Ledger, domain.EvidenceLedgerEntry{
				ID:             "orphan-ledger-entry",
				HypothesisID:   hypothesis.ID,
				Role:           domain.EvidenceTestSupport,
				Result:         domain.EvidenceTestUnknown,
				EvidenceIDs:    []string{"ev-current"},
				ChangeEventIDs: []string{changeID},
			})
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, orphanLedger)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.EvidenceCoverage >= 1 {
		t.Fatalf("orphan ledger entry did not fail evidence gate: metrics=%#v err=%v", report.Metrics, err)
	}

	unexpectedChange := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			extra := report.CauseAnalysis.Changes[0]
			extra.ID = "chg-unexpected"
			report.CauseAnalysis.Changes = append(report.CauseAnalysis.Changes, extra)
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, unexpectedChange)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.CauseExactAccuracy >= 1 {
		t.Fatalf("unexpected untested change did not fail cause gate: metrics=%#v err=%v", report.Metrics, err)
	}

	incompleteGrounding := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Evidence[0].Complete = false
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, incompleteGrounding)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.EvidenceCoverage >= 1 {
		t.Fatalf("conclusive finding backed by incomplete evidence did not fail grounding gate: metrics=%#v err=%v", report.Metrics, err)
	}

	tamperedEvidence := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Evidence[0].QuerySpecHash = "non-empty-but-untraceable"
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, tamperedEvidence)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.EvidenceContractAccuracy >= 1 {
		t.Fatalf("tampered evidence identity did not fail evidence-contract gate: metrics=%#v err=%v", report.Metrics, err)
	}

	divergedEngineEvidence := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			stats.EngineEvidence[0].QuerySpecHash = ""
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, divergedEngineEvidence)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.EvidenceContractAccuracy >= 1 {
		t.Fatalf("diverged engine evidence did not fail evidence-contract gate: metrics=%#v err=%v", report.Metrics, err)
	}

	missingRecommendations := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Recommendations = nil
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, missingRecommendations)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.RecommendationExactAccuracy >= 1 {
		t.Fatalf("missing recommendations did not fail recommendation gate: metrics=%#v err=%v", report.Metrics, err)
	}

	unsafeRecommendation := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			report.Recommendations = append(report.Recommendations, domain.Recommendation{
				Code: "delete_production", Statement: "unsafe synthetic recommendation", EvidenceIDs: []string{"ev-current"},
			})
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, unsafeRecommendation)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.RecommendationExactAccuracy >= 1 {
		t.Fatalf("unexpected recommendation did not fail recommendation gate: metrics=%#v err=%v", report.Metrics, err)
	}

	misgroundedRecommendation := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		report, stats, err := successfulExecution(ctx, evaluationCase)
		if evaluationCase.ID == dataset.Cases[0].ID {
			for index := range report.Recommendations {
				if report.Recommendations[index].Code == "inspect_hot_instance" {
					report.Recommendations[index].EvidenceIDs = []string{"ev-baseline"}
				}
			}
		}
		return report, stats, err
	}
	report, err = evaluateMetrics(context.Background(), dataset, misgroundedRecommendation)
	if !errors.Is(err, ErrGateFailed) || report.Metrics.RecommendationExactAccuracy >= 1 {
		t.Fatalf("misgrounded recommendation did not fail recommendation gate: metrics=%#v err=%v", report.Metrics, err)
	}
}

func TestEvaluateCountsExecutionFailureInsteadOfSkippingCase(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	execute := func(ctx context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
		if evaluationCase.ID == dataset.Cases[2].ID {
			return domain.Report{}, ExecutionStats{}, errors.New("synthetic engine failure")
		}
		return successfulExecution(ctx, evaluationCase)
	}
	report, err := evaluateMetrics(context.Background(), dataset, execute)
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("execution failure error=%v", err)
	}
	if report.Metrics.ExecutedCases != len(dataset.Cases) || report.Metrics.ExecutionFailures != 1 || report.Metrics.PassedCases != len(dataset.Cases)-1 {
		t.Fatalf("execution failure was skipped: %#v", report.Metrics)
	}
}

func TestEvaluateRejectsOutputThatProductionWorkerWouldReject(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(context.Background(), dataset, successfulExecution)
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("invalid production output error=%v, want gate failure", err)
	}
	if report.Metrics.ProductionOutputFailures != len(dataset.Cases) || report.Metrics.ProductionOutputAccuracy != 0 {
		t.Fatalf("production validator mismatch was not visible: %#v", report.Metrics)
	}
}

func evaluateMetrics(ctx context.Context, dataset Dataset, execute ExecuteCase) (EvaluationReport, error) {
	return evaluateWithVersions(ctx, dataset, DefaultVersionInfo(), func(string, []domain.Evidence, domain.Report) error {
		return nil
	}, execute)
}

func successfulExecution(_ context.Context, evaluationCase EvaluationCase) (domain.Report, ExecutionStats, error) {
	investigationID := "inv-" + evaluationCase.ID
	duration := evaluationCase.Request.EndTime.Sub(evaluationCase.Request.StartTime)
	specs := []domain.QuerySpec{
		{InvestigationID: investigationID, Name: "current", TemplateID: domain.ErrorAnalysisTemplateID, Service: evaluationCase.Request.Service, Environment: evaluationCase.Request.Environment, StartTime: evaluationCase.Request.StartTime, EndTime: evaluationCase.Request.EndTime, Requester: evaluationCase.Request.Requester},
		{InvestigationID: investigationID, Name: "baseline", TemplateID: domain.ErrorAnalysisTemplateID, Service: evaluationCase.Request.Service, Environment: evaluationCase.Request.Environment, StartTime: evaluationCase.Request.StartTime.Add(-duration), EndTime: evaluationCase.Request.StartTime, Requester: evaluationCase.Request.Requester},
	}
	currentEvidence, err := evidenceFromFixture(evaluationCase.Current, specs[0])
	if err != nil {
		return domain.Report{}, ExecutionStats{}, err
	}
	currentEvidence.ID = "ev-current"
	baselineEvidence, err := evidenceFromFixture(evaluationCase.Baseline, specs[1])
	if err != nil {
		return domain.Report{}, ExecutionStats{}, err
	}
	baselineEvidence.ID = "ev-baseline"
	evidence := []domain.Evidence{currentEvidence, baselineEvidence}
	recommendations := make([]domain.Recommendation, 0, len(evaluationCase.Expected.Recommendations))
	for _, expected := range evaluationCase.Expected.Recommendations {
		evidenceIDs := make([]string, 0, len(expected.EvidenceNames))
		for _, name := range expected.EvidenceNames {
			switch name {
			case "current":
				evidenceIDs = append(evidenceIDs, "ev-current")
			case "baseline":
				evidenceIDs = append(evidenceIDs, "ev-baseline")
			}
		}
		recommendations = append(recommendations, domain.Recommendation{Code: expected.Code, Statement: "synthetic recommendation", EvidenceIDs: evidenceIDs})
	}
	findings := make([]domain.Finding, 0, len(evaluationCase.Expected.ConclusiveFindingCodes)+len(evaluationCase.Expected.NonconclusiveFindingCodes))
	for _, code := range evaluationCase.Expected.ConclusiveFindingCodes {
		findings = append(findings, domain.Finding{Code: code, Conclusive: true, EvidenceIDs: []string{"ev-current", "ev-baseline"}})
	}
	for _, code := range evaluationCase.Expected.NonconclusiveFindingCodes {
		findings = append(findings, domain.Finding{Code: code, Conclusive: false, EvidenceIDs: []string{"ev-current", "ev-baseline"}})
	}
	analysis := &domain.CauseAnalysis{
		Status:  evaluationCase.Expected.CauseStatus,
		Changes: append([]domain.ChangeEvent(nil), evaluationCase.ChangeSet.Events...),
	}
	for index, expected := range evaluationCase.Expected.CauseVerdicts {
		hypothesisID := fmt.Sprintf("hyp-%d", index)
		supportID := fmt.Sprintf("ledger-%d-support", index)
		counterID := fmt.Sprintf("ledger-%d-counter", index)
		analysis.Ledger = append(analysis.Ledger,
			domain.EvidenceLedgerEntry{ID: supportID, HypothesisID: hypothesisID, Role: domain.EvidenceTestSupport, Result: domain.EvidenceTestPass, EvidenceIDs: []string{"ev-current"}, ChangeEventIDs: []string{expected.ChangeID}},
			domain.EvidenceLedgerEntry{ID: counterID, HypothesisID: hypothesisID, Role: domain.EvidenceTestCounter, Result: domain.EvidenceTestFail, EvidenceIDs: []string{"ev-baseline"}, ChangeEventIDs: []string{expected.ChangeID}},
		)
		analysis.Hypotheses = append(analysis.Hypotheses, domain.CauseHypothesis{
			ID: hypothesisID, Verdict: expected.Verdict,
			SupportEntryIDs: []string{supportID}, CounterEntryIDs: []string{counterID},
		})
	}
	report := domain.Report{
		InvestigationID: investigationID,
		Outcome:         evaluationCase.Expected.Outcome,
		Findings:        findings,
		Recommendations: recommendations,
		Evidence:        evidence,
		CauseAnalysis:   analysis,
	}
	return report, ExecutionStats{
		EngineEvidence: evidence, QuerySpecs: specs, QueryContractValid: true,
		LogicalSLSCalls: len(specs), ProviderAPICalls: evaluationCase.Expected.ProviderAPICalls,
		ChangeSourceCalls: evaluationCase.Expected.ChangeSourceCalls,
		ProcessedBytes:    evaluationCase.Current.ProcessedBytes + evaluationCase.Baseline.ProcessedBytes,
	}, nil
}
