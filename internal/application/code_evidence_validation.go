package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"logagent/internal/application/anchors"
	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

func validateDeploymentEvidence(query domain.DeploymentQuery, evidence domain.DeploymentEvidence) error {
	if evidence.Version != domain.DeploymentEvidenceVersion || evidence.Service != query.Service || evidence.Environment != query.Environment ||
		!domain.ValidCodeIdentifier(evidence.SourceVersion) || evidence.Fingerprint == "" {
		return errors.New("deployment evidence envelope is invalid")
	}
	expectedFingerprint, err := domain.DeploymentEvidenceFingerprint(evidence)
	if err != nil || expectedFingerprint != evidence.Fingerprint {
		return errors.New("deployment evidence fingerprint is invalid")
	}
	switch evidence.Status {
	case domain.DeploymentComplete:
		if evidence.ReasonCode != "" || !domain.ValidCodeIdentifier(evidence.RepositoryID) || !domain.ValidFullCommitSHA(evidence.CommitSHA) ||
			(evidence.PreviousCommitSHA != "" && !domain.ValidFullCommitSHA(evidence.PreviousCommitSHA)) || evidence.DeployedAt.IsZero() ||
			query.At.Before(evidence.DeployedAt) || (!evidence.RetiredAt.IsZero() && !query.At.Before(evidence.RetiredAt)) || !domain.ValidArtifactDigest(evidence.ArtifactDigest) {
			return errors.New("complete deployment evidence is invalid")
		}
	case domain.DeploymentUnavailable:
		if evidence.ReasonCode != domain.CodeReasonDeploymentNotFound && evidence.ReasonCode != domain.CodeReasonDeploymentInvalid {
			return errors.New("unavailable deployment reason is invalid")
		}
		if evidence.RepositoryID != "" || evidence.CommitSHA != "" || evidence.PreviousCommitSHA != "" || evidence.ArtifactDigest != "" || !evidence.DeployedAt.IsZero() || !evidence.RetiredAt.IsZero() {
			return errors.New("unavailable deployment contains a code version")
		}
	case domain.DeploymentConflict:
		if evidence.ReasonCode != domain.CodeReasonDeploymentConflict || evidence.RepositoryID != "" || evidence.CommitSHA != "" ||
			evidence.PreviousCommitSHA != "" || evidence.ArtifactDigest != "" || !evidence.DeployedAt.IsZero() || !evidence.RetiredAt.IsZero() {
			return errors.New("conflicting deployment evidence is invalid")
		}
	default:
		return errors.New("deployment status is invalid")
	}
	return nil
}

