package application

import (
	"errors"
	"reflect"

	"logagent/internal/domain"
)

func validateJointRCA(analysis *domain.JointRCA, trace *domain.TraceInvestigation, code *domain.CodeInvestigation) error {
	if analysis == nil {
		return nil
	}
	if analysis.Version != domain.JointRCAVersion || analysis.GeneratedAt.IsZero() || !analysis.HumanReviewOnly {
		return errors.New("joint RCA envelope is invalid")
	}
	if len(analysis.Candidates) > domain.JointRCAMaxCandidates || len(analysis.Factors) > domain.JointRCAMaxFactors || len(analysis.Actions) > domain.JointRCAMaxActions {
		return errors.New("joint RCA exceeds fixed limits")
	}
	expected := BuildJointRCA(trace, code, analysis.GeneratedAt)
	if !reflect.DeepEqual(analysis, expected) {
		return errors.New("joint RCA does not match deterministic evidence projection")
	}
	return nil
}
