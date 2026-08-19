package application

import (
	"math"
	"testing"
	"time"

	"logagent/internal/domain"
)

func TestValidateEngineOutputRejectsUnsupportedConclusionsAndReferences(t *testing.T) {
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	job := domain.Job{InvestigationID: "inv-validation"}
	baseEvidence := domain.Evidence{
		ID: "ev-1", QueryID: "query-1", QuerySpecHash: "hash-1", Complete: true,
	}
	baseReport := domain.Report{
		InvestigationID: job.InvestigationID,
		Outcome:         "ok",
		GeneratedAt:     now,
		Findings: []domain.Finding{{
			Code: "finding", Statement: "supported", Confidence: .9, Conclusive: true, EvidenceIDs: []string{"ev-1"},
		}},
	}
	if err := validateEngineOutput(job, []domain.Evidence{baseEvidence}, baseReport); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}

	invalidConfidence := baseReport
	invalidConfidence.Findings = append([]domain.Finding(nil), baseReport.Findings...)
	invalidConfidence.Findings[0].Confidence = math.NaN()
	if err := validateEngineOutput(job, []domain.Evidence{baseEvidence}, invalidConfidence); err == nil {
		t.Fatal("finding with NaN confidence was accepted")
	}

	incomplete := baseEvidence
	incomplete.Complete = false
	if err := validateEngineOutput(job, []domain.Evidence{incomplete}, baseReport); err == nil {
		t.Fatal("conclusive finding over incomplete evidence was accepted")
	}

	unknownRecommendation := baseReport
	unknownRecommendation.Recommendations = []domain.Recommendation{{
		Code: "next", Statement: "next step", EvidenceIDs: []string{"ev-unknown"},
	}}
	if err := validateEngineOutput(job, []domain.Evidence{baseEvidence}, unknownRecommendation); err == nil {
		t.Fatal("recommendation with unknown evidence was accepted")
	}

	invalidAnalysis := baseEvidence
	invalidAnalysis.TemplateID = domain.ErrorAnalysisTemplateID
	invalidAnalysis.APICalls = 2
	if err := validateEngineOutput(job, []domain.Evidence{invalidAnalysis}, baseReport); err == nil {
		t.Fatal("analysis evidence with the wrong API-call count was accepted")
	}
}

func TestValidateAnalysisEvidenceAllowsConservativeIncompleteExhaustiveness(t *testing.T) {
	evidence := domain.Evidence{
		ID: "ev-snapshot", QueryID: "count-before,patterns,instances,count-after", QuerySpecHash: "hash",
		TemplateID: domain.ErrorAnalysisTemplateID, Complete: false, Progress: "Incomplete",
		APICalls: domain.ErrorAnalysisAPICalls, ErrorCount: 100,
		PatternLimit: domain.ErrorAnalysisPatternLimit, InstanceLimit: domain.ErrorAnalysisInstanceLimit,
		ErrorPatterns: []domain.CountBucket{{Label: "timeout", Count: 100}},
		Instances:     []domain.CountBucket{{Label: "pod-a", Count: 100}},
		// A changing visible set must not claim exhaustiveness even when the
		// bounded rows happen to add up to the conservative total.
		ErrorPatternsExhaustive: false,
		InstancesExhaustive:     false,
	}
	if err := validateAnalysisEvidence(evidence); err != nil {
		t.Fatalf("conservative incomplete evidence was rejected: %v", err)
	}
	evidence.Complete = true
	if err := validateAnalysisEvidence(evidence); err == nil {
		t.Fatal("complete evidence with false exhaustiveness was accepted")
	}
}

