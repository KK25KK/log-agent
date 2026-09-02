package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"logagent/internal/domain"
	"logagent/internal/ports"
)

// Worker claims and executes one durable investigation job at a time.
type Worker struct {
	store         ports.Store
	engine        ports.InvestigationEngine
	workerID      string
	leaseDuration time.Duration
	now           func() time.Time
	runbook       *RunbookService
	summary       *SummaryService
	codeEvidence  *CodeEvidenceService
}

type WorkerOption func(*Worker)

// WithWorkerClock provides a deterministic clock for tests and local demos.
func WithWorkerClock(now func() time.Time) WorkerOption {
	return func(worker *Worker) {
		if now != nil {
			worker.now = now
		}
	}
}

func WithWorkerSummary(service *SummaryService) WorkerOption {
	return func(worker *Worker) {
		worker.summary = service
	}
}

// WithWorkerCodeEvidence enables the governed post-Trace deployment and local
// code evidence stage. It remains outside the Eino graph and LLM summary.
func WithWorkerCodeEvidence(service *CodeEvidenceService) WorkerOption {
	return func(worker *Worker) {
		worker.codeEvidence = service
	}
}

// WithWorkerRunbook enables optional post-engine, human-review-only SOP
// guidance. The service is deliberately separate from the Eino graph.
func WithWorkerRunbook(service *RunbookService) WorkerOption {
	return func(worker *Worker) {
		worker.runbook = service
	}
}

func NewWorker(store ports.Store, engine ports.InvestigationEngine, workerID string, leaseDuration time.Duration, options ...WorkerOption) (*Worker, error) {
	if workerID == "" {
		return nil, fmt.Errorf("worker ID is required")
	}
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}
	worker := &Worker{
		store:         store,
		engine:        engine,
		workerID:      workerID,
		leaseDuration: leaseDuration,
		now:           time.Now,
	}
	for _, option := range options {
		option(worker)
	}
	return worker, nil
}

// RunOne returns false when no runnable job exists.
func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	job, ok, err := w.store.ClaimNext(ctx, w.workerID, w.now().UTC(), w.leaseDuration)
	if err != nil || !ok {
		return ok, err
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	runCtx = withRunJob(runCtx, job)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := w.startHeartbeat(runCtx, cancelRun, stopHeartbeat, job)
	evidence, report, runErr := w.engine.Run(runCtx, job.InvestigationID, job.Request)
	if runErr == nil && report.RunbookGuidance != nil {
		runErr = errors.New("investigation engine returned runbook guidance before governed post-processing")
	}
	if runErr == nil && report.CodeInvestigation != nil {
		runErr = errors.New("investigation engine returned code evidence before governed post-processing")
	}
	if runErr == nil {
		runErr = validateEngineOutput(job, evidence, report)
	}
	isTraceInvestigation := job.Request.TemplateID == domain.TraceSearchTemplateID
	if runErr == nil && w.codeEvidence != nil && isTraceInvestigation {
		report = w.codeEvidence.Enrich(runCtx, job.Request, report)
		runErr = validateEngineOutput(job, evidence, report)
	}
	if runErr == nil && w.runbook != nil && !isTraceInvestigation {
		report, runErr = w.runbook.Enrich(runCtx, evidence, report)
		if runErr == nil {
			runErr = validateEngineOutput(job, evidence, report)
		}
	}
	if runErr == nil && w.summary != nil && !isTraceInvestigation {
		report = w.summary.Enrich(runCtx, job.Request.Requester, evidence, report)
		runErr = validateEngineOutput(job, evidence, report)
	}
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatDone
	cancelRun()
	if heartbeatErr != nil {
		if errors.Is(heartbeatErr, ports.ErrLeaseLost) && w.investigationCancelled(job.InvestigationID) {
			return true, nil
		}
		return true, fmt.Errorf("maintain job lease: %w", heartbeatErr)
	}
	if ctx.Err() != nil {
		// On process shutdown the RUNNING lease is deliberately left for another
		// worker to reclaim. Writing with the cancelled context cannot be durable.
		return true, ctx.Err()
	}
	finishedAt := w.now().UTC()
	if runErr != nil {
		if errors.Is(runErr, ports.ErrExternalOutcomeUnknown) {
			// The request context may already be tied to a shutting-down HTTP or
			// worker process. Persist this fail-closed state with a short,
			// independent context and a stable reason code rather than raw SDK
			// output. No automatic retry is allowed from NEEDS_REVIEW.
			finishCtx, cancelFinish := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelFinish()
			if finishErr := w.store.FinishNeedsReview(finishCtx, job, domain.ReviewReasonExternalQueryOutcomeUnknown, finishedAt); finishErr != nil {
				if errors.Is(finishErr, ports.ErrLeaseLost) && w.investigationCancelled(job.InvestigationID) {
					return true, nil
				}
				return true, fmt.Errorf("mark investigation for review: %w", finishErr)
			}
			return true, fmt.Errorf("investigation requires operator review: %w", ports.ErrExternalOutcomeUnknown)
		}
		failureCause := runErr.Error()
		if stableCode, ok := ports.QueryStepFailureCode(runErr); ok {
			failureCause = stableCode
		}
		if finishErr := w.store.FinishFailure(ctx, job, failureCause, finishedAt); finishErr != nil {
			if errors.Is(finishErr, ports.ErrLeaseLost) && w.investigationCancelled(job.InvestigationID) {
				return true, nil
			}
			return true, fmt.Errorf("run investigation: %v; persist failure: %w", runErr, finishErr)
		}
		return true, runErr
	}

	if err := w.store.FinishSuccess(ctx, job, evidence, report, finishedAt); err != nil {
		if errors.Is(err, ports.ErrLeaseLost) && w.investigationCancelled(job.InvestigationID) {
			return true, nil
		}
		return true, fmt.Errorf("persist investigation success: %w", err)
	}
	return true, nil
}

