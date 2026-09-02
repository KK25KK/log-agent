package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"logagent/internal/domain"
	"logagent/internal/fingerprint"
)

type JointRCAService struct {
	now func() time.Time
}

func NewJointRCAService(now func() time.Time) *JointRCAService {
	if now == nil {
		now = time.Now
	}
	return &JointRCAService{now: now}
}

func (service *JointRCAService) Enrich(report domain.Report) domain.Report {
	report.JointRCA = BuildJointRCA(report.TraceInvestigation, report.CodeInvestigation, service.now().UTC())
	return report
}

// BuildJointRCA is deliberately deterministic: it does not call an LLM or an
// external tool. It joins already validated runtime, deployment, code, and
// trusted diff evidence into human-review-only candidates.
func BuildJointRCA(trace *domain.TraceInvestigation, code *domain.CodeInvestigation, generatedAt time.Time) *domain.JointRCA {
	result := &domain.JointRCA{
		Version: domain.JointRCAVersion, Status: domain.JointRCAUnavailable,
		Limitations: []string{
			"自动分析最高只输出候选原因，不会标记为已确认根因。",
			"代码命中只能证明部署版本中存在相关路径，不能单独证明运行时执行。",
			"部署版本间文件重叠只表示相关，仍需复现、输入和依赖状态验证。",
		},
		HumanReviewOnly: true, GeneratedAt: generatedAt.UTC(),
	}
	if trace == nil || !trace.Complete || trace.Status != domain.TraceInvestigationComplete || trace.AnchorSet == nil || trace.AnchorSet.Status == domain.RuntimeAnchorsPartial {
		result.Status, result.ReasonCode = domain.JointRCASkipped, domain.JointRCAReasonTraceIncomplete
		result.MissingInputs = []string{"complete_trace_timeline"}
		return result
	}
	if trace.AnchorSet.Status == domain.RuntimeAnchorsNone || len(trace.AnchorSet.Anchors) == 0 {
		result.Status, result.ReasonCode = domain.JointRCASkipped, domain.JointRCAReasonNoRuntimeAnchors
		result.MissingInputs = []string{"searchable_runtime_anchor"}
		return result
	}
	if code == nil {
		result.ReasonCode = domain.JointRCAReasonCodeUnavailable
		result.MissingInputs = []string{"code_evidence"}
		return result
	}
	switch code.Status {
	case domain.CodeInvestigationSkipped:
		result.Status, result.ReasonCode = domain.JointRCASkipped, domain.JointRCAReasonNoRuntimeAnchors
		result.MissingInputs = []string{"searchable_runtime_anchor"}
		return result
	case domain.CodeInvestigationUnavailable:
		if code.ReasonCode == domain.CodeReasonDeploymentConflict {
			result.Status, result.ReasonCode = domain.JointRCANeedsReview, domain.JointRCAReasonDeploymentReview
			result.MissingInputs = []string{"unique_deployment_version"}
		} else {
			result.ReasonCode = domain.JointRCAReasonCodeUnavailable
			result.MissingInputs = []string{"trusted_deployment_or_code_evidence"}
		}
		return result
	case domain.CodeInvestigationNoMatch:
		result.Status, result.ReasonCode = domain.JointRCAInconclusive, domain.JointRCAReasonNoCodeMatch
		result.MissingInputs = []string{"exact_code_match"}
		return result
	case domain.CodeInvestigationComplete, domain.CodeInvestigationPartial:
		// Continue below.
	default:
		result.ReasonCode = domain.JointRCAReasonCodeUnavailable
		result.MissingInputs = []string{"valid_code_evidence"}
		return result
	}
	if code.Deployment == nil || code.Deployment.Status != domain.DeploymentComplete {
		result.Status, result.ReasonCode = domain.JointRCAUnavailable, domain.JointRCAReasonCodeUnavailable
		result.MissingInputs = []string{"trusted_deployment_version"}
		return result
	}

	groups := groupCodeMatches(code.Matches)
	if len(groups) == 0 {
		result.Status, result.ReasonCode = domain.JointRCAInconclusive, domain.JointRCAReasonNoCodeMatch
		result.MissingInputs = []string{"exact_code_match"}
		return result
	}
	if len(groups) > domain.JointRCAMaxCandidates {
		groups = groups[:domain.JointRCAMaxCandidates]
	}
	anchorKinds := make(map[string]domain.RuntimeAnchorKind, len(trace.AnchorSet.Anchors))
	for _, anchor := range trace.AnchorSet.Anchors {
		anchorKinds[anchor.ID] = anchor.Kind
	}
	for _, group := range groups {
		candidate, factors, actions := buildJointCandidate(group, anchorKinds, code)
		if code.Status == domain.CodeInvestigationPartial {
			candidate.Verdict = domain.JointRCACandidateUnknown
			candidate.Confidence = math.Min(candidate.Confidence, .45)
			candidate.MissingInputs = append(candidate.MissingInputs, "complete_code_search")
		}
		result.Candidates = append(result.Candidates, candidate)
		result.Factors = append(result.Factors, factors...)
		result.Actions = append(result.Actions, actions...)
	}
	if code.Status == domain.CodeInvestigationPartial {
		result.Status, result.ReasonCode = domain.JointRCAInconclusive, domain.JointRCAReasonCodePartial
		result.MissingInputs = []string{"complete_code_search"}
	} else {
		result.Status = domain.JointRCAComplete
	}
	return result
}

