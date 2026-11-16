package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// GateStore combines authoritative trial history with append-only decisions.
type GateStore interface {
	strategytrial.Store
	strategytrial.FamilyReader
	strategygate.Store
}

// DeclareGate freezes the exact experiment before its first registry start.
// Thresholds are versioned code: DSR >= .95, PBO <= .2 when available, positive
// benchmark/stress excess and at least two-thirds positive neighbour tests.
func DeclareGate(ctx context.Context, store GateStore, d strategygate.Declaration) (strategygate.Record, error) {
	if e := validateGateDeclaration(d); e != nil {
		return strategygate.Record{}, e
	}
	return store.DeclareStrategyGate(ctx, d)
}
func validateGateDeclaration(d strategygate.Declaration) error {
	if e := d.Validate(); e != nil {
		return e
	}
	var spec strategy.Spec
	if json.Unmarshal(d.Spec, &spec) != nil {
		return strategygate.ErrInvalid
	}
	sid, e := spec.ID()
	if e != nil || sid != d.StrategyID {
		return strategygate.ErrInvalid
	}
	b, ok := decimal(d.Balance)
	if !ok || b.Sign() <= 0 {
		return strategygate.ErrInvalid
	}
	if d.Account != "cash" && d.Account != "margin" {
		return strategygate.ErrInvalid
	}
	if spec.ModelDriven {
		m, ok := decimal(d.Minimum)
		if !ok || m.Sign() < 0 {
			return strategygate.ErrInvalid
		}
		return nil
	}
	var declaredBase, declaredStress engine.CostModel
	if d.EngineVersion != engine.Version || json.Unmarshal(d.BaseCost, &declaredBase) != nil || json.Unmarshal(d.StressCost, &declaredStress) != nil || !HarsherCost(declaredBase, declaredStress) {
		return errors.New("gate requires the current engine and frozen base/stress costs")
	}
	assets := slices.Clone(d.Assets)
	if d.SettlementCalendar != "us-equity-t1-v1" {
		return errors.New("gate policy requires the default reviewed settlement calendar")
	}
	slices.Sort(assets)
	if len(assets) == 0 || len(assets) > strategy.MaxAssets || len(slices.Compact(slices.Clone(assets))) != len(assets) {
		return errors.New("gate requires distinct predeclared assets")
	}
	if !strategytrial.ValidID(d.ExperimentID) || len(d.Candidates) < 3 {
		return errors.New("gate requires base and at least two predeclared neighbours")
	}
	seen := map[string]bool{}
	identities := map[string]bool{}
	base := false
	for _, v := range d.Candidates {
		var s strategy.Spec
		if json.Unmarshal(v.Spec, &s) != nil {
			return strategygate.ErrInvalid
		}
		id, e := s.ID()
		if e != nil || s.ModelDriven || !strategytrial.ValidID(v.Name) || seen[v.Name] || identities[id] {
			return strategygate.ErrInvalid
		}
		seen[v.Name], identities[id] = true, true
		var minimum TradeabilityPlan
		if json.Unmarshal(v.Minimum, &minimum) != nil || minimum.StrategyID != id || minimum.MarksAt.After(time.Now()) {
			return errors.New("gate variant requires its frozen minimum trade plan")
		}
		if _, e := Tradeability(minimum); e != nil {
			return e
		}
		if string(minimum.Account) != d.Account {
			return errors.New("minimum plan account differs from declaration")
		}
		for _, unit := range minimum.Requirements {
			if !slices.Contains(assets, unit.Asset) {
				return errors.New("minimum plan asset is not declared")
			}
		}
		if v.Name == d.BaseCandidate {
			if id != d.StrategyID {
				return strategygate.ErrInvalid
			}
			base = true
		}
	}
	if !base {
		return strategygate.ErrInvalid
	}
	folds, e := Folds(d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo})
	if e != nil || len(folds) < 2 || d.Test < 20 || len(folds)*len(d.Candidates) > 1000 {
		return errors.New("gate requires bounded complete OOS folds of at least twenty sessions")
	}
	return nil
}