func (w *Worker) startHeartbeat(ctx context.Context, cancelRun context.CancelFunc, stop <-chan struct{}, job domain.Job) <-chan error {
	done := make(chan error, 1)
	interval := w.leaseDuration / 3
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	if interval <= 0 {
		interval = time.Nanosecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-ctx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := w.store.RenewLease(ctx, job, w.now().UTC(), w.leaseDuration); err != nil {
					cancelRun()
					done <- err
					return
				}
			}
		}
	}()
	return done
}

func (w *Worker) investigationCancelled(investigationID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	item, err := w.store.GetInvestigation(ctx, investigationID)
	return err == nil && item.Status == domain.StatusCancelled
}

func validateEngineOutput(job domain.Job, evidence []domain.Evidence, report domain.Report) error {
	return ValidateEngineOutput(job.InvestigationID, evidence, report)
}

// ValidateEngineOutput is the shared production trust boundary used by the
// Worker and the offline evaluation gate. Keeping one validator prevents a
// synthetic score from accepting output that the durable Worker would reject.
func ValidateEngineOutput(investigationID string, evidence []domain.Evidence, report domain.Report) error {
	if report.InvestigationID != investigationID {
		return fmt.Errorf("engine report investigation ID %q does not match job %q", report.InvestigationID, investigationID)
	}
	if len(evidence) == 0 {
		return errors.New("engine returned no evidence")
	}
	if report.Outcome == "" || report.GeneratedAt.IsZero() {
		return errors.New("engine returned an incomplete report envelope")
	}
	knownEvidence := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		if item.ID == "" || item.QueryID == "" || item.QuerySpecHash == "" {
			return fmt.Errorf("engine returned untraceable evidence %q", item.ID)
		}
		if item.ErrorCount < 0 || item.TopErrorCount < 0 || item.TopErrorCount > item.ErrorCount {
			return fmt.Errorf("engine returned inconsistent evidence %q", item.ID)
		}
		if item.TemplateID == domain.ErrorAnalysisTemplateID {
			if err := validateAnalysisEvidence(item); err != nil {
				return fmt.Errorf("engine returned invalid analysis evidence %q: %w", item.ID, err)
			}
		} else if item.TemplateID == domain.ErrorCountTemplateID {
			if err := validateCountEvidence(item); err != nil {
				return fmt.Errorf("engine returned invalid count-only evidence %q: %w", item.ID, err)
			}
		} else if item.TemplateID == domain.TraceSearchTemplateID {
			if err := validateTraceEvidence(item); err != nil {
				return fmt.Errorf("engine returned invalid Trace evidence %q: %w", item.ID, err)
			}
		} else if item.TemplateID != "" {
			return fmt.Errorf("engine returned unknown evidence template %q", item.TemplateID)
		}
		if _, duplicate := knownEvidence[item.ID]; duplicate {
			return fmt.Errorf("engine returned duplicate evidence ID %q", item.ID)
		}
		knownEvidence[item.ID] = item
	}
	if len(report.Findings) == 0 {
		return errors.New("engine returned no findings")
	}
	for _, finding := range report.Findings {
		if finding.Code == "" || finding.Statement == "" || invalidFiniteRange(finding.Confidence, 0, 1) {
			return errors.New("engine returned an invalid finding")
		}
		if len(finding.EvidenceIDs) == 0 {
			return errors.New("engine returned a finding without evidence")
		}
		for _, evidenceID := range finding.EvidenceIDs {
			item, exists := knownEvidence[evidenceID]
			if !exists {
				return fmt.Errorf("finding references unknown evidence %q", evidenceID)
			}
			if finding.Conclusive && (!item.Complete || item.Truncated) {
				return fmt.Errorf("conclusive finding references incomplete evidence %q", evidenceID)
			}
		}
	}
	for _, recommendation := range report.Recommendations {
		if recommendation.Code == "" || recommendation.Statement == "" || len(recommendation.EvidenceIDs) == 0 {
			return errors.New("engine returned an invalid recommendation")
		}
		for _, evidenceID := range recommendation.EvidenceIDs {
			if _, exists := knownEvidence[evidenceID]; !exists {
				return fmt.Errorf("recommendation references unknown evidence %q", evidenceID)
			}
		}
	}
	if err := validateRunbookGuidance(report.RunbookGuidance, knownEvidence, report); err != nil {
		return fmt.Errorf("engine returned invalid runbook guidance: %w", err)
	}
	if err := validateCauseAnalysis(report.CauseAnalysis, knownEvidence); err != nil {
		return fmt.Errorf("engine returned invalid cause analysis: %w", err)
	}
	if err := validateIncidentTimeline(report.IncidentTimeline, knownEvidence, report.CauseAnalysis); err != nil {
		return fmt.Errorf("engine returned invalid incident timeline: %w", err)
	}
	if err := validateTraceInvestigation(report.TraceInvestigation, knownEvidence, report); err != nil {
		return fmt.Errorf("engine returned invalid Trace investigation: %w", err)
	}
	if err := validateCodeInvestigation(report.CodeInvestigation, report.TraceInvestigation); err != nil {
		return fmt.Errorf("engine returned invalid code investigation: %w", err)
	}
	if report.Summary != nil {
		if err := ValidateReportSummary(report, *report.Summary); err != nil {
			return fmt.Errorf("engine returned invalid report summary: %w", err)
		}
	}
	return nil
}