func TestValidateEngineOutputAcceptsACompleteCauseLedger(t *testing.T) {
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	current := domain.Evidence{
		ID: "ev-current", QueryID: "query-current", QuerySpecHash: "hash-current", ResourceID: "resource", Name: "current",
		StartTime: now.Add(-30 * time.Minute), EndTime: now, Complete: true, ErrorCount: 120,
		Instances: []domain.CountBucket{{Label: "pod-a", Count: 80}, {Label: "pod-b", Count: 40}}, InstancesExhaustive: true,
	}
	baseline := domain.Evidence{
		ID: "ev-baseline", QueryID: "query-baseline", QuerySpecHash: "hash-baseline", ResourceID: "resource", Name: "baseline",
		StartTime: now.Add(-60 * time.Minute), EndTime: now.Add(-30 * time.Minute), Complete: true, ErrorCount: 20,
		Instances: []domain.CountBucket{{Label: "pod-b", Count: 20}}, InstancesExhaustive: true,
	}
	change := domain.ChangeEvent{
		ID: "chg-release", ResourceID: "resource", Kind: domain.ChangeKindRelease,
		StartedAt: now.Add(-45 * time.Minute), CompletedAt: now.Add(-35 * time.Minute), ToVersion: "v2", Owner: "team", Summary: "release",
		AffectedInstances: []string{"pod-a"}, AffectedInstancesComplete: true,
	}
	definitions := []struct {
		code   string
		role   domain.EvidenceTestRole
		result domain.EvidenceTestResult
		weight float64
	}{
		{code: "error_spike", role: domain.EvidenceTestSupport, result: domain.EvidenceTestPass, weight: .25},
		{code: "temporal_precedence", role: domain.EvidenceTestSupport, result: domain.EvidenceTestPass, weight: .20},
		{code: "affected_instance_concentration", role: domain.EvidenceTestSupport, result: domain.EvidenceTestPass, weight: .30},
		{code: "baseline_shift", role: domain.EvidenceTestSupport, result: domain.EvidenceTestPass, weight: .10},
		{code: "no_instance_overlap", role: domain.EvidenceTestCounter, result: domain.EvidenceTestFail, weight: .40},
		{code: "preexisting_concentration", role: domain.EvidenceTestCounter, result: domain.EvidenceTestFail, weight: .15},
		{code: "confounding_changes", role: domain.EvidenceTestCounter, result: domain.EvidenceTestFail, weight: .10},
	}
	ledger := make([]domain.EvidenceLedgerEntry, 0, len(definitions))
	supportIDs := make([]string, 0, 4)
	counterIDs := make([]string, 0, 3)
	for _, definition := range definitions {
		evidenceIDs := []string{current.ID, baseline.ID}
		changeIDs := []string{change.ID}
		switch definition.code {
		case "error_spike":
			changeIDs = nil
		case "affected_instance_concentration", "no_instance_overlap":
			evidenceIDs = []string{current.ID}
		case "preexisting_concentration":
			evidenceIDs = []string{baseline.ID}
		}
		entry := domain.EvidenceLedgerEntry{
			ID: "test-" + definition.code, HypothesisID: "hyp-1", Code: definition.code, Role: definition.role,
			Result: definition.result, Weight: definition.weight, Statement: definition.code,
			EvidenceIDs: evidenceIDs, ChangeEventIDs: changeIDs,
		}
		ledger = append(ledger, entry)
		if definition.role == domain.EvidenceTestSupport {
			supportIDs = append(supportIDs, entry.ID)
		} else {
			counterIDs = append(counterIDs, entry.ID)
		}
	}
	report := domain.Report{
		InvestigationID: "inv-validation", Outcome: "spike_detected", GeneratedAt: now,
		Findings: []domain.Finding{{Code: "spike", Statement: "spike", Confidence: .9, Conclusive: true, EvidenceIDs: []string{current.ID, baseline.ID}}},
		CauseAnalysis: &domain.CauseAnalysis{
			Status: domain.CauseAnalysisComplete, SourceVersion: "changes-v1", Changes: []domain.ChangeEvent{change},
			Ledger: ledger,
			Hypotheses: []domain.CauseHypothesis{{
				ID: "hyp-1", Code: "release_correlation", Statement: "candidate", Verdict: domain.CauseVerdictSupportedCandidate,
				Confidence: .85, ConfidenceMethod: domain.CauseConfidenceMethod,
				SupportEntryIDs: supportIDs, CounterEntryIDs: counterIDs, Limitations: []string{"correlation is not causation"},
			}},
		},
	}
	evidence := []domain.Evidence{current, baseline}
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err != nil {
		t.Fatalf("valid cause ledger rejected: %v", err)
	}

	originalLedger := report.CauseAnalysis.Ledger
	orphan := originalLedger[0]
	orphan.ID = "test-orphan-unknown"
	orphan.Result = domain.EvidenceTestUnknown
	report.CauseAnalysis.Ledger = append(append([]domain.EvidenceLedgerEntry(nil), originalLedger...), orphan)
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("orphan UNKNOWN ledger entry was accepted")
	}
	report.CauseAnalysis.Ledger = originalLedger

	report.CauseAnalysis.Ledger[0].Weight = .26
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("cause ledger with a modified fixed weight was accepted")
	}
	report.CauseAnalysis.Ledger[0].Weight = .25
	report.CauseAnalysis.Ledger[0].Weight = math.NaN()
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("cause ledger with NaN weight was accepted")
	}
	report.CauseAnalysis.Ledger[0].Weight = .25

	report.CauseAnalysis.Hypotheses[0].Confidence = math.NaN()
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("cause hypothesis with NaN confidence was accepted")
	}
	report.CauseAnalysis.Hypotheses[0].Confidence = .85

	changes := report.CauseAnalysis.Changes
	sourceVersion := report.CauseAnalysis.SourceVersion
	report.CauseAnalysis.Changes = nil
	report.CauseAnalysis.SourceVersion = ""
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("supported change hypothesis without a stored change was accepted")
	}
	report.CauseAnalysis.Changes = changes
	report.CauseAnalysis.SourceVersion = sourceVersion

	report.CauseAnalysis.Hypotheses = nil
	report.CauseAnalysis.Ledger = nil
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("stored change without its fixed hypothesis tests was accepted")
	}
	report.CauseAnalysis.Hypotheses = []domain.CauseHypothesis{{
		ID: "hyp-1", Code: "release_correlation", Statement: "candidate", Verdict: domain.CauseVerdictSupportedCandidate,
		Confidence: .85, ConfidenceMethod: domain.CauseConfidenceMethod,
		SupportEntryIDs: supportIDs, CounterEntryIDs: counterIDs, Limitations: []string{"correlation is not causation"},
	}}
	report.CauseAnalysis.Ledger = ledger

	report.CauseAnalysis.Hypotheses[0].SupportEntryIDs = supportIDs[1:]
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("cause hypothesis missing a fixed support test was accepted")
	}
	report.CauseAnalysis.Hypotheses[0].SupportEntryIDs = supportIDs

	report.CauseAnalysis.Hypotheses[0].Verdict = domain.CauseVerdictInconclusive
	if err := validateEngineOutput(domain.Job{InvestigationID: report.InvestigationID}, evidence, report); err == nil {
		t.Fatal("complete cause analysis with an inconclusive hypothesis was accepted")
	}
}

