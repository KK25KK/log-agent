package application

import (
	"context"
	"reflect"
	"testing"

	"logagent/internal/domain"
)

func TestRunbookServiceDegradesSourceContextErrorsWithActiveParent(t *testing.T) {
	for _, sourceErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(sourceErr.Error(), func(t *testing.T) {
			evidence, report := runbookServiceFixture()
			service := mustRunbookService(t, runbookSourceFunc(func(context.Context, domain.RunbookQuery) (domain.RunbookSet, error) {
				return domain.RunbookSet{}, sourceErr
			}))
			result, err := service.Enrich(runbookServiceContext(context.Background()), evidence, report)
			if err != nil {
				t.Fatalf("Enrich() error=%v, want fail-soft guidance", err)
			}
			if result.RunbookGuidance == nil || result.RunbookGuidance.Status != domain.RunbookGuidanceUnavailable ||
				!reflect.DeepEqual(result.RunbookGuidance.MissingInputs, []string{runbookMissingSourceAvailable}) {
				t.Fatalf("unexpected degraded guidance: %#v", result.RunbookGuidance)
			}
			assertRunbookReportPreserved(t, report, result)
		})
	}
}