func validateCauseAnalysis(analysis *domain.CauseAnalysis, evidence map[string]domain.Evidence) error {
	if analysis == nil {
		return nil
	}
	switch analysis.Status {
	case domain.CauseAnalysisComplete, domain.CauseAnalysisInconclusive, domain.CauseAnalysisUnavailable, domain.CauseAnalysisSkippedNoSpike:
	default:
		return fmt.Errorf("unknown status %q", analysis.Status)
	}
	if len(analysis.Changes) > domain.MaxChangeEvents || len(analysis.Hypotheses) > domain.MaxChangeEvents || len(analysis.Ledger) > domain.MaxChangeEvents*7 {
		return errors.New("cause analysis exceeds the fixed collection limits")
	}
	if (analysis.Status == domain.CauseAnalysisUnavailable || analysis.Status == domain.CauseAnalysisSkippedNoSpike) && (len(analysis.Changes) != 0 || len(analysis.Hypotheses) != 0 || len(analysis.Ledger) != 0) {
		return errors.New("unavailable or skipped analysis contains change evidence")
	}
	if analysis.Status == domain.CauseAnalysisComplete && len(analysis.MissingInputs) != 0 {
		return errors.New("complete cause analysis contains missing inputs")
	}

	governedResources := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if item.ResourceID != "" {
			governedResources[item.ResourceID] = struct{}{}
		}
	}
	if len(analysis.Changes) > 0 && analysis.SourceVersion == "" {
		return errors.New("change source version is missing")
	}
	if analysis.SourceVersion != "" {
		if err := domain.ValidateChangeSourceVersion(analysis.SourceVersion); err != nil {
			return err
		}
	}
	changeIDs := make(map[string]struct{}, len(analysis.Changes))
	changesByID := make(map[string]domain.ChangeEvent, len(analysis.Changes))
	for _, change := range analysis.Changes {
		if err := domain.ValidateChangeEvent(change); err != nil {
			return fmt.Errorf("invalid change event %q: %w", change.ID, err)
		}
		if _, governed := governedResources[change.ResourceID]; !governed {
			return fmt.Errorf("change %q references an ungoverned resource", change.ID)
		}
		if _, duplicate := changeIDs[change.ID]; duplicate {
			return fmt.Errorf("duplicate change event ID %q", change.ID)
		}
		changeIDs[change.ID] = struct{}{}
		changesByID[change.ID] = change
	}

	ledger := make(map[string]domain.EvidenceLedgerEntry, len(analysis.Ledger))
	for _, entry := range analysis.Ledger {
		if entry.ID == "" || entry.HypothesisID == "" || entry.Code == "" || entry.Statement == "" || invalidFiniteRange(entry.Weight, 0, 1) || entry.Weight == 0 {
			return errors.New("invalid evidence-ledger entry")
		}
		if entry.Role != domain.EvidenceTestSupport && entry.Role != domain.EvidenceTestCounter {
			return fmt.Errorf("ledger entry %q has invalid role", entry.ID)
		}
		if entry.Result != domain.EvidenceTestPass && entry.Result != domain.EvidenceTestFail && entry.Result != domain.EvidenceTestUnknown {
			return fmt.Errorf("ledger entry %q has invalid result", entry.ID)
		}
		if len(entry.EvidenceIDs) == 0 && len(entry.ChangeEventIDs) == 0 {
			return fmt.Errorf("ledger entry %q has no references", entry.ID)
		}
		for _, evidenceID := range entry.EvidenceIDs {
			if _, exists := evidence[evidenceID]; !exists {
				return fmt.Errorf("ledger entry %q references unknown evidence %q", entry.ID, evidenceID)
			}
		}
		for _, changeID := range entry.ChangeEventIDs {
			if _, exists := changeIDs[changeID]; !exists {
				return fmt.Errorf("ledger entry %q references unknown change %q", entry.ID, changeID)
			}
		}
		if _, duplicate := ledger[entry.ID]; duplicate {
			return fmt.Errorf("duplicate ledger entry ID %q", entry.ID)
		}
		ledger[entry.ID] = entry
	}

	hypothesisIDs := make(map[string]struct{}, len(analysis.Hypotheses))
	type testRule struct {
		role   domain.EvidenceTestRole
		weight float64
	}
	requiredTests := map[string]testRule{
		"error_spike":                     {role: domain.EvidenceTestSupport, weight: .25},
		"temporal_precedence":             {role: domain.EvidenceTestSupport, weight: .20},
		"affected_instance_concentration": {role: domain.EvidenceTestSupport, weight: .30},
		"baseline_shift":                  {role: domain.EvidenceTestSupport, weight: .10},
		"no_instance_overlap":             {role: domain.EvidenceTestCounter, weight: .40},
		"preexisting_concentration":       {role: domain.EvidenceTestCounter, weight: .15},
		"confounding_changes":             {role: domain.EvidenceTestCounter, weight: .10},
	}
	hasInconclusiveHypothesis := false
	hypothesisChangeIDs := make(map[string]struct{}, len(analysis.Hypotheses))
	referencedLedgerIDs := make(map[string]struct{}, len(analysis.Ledger))
	for _, hypothesis := range analysis.Hypotheses {
		if hypothesis.ID == "" || hypothesis.Code == "" || hypothesis.Statement == "" || hypothesis.ConfidenceMethod != domain.CauseConfidenceMethod || invalidFiniteRange(hypothesis.Confidence, 0, domain.CauseConfidenceCap) || len(hypothesis.Limitations) == 0 {
			return errors.New("invalid cause hypothesis")
		}
		if hypothesis.Verdict != domain.CauseVerdictSupportedCandidate && hypothesis.Verdict != domain.CauseVerdictRefuted && hypothesis.Verdict != domain.CauseVerdictInconclusive {
			return fmt.Errorf("hypothesis %q has invalid verdict", hypothesis.ID)
		}
		if hypothesis.Verdict == domain.CauseVerdictInconclusive {
			hasInconclusiveHypothesis = true
		}
		if _, duplicate := hypothesisIDs[hypothesis.ID]; duplicate {
			return fmt.Errorf("duplicate hypothesis ID %q", hypothesis.ID)
		}
		hypothesisIDs[hypothesis.ID] = struct{}{}
		if len(hypothesis.SupportEntryIDs) == 0 || len(hypothesis.CounterEntryIDs) == 0 {
			return fmt.Errorf("hypothesis %q lacks support or counter tests", hypothesis.ID)
		}
		seenTestCodes := make(map[string]domain.EvidenceTestResult, len(requiredTests))
		seenEntries := make(map[string]domain.EvidenceLedgerEntry, len(requiredTests))
		for _, entryID := range hypothesis.SupportEntryIDs {
			if _, duplicate := referencedLedgerIDs[entryID]; duplicate {
				return fmt.Errorf("ledger entry %q is referenced more than once", entryID)
			}
			referencedLedgerIDs[entryID] = struct{}{}
			entry, exists := ledger[entryID]
			if !exists || entry.HypothesisID != hypothesis.ID || entry.Role != domain.EvidenceTestSupport {
				return fmt.Errorf("hypothesis %q has invalid support entry %q", hypothesis.ID, entryID)
			}
			if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate {
				if entry.Result != domain.EvidenceTestPass {
					return fmt.Errorf("supported hypothesis %q has a non-passing support test", hypothesis.ID)
				}
				for _, evidenceID := range entry.EvidenceIDs {
					item := evidence[evidenceID]
					if !item.Complete || item.Truncated {
						return fmt.Errorf("supported hypothesis %q references incomplete evidence %q", hypothesis.ID, evidenceID)
					}
				}
			}
			rule, required := requiredTests[entry.Code]
			if !required || rule.role != entry.Role || math.Abs(rule.weight-entry.Weight) > 0.0001 {
				return fmt.Errorf("hypothesis %q has unexpected support test %q", hypothesis.ID, entry.Code)
			}
			if _, duplicate := seenTestCodes[entry.Code]; duplicate {
				return fmt.Errorf("hypothesis %q repeats test %q", hypothesis.ID, entry.Code)
			}
			seenTestCodes[entry.Code] = entry.Result
			seenEntries[entry.Code] = entry
		}
		for _, entryID := range hypothesis.CounterEntryIDs {
			if _, duplicate := referencedLedgerIDs[entryID]; duplicate {
				return fmt.Errorf("ledger entry %q is referenced more than once", entryID)
			}
			referencedLedgerIDs[entryID] = struct{}{}
			entry, exists := ledger[entryID]
			if !exists || entry.HypothesisID != hypothesis.ID || entry.Role != domain.EvidenceTestCounter {
				return fmt.Errorf("hypothesis %q has invalid counter entry %q", hypothesis.ID, entryID)
			}
			if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && entry.Result != domain.EvidenceTestFail {
				return fmt.Errorf("supported hypothesis %q has unresolved counterevidence", hypothesis.ID)
			}
			rule, required := requiredTests[entry.Code]
			if !required || rule.role != entry.Role || math.Abs(rule.weight-entry.Weight) > 0.0001 {
				return fmt.Errorf("hypothesis %q has unexpected counter test %q", hypothesis.ID, entry.Code)
			}
			if _, duplicate := seenTestCodes[entry.Code]; duplicate {
				return fmt.Errorf("hypothesis %q repeats test %q", hypothesis.ID, entry.Code)
			}
			seenTestCodes[entry.Code] = entry.Result
			seenEntries[entry.Code] = entry
		}
		if len(seenTestCodes) != len(requiredTests) {
			return fmt.Errorf("hypothesis %q does not contain the fixed seven tests", hypothesis.ID)
		}
		candidate, err := validateHypothesisReferences(hypothesis, seenEntries, changesByID, evidence)
		if err != nil {
			return err
		}
		if _, duplicate := hypothesisChangeIDs[candidate.ID]; duplicate {
			return fmt.Errorf("multiple hypotheses reference change %q", candidate.ID)
		}
		hypothesisChangeIDs[candidate.ID] = struct{}{}
		if expected := causeConfidenceForHypothesis(hypothesis, ledger); math.Abs(expected-hypothesis.Confidence) > 0.0001 {
			return fmt.Errorf("hypothesis %q confidence %.2f does not match ledger %.2f", hypothesis.ID, hypothesis.Confidence, expected)
		}
		supportsAll := true
		countersAllFail := true
		for code, rule := range requiredTests {
			result := seenTestCodes[code]
			if rule.role == domain.EvidenceTestSupport && result != domain.EvidenceTestPass {
				supportsAll = false
			}
			if rule.role == domain.EvidenceTestCounter && result != domain.EvidenceTestFail {
				countersAllFail = false
			}
		}
		if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && (!supportsAll || !countersAllFail) {
			return fmt.Errorf("supported hypothesis %q does not satisfy the fixed verdict rule", hypothesis.ID)
		}
		hasUnknown := false
		for _, result := range seenTestCodes {
			hasUnknown = hasUnknown || result == domain.EvidenceTestUnknown
		}
		if hypothesis.Verdict != domain.CauseVerdictInconclusive && (hasUnknown || seenTestCodes["confounding_changes"] == domain.EvidenceTestPass) {
			return fmt.Errorf("conclusive hypothesis %q contains unknown or confounded evidence", hypothesis.ID)
		}
		if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate {
			if err := validateSupportedChangeCandidate(candidate, analysis.Changes, evidence); err != nil {
				return fmt.Errorf("supported hypothesis %q: %w", hypothesis.ID, err)
			}
		}
		if hypothesis.Verdict == domain.CauseVerdictRefuted {
			if seenTestCodes["no_instance_overlap"] != domain.EvidenceTestPass {
				return fmt.Errorf("refuted hypothesis %q lacks the hard zero-overlap counterevidence", hypothesis.ID)
			}
			if err := validateHardZeroOverlap(candidate, evidence); err != nil {
				return fmt.Errorf("refuted hypothesis %q: %w", hypothesis.ID, err)
			}
		}
		if hypothesis.Verdict == domain.CauseVerdictSupportedCandidate && analysis.Status != domain.CauseAnalysisComplete {
			return fmt.Errorf("supported hypothesis %q is not in a complete analysis", hypothesis.ID)
		}
	}
	if analysis.Status == domain.CauseAnalysisComplete && len(analysis.Hypotheses) == 0 {
		return errors.New("complete cause analysis has no hypotheses")
	}
	if analysis.Status == domain.CauseAnalysisComplete && hasInconclusiveHypothesis {
		return errors.New("complete cause analysis contains an inconclusive hypothesis")
	}
	if len(analysis.Ledger) != len(analysis.Hypotheses)*7 || len(referencedLedgerIDs) != len(ledger) {
		return errors.New("cause ledger is not covered exactly once by the fixed hypothesis tests")
	}
	if len(hypothesisChangeIDs) != len(changeIDs) {
		return errors.New("cause hypotheses do not cover the stored change events exactly once")
	}
	for _, entry := range analysis.Ledger {
		if _, exists := hypothesisIDs[entry.HypothesisID]; !exists {
			return fmt.Errorf("ledger entry %q references unknown hypothesis %q", entry.ID, entry.HypothesisID)
		}
	}
	return nil
}