func validateCodeSearchResult(request domain.CodeSearchRequest, result domain.CodeSearchResult) error {
	if result.Version != domain.CodeEvidenceVersion || result.PolicyVersion != domain.CodeSearchPolicyVersion ||
		result.RepositoryID != request.RepositoryID || result.CommitSHA != request.CommitSHA ||
		result.AnchorsSearched < 0 || result.AnchorsSearched > len(request.Anchors) || result.FilesRead < 0 || result.FilesRead > request.MaxFiles ||
		result.LinesReturned < 0 || result.LinesReturned > request.MaxLines || result.BytesReturned < 0 || result.BytesReturned > request.MaxBytes ||
		result.CommandsRun < 0 || result.CommandsRun > 48 || result.SensitiveSkips < 0 || len(result.Matches) > request.MaxMatches ||
		(result.Complete && result.Truncated) || (!result.Complete && !result.Truncated) {
		return errors.New("code search result envelope is invalid")
	}
	if result.DiffChecked != (request.PreviousCommitSHA != "") || len(result.ChangedFiles) > 64 || (!result.DiffChecked && len(result.ChangedFiles) != 0) {
		return errors.New("code search diff state is invalid")
	}
	anchorByID := make(map[string]domain.RuntimeAnchor, len(request.Anchors))
	for _, anchor := range request.Anchors {
		if err := anchors.Validate(anchor); err != nil {
			return fmt.Errorf("invalid requested runtime anchor: %w", err)
		}
		anchorByID[anchor.ID] = anchor
	}
	changedFiles := make(map[string]struct{}, len(result.ChangedFiles))
	for index, file := range result.ChangedFiles {
		if !validSourceFile(file) || (index > 0 && result.ChangedFiles[index-1] >= file) {
			return errors.New("changed file list is invalid")
		}
		changedFiles[file] = struct{}{}
	}
	matchIDs := make(map[string]struct{}, len(result.Matches))
	var linesReturned, bytesReturned int
	for index, match := range result.Matches {
		anchor, exists := anchorByID[match.AnchorID]
		if !exists || match.RepositoryID != request.RepositoryID || match.CommitSHA != request.CommitSHA ||
			!validSourceFile(match.File) || match.StartLine <= 0 || match.MatchLine < match.StartLine || match.EndLine < match.MatchLine ||
			match.EndLine-match.StartLine+1 > request.ContextRadius*2+1 || !domain.ValidGitBlobHash(match.BlobHash) ||
			!domain.ValidateCodeSnippet(match.Snippet) || utf8.RuneCountInString(match.Symbol) > 192 {
			return errors.New("code match shape is invalid")
		}
		if match.Kind == domain.CodeMatchStackFrame {
			if anchor.Kind != domain.RuntimeAnchorStackFrame || anchor.File != match.File || anchor.Line != match.MatchLine {
				return errors.New("stack-frame code match does not bind its anchor")
			}
		} else if match.Kind != domain.CodeMatchExactText {
			return errors.New("code match kind is invalid")
		} else if anchor.Kind == domain.RuntimeAnchorStackFrame {
			return errors.New("stack-frame anchor cannot produce an exact-text match")
		} else {
			expectedText := anchor.Value
			if anchor.Kind == domain.RuntimeAnchorSymbol {
				expectedText = anchor.Symbol
			}
			if expectedText == "" || !strings.Contains(match.Snippet, expectedText) {
				return errors.New("exact-text code match does not contain its anchor")
			}
		}
		_, changed := changedFiles[match.File]
		if match.ChangedSincePrevious != changed {
			return errors.New("code match change flag is invalid")
		}
		expectedQuery, err := fingerprint.JSON(struct{ AnchorID, RepositoryID, CommitSHA string }{match.AnchorID, request.RepositoryID, request.CommitSHA})
		if err != nil || match.QueryFingerprint != expectedQuery {
			return errors.New("code match query fingerprint is invalid")
		}
		expectedContent, err := domain.CodeMatchFingerprint(match)
		if err != nil || match.ContentFingerprint != expectedContent {
			return errors.New("code match content fingerprint is invalid")
		}
		digest := sha256.Sum256([]byte(match.AnchorID + "|" + expectedQuery + "|" + expectedContent))
		if match.ID != "code_"+hex.EncodeToString(digest[:12]) {
			return errors.New("code match ID is invalid")
		}
		if _, duplicate := matchIDs[match.ID]; duplicate {
			return errors.New("duplicate code match ID")
		}
		matchIDs[match.ID] = struct{}{}
		if index > 0 && !codeMatchLess(result.Matches[index-1], match) {
			return errors.New("code matches are not strictly sorted")
		}
		linesReturned += match.EndLine - match.StartLine + 1
		bytesReturned += len(match.Snippet)
	}
	if linesReturned != result.LinesReturned || bytesReturned != result.BytesReturned {
		return errors.New("code search usage does not match returned evidence")
	}
	return nil
}

