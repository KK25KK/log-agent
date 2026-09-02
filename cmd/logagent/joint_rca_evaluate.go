package main

import (
	"errors"
	"time"

	"logagent/internal/evaluation/jointrca"
)

func runJointRCAEvaluate(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: logagent joint-rca-evaluate")
	}
	dataset, err := jointrca.LoadSyntheticV1()
	if err != nil {
		return err
	}
	report, evaluationErr := jointrca.Evaluate(dataset, time.Now())
	if err := printJSON(report); err != nil {
		return err
	}
	return evaluationErr
}