func validateHypothesisReferences(
	hypothesis domain.CauseHypothesis,
	entries map[string]domain.EvidenceLedgerEntry,
	changes map[string]domain.ChangeEvent,
	evidence map[string]domain.Evidence,
) (domain.ChangeEvent, error) {
	current, baseline, err := causeObservationPair(evidence)
	if err != nil {
		return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q: %w", hypothesis.ID, err)
	}
	expectedEvidence := map[string][]string{
		"error_spike":                     {current.ID, baseline.ID},
		"temporal_precedence":             {current.ID, baseline.ID},
		"affected_instance_concentration": {current.ID},
		"baseline_shift":                  {current.ID, baseline.ID},
		"no_instance_overlap":             {current.ID},
		"preexisting_concentration":       {baseline.ID},
		"confounding_changes":             {current.ID, baseline.ID},
	}
	for code, expected := range expectedEvidence {
		entry := entries[code]
		if !sameIDSet(entry.EvidenceIDs, expected) {
			return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q test %q has incorrect evidence references", hypothesis.ID, code)
		}
	}
	if len(entries["error_spike"].ChangeEventIDs) != 0 {
		return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q spike test must not invent a change reference", hypothesis.ID)
	}

	candidateCodes := []string{
		"temporal_precedence",
		"affected_instance_concentration",
		"baseline_shift",
		"no_instance_overlap",
		"preexisting_concentration",
	}
	candidateID := ""
	for _, code := range candidateCodes {
		ids := entries[code].ChangeEventIDs
		if len(ids) != 1 {
			return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q test %q must reference exactly one change", hypothesis.ID, code)
		}
		if candidateID == "" {
			candidateID = ids[0]
		} else if candidateID != ids[0] {
			return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q mixes change references", hypothesis.ID)
		}
	}
	candidate, exists := changes[candidateID]
	if !exists {
		return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q references unknown candidate change %q", hypothesis.ID, candidateID)
	}
	allChangeIDs := make([]string, 0, len(changes))
	for changeID := range changes {
		allChangeIDs = append(allChangeIDs, changeID)
	}
	if !sameIDSet(entries["confounding_changes"].ChangeEventIDs, allChangeIDs) {
		return domain.ChangeEvent{}, fmt.Errorf("hypothesis %q confounding test does not cover the complete change set", hypothesis.ID)
	}
	return candidate, nil
}