func TestValidateHardZeroOverlapRequiresCompleteComparableEvidence(t *testing.T) {
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	candidate := domain.ChangeEvent{
		ID: "chg-zero-overlap", ResourceID: "resource", Kind: domain.ChangeKindConfig,
		StartedAt: now.Add(-45 * time.Minute), CompletedAt: now.Add(-35 * time.Minute), Owner: "team", Summary: "config",
		AffectedInstances: []string{"pod-z"}, AffectedInstancesComplete: true,
	}
	current := domain.Evidence{
		ID: "ev-current", Name: "current", ResourceID: "resource", Complete: true, ErrorCount: 10,
		Instances: []domain.CountBucket{{Label: "pod-a", Count: 10}}, InstancesExhaustive: true,
	}
	baseline := domain.Evidence{ID: "ev-baseline", Name: "baseline", ResourceID: "resource", Complete: true}
	evidence := map[string]domain.Evidence{current.ID: current, baseline.ID: baseline}
	if err := validateHardZeroOverlap(candidate, evidence); err != nil {
		t.Fatalf("valid hard zero-overlap evidence rejected: %v", err)
	}

	current.InstancesExhaustive = false
	evidence[current.ID] = current
	if err := validateHardZeroOverlap(candidate, evidence); err == nil {
		t.Fatal("non-exhaustive Top-K was accepted as hard zero-overlap evidence")
	}
	current.InstancesExhaustive = true
	current.Instances[0].Redacted = true
	evidence[current.ID] = current
	if err := validateHardZeroOverlap(candidate, evidence); err == nil {
		t.Fatal("redacted labels were accepted as hard zero-overlap evidence")
	}
	current.Instances[0].Redacted = false
	evidence[current.ID] = current
	candidate.AffectedInstancesComplete = false
	if err := validateHardZeroOverlap(candidate, evidence); err == nil {
		t.Fatal("incomplete affected-instance set was accepted as hard zero-overlap evidence")
	}
}

func TestValidateEngineOutputRejectsBrokenCauseLedger(t *testing.T) {
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)
	evidence := domain.Evidence{ID: "ev-1", QueryID: "query-1", QuerySpecHash: "hash-1", Complete: true}
	base := domain.Report{
		InvestigationID: "inv-validation", Outcome: "spike_detected", GeneratedAt: now,
		Findings: []domain.Finding{{Code: "spike", Statement: "spike", Confidence: .9, Conclusive: true, EvidenceIDs: []string{evidence.ID}}},
		CauseAnalysis: &domain.CauseAnalysis{
			Status: domain.CauseAnalysisComplete,
			Hypotheses: []domain.CauseHypothesis{{
				ID: "hyp-1", Code: "candidate", Statement: "candidate", Verdict: domain.CauseVerdictSupportedCandidate,
				Confidence: .9, ConfidenceMethod: domain.CauseConfidenceMethod,
				SupportEntryIDs: []string{"missing"}, CounterEntryIDs: []string{"also-missing"}, Limitations: []string{"limited"},
			}},
		},
	}
	if err := validateEngineOutput(domain.Job{InvestigationID: base.InvestigationID}, []domain.Evidence{evidence}, base); err == nil {
		t.Fatal("broken or over-cap cause ledger was accepted")
	}
}
