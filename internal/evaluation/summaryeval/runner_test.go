package summaryeval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"logagent/internal/domain"
)

func TestEvaluateFailsClosedWithoutLeakingExecutionError(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(context.Background(), dataset, func(context.Context, Case) (Observation, error) {
		return Observation{}, errors.New("Bearer secret-provider-error-123456")
	})
	if !errors.Is(err, ErrGateFailed) || report.Status != EvaluationFailed || report.Metrics.ExecutionFailures != len(dataset.Cases) {
		t.Fatalf("unexpected gate result: status=%q metrics=%#v err=%v", report.Status, report.Metrics, err)
	}
	payload, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(payload), "secret-provider-error") {
		t.Fatal("execution error leaked into evaluation report")
	}
	for _, result := range report.Cases {
		if len(result.FailureCodes) != 1 || result.FailureCodes[0] != "execution_error" {
			t.Fatalf("unsafe failure output: %#v", result)
		}
	}
}

func TestEvaluateRecomputesDatasetFingerprint(t *testing.T) {
	dataset, err := LoadSyntheticV1()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Fingerprint = ""
	report, err := Evaluate(context.Background(), dataset, func(context.Context, Case) (Observation, error) {
		return Observation{}, errors.New("expected failure")
	})
	if !errors.Is(err, ErrGateFailed) || report.DatasetFingerprint == "" || report.EvaluationRunID == "" {
		t.Fatalf("fingerprint was not safely recomputed: %#v err=%v", report, err)
	}
}

func TestObservationCannotBeSerialized(t *testing.T) {
	payload, err := json.Marshal(Observation{Requester: domain.Principal{AppID: "secret-app"}, ProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "{}" {
		t.Fatalf("observation leaked into JSON: %s", payload)
	}
}