type codeMatchGroup struct {
	file    string
	line    int
	matches []domain.CodeMatch
}

func groupCodeMatches(matches []domain.CodeMatch) []codeMatchGroup {
	groups := make(map[string]*codeMatchGroup)
	for _, match := range matches {
		key := fmt.Sprintf("%s\x00%09d", match.File, match.MatchLine)
		group := groups[key]
		if group == nil {
			group = &codeMatchGroup{file: match.File, line: match.MatchLine}
			groups[key] = group
		}
		group.matches = append(group.matches, match)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]codeMatchGroup, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		sort.Slice(group.matches, func(i, j int) bool { return group.matches[i].ID < group.matches[j].ID })
		result = append(result, *group)
	}
	return result
}

func buildJointCandidate(group codeMatchGroup, anchorKinds map[string]domain.RuntimeAnchorKind, code *domain.CodeInvestigation) (domain.JointRCACandidate, []domain.JointRCAFactor, []domain.JointRCAAction) {
	anchorIDs := make([]string, 0, len(group.matches))
	matchIDs := make([]string, 0, len(group.matches))
	stackBound := false
	changed := false
	for _, match := range group.matches {
		anchorIDs = append(anchorIDs, match.AnchorID)
		matchIDs = append(matchIDs, match.ID)
		stackBound = stackBound || anchorKinds[match.AnchorID] == domain.RuntimeAnchorStackFrame
		changed = changed || match.ChangedSincePrevious
	}
	anchorIDs = uniqueSortedStrings(anchorIDs)
	matchIDs = uniqueSortedStrings(matchIDs)
	candidateID := jointRCAID("candidate", struct {
		Version               string   `json:"version"`
		DeploymentFingerprint string   `json:"deployment_fingerprint"`
		File                  string   `json:"file"`
		Line                  int      `json:"line"`
		AnchorIDs             []string `json:"anchor_ids"`
		MatchIDs              []string `json:"match_ids"`
	}{domain.JointRCAVersion, code.Deployment.Fingerprint, group.file, group.line, anchorIDs, matchIDs})

	relation := domain.JointRCAChangeUnknown
	changeRole, changeResult := domain.JointRCAFactorMissing, domain.JointRCAFactorUnknown
	changeStatement := "未提供可信上一部署版本，无法判断该文件是否为最近变更。"
	confidence := .60
	if stackBound {
		confidence = .65
	}
	if code.DiffChecked && changed {
		relation, changeRole, changeResult = domain.JointRCAChangeOverlap, domain.JointRCAFactorSupport, domain.JointRCAFactorPass
		changeStatement = "该代码位置所在文件与可信上一部署版本相比发生过变更。"
		confidence += .10
	} else if code.DiffChecked {
		relation, changeRole, changeResult = domain.JointRCAChangeUnchanged, domain.JointRCAFactorCounter, domain.JointRCAFactorPass
		changeStatement = "该代码位置所在文件与可信上一部署版本相比未发生变更，削弱最近代码发布导致问题的解释。"
		confidence -= .05
	}
	confidence = math.Round(math.Min(.75, math.Max(0, confidence))*100) / 100

	factors := []domain.JointRCAFactor{
		jointRCAFactor(candidateID, "runtime_anchor_observed", domain.JointRCAFactorSupport, domain.JointRCAFactorPass, "完整 Trace 时间线中存在与该位置绑定的运行时锚点。", anchorIDs, nil, ""),
		jointRCAFactor(candidateID, "deployed_commit_bound", domain.JointRCAFactorSupport, domain.JointRCAFactorPass, "代码证据来自事故时刻唯一匹配的不可变部署 Commit。", nil, nil, code.Deployment.Fingerprint),
		jointRCAFactor(candidateID, "exact_code_match", domain.JointRCAFactorSupport, domain.JointRCAFactorPass, "运行时锚点在目标部署 Commit 中得到精确代码位置匹配。", anchorIDs, matchIDs, code.Deployment.Fingerprint),
		jointRCAFactor(candidateID, "recent_change_overlap", changeRole, changeResult, changeStatement, nil, matchIDs, code.Deployment.Fingerprint),
		jointRCAFactor(candidateID, "runtime_branch_execution", domain.JointRCAFactorMissing, domain.JointRCAFactorUnknown, "现有日志尚未证明命中位置附近的具体条件分支、输入和下游状态。", anchorIDs, matchIDs, code.Deployment.Fingerprint),
	}
	factorIDs := make([]string, 0, len(factors))
	for _, factor := range factors {
		factorIDs = append(factorIDs, factor.ID)
	}
	statement := fmt.Sprintf("运行时锚点与部署版本中的 %s:%d 精确对应，该位置是需要人工验证的故障路径候选。", group.file, group.line)
	candidate := domain.JointRCACandidate{
		ID: candidateID, Kind: "DEPLOYED_CODE_PATH", Verdict: domain.JointRCASupportedCandidate,
		Statement: statement, Confidence: confidence, ConfidenceMethod: domain.JointRCAConfidenceMethod,
		File: group.file, Line: group.line, ChangeRelation: relation,
		RuntimeAnchorIDs: anchorIDs, CodeMatchIDs: matchIDs, FactorIDs: factorIDs,
		MissingInputs: []string{"runtime_branch_execution", "business_input_and_dependency_state"},
		Limitations:   []string{"候选分数是固定证据规则评分，不是统计概率。", "必须由人工结合复现或更多运行时证据确认。"},
	}
	if relation == domain.JointRCAChangeUnknown {
		candidate.MissingInputs = append(candidate.MissingInputs, "previous_deployment_version")
	}
	actions := []domain.JointRCAAction{
		jointRCAAction("VERIFY_BRANCH_PRECONDITIONS", candidate, "在目标 Commit 的该位置核对错误分支前置条件，并与 Trace 中的输入和下游返回状态逐项对照。"),
		jointRCAAction("REPRODUCE_AT_DEPLOYED_COMMIT", candidate, "使用同一部署 Commit 和脱敏后的等价输入复现，验证候选路径是否实际执行。"),
	}
	if relation == domain.JointRCAChangeOverlap {
		actions = append(actions, jointRCAAction("REVIEW_TRUSTED_DIFF", candidate, "人工审阅该文件在可信前后部署 Commit 间的 Diff，并补充覆盖相关分支的回归测试。"))
	} else {
		actions = append(actions, jointRCAAction("VERIFY_RUNTIME_DEPENDENCIES", candidate, "优先核对配置、数据状态和下游依赖；当前证据未支持最近源代码变更解释。"))
	}
	return candidate, factors, actions
}

