package jointrca

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestSyntheticJointRCAGatePassesWithoutRealClaims(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(dataset, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASSED" || report.Metrics.PassedCases != 8 || report.Metrics.UnsafeClaims != 0 || report.Metrics.AutomaticActions != 0 || report.DataBoundary.RealIncidentCount != 0 || report.DataBoundary.ExpertLabelCount != 0 || report.DataBoundary.ProductionClaimAllowed {
		t.Fatalf("unexpected evaluation report: %#v", report)
	}
}

func TestJointRCAGateDetectsExpectedLabelDrift(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Cases[0].ExpectedConfidence = .70
	report, err := Evaluate(dataset, time.Now())
	if !errors.Is(err, ErrGateFailed) || report.Status != "FAILED" || report.Metrics.CasePassRate >= 1 {
		t.Fatalf("drift did not fail the gate: report=%#v err=%v", report, err)
	}
}

func TestJointRCADatasetParserRejectsUnknownAndRealClaims(t *testing.T) {
	payload, err := fixtureFiles.ReadFile("fixtures/synthetic-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(payload, []byte(`"schema_version"`), []byte(`"unknown":true,"schema_version"`), 1)
	if _, err := ParseDataset(unknown); err == nil {
		t.Fatal("unknown dataset field was accepted")
	}
	realClaim := bytes.Replace(payload, []byte(`"real_incident_count": 0`), []byte(`"real_incident_count": 1`), 1)
	if _, err := ParseDataset(realClaim); err == nil {
		t.Fatal("synthetic dataset claimed a real incident")
	}
}
