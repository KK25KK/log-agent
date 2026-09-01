package application

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"logagent/internal/domain"
)

var allowedRunbookMissingInputs = map[string]struct{}{
	runbookMissingDimensionalEvidence: {},
	runbookMissingConclusiveSpike:     {},
	runbookMissingDeterministicAdvice: {},
	runbookMissingResourceIdentity:    {},
	runbookMissingSourceDisabled:      {},
	runbookMissingSourceAvailable:     {},
	runbookMissingValidSet:            {},
	runbookMissingCompleteSet:         {},
	runbookMissingUntruncatedSet:      {},
	runbookMissingMatch:               {},
}

func validateRunbookGuidance(guidance *domain.RunbookGuidance, evidence map[string]domain.Evidence, report domain.Report) error {
	if guidance == nil {
		return nil
	}
	if guidance.MethodVersion != domain.RunbookGuidanceVersion {
		return fmt.Errorf("unsupported method version %q", guidance.MethodVersion)
	}
	if !domain.ValidateRunbookGuidanceDataSource(guidance.DataSource) {
		return fmt.Errorf("unsupported runbook guidance data source %q", guidance.DataSource)
	}
	if len(guidance.Items) > domain.MaxRunbookEntries {
		return errors.New("runbook guidance exceeds bounded size")
	}
	if err := validateRunbookMissingInputs(guidance.MissingInputs); err != nil {
		return err
	}

	hasTrigger := reportHasConclusiveErrorSpike(report)
	if !hasTrigger {
		if guidance.Status != domain.RunbookGuidanceSkippedNoTrigger {
			return errors.New("runbook guidance was generated without a conclusive error spike")
		}
		if hasRunbookSourceOutput(guidance) || len(guidance.MissingInputs) != 0 {
			return errors.New("skipped runbook guidance contains source output")
		}
		return nil
	}
	if guidance.Status == domain.RunbookGuidanceSkippedNoTrigger {
		return errors.New("runbook guidance skipped despite a conclusive error spike")
	}
	if countOnlyEvidence(evidence) {
		if guidance.Status != domain.RunbookGuidanceInconclusive || hasRunbookSourceOutput(guidance) || !equalStrings(guidance.MissingInputs, []string{runbookMissingDimensionalEvidence}) {
			return errors.New("count-only runbook guidance must remain source-free and inconclusive")
		}
		return nil
	}

	resourceID, resourceOK := governedRunbookResourceFromMap(evidence)
	if !resourceOK {
		return validateUnavailableRunbookGuidance(guidance, runbookMissingResourceIdentity)
	}
	recommendations := governedRunbookRecommendations(evidence, report)
	if len(recommendations.codes) == 0 {
		return validateUnavailableRunbookGuidance(guidance, runbookMissingDeterministicAdvice)
	}
	codes := recommendations.codes
	if len(codes) > domain.MaxRunbookEntries {
		codes = codes[:domain.MaxRunbookEntries]
	}
	query := domain.RunbookQuery{
		ResourceID:          resourceID,
		RecommendationCodes: append([]string(nil), codes...),
		Limit:               domain.MaxRunbookEntries,
	}
	if err := domain.ValidateRunbookQuery(query); err != nil {
		return err
	}

	switch guidance.Status {
	case domain.RunbookGuidanceComplete:
		if guidance.SourceVersion == "" || !guidance.SourceComplete || guidance.SourceTruncated || len(guidance.Items) == 0 || len(guidance.MissingInputs) != 0 {
			return errors.New("complete runbook guidance has inconsistent source metadata")
		}
	case domain.RunbookGuidanceNoMatch:
		if guidance.SourceVersion == "" || !guidance.SourceComplete || guidance.SourceTruncated || len(guidance.Items) != 0 || len(guidance.MissingInputs) != 0 {
			return errors.New("no-match runbook guidance has inconsistent source metadata")
		}
	case domain.RunbookGuidanceInconclusive:
		if guidance.SourceVersion == "" || guidance.SourceComplete {
			return errors.New("inconclusive runbook guidance has inconsistent source metadata")
		}
		expected := expectedInconclusiveRunbookMissingInputs(guidance)
		if !equalStrings(guidance.MissingInputs, expected) {
			return errors.New("inconclusive runbook missing inputs do not match source coverage")
		}
	case domain.RunbookGuidanceUnavailable:
		return validateUnavailableRunbookGuidance(guidance, "")
	default:
		return fmt.Errorf("unsupported runbook guidance status %q", guidance.Status)
	}
	if err := domain.ValidateRunbookSourceVersion(guidance.SourceVersion); err != nil {
		return err
	}
	return validateRunbookGuidanceItems(guidance.Items, query, recommendations.evidenceByCode, report.GeneratedAt)
}

