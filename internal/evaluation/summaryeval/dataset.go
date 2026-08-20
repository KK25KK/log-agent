package summaryeval

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"logagent/internal/domain"
)

const (
	DatasetSchemaVersion = "summary-evaluation-dataset-v1"
	SyntheticDatasetID   = "synthetic-summary-v1"
	SyntheticDataSource  = "SYNTHETIC_MOCK"

	expectedCases = 9
)

type ProviderBehavior string

const (
	BehaviorValid                  ProviderBehavior = "VALID"
	BehaviorProviderError          ProviderBehavior = "PROVIDER_ERROR"
	BehaviorInventedEvidence       ProviderBehavior = "INVENTED_EVIDENCE"
	BehaviorInventedRecommendation ProviderBehavior = "INVENTED_RECOMMENDATION"
	BehaviorUnsupportedCause       ProviderBehavior = "UNSUPPORTED_CAUSE"
	BehaviorUnsafeAction           ProviderBehavior = "UNSAFE_ACTION"
)

type ReportMutation string

const (
	MutationNone             ReportMutation = "NONE"
	MutationSensitiveFinding ReportMutation = "SENSITIVE_FINDING"
)

//go:embed fixtures/synthetic-v1.json
var fixtureFiles embed.FS

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hashPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var fixedScenarios = map[string]Case{
	"valid-supported": {
		ID: "valid-supported", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorValid, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryGenerated, Mode: domain.SummaryModeMock, ProviderCalls: 1, CauseSelected: true},
	},
	"valid-no-spike": {
		ID: "valid-no-spike", BaseCaseID: "no-significant-spike", ProviderBehavior: BehaviorValid, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryGenerated, Mode: domain.SummaryModeMock, ProviderCalls: 1},
	},
	"valid-incomplete": {
		ID: "valid-incomplete", BaseCaseID: "incomplete-observation", ProviderBehavior: BehaviorValid, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryGenerated, Mode: domain.SummaryModeMock, ProviderCalls: 1},
	},
	"provider-error": {
		ID: "provider-error", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorProviderError, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 1, CauseSelected: true},
	},
	"invented-evidence": {
		ID: "invented-evidence", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorInventedEvidence, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 1, CauseSelected: true},
	},
	"invented-recommendation": {
		ID: "invented-recommendation", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorInventedRecommendation, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 1, CauseSelected: true},
	},
	"unsupported-cause": {
		ID: "unsupported-cause", BaseCaseID: "spike-refuted", ProviderBehavior: BehaviorUnsupportedCause, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 1},
	},
	"unsafe-action": {
		ID: "unsafe-action", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorUnsafeAction, ReportMutation: MutationNone,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 1, CauseSelected: true},
	},
	"sensitive-outbound": {
		ID: "sensitive-outbound", BaseCaseID: "spike-supported", ProviderBehavior: BehaviorValid, ReportMutation: MutationSensitiveFinding,
		Expected: Expected{Status: domain.SummaryFallback, Mode: domain.SummaryModeFallback, ProviderCalls: 0, CauseSelected: true},
	},
}

type Dataset struct {
	SchemaVersion          string `json:"schema_version"`
	DatasetID              string `json:"dataset_id"`
	DataSource             string `json:"data_source"`
	PromptVersion          string `json:"prompt_version"`
	MockPromptFingerprint  string `json:"mock_prompt_fingerprint"`
	RealIncidentCount      int    `json:"real_incident_count"`
	ExpertLabelCount       int    `json:"expert_label_count"`
	CredentialsRequired    bool   `json:"credentials_required"`
	ExternalNetworkCalls   int    `json:"external_network_calls"`
	ProductionClaimAllowed bool   `json:"production_claim_allowed"`
	Cases                  []Case `json:"cases"`

	Fingerprint string `json:"-"`
}

type Case struct {
	ID               string           `json:"id"`
	BaseCaseID       string           `json:"base_case_id"`
	ProviderBehavior ProviderBehavior `json:"provider_behavior"`
	ReportMutation   ReportMutation   `json:"report_mutation"`
	Expected         Expected         `json:"expected"`
}

type Expected struct {
	Status        domain.SummaryStatus `json:"status"`
	Mode          domain.SummaryMode   `json:"mode"`
	ProviderCalls int                  `json:"provider_calls"`
	CauseSelected bool                 `json:"cause_selected"`
}

func LoadSyntheticV1() (Dataset, error) {
	payload, err := fixtureFiles.ReadFile("fixtures/synthetic-v1.json")
	if err != nil {
		return Dataset{}, fmt.Errorf("read embedded summary evaluation dataset: %w", err)
	}
	return ParseDataset(payload)
}

