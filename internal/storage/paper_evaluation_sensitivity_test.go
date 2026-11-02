package storage

import (
	"errors"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func sensitivityCommand() PaperEvaluationDeclarationCommand {
	c := PaperEvaluationDeclarationCommand{Plan: PaperEvaluationPlan{TotalReturnMethod: papereconomics.TotalReturnMethod, Variants: []PaperEvaluationVariant{{ID: "a"}, {ID: "b"}, {ID: "c"}}}}
	c.Plan.Sensitivity = &PaperEvaluationSensitivityPlan{Method: PaperThresholdSensitivityMethod, MinimumNeighborNetCents: 100, Neighborhoods: []PaperEvaluationNeighborhood{{StrategyID: "b", NeighborIDs: []string{"a", "c"}}}}
	return c
}

func TestSensitivityPlanShape(t *testing.T) {
	if !validSensitivityPlan(sensitivityCommand()) {
		t.Fatal("valid sensitivity plan refused")
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"method":           func(c *PaperEvaluationDeclarationCommand) { c.Plan.Sensitivity.Method = "other" },
		"no total return":  func(c *PaperEvaluationDeclarationCommand) { c.Plan.TotalReturnMethod = "" },
		"negative floor":   func(c *PaperEvaluationDeclarationCommand) { c.Plan.Sensitivity.MinimumNeighborNetCents = -1 },
		"no neighborhoods": func(c *PaperEvaluationDeclarationCommand) { c.Plan.Sensitivity.Neighborhoods = nil },
		"undeclared center": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.Sensitivity.Neighborhoods[0].StrategyID = "z"
		},
		"undeclared neighbor": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.Sensitivity.Neighborhoods[0].NeighborIDs = []string{"a", "z"}
		},
		"self neighbor": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.Sensitivity.Neighborhoods[0].NeighborIDs = []string{"a", "b"}
		},
		"one neighbor": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.Sensitivity.Neighborhoods[0].NeighborIDs = []string{"a"}
		},
		"unsorted neighbors": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.Sensitivity.Neighborhoods[0].NeighborIDs = []string{"c", "a"}
		},
		"duplicate center": func(c *PaperEvaluationDeclarationCommand) {
			n := c.Plan.Sensitivity.Neighborhoods[0]
			c.Plan.Sensitivity.Neighborhoods = append(c.Plan.Sensitivity.Neighborhoods, n)
		},
	} {
		bad := sensitivityCommand()
		change(&bad)
		if validSensitivityPlan(bad) {
			t.Fatal(name, "invalid sensitivity plan accepted")
		}
	}
}

func TestSensitivityNeighborsBracketOnlyTheThreshold(t *testing.T) {
	record := func(id string, entry int64) paperstrategy.Record {
		d := strategyDefinition(id)
		d.EntryAtOrBelowCents = entry
		return paperstrategy.Record{Command: paperstrategy.Command{Definition: d}}
	}
	p := sensitivityCommand().Plan.Sensitivity
	if err := sensitivityNeighborhoodTerms(p, []paperstrategy.Record{record("a", 9900), record("b", 10000), record("c", 10100)}); err != nil {
		t.Fatal(err)
	}
	for name, variants := range map[string][]paperstrategy.Record{
		"both below": {record("a", 9900), record("b", 10000), record("c", 9800)},
		"both above": {record("a", 10100), record("b", 10000), record("c", 10200)},
		"equal":      {record("a", 10000), record("b", 10000), record("c", 10100)},
	} {
		if err := sensitivityNeighborhoodTerms(p, variants); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
			t.Fatal(name, "unbracketed neighborhood accepted", err)
		}
	}
	changed := record("c", 10100)
	changed.Command.Definition.Quantity++
	if err := sensitivityNeighborhoodTerms(p, []paperstrategy.Record{record("a", 9900), record("b", 10000), changed}); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("neighbor with other terms accepted", err)
	}
	changed = record("a", 9900)
	changed.Command.Definition.Safety.StopCents--
	if err := sensitivityNeighborhoodTerms(p, []paperstrategy.Record{changed, record("b", 10000), record("c", 10100)}); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("neighbor with other safety accepted", err)
	}
}