func validateSupportedChangeCandidate(candidate domain.ChangeEvent, changes []domain.ChangeEvent, evidence map[string]domain.Evidence) error {
	if len(changes) != 1 {
		return errors.New("a supported candidate requires exactly one unconfounded change")
	}
	current, baseline, err := causeObservationPair(evidence)
	if err != nil {
		return err
	}
	if candidate.ResourceID != current.ResourceID || candidate.ResourceID != baseline.ResourceID {
		return errors.New("candidate resource does not match current and baseline evidence")
	}
	if candidate.CompletedAt.Before(baseline.StartTime) || !candidate.CompletedAt.Before(current.StartTime) {
		return errors.New("candidate does not satisfy temporal precedence")
	}
	currentCount, err := affectedErrorCount(current, candidate)
	if err != nil {
		return err
	}
	baselineCount, err := affectedErrorCount(baseline, candidate)
	if err != nil {
		return err
	}
	currentShare := float64(currentCount) / float64(current.ErrorCount) * 100
	baselineShare := float64(baselineCount) / float64(baseline.ErrorCount) * 100
	if currentShare < 50 || currentShare-baselineShare < 20 || baselineShare >= 50 {
		return errors.New("instance concentration, baseline shift, or preexisting counterevidence does not satisfy the supported-candidate rule")
	}
	return nil
}

