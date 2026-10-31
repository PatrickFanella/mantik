package storage

import (
	"errors"
	"slices"
	"time"

	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/corporatecoverage"
	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func coveredInstrument(status string) PaperEvaluationInstrumentCoverage {
	return PaperEvaluationInstrumentCoverage{Plan: PaperEvaluationCoverageInstrument{Symbol: "SPY"}, StrategyIDs: []string{"s"}, Result: corporatecoverage.Result{Status: status, Window: corporatecoverage.Window{Symbol: "SPY", Start: "2026-10-01", End: "2026-10-31"}, Dividends: []corporatecoverage.Dividend{{ExDate: "2026-10-21", Amount: "1.903516", PayDate: "2026-10-31"}}, Splits: []corporatecoverage.Split{}}}
}

func netReturn(hash, status, net string) papereconomics.TotalReturn {
	t := papereconomics.TotalReturn{Status: status, ActionsSHA256: hash}
	if net != "" {
		t.NetCents = &net
	}
	return t
}

// totalReturnAssessment builds the retained shape the decision reads: base and
// stress strategy/passive nets for one covered instrument.
func totalReturnAssessment(t *testing.T, base, stress [2]string) PaperEvaluationAssessment {
	t.Helper()
	in := coveredInstrument("covered")
	a, ok := instrumentActions(in)
	if !ok || len(a.Actions) != 1 || a.Actions[0].Kind != "cash_dividend" || a.Actions[0].PayableDate != "2026-10-31" {
		t.Fatal(a)
	}
	hash := workerplan.Hash(a)
	row := func(nets [2]string) *PaperEvaluationTotalReturn {
		passive := netReturn(hash, "observed_total_return", nets[1])
		return &PaperEvaluationTotalReturn{Strategy: netReturn(hash, "observed_total_return", nets[0]), Passive: &passive}
	}
	full := &papereconomics.Benchmarks{PassiveStatus: fullPassiveEntry}
	return PaperEvaluationAssessment{
		Status:            "incomplete",
		Evidence:          PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"},
		Checks:            []PaperEvaluationCheck{{StrategyID: "s", Name: "benchmark_comparison", Status: "unavailable"}},
		CorporateCoverage: &PaperEvaluationCorporateCoverage{Instruments: []PaperEvaluationInstrumentCoverage{in}},
		Portfolio: &PaperEvaluationPortfolio{Variants: []PaperEvaluationPortfolioVariant{{
			StrategyID: "s", Base: &papereconomics.Result{}, Stress: &papereconomics.Result{},
			BaseBenchmarks: full, StressBenchmarks: full, BaseTotalReturn: row(base), StressTotalReturn: row(stress),
		}}},
	}
}

func decide(t *testing.T, r PaperEvaluationAssessment, minimum int64) PaperEvaluationCheck {
	t.Helper()
	if err := applyTotalReturnBenchmarks(&r, minimum); err != nil {
		t.Fatal(err)
	}
	return r.Checks[0]
}

func TestTotalReturnBenchmarkDecision(t *testing.T) {
	cases := []struct {
		name           string
		base, stress   [2]string
		minimum        int64
		status, reason string
	}{
		{"exceeds both in both models", [2]string{"500", "100"}, [2]string{"400", "100"}, 300, "passed", "total_return_exceeds_cash_and_passive_by_predeclared_minimum"},
		{"exact minimum passes", [2]string{"400", "100"}, [2]string{"400", "100"}, 300, "passed", "total_return_exceeds_cash_and_passive_by_predeclared_minimum"},
		{"stress misses the passive margin", [2]string{"500", "100"}, [2]string{"399", "100"}, 300, "failed", "total_return_excess_below_predeclared_minimum"},
		{"fractional cents count exactly", [2]string{"300", "0"}, [2]string{"1199/4", "0"}, 300, "failed", "total_return_excess_below_predeclared_minimum"},
		{"loss below cash with negative minimum passes", [2]string{"-50", "-200"}, [2]string{"-50", "-200"}, -100, "passed", "total_return_exceeds_cash_and_passive_by_predeclared_minimum"},
		{"below cash fails", [2]string{"-50", "-200"}, [2]string{"-50", "-200"}, 0, "failed", "total_return_excess_below_predeclared_minimum"},
	}
	for _, c := range cases {
		r := totalReturnAssessment(t, c.base, c.stress)
		if err := applyTotalReturnBenchmarks(&r, c.minimum); err != nil {
			t.Fatal(c.name, err)
		}
		if r.Checks[0].Status != c.status || r.Checks[0].ReasonCode != c.reason || (c.status == "failed") != (r.Status == "blocked") {
			t.Fatal(c.name, r.Checks[0], r.Status)
		}
	}
}

func TestTotalReturnBenchmarkStaysOpen(t *testing.T) {
	ok := [2]string{"500", "100"}
	r := totalReturnAssessment(t, ok, ok)
	r.Evidence.Phase = "observation_window"
	if c := decide(t, r, 0); c.Status != "pending" || c.ReasonCode != "predeclared_outcome_window_not_closed" {
		t.Fatal(c)
	}
	r = totalReturnAssessment(t, ok, ok)
	r.Portfolio.Variants[0].StressTotalReturn.Strategy.Status = "terminal_mark_unavailable"
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "strategy_total_return_unavailable" {
		t.Fatal(c)
	}
	r = totalReturnAssessment(t, ok, ok)
	r.Portfolio.Variants[0].BaseBenchmarks = &papereconomics.Benchmarks{PassiveStatus: "partial_passive_entry_price_path_only"}
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "passive_benchmark_entry_incomplete" {
		t.Fatal(c)
	}
	r = totalReturnAssessment(t, ok, ok)
	r.Portfolio.Variants[0].BaseTotalReturn.Passive.Status = "holding_outside_corporate_coverage_window"
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "passive_total_return_unavailable" {
		t.Fatal(c)
	}
	// An unreconciled window has no total return at all.
	r = totalReturnAssessment(t, ok, ok)
	r.CorporateCoverage.Instruments[0].Result.Status = "unreconciled"
	r.Portfolio.Variants[0].BaseTotalReturn, r.Portfolio.Variants[0].StressTotalReturn = nil, nil
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "observed_price_benchmarks_do_not_establish_total_return_or_corporate_coverage" {
		t.Fatal(c)
	}
	// Covered, but its reconciled values cannot be applied.
	r = totalReturnAssessment(t, ok, ok)
	r.CorporateCoverage.Instruments[0].Result.Dividends[0].PayDate = "2026-10-20"
	r.Portfolio.Variants[0].BaseTotalReturn, r.Portfolio.Variants[0].StressTotalReturn = nil, nil
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "reconciled_actions_not_applicable_to_total_return" {
		t.Fatal(c)
	}
	// No replayed books for the variant.
	r = totalReturnAssessment(t, ok, ok)
	v := &r.Portfolio.Variants[0]
	v.Base, v.Stress, v.BaseTotalReturn, v.StressTotalReturn = nil, nil, nil, nil
	if c := decide(t, r, 0); c.Status != "unavailable" || c.ReasonCode != "variant_books_unavailable_for_total_return" {
		t.Fatal(c)
	}
}

func TestTotalReturnBenchmarkRefusesDetachedRows(t *testing.T) {
	ok := [2]string{"500", "100"}
	for name, change := range map[string]func(*PaperEvaluationAssessment){
		"other actions": func(r *PaperEvaluationAssessment) {
			r.Portfolio.Variants[0].BaseTotalReturn.Strategy.ActionsSHA256 = "forged"
		},
		"row for an unreconciled window": func(r *PaperEvaluationAssessment) {
			r.CorporateCoverage.Instruments[0].Result.Status = "unreconciled"
		},
		"covered variant missing its row": func(r *PaperEvaluationAssessment) {
			r.Portfolio.Variants[0].StressTotalReturn = nil
		},
		"no portfolio": func(r *PaperEvaluationAssessment) { r.Portfolio = nil },
	} {
		r := totalReturnAssessment(t, ok, ok)
		change(&r)
		if err := applyTotalReturnBenchmarks(&r, 0); err != ErrPaperEvaluationAssessmentInvalid {
			t.Fatal(name, err)
		}
	}
}

func TestTotalReturnPlanRequiresCoverage(t *testing.T) {
	m := NewMemory()
	c, at := coverageDeclarationCommand(t, m)
	c.Recipe, c.Plan.TotalReturnMethod = PaperTotalReturnEvaluationRecipe, papereconomics.TotalReturnMethod
	if c.Validate() != nil {
		t.Fatal("total-return plan refused")
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"method":      func(c *PaperEvaluationDeclarationCommand) { c.Plan.TotalReturnMethod = "other" },
		"no coverage": func(c *PaperEvaluationDeclarationCommand) { c.Plan.CorporateCoverage = nil },
		"old recipe":  func(c *PaperEvaluationDeclarationCommand) { c.Recipe = PaperCoverageEvaluationRecipe },
		"no method":   func(c *PaperEvaluationDeclarationCommand) { c.Plan.TotalReturnMethod = "" },
	} {
		bad := clone(c)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal(name, "invalid total-return plan accepted")
		}
	}
	strategies, _ := m.PaperStrategies(t.Context())
	policies, _ := m.AllocationPolicies(t.Context())
	d, err := newPaperEvaluationDeclaration(c, strategies, policies, at.Add(time.Second))
	if err != nil || d.Schema != "mantik-owned-paper-evaluation-declaration-v10" || !slices.Contains(d.PlannedChecks, "corporate_coverage") {
		t.Fatal(d.Schema, err)
	}
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, at.Add(time.Second), "mantik-owned-paper-evaluation-declaration-v9"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("total-return plan downgrade accepted", err)
	}
}