type frozenFamily struct {
	strategytrial.Store
	records []strategytrial.Record
}

func (f frozenFamily) StrategyTrials(context.Context, string) ([]strategytrial.Record, error) {
	return f.records, nil
}
func (f frozenFamily) StrategyTrial(_ context.Context, id string) (strategytrial.Record, error) {
	for _, r := range f.records {
		if r.Start.ID == id {
			return r, nil
		}
	}
	return strategytrial.Record{}, strategytrial.ErrNotFound
}

func gateBenchmark(r strategygate.Reference, dates []strategytrial.DailyReturn) Benchmark {
	b := Benchmark{Name: r.Name, DataSHA256: r.DataSHA256, Returns: []BenchmarkReturn{}}
	byDate := map[string]string{}
	for _, v := range r.Returns {
		if _, ok := byDate[v.Date]; ok {
			return Benchmark{}
		}
		byDate[v.Date] = v.Return
	}
	for _, v := range dates {
		x, ok := byDate[v.Date]
		if !ok {
			return Benchmark{}
		}
		b.Returns = append(b.Returns, BenchmarkReturn{Date: v.Date, Return: x})
	}
	return b
}

// EvaluateGate computes once from retained runs, preserving every failed check.
// An exact retry reads the original decision, including its snapshot/search N.
func EvaluateGate(ctx context.Context, store GateStore, id string) (strategygate.Record, error) {
	r, e := store.StrategyGate(ctx, id)
	if e != nil {
		return r, e
	}
	if r.Decision != nil {
		return r, nil
	}
	d := r.Declaration
	if e = validateGateDeclaration(d); e != nil {
		return r, e
	}
	family, e := store.StrategyTrials(ctx, d.Family)
	if e != nil {
		return r, e
	}
	familyHash, e := strategygate.Hash(family)
	if e != nil {
		return r, e
	}
	declarationHash, e := strategygate.Hash(d)
	if e != nil {
		return r, e
	}
	out := strategygate.Decision{ID: id, StrategyID: d.StrategyID, DeclarationSHA256: declarationHash, FamilySHA256: familyHash, Policy: d.Policy, SearchTotal: int64(len(family)), At: time.Now().UTC(), Status: "passed", Stage: "incubation", Checks: []strategygate.Check{}}
	check := func(code string, pass bool, reason string) {
		out.Checks = append(out.Checks, strategygate.Check{Code: code, Passed: pass, Reason: reason})
		if !pass {
			out.Status, out.Stage = "failed", "idea"
		}
	}
	var spec strategy.Spec
	if e = json.Unmarshal(d.Spec, &spec); e != nil {
		return r, e
	}
	if spec.ModelDriven {
		out.Status, out.Stage, out.Minimum = "forward_only", "incubation", d.Minimum
		out.EvidenceClass = "forward_only"
		out.Checks = append(out.Checks, strategygate.Check{Code: "backtest_gate", Skipped: true, Reason: "runtime_model_requires_forward_only_path"})
		return store.RecordStrategyGate(ctx, out)
	}
	if len(family) > 0 {
		out.EvidenceClass = "historical_market_unqualified"
		for _, trial := range family {
			if trial.Start.EvidenceClass != "historical_market_unqualified" {
				out.EvidenceClass = "synthetic_fixture"
				break
			}
		}
	}
	check("evidence_class", out.EvidenceClass == "historical_market_unqualified", "operational_gate_requires_historical_market_trials")
	check("source_coverage", d.Coverage, "reviewed_universe_and_price_coverage_required")
	for _, v := range d.Candidates {
		var plan TradeabilityPlan
		if e = json.Unmarshal(v.Minimum, &plan); e != nil {
			return r, e
		}
		check("minimum_plan_clock/"+v.Name, !plan.MarksAt.After(r.DeclaredAt), "minimum_trade_plan_must_be_known_when_gate_is_declared")
		if v.Name == d.BaseCandidate {
			minimum, e := Tradeability(plan)
			if e != nil {
				return r, e
			}
			out.Minimum = minimum.MinimumBalance
		}
	}
	check("point_in_time", d.Availability, "historical_availability_evidence_required")
	folds, _ := Folds(d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo})
	frozen := frozenFamily{Store: store, records: family}
	byID := map[string]strategytrial.Record{}
	for _, v := range family {
		byID[v.Start.ID] = v
	}
	neighbours, positive := 0, 0
	for fi, fold := range folds {
		baseCosts := map[string]engine.CostModel{}
		for _, variant := range d.Candidates {
			var variantSpec strategy.Spec
			if e = json.Unmarshal(variant.Spec, &variantSpec); e != nil {
				return r, e
			}
			wantedID, _ := variantSpec.ID()
			for _, role := range []string{"train", "test", "stress"} {
				runID := fmt.Sprintf("%s-f%d-%s-%s", d.ExperimentID, fi, variant.Name, role)
				v, found := byID[runID]
				code := fmt.Sprintf("fold_%d/%s/%s", fi, variant.Name, role)
				complete := found && v.Outcome != nil && v.Outcome.Status == "completed" && len(v.Returns) > 0 && !v.Outcome.FinishedAt.After(out.At) && !v.Outcome.FinishedAt.Before(v.Start.StartedAt)
				check(code+"/completed", complete, "expected_registered_attempt_must_complete")
				if !found {
					continue
				}
				var cfg struct {
					EngineVersion      string `json:"engine_version"`
					Account            string `json:"account"`
					InitialCash        string `json:"initial_cash"`
					SettlementCalendar string `json:"settlement_calendar"`
					Unlevered          bool
					Deposits           json.RawMessage
					Assets             []string
					Cost               engine.CostModel `json:"cost"`
					Experiment         struct {
						Version, Candidate, Role string
						Fold                     int
						Design                   Design
						Boundaries               FoldManifest
						Minimum                  *TradeabilityPlan
					} `json:"experiment"`
				}
				if e = json.Unmarshal(v.Start.Config, &cfg); e != nil {
					check(code+"/config_integrity", false, "retained_engine_config_cannot_be_decoded")
					continue
				}
				wantManifest := manifest(fold, d.Calendar)
				identity := v.Start.StrategyID == wantedID && v.Start.DataSHA256 == d.DataSHA256 && !v.Start.StartedAt.Before(r.DeclaredAt) && !v.Start.StartedAt.After(out.At) && cfg.Experiment.Version == "walk-forward-v1" && cfg.Experiment.Candidate == variant.Name && cfg.Experiment.Role == role && cfg.Experiment.Fold == fi && cfg.Experiment.Design == (Design{d.Train, d.Test, d.Purge, d.Embargo}) && cfg.Experiment.Boundaries == wantManifest
				check(code+"/declaration", identity, "run_must_match_predeclared_version_inputs_and_purged_fold")
				capital, cok := decimal(cfg.InitialCash)
				declaredCapital, dok := decimal(d.Balance)
				costHash, _ := strategygate.Hash(cfg.Cost)
				wantCost := d.BaseCost
				if role == "stress" {
					wantCost = d.StressCost
				}
				var declaredCost engine.CostModel
				if e = json.Unmarshal(wantCost, &declaredCost); e != nil {
					return r, e
				}
				declaredCostHash, _ := strategygate.Hash(declaredCost)
				declaredAssets := slices.Clone(d.Assets)
				slices.Sort(declaredAssets)
				settings := cfg.EngineVersion == d.EngineVersion && cfg.Account == d.Account && cok && dok && capital.Cmp(declaredCapital) == 0 && cfg.Unlevered == d.Unlevered && (len(cfg.Deposits) == 0 || string(cfg.Deposits) == "null") && costHash == declaredCostHash && slices.Equal(cfg.Assets, declaredAssets) && cfg.SettlementCalendar == d.SettlementCalendar
				check(code+"/engine_settings", settings, "engine_account_capital_and_costs_must_match_declaration_without_extra_deposits")
				var declaredPlan TradeabilityPlan
				if e = json.Unmarshal(variant.Minimum, &declaredPlan); e != nil {
					return r, e
				}
				declaredPlanHash, _ := strategygate.Hash(declaredPlan)
				runPlanHash, _ := strategygate.Hash(cfg.Experiment.Minimum)
				planMatches := declaredPlanHash == runPlanHash
				check(code+"/minimum_plan", planMatches, "run_minimum_trade_plan_must_match_the_frozen_gate_declaration")
				if role == "test" {
					baseCosts[variant.Name] = cfg.Cost
				}
				if role == "stress" {
					base, ok := baseCosts[variant.Name]
					check(code+"/harsher_cost", ok && HarsherCost(base, cfg.Cost), "each_cost_component_must_not_improve")
				}
				if !complete || !identity || !settings || !planMatches {
					continue
				}
				wantDates := fold.Test
				if role == "train" {
					wantDates = fold.Train
				}
				dates := []string{}
				for _, day := range v.Returns {
					dates = append(dates, day.Date)
				}
				check(code+"/dates", slices.Equal(dates, wantDates), "all_declared_sessions_must_be_retained")
				if role == "train" {
					continue
				}
				cash := gateBenchmark(d.Cash, v.Returns)
				spy := gateBenchmark(d.SPY, v.Returns)
				cashExcess := benchmarkExcess(v.Returns, cash)
				spyExcess := benchmarkExcess(v.Returns, spy)
				if variant.Name != d.BaseCandidate && role == "test" {
					neighbours++
					if cashExcess.Value != nil && *cashExcess.Value > 0 {
						positive++
					}
				}
				if variant.Name != d.BaseCandidate {
					continue
				}
				check(code+"/cash", cashExcess.Value != nil && *cashExcess.Value > 0, "positive_costed_excess_over_dated_cash_required")
				if d.Benchmark == "spy" {
					check(code+"/spy", spyExcess.Value != nil && *spyExcess.Value > 0, "positive_costed_excess_over_dated_SPY_required")
				} else {
					check(code+"/spy_comparison", spyExcess.Value != nil, "dated_SPY_comparison_must_be_available")
				}
				if role == "stress" {
					continue
				}
				a, e := Assess(ctx, frozen, runID, cash, spy, 4)
				if e != nil {
					check(code+"/statistics_integrity", false, "comparable_family_statistics_could_not_be_computed")
					continue
				}
				metric := func(s Statistic) strategygate.Metric { return strategygate.Metric{Value: s.Value, Reason: s.Reason} }
				out.Diagnostics = append(out.Diagnostics, strategygate.Diagnostic{TrialID: runID, DSR: metric(a.DSR), PBO: metric(a.PBO), CashExcess: metric(a.CashExcess), SPYExcess: metric(a.SPYExcess)})
				check(code+"/DSR", a.DSR.Value != nil && *a.DSR.Value >= .95, "deflated_sharpe_at_least_0.95; "+a.DSR.Reason)
				if a.PBO.Value == nil && a.PBO.Reason == "too_few_trials" {
					out.Checks = append(out.Checks, strategygate.Check{Code: code + "/PBO", Skipped: true, Reason: "unavailable_with_fewer_than_four_comparable_configurations"})
				} else {
					check(code+"/PBO", a.PBO.Value != nil && *a.PBO.Value <= .2, "CSCV_probability_at_most_0.20; "+a.PBO.Reason)
				}
				if a.Minimum != nil {
					need, ok := decimal(out.Minimum)
					balance, bok := decimal(d.Balance)
					check(code+"/minimum", ok && bok && balance.Cmp(need) >= 0 && string(a.Minimum.Plan.Account) == d.Account, "current_book_and_account_must_fit_predeclared_trade_plan")
				} else {
					check(code+"/minimum", false, "predeclared_trade_plan_unavailable; "+a.MinimumReason)
				}
			}
		}
	}
	check("neighbour_sensitivity", neighbours == len(folds)*(len(d.Candidates)-1) && positive*3 >= neighbours*2, "at_least_two_thirds_of_all_predeclared_neighbour_tests_must_beat_cash")
	return store.RecordStrategyGate(ctx, out)
}