func jointRCAFactor(candidateID, code string, role domain.JointRCAFactorRole, result domain.JointRCAFactorResult, statement string, anchorIDs, matchIDs []string, deploymentFingerprint string) domain.JointRCAFactor {
	return domain.JointRCAFactor{
		ID: jointRCAID("factor", candidateID+"|"+code), CandidateID: candidateID, Code: code,
		Role: role, Result: result, Statement: statement,
		RuntimeAnchorIDs: append([]string(nil), anchorIDs...), CodeMatchIDs: append([]string(nil), matchIDs...),
		DeploymentFingerprint: deploymentFingerprint,
	}
}

func jointRCAAction(code string, candidate domain.JointRCACandidate, statement string) domain.JointRCAAction {
	return domain.JointRCAAction{
		Code: code, CandidateID: candidate.ID, Statement: statement, ExecutionMode: "HUMAN_REVIEW_ONLY",
		RuntimeAnchorIDs: append([]string(nil), candidate.RuntimeAnchorIDs...), CodeMatchIDs: append([]string(nil), candidate.CodeMatchIDs...),
	}
}

func jointRCAID(prefix string, value any) string {
	digest, err := fingerprint.JSON(value)
	if err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprint(value)))
		digest = hex.EncodeToString(fallback[:])
	}
	return prefix + "_" + digest[:24]
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