func TestRetainedTotalReturnValuesMatchStatus(t *testing.T) {
	sealed := func(change func(*papereconomics.TotalReturn)) *papereconomics.TotalReturn {
		net, equity := "-7000", "193000"
		r := papereconomics.TotalReturn{Method: papereconomics.TotalReturnMethod, InputSHA256: "in", Status: "observed_total_return", EquityCents: &equity, NetCents: &net, Authority: "owner_selected_reconciled_total_return_accounting_only"}
		change(&r)
		r.SHA256 = workerplan.Hash(r)
		return &r
	}
	if !validTotalReturn(sealed(func(*papereconomics.TotalReturn) {}), "in") {
		t.Fatal("sealed result refused")
	}
	for name, change := range map[string]func(*papereconomics.TotalReturn){
		"available without net":    func(r *papereconomics.TotalReturn) { r.NetCents = nil },
		"available without values": func(r *papereconomics.TotalReturn) { r.NetCents, r.EquityCents = nil, nil },
		"values without status":    func(r *papereconomics.TotalReturn) { r.Status = "terminal_mark_unavailable" },
		"unknown status":           func(r *papereconomics.TotalReturn) { r.Status = "fabricated" },
		"exponent net": func(r *papereconomics.TotalReturn) {
			n := "1e999999999"
			r.NetCents = &n
		},
		"unreduced text": func(r *papereconomics.TotalReturn) {
			n := "0x10"
			r.NetCents = &n
		},
	} {
		if validTotalReturn(sealed(change), "in") {
			t.Fatal(name, "accepted")
		}
	}
	// The decision itself refuses unusable retained values instead of panicking.
	ok := [2]string{"500", "100"}
	for name, change := range map[string]func(*PaperEvaluationAssessment){
		"nil net": func(r *PaperEvaluationAssessment) { r.Portfolio.Variants[0].BaseTotalReturn.Strategy.NetCents = nil },
		"exponent net": func(r *PaperEvaluationAssessment) {
			n := "1e999999999"
			r.Portfolio.Variants[0].StressTotalReturn.Passive.NetCents = &n
		},
	} {
		r := totalReturnAssessment(t, ok, ok)
		change(&r)
		if err := applyTotalReturnBenchmarks(&r, 0); err != ErrPaperEvaluationAssessmentInvalid {
			t.Fatal(name, err)
		}
	}
}

func TestTotalReturnCoverageWindowReachesTheOutcomeDeadline(t *testing.T) {
	p := PaperEvaluationPlan{WindowStart: time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC), WindowEnd: time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC), OutcomeDeadline: time.Date(2026, 10, 23, 19, 0, 0, 0, time.UTC), CorporateCoverage: &PaperEvaluationCoveragePlan{ProcessLagDays: 10}}
	if w := coverageWindow(p, "SPY"); w.Start != "2026-10-20" || w.End != "2026-10-21" {
		t.Fatal("coverage-only window changed", w)
	}
	p.TotalReturnMethod = papereconomics.TotalReturnMethod
	if w := coverageWindow(p, "SPY"); w.Start != "2026-10-20" || w.End != "2026-10-23" || w.ProcessLagDays != 10 {
		t.Fatal("total-return window ends before the outcome cut", w)
	}
}
