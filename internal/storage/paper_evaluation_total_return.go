package storage

import (
	"math/big"
	"regexp"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperTotalReturnEvaluationRecipe = "native-threshold-total-return-plan-v8"
const PaperTotalReturnAssessmentRecipe = "native-predeclared-total-return-benchmarks-v13"
const PaperTotalReturnPortfolioMethod = "owner-selected-finite-capital-reconciled-total-return-v3"

// PaperEvaluationTotalReturn applies one covered instrument's reconciled
// actions to a variant's strategy book and to its passive benchmark book.
type PaperEvaluationTotalReturn struct {
	Strategy papereconomics.TotalReturn  `json:"strategy"`
	Passive  *papereconomics.TotalReturn `json:"passive,omitempty"`
}

// coverageActions derives the reconciled actions of every covered instrument.
// An instrument whose reconciled values cannot be applied, such as a payable
// date before its ex-date, is omitted and its variants stay unavailable.
func coverageActions(c PaperEvaluationCorporateCoverage) []papereconomics.Actions {
	out := []papereconomics.Actions{}
	for _, in := range c.Instruments {
		if a, ok := instrumentActions(in); ok {
			out = append(out, a)
		}
	}
	return out
}

func instrumentActions(in PaperEvaluationInstrumentCoverage) (papereconomics.Actions, bool) {
	if !in.Result.Covered() {
		return papereconomics.Actions{}, false
	}
	a := papereconomics.Actions{Symbol: in.Plan.Symbol, WindowStart: in.Result.Window.Start, WindowEnd: in.Result.Window.End, Actions: []papereconomics.Action{}}
	for _, d := range in.Result.Dividends {
		a.Actions = append(a.Actions, papereconomics.Action{Kind: "cash_dividend", ExDate: d.ExDate, PayableDate: d.PayDate, Amount: d.Amount})
	}
	for _, s := range in.Result.Splits {
		a.Actions = append(a.Actions, papereconomics.Action{Kind: "split", ExDate: s.ExDate, From: s.From, To: s.To})
	}
	return a, a.Validate() == nil
}

// Retained cents and quantities are exact decimals or reduced a/b fractions.
var exactCentsText = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$|^-?[0-9]+/[0-9]+$`)

func exactCents(s *string) (*big.Rat, bool) {
	if s == nil || len(*s) > 128 || !exactCentsText.MatchString(*s) {
		return nil, false
	}
	return new(big.Rat).SetString(*s)
}

var totalReturnStatuses = []string{"observed_total_return", "holding_outside_corporate_coverage_window", "fill_exceeds_split_adjusted_position", "terminal_mark_unavailable", "terminal_mark_predates_corporate_action", "same_ex_date_split_and_other_action_unsupported"}

// A retained result's values must agree with its status, so a rehashed row
// cannot claim availability without the values it is decided on.
func validTotalReturn(t *papereconomics.TotalReturn, inputSHA256 string) bool {
	u := clone(*t)
	u.SHA256 = ""
	_, equity := exactCents(t.EquityCents)
	_, net := exactCents(t.NetCents)
	if !slices.Contains(totalReturnStatuses, t.Status) || t.Available() != (t.EquityCents != nil) || (t.EquityCents != nil) != (t.NetCents != nil) || t.EquityCents != nil && (!equity || !net) {
		return false
	}
	return t.Method == papereconomics.TotalReturnMethod && t.InputSHA256 == inputSHA256 && t.Authority == "owner_selected_reconciled_total_return_accounting_only" && t.SHA256 == workerplan.Hash(u) && !t.AdmissionAvailable && !t.ExecutionAvailable && !t.QualificationCredit
}

func validPortfolioTotalReturn(t *PaperEvaluationTotalReturn, value *papereconomics.Result, b *papereconomics.Benchmarks) bool {
	if t == nil {
		return true
	}
	if value == nil || b == nil || !validTotalReturn(&t.Strategy, value.InputSHA256) || (t.Passive == nil) != (b.Passive == nil) {
		return false
	}
	return t.Passive == nil || validTotalReturn(t.Passive, b.Passive.InputSHA256) && t.Passive.ActionsSHA256 == t.Strategy.ActionsSHA256
}

// The passive benchmark is a fair comparison only when its whole intended
// quantity filled; a partial entry leaves most of it in cash.
const fullPassiveEntry = "observed_price_path_only_total_return_unavailable"

// applyTotalReturnBenchmarks decides benchmark_comparison from retained total
// returns. It passes only when the strategy's net exceeds both benchmarks by
// the predeclared minimum in base and stress models, fails when either falls
// short, and otherwise stays unavailable or pending. Cash earns no interest,
// so its net is zero.
func applyTotalReturnBenchmarks(r *PaperEvaluationAssessment, minimum int64) error {
	if r.Portfolio == nil || r.CorporateCoverage == nil {
		return ErrPaperEvaluationAssessmentInvalid
	}
	type instrument struct {
		covered bool
		hash    string
	}
	byStrategy := map[string]instrument{}
	for _, in := range r.CorporateCoverage.Instruments {
		v := instrument{covered: in.Result.Covered()}
		if a, ok := instrumentActions(in); ok {
			v.hash = workerplan.Hash(a)
		}
		for _, id := range in.StrategyIDs {
			byStrategy[id] = v
		}
	}
	rows := map[string]PaperEvaluationPortfolioVariant{}
	for _, v := range r.Portfolio.Variants {
		in := byStrategy[v.StrategyID]
		for _, t := range []*PaperEvaluationTotalReturn{v.BaseTotalReturn, v.StressTotalReturn} {
			// Total return exists exactly for applicable covered instruments and
			// uses that instrument's actions; a forged row cannot be attached.
			if t != nil && (in.hash == "" || t.Strategy.ActionsSHA256 != in.hash) {
				return ErrPaperEvaluationAssessmentInvalid
			}
		}
		if in.hash != "" && v.Base != nil && (v.BaseTotalReturn == nil || v.StressTotalReturn == nil) {
			return ErrPaperEvaluationAssessmentInvalid
		}
		rows[v.StrategyID] = v
	}
	limit := new(big.Rat).SetInt64(minimum)
	for i := range r.Checks {
		check := &r.Checks[i]
		if check.Name != "benchmark_comparison" || check.Status == "failed" {
			continue
		}
		in, v := byStrategy[check.StrategyID], rows[check.StrategyID]
		check.Status = "unavailable"
		switch {
		case !in.covered:
			check.ReasonCode = "observed_price_benchmarks_do_not_establish_total_return_or_corporate_coverage"
			continue
		case in.hash == "":
			check.ReasonCode = "reconciled_actions_not_applicable_to_total_return"
			continue
		case v.BaseTotalReturn == nil:
			check.ReasonCode = "variant_books_unavailable_for_total_return"
			continue
		}
		reason := ""
		excess := []*big.Rat{}
		for j, t := range []*PaperEvaluationTotalReturn{v.BaseTotalReturn, v.StressTotalReturn} {
			b := []*papereconomics.Benchmarks{v.BaseBenchmarks, v.StressBenchmarks}[j]
			switch {
			case !t.Strategy.Available():
				reason = "strategy_total_return_unavailable"
			case t.Passive == nil || b.PassiveStatus != fullPassiveEntry:
				reason = "passive_benchmark_entry_incomplete"
			case !t.Passive.Available():
				reason = "passive_total_return_unavailable"
			}
			if reason != "" {
				break
			}
			strategy, ok1 := exactCents(t.Strategy.NetCents)
			passive, ok2 := exactCents(t.Passive.NetCents)
			if !ok1 || !ok2 {
				return ErrPaperEvaluationAssessmentInvalid
			}
			excess = append(excess, strategy, new(big.Rat).Sub(strategy, passive))
		}
		if reason != "" {
			check.ReasonCode = reason
			continue
		}
		if r.Evidence.Phase != "outcome_deadline_elapsed" {
			check.Status, check.ReasonCode = "pending", "predeclared_outcome_window_not_closed"
			continue
		}
		if slices.ContainsFunc(excess, func(x *big.Rat) bool { return x.Cmp(limit) < 0 }) {
			check.Status, check.ReasonCode = "failed", "total_return_excess_below_predeclared_minimum"
			r.Status = "blocked"
			continue
		}
		check.Status, check.ReasonCode = "passed", "total_return_exceeds_cash_and_passive_by_predeclared_minimum"
	}
	return nil
}