// sensitivityAssessment retains base and stress strategy nets for a, b and c.
func sensitivityAssessment(nets map[string][2]string) PaperEvaluationAssessment {
	r := PaperEvaluationAssessment{Status: "incomplete", Evidence: PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"}, Portfolio: &PaperEvaluationPortfolio{}}
	for _, id := range []string{"a", "b", "c"} {
		r.Checks = append(r.Checks, PaperEvaluationCheck{StrategyID: id, Name: "parameter_and_cost_sensitivity", Status: "unavailable"})
		v := PaperEvaluationPortfolioVariant{StrategyID: id}
		if n, ok := nets[id]; ok {
			row := func(net string) *PaperEvaluationTotalReturn {
				if net == "" {
					return &PaperEvaluationTotalReturn{Strategy: netReturn("h", "terminal_mark_unavailable", "")}
				}
				return &PaperEvaluationTotalReturn{Strategy: netReturn("h", "observed_total_return", net)}
			}
			v.BaseTotalReturn, v.StressTotalReturn = row(n[0]), row(n[1])
		}
		r.Portfolio.Variants = append(r.Portfolio.Variants, v)
	}
	return r
}

func TestThresholdSensitivityDecision(t *testing.T) {
	p := sensitivityCommand().Plan.Sensitivity
	all := map[string][2]string{"a": {"150", "100"}, "b": {"300", "250"}, "c": {"201/2", "100.5"}}
	for name, tc := range map[string]struct {
		nets           map[string][2]string
		phase          string
		status, reason string
	}{
		"pass at floor":         {all, "outcome_deadline_elapsed", "passed", "threshold_neighborhood_meets_predeclared_net_in_base_and_stress"},
		"neighbor stress short": {map[string][2]string{"a": {"150", "99.99"}, "b": all["b"], "c": all["c"]}, "outcome_deadline_elapsed", "failed", "threshold_neighborhood_net_below_predeclared_minimum"},
		"center base loss":      {map[string][2]string{"a": all["a"], "b": {"-1", "250"}, "c": all["c"]}, "outcome_deadline_elapsed", "failed", "threshold_neighborhood_net_below_predeclared_minimum"},
		"neighbor unavailable":  {map[string][2]string{"a": all["a"], "b": all["b"], "c": {"", ""}}, "outcome_deadline_elapsed", "unavailable", "threshold_neighborhood_total_return_unavailable"},
		"neighbor missing rows": {map[string][2]string{"b": all["b"], "c": all["c"]}, "outcome_deadline_elapsed", "unavailable", "threshold_neighborhood_total_return_unavailable"},
		"window open":           {all, "outcome_deadline_pending", "pending", "predeclared_outcome_window_not_closed"},
	} {
		r := sensitivityAssessment(tc.nets)
		r.Evidence.Phase = tc.phase
		if err := applyThresholdSensitivity(&r, p); err != nil {
			t.Fatal(name, err)
		}
		got := r.Checks[1]
		if got.Status != tc.status || got.ReasonCode != tc.reason || (r.Status == "blocked") != (tc.status == "failed") {
			t.Fatal(name, got, r.Status)
		}
		for _, other := range []PaperEvaluationCheck{r.Checks[0], r.Checks[2]} {
			if other.Status != "unavailable" || other.ReasonCode != "threshold_neighbors_not_declared" {
				t.Fatal(name, "undeclared center decided", other)
			}
		}
	}
	r := sensitivityAssessment(all)
	bad := "1e3"
	r.Portfolio.Variants[0].BaseTotalReturn.Strategy.NetCents = &bad
	if err := applyThresholdSensitivity(&r, p); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("inexact retained net accepted", err)
	}
}
