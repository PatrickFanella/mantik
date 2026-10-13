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
	for _, name := range []string{"unfinished", "expired", "missing_checks", "changed_size", "changed_safety", "wrong_account_case"} {
		t.Run(name, func(t *testing.T) {
			state := workflow.InitialState()
			state.LastOccurredAt = o.OccurredAt
			d := decision
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
			case "wrong_account_case":
				d.CaseID = "other"
			}
			if _, err := operations.ExecutePaper(state, o, d); err == nil {
				t.Fatal("invalid action executed")
			}
		})
	}
}