func validateHardZeroOverlap(candidate domain.ChangeEvent, evidence map[string]domain.Evidence) error {
	current, _, err := causeObservationPair(evidence)
	if err != nil {
		return err
	}
	if candidate.ResourceID != current.ResourceID {
		return errors.New("candidate resource does not match current evidence")
	}
	count, err := affectedErrorCount(current, candidate)
	if err != nil {
		return err
	}
	if count != 0 {
		return errors.New("current instance evidence is not actually zero-overlap")
	}
	return nil
}

func causeObservationPair(evidence map[string]domain.Evidence) (domain.Evidence, domain.Evidence, error) {
	var current, baseline domain.Evidence
	currentFound, baselineFound := false, false
	for _, item := range evidence {
		switch item.Name {
		case "current":
			if currentFound {
				return domain.Evidence{}, domain.Evidence{}, errors.New("duplicate current evidence")
			}
			current, currentFound = item, true
		case "baseline":
			if baselineFound {
				return domain.Evidence{}, domain.Evidence{}, errors.New("duplicate baseline evidence")
			}
			baseline, baselineFound = item, true
		}
	}
	if !currentFound || !baselineFound {
		return domain.Evidence{}, domain.Evidence{}, errors.New("current and baseline evidence are required")
	}
	return current, baseline, nil
}