func ParseDataset(payload []byte) (Dataset, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return Dataset{}, errors.New("summary evaluation dataset is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("decode summary evaluation dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Dataset{}, errors.New("decode summary evaluation dataset: trailing JSON value")
		}
		return Dataset{}, fmt.Errorf("decode summary evaluation dataset trailing content: %w", err)
	}
	if err := ValidateDataset(dataset); err != nil {
		return Dataset{}, err
	}
	fingerprint, err := normalizedFingerprint(dataset)
	if err != nil {
		return Dataset{}, err
	}
	dataset.Fingerprint = fingerprint
	return dataset, nil
}

func normalizedFingerprint(dataset Dataset) (string, error) {
	normalized := dataset
	normalized.Fingerprint = ""
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode normalized summary evaluation dataset: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != DatasetSchemaVersion || dataset.DatasetID != SyntheticDatasetID || dataset.DataSource != SyntheticDataSource {
		return errors.New("summary evaluation dataset identity is invalid")
	}
	if dataset.PromptVersion != domain.EvidenceSummaryPromptVersion {
		return fmt.Errorf("summary evaluation prompt version must be %q", domain.EvidenceSummaryPromptVersion)
	}
	if !hashPattern.MatchString(dataset.MockPromptFingerprint) {
		return errors.New("summary evaluation Mock Prompt fingerprint is invalid")
	}
	if dataset.RealIncidentCount != 0 || dataset.ExpertLabelCount != 0 || dataset.CredentialsRequired || dataset.ExternalNetworkCalls != 0 || dataset.ProductionClaimAllowed {
		return errors.New("summary evaluation dataset must remain synthetic, credential-free, offline, and non-production")
	}
	if len(dataset.Cases) != expectedCases {
		return fmt.Errorf("summary evaluation dataset must contain exactly %d cases", expectedCases)
	}
	seen := make(map[string]struct{}, len(dataset.Cases))
	for index, evaluationCase := range dataset.Cases {
		if !identifierPattern.MatchString(evaluationCase.ID) || !identifierPattern.MatchString(evaluationCase.BaseCaseID) {
			return fmt.Errorf("summary evaluation case %d has an invalid identity", index)
		}
		if _, duplicate := seen[evaluationCase.ID]; duplicate {
			return fmt.Errorf("summary evaluation case %q is duplicated", evaluationCase.ID)
		}
		seen[evaluationCase.ID] = struct{}{}
		if err := validateCase(evaluationCase); err != nil {
			return fmt.Errorf("summary evaluation case %q: %w", evaluationCase.ID, err)
		}
		fixed, exists := fixedScenarios[evaluationCase.ID]
		if !exists || evaluationCase != fixed {
			return fmt.Errorf("summary evaluation case %q does not match the fixed scenario contract", evaluationCase.ID)
		}
	}
	for required := range fixedScenarios {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("summary evaluation dataset is missing fixed scenario %q", required)
		}
	}
	return nil
}

func validateCase(evaluationCase Case) error {
	switch evaluationCase.ProviderBehavior {
	case BehaviorValid, BehaviorProviderError, BehaviorInventedEvidence, BehaviorInventedRecommendation, BehaviorUnsupportedCause, BehaviorUnsafeAction:
	default:
		return fmt.Errorf("unknown Provider behavior %q", evaluationCase.ProviderBehavior)
	}
	switch evaluationCase.ReportMutation {
	case MutationNone, MutationSensitiveFinding:
	default:
		return fmt.Errorf("unknown report mutation %q", evaluationCase.ReportMutation)
	}
	wantStatus := domain.SummaryFallback
	wantMode := domain.SummaryModeFallback
	wantCalls := 1
	if evaluationCase.ProviderBehavior == BehaviorValid && evaluationCase.ReportMutation == MutationNone {
		wantStatus = domain.SummaryGenerated
		wantMode = domain.SummaryModeMock
	}
	if evaluationCase.ReportMutation == MutationSensitiveFinding {
		if evaluationCase.ProviderBehavior != BehaviorValid {
			return errors.New("sensitive outbound scenario must use the valid Provider behavior")
		}
		wantCalls = 0
	}
	if evaluationCase.Expected.Status != wantStatus || evaluationCase.Expected.Mode != wantMode || evaluationCase.Expected.ProviderCalls != wantCalls {
		return errors.New("expected summary status, mode, or Provider calls do not match the fixed scenario contract")
	}
	return nil
}
