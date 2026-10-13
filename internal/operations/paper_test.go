package operations_test

import (
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operations"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestExecutionRejectsChangedAuthority(t *testing.T) {
	o := workflow.Fixtures()[1]
	_, events, err := workflow.Apply(workflow.InitialState(), o)
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.Decision
	for _, e := range events {
		if e.Decision != nil {
			decision = *e.Decision
		}
	}
	for _, name := range []string{"unfinished", "expired", "missing_checks", "changed_size", "changed_safety", "wrong_account_case", "check_version", "unknown_strategy", "changed_configuration"} {
		t.Run(name, func(t *testing.T) {
			state := workflow.InitialState()
			state.LastOccurredAt = o.OccurredAt
			d := decision
			d.Evaluations = append([]domain.EvaluationResult(nil), decision.Evaluations...)
			switch name {
			case "unfinished":
				d.Status = "proposed"
			case "expired":
				d.ValidUntil = o.ReceivedAt.Add(-time.Second)
			case "missing_checks":
				d.Evaluations = d.Evaluations[:2]
			case "changed_size":
				d.Quantity++
			case "changed_safety":
				d.Safety.StopCents = 1
			case "check_version":
				d.Evaluations[0].Version = "v2"
			case "unknown_strategy":
				state.Strategy.ID = "demo-threshold-v2"
				d.StrategyVersionID = state.Strategy.ID
			case "changed_configuration":
				state.Strategy.EntryAtOrBelowCents++
			case "wrong_account_case":
				d.CaseID = "other"
			}
			if _, err := operations.ExecutePaper(state, o, d); err == nil {
				t.Fatal("invalid action executed")
			}
		})
	}
	// Existing stored bootstrap decisions have no explicit evaluation versions.
	legacy := decision
	legacy.Evaluations = append([]domain.EvaluationResult(nil), decision.Evaluations...)
	for i := range legacy.Evaluations {
		legacy.Evaluations[i].Version = ""
	}
	state := workflow.InitialState()
	state.LastOccurredAt = o.OccurredAt
	if _, err := operations.ExecutePaper(state, o, legacy); err != nil {
		t.Fatalf("retained v1 decision rejected: %v", err)
	}
}
