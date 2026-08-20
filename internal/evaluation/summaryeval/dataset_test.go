package summaryeval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadSyntheticV1IsStrictOfflineContract(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Fingerprint != "82e813aed0721f15b89a19b053da6b1d47509ab07f45122af4ed0c075e60a0b1" {
		t.Fatalf("fingerprint=%q", dataset.Fingerprint)
	}
	if len(dataset.Cases) != 9 || dataset.RealIncidentCount != 0 || dataset.ExpertLabelCount != 0 ||
		dataset.CredentialsRequired || dataset.ExternalNetworkCalls != 0 || dataset.ProductionClaimAllowed {
		t.Fatalf("synthetic contract was weakened: %#v", dataset)
	}
}

func TestParseDatasetRejectsUnknownOrTrailingJSON(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Fingerprint = ""
	payload, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(payload), `"schema_version":`, `"unknown":true,"schema_version":`, 1)
	if _, err := ParseDataset([]byte(unknown)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if _, err := ParseDataset(append(payload, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestValidateDatasetRejectsWeakenedCoverageAndExpectations(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Cases = dataset.Cases[:8]
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("missing sensitive-outbound scenario was accepted")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.Cases[0].Expected.ProviderCalls = 0
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("weakened provider-call expectation was accepted")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.Cases[0].Expected.CauseSelected = false
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("weakened cause-selection expectation was accepted")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.MockPromptFingerprint = strings.Repeat("0", 63)
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("invalid Mock Prompt fingerprint was accepted")
	}
}