func affectedErrorCount(evidence domain.Evidence, change domain.ChangeEvent) (int64, error) {
	if !evidence.Complete || evidence.Truncated || !evidence.InstancesExhaustive || evidence.ErrorCount <= 0 {
		return 0, errors.New("instance evidence is incomplete or non-exhaustive")
	}
	if !change.AffectedInstancesComplete || len(change.AffectedInstances) == 0 {
		return 0, errors.New("affected-instance set is incomplete or empty")
	}
	affected := make(map[string]struct{}, len(change.AffectedInstances))
	for _, instance := range change.AffectedInstances {
		affected[instance] = struct{}{}
	}
	var count int64
	for _, bucket := range evidence.Instances {
		if bucket.Redacted {
			return 0, errors.New("instance evidence contains redacted labels")
		}
		if _, exists := affected[bucket.Label]; exists {
			count += bucket.Count
		}
	}
	return count, nil
}

func sameIDSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	values := make(map[string]struct{}, len(actual))
	for _, id := range actual {
		if id == "" {
			return false
		}
		if _, duplicate := values[id]; duplicate {
			return false
		}
		values[id] = struct{}{}
	}
	for _, id := range expected {
		if _, exists := values[id]; !exists {
			return false
		}
	}
	return true
}

func causeConfidenceForHypothesis(hypothesis domain.CauseHypothesis, ledger map[string]domain.EvidenceLedgerEntry) float64 {
	var score float64
	for _, entryID := range append(append([]string(nil), hypothesis.SupportEntryIDs...), hypothesis.CounterEntryIDs...) {
		entry, exists := ledger[entryID]
		if !exists || entry.Result != domain.EvidenceTestPass {
			continue
		}
		if entry.Role == domain.EvidenceTestSupport {
			score += entry.Weight
		} else {
			score -= entry.Weight
		}
	}
	if score < 0 {
		score = 0
	}
	if score > domain.CauseConfidenceCap {
		score = domain.CauseConfidenceCap
	}
	return math.Round(score*100) / 100
}