func validateCodeInvestigation(investigation *domain.CodeInvestigation, trace *domain.TraceInvestigation) error {
	if investigation == nil {
		return nil
	}
	if investigation.Version != domain.CodeEvidenceVersion || investigation.GeneratedAt.IsZero() || len(investigation.Limitations) < 2 || len(investigation.Limitations) > 3 {
		return errors.New("code investigation envelope is invalid")
	}
	for _, limitation := range investigation.Limitations {
		if limitation == "" || utf8.RuneCountInString(limitation) > 120 || strings.ContainsAny(limitation, "\r\n") {
			return errors.New("code investigation limitation is invalid")
		}
	}
	if trace == nil {
		return errors.New("code investigation has no Trace investigation")
	}
	if investigation.Deployment != nil {
		query := domain.DeploymentQuery{Service: investigation.Deployment.Service, Environment: investigation.Deployment.Environment, At: trace.EndTime}
		if err := validateDeploymentEvidence(query, *investigation.Deployment); err != nil {
			return err
		}
	}
	switch investigation.Status {
	case domain.CodeInvestigationSkipped:
		if investigation.ReasonCode != domain.CodeReasonTraceIncomplete && investigation.ReasonCode != domain.CodeReasonNoAnchors {
			return errors.New("skipped code investigation reason is invalid")
		}
		if investigation.Complete || investigation.Deployment != nil || !emptyCodeInvestigationUsage(investigation) {
			return errors.New("skipped code investigation contains evidence")
		}
	case domain.CodeInvestigationUnavailable:
		if !validUnavailableCodeReason(investigation.ReasonCode) || investigation.Complete || !emptyCodeInvestigationUsage(investigation) {
			return errors.New("unavailable code investigation is invalid")
		}
	case domain.CodeInvestigationComplete, domain.CodeInvestigationNoMatch, domain.CodeInvestigationPartial:
		if investigation.Deployment == nil || investigation.Deployment.Status != domain.DeploymentComplete || investigation.AnchorsUsed <= 0 || investigation.AnchorsUsed > domain.CodeSearchMaxAnchors {
			return errors.New("code investigation lacks trusted deployment scope")
		}
		if investigation.Status == domain.CodeInvestigationComplete && (!investigation.Complete || len(investigation.Matches) == 0 || investigation.ReasonCode != "") {
			return errors.New("complete code investigation is inconsistent")
		}
		if investigation.Status == domain.CodeInvestigationNoMatch && (!investigation.Complete || len(investigation.Matches) != 0 || investigation.ReasonCode != "") {
			return errors.New("no-match code investigation is inconsistent")
		}
		if investigation.Status == domain.CodeInvestigationPartial && (investigation.Complete || investigation.ReasonCode != domain.CodeReasonResultTruncated) {
			return errors.New("partial code investigation is inconsistent")
		}
		request := domain.CodeSearchRequest{
			RepositoryID: investigation.Deployment.RepositoryID, CommitSHA: investigation.Deployment.CommitSHA,
			PreviousCommitSHA: investigation.Deployment.PreviousCommitSHA, Anchors: append([]domain.RuntimeAnchor(nil), trace.AnchorSet.Anchors...),
			MaxMatches: domain.CodeSearchMaxMatches, MaxFiles: domain.CodeSearchMaxFiles, MaxLines: domain.CodeSearchMaxLines,
			MaxBytes: domain.CodeSearchMaxBytes, ContextRadius: domain.CodeSearchContextRadius, PolicyVersion: domain.CodeSearchPolicyVersion,
		}
		if len(request.Anchors) > domain.CodeSearchMaxAnchors {
			request.Anchors = request.Anchors[:domain.CodeSearchMaxAnchors]
		}
		result := domain.CodeSearchResult{
			Version: domain.CodeEvidenceVersion, PolicyVersion: domain.CodeSearchPolicyVersion,
			RepositoryID: investigation.Deployment.RepositoryID, CommitSHA: investigation.Deployment.CommitSHA,
			Complete: investigation.Complete, Truncated: !investigation.Complete, Matches: investigation.Matches,
			AnchorsSearched: investigation.AnchorsUsed, FilesRead: investigation.FilesRead,
			LinesReturned: investigation.LinesReturned, BytesReturned: investigation.BytesReturned, CommandsRun: investigation.CommandsRun,
			SensitiveSkips: investigation.SensitiveSkips,
			DiffChecked:    investigation.DiffChecked, ChangedFiles: investigation.ChangedFiles,
		}
		if err := validateCodeSearchResult(request, result); err != nil {
			return err
		}
	default:
		return errors.New("code investigation status is invalid")
	}
	return nil
}

func emptyCodeInvestigationUsage(investigation *domain.CodeInvestigation) bool {
	return len(investigation.Matches) == 0 && investigation.AnchorsUsed == 0 && investigation.FilesRead == 0 &&
		investigation.LinesReturned == 0 && investigation.BytesReturned == 0 && investigation.CommandsRun == 0 &&
		investigation.SensitiveSkips == 0 && !investigation.DiffChecked && len(investigation.ChangedFiles) == 0
}

func validUnavailableCodeReason(reason string) bool {
	switch reason {
	case domain.CodeReasonDisabled, domain.CodeReasonDeploymentNotFound, domain.CodeReasonDeploymentConflict,
		domain.CodeReasonDeploymentInvalid, domain.CodeReasonRepositoryUnavailable,
		domain.CodeReasonProviderUnavailable, domain.CodeReasonResultInvalid:
		return true
	default:
		return false
	}
}

func validSourceFile(file string) bool {
	if !domain.ValidCodePath(file) {
		return false
	}
	lower := strings.ToLower(file)
	return strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".java") || strings.HasSuffix(lower, ".py")
}

func codeMatchLess(left, right domain.CodeMatch) bool {
	leftKey := left.File + "\x00" + fmt.Sprintf("%09d", left.MatchLine) + "\x00" + left.AnchorID
	rightKey := right.File + "\x00" + fmt.Sprintf("%09d", right.MatchLine) + "\x00" + right.AnchorID
	return leftKey < rightKey
}