func validateUnavailableRunbookGuidance(guidance *domain.RunbookGuidance, requiredReason string) error {
	if guidance.Status != domain.RunbookGuidanceUnavailable {
		return errors.New("runbook guidance must be unavailable when governed inputs are missing")
	}
	if hasRunbookSourceOutput(guidance) {
		return errors.New("unavailable runbook guidance contains source output")
	}
	if len(guidance.MissingInputs) != 1 {
		return errors.New("unavailable runbook guidance requires one stable missing input")
	}
	reason := guidance.MissingInputs[0]
	if requiredReason != "" && reason != requiredReason {
		return fmt.Errorf("unavailable runbook guidance requires missing input %q", requiredReason)
	}
	if requiredReason == "" {
		switch reason {
		case runbookMissingResourceIdentity, runbookMissingSourceDisabled, runbookMissingSourceAvailable, runbookMissingValidSet:
			return nil
		default:
			return fmt.Errorf("unexpected unavailable runbook missing input %q for complete governed inputs", reason)
		}
	}
	switch reason {
	case runbookMissingDeterministicAdvice, runbookMissingResourceIdentity:
		return nil
	default:
		return fmt.Errorf("unsupported unavailable runbook missing input %q", reason)
	}
}

func validateRunbookGuidanceItems(items []domain.RunbookGuidanceItem, query domain.RunbookQuery, evidenceByCode map[string][]string, generatedAt time.Time) error {
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if err := domain.ValidateRunbookGuidanceItem(item); err != nil {
			return fmt.Errorf("invalid runbook guidance item %q: %w", item.EntryID, err)
		}
		if _, duplicate := seen[item.EntryID]; duplicate {
			return fmt.Errorf("duplicate runbook guidance entry ID %q", item.EntryID)
		}
		seen[item.EntryID] = struct{}{}
		if index > 0 && !domain.RunbookGuidanceItemLess(items[index-1], item) {
			return errors.New("runbook guidance items are not deterministically ordered")
		}
		if item.UpdatedAt.After(generatedAt.Add(maxRunbookFutureSkew)) {
			return fmt.Errorf("runbook guidance item %q has a future update time", item.EntryID)
		}
		entry := domain.RunbookEntry{
			ID:                         item.EntryID,
			Revision:                   item.Revision,
			ResourceID:                 query.ResourceID,
			Title:                      item.Title,
			OwnerTeam:                  item.Owner,
			UpdatedAt:                  item.UpdatedAt,
			MatchedRecommendationCodes: append([]string(nil), item.RecommendationCodes...),
			Steps:                      append([]domain.RunbookStep(nil), item.Steps...),
		}
		if err := domain.ValidateRunbookEntry(entry, query); err != nil {
			return fmt.Errorf("runbook guidance item %q is outside governed scope: %w", item.EntryID, err)
		}
		expectedFingerprint, err := domain.RunbookEntryFingerprint(entry)
		if err != nil {
			return err
		}
		if item.Fingerprint != expectedFingerprint {
			return fmt.Errorf("runbook guidance item %q has a fabricated fingerprint", item.EntryID)
		}
		expectedEvidence := runbookEvidenceUnion(item.RecommendationCodes, evidenceByCode)
		if !equalStrings(item.EvidenceIDs, expectedEvidence) {
			return fmt.Errorf("runbook guidance item %q has fabricated evidence references", item.EntryID)
		}
	}
	return nil
}

func validateRunbookMissingInputs(inputs []string) error {
	if !sort.StringsAreSorted(inputs) {
		return errors.New("runbook missing inputs are not deterministically ordered")
	}
	for index, input := range inputs {
		if _, allowed := allowedRunbookMissingInputs[input]; !allowed {
			return fmt.Errorf("unsupported runbook missing input %q", input)
		}
		if index > 0 && inputs[index-1] == input {
			return fmt.Errorf("duplicate runbook missing input %q", input)
		}
	}
	return nil
}

func expectedInconclusiveRunbookMissingInputs(guidance *domain.RunbookGuidance) []string {
	missing := make([]string, 0, 3)
	if !guidance.SourceComplete {
		missing = append(missing, runbookMissingCompleteSet)
	}
	if guidance.SourceTruncated {
		missing = append(missing, runbookMissingUntruncatedSet)
	}
	if len(guidance.Items) == 0 {
		missing = append(missing, runbookMissingMatch)
	}
	sort.Strings(missing)
	return missing
}

func governedRunbookResourceFromMap(evidence map[string]domain.Evidence) (string, bool) {
	current, _, ok := governedRunbookEvidencePair(evidence)
	if !ok || current.ResourceID == "" {
		return "", false
	}
	if err := domain.ValidateResourceID(current.ResourceID); err != nil {
		return "", false
	}
	return current.ResourceID, true
}

func hasRunbookSourceOutput(guidance *domain.RunbookGuidance) bool {
	return guidance.SourceVersion != "" || guidance.SourceComplete || guidance.SourceTruncated || len(guidance.Items) != 0
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