func invalidFiniteRange(value, minimum, maximum float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0) || value < minimum || value > maximum
}

func validateAnalysisEvidence(item domain.Evidence) error {
	if item.TraceMember != nil {
		return errors.New("analysis evidence contains a Trace member projection")
	}
	if item.APICalls != domain.ErrorAnalysisAPICalls || item.PatternLimit != domain.ErrorAnalysisPatternLimit || item.InstanceLimit != domain.ErrorAnalysisInstanceLimit {
		return errors.New("fixed template call or bucket limits do not match")
	}
	patternTotal, err := validateEvidenceBuckets(item.ErrorPatterns, item.PatternLimit, item.ErrorCount)
	if err != nil {
		return fmt.Errorf("error patterns: %w", err)
	}
	instanceTotal, err := validateEvidenceBuckets(item.Instances, item.InstanceLimit, item.ErrorCount)
	if err != nil {
		return fmt.Errorf("instances: %w", err)
	}
	if item.ErrorCount > 0 && (len(item.ErrorPatterns) == 0 || len(item.Instances) == 0) {
		return errors.New("non-zero error count lacks aggregate buckets")
	}
	if (item.ErrorPatternsExhaustive && patternTotal != item.ErrorCount) || (item.InstancesExhaustive && instanceTotal != item.ErrorCount) {
		return errors.New("aggregate exhaustiveness is inconsistent")
	}
	if item.Complete && !item.Truncated && (item.ErrorPatternsExhaustive != (patternTotal == item.ErrorCount) || item.InstancesExhaustive != (instanceTotal == item.ErrorCount)) {
		return errors.New("complete aggregate exhaustiveness is inconsistent")
	}
	return nil
}

func validateCountEvidence(item domain.Evidence) error {
	if item.TraceMember != nil {
		return errors.New("count-only evidence contains a Trace member projection")
	}
	if item.APICalls != domain.ErrorCountAPICalls || item.PatternLimit != 0 || item.InstanceLimit != 0 {
		return errors.New("count-only call or bucket limits do not match")
	}
	if item.TopError != "" || item.TopErrorCount != 0 || len(item.ErrorPatterns) != 0 || len(item.Instances) != 0 || item.ErrorPatternsExhaustive || item.InstancesExhaustive {
		return errors.New("count-only evidence contains dimensional claims")
	}
	return nil
}

func validateEvidenceBuckets(buckets []domain.CountBucket, limit int, total int64) (int64, error) {
	if len(buckets) > limit {
		return 0, errors.New("bucket count exceeds fixed limit")
	}
	var sum int64
	for _, bucket := range buckets {
		if bucket.Label == "" || bucket.Count <= 0 || bucket.Count > total-sum {
			return 0, errors.New("bucket label or count is invalid")
		}
		sum += bucket.Count
	}
	return sum, nil
}
