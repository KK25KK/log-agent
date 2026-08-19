package evaluation

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoadSyntheticV1ValidatesRequiredScenariosAndBoundary(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	if dataset.SchemaVersion != DatasetSchemaVersion || dataset.DatasetID != SyntheticDatasetID || dataset.DataSource != SyntheticDataSource {
		t.Fatalf("unexpected dataset identity: %#v", dataset)
	}
	if len(dataset.Cases) != 5 || !isSHA256(dataset.Fingerprint) {
		t.Fatalf("unexpected synthetic dataset: cases=%d fingerprint=%q", len(dataset.Cases), dataset.Fingerprint)
	}
	if dataset.RealIncidentCount != 0 || dataset.ExpertLabelCount != 0 || dataset.CredentialsRequired || dataset.ExternalNetworkCalls != 0 || dataset.ProductionClaimAllowed {
		t.Fatalf("synthetic boundary was weakened: %#v", dataset)
	}
	for _, evaluationCase := range dataset.Cases {
		if len(evaluationCase.Expected.Recommendations) == 0 {
			t.Fatalf("case %q has no expected recommendations", evaluationCase.ID)
		}
	}
}

func TestParseDatasetRejectsUnknownFieldsTrailingJSONAndDuplicateCases(t *testing.T) {
	payload, err := fixtureFiles.ReadFile("fixtures/synthetic-v1.json")
	if err != nil {
		t.Fatal(err)
	}

	unknown := bytes.Replace(payload, []byte(`"dataset_id": "synthetic-m5a-v1",`), []byte(`"dataset_id": "synthetic-m5a-v1", "unknown_contract": true,`), 1)
	if _, err := ParseDataset(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown JSON field error=%v", err)
	}

	if _, err := ParseDataset(append(append([]byte(nil), payload...), []byte(` {"second":true}`)...)); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("trailing JSON error=%v", err)
	}

	dataset, err := ParseDataset(payload)
	if err != nil {
		t.Fatal(err)
	}
	dataset.Cases[1].ID = dataset.Cases[0].ID
	if err := ValidateDataset(dataset); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate case error=%v", err)
	}
}

func TestValidateDatasetRejectsBoundaryAndFixedGateWeakening(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}

	dataset.RealIncidentCount = 1
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("synthetic dataset claimed a real incident")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.ExternalNetworkCalls = 1
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("synthetic dataset claimed a network call")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.Cases[0].Expected.ProviderAPICalls--
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("fixture lowered the fixed provider-call contract")
	}
	dataset, _ = LoadSyntheticV1()
	dataset.Cases[0].Expected.MaxProcessedBytes = MaxProcessedBytesPerCase + 1
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("fixture raised the fixed processed-byte ceiling")
	}
}

func TestIncompleteFixtureExhaustivenessMatchesEngineContract(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	result := dataset.Cases[2].Current
	result.ErrorPatternsExhaustive = false
	result.InstancesExhaustive = false
	if err := validateFixtureResult("incomplete", result); err != nil {
		t.Fatalf("engine-compatible incomplete Top-K fixture was rejected: %v", err)
	}

	result.ErrorPatternsExhaustive = true
	result.ErrorPatterns = result.ErrorPatterns[:1]
	if err := validateFixtureResult("incomplete", result); err == nil {
		t.Fatal("incomplete fixture claimed exhaustive coverage without covering the count")
	}
}

func TestValidateDatasetRejectsInvalidExpectedRecommendations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Dataset)
		wantErr string
	}{
		{
			name: "empty recommendation list",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations = nil
			},
			wantErr: "recommendations cannot be empty",
		},
		{
			name: "duplicate recommendation code",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations[1].Code = dataset.Cases[0].Expected.Recommendations[0].Code
			},
			wantErr: "recommendation code \"inspect_top_error_pattern\" is duplicated",
		},
		{
			name: "missing recommendation code",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations[0].Code = ""
			},
			wantErr: "code is missing or invalid",
		},
		{
			name: "empty evidence names",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations[0].EvidenceNames = nil
			},
			wantErr: "evidence names cannot be empty",
		},
		{
			name: "unsupported evidence name",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations[0].EvidenceNames = []string{"current", "other"}
			},
			wantErr: "unsupported evidence name \"other\"",
		},
		{
			name: "duplicate evidence name",
			mutate: func(dataset *Dataset) {
				dataset.Cases[0].Expected.Recommendations[0].EvidenceNames = []string{"current", "current"}
			},
			wantErr: "duplicates evidence name \"current\"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataset, err := LoadSyntheticV1()
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&dataset)
			if err := ValidateDataset(dataset); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateDataset() error=%v, want substring %q", err, test.wantErr)
			}
		})
	}
}
