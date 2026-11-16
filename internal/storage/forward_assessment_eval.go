package storage

import (
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// evaluateForward never consumes IncubationReturns or registry outcome returns.
// Samples are flat-to-flat episodes of the whole sleeve, clustered by New York entry
// day. A multi-day/overlapping holding cannot create independent daily samples.
func evaluateForward(p forward.AssessmentPlanRecord, w forward.Record, b account.CappedState, starts []strategytrial.Start, pending bool, at time.Time) forward.Assessment {
	d := w.Declaration
	a := forward.Assessment{PlanID: p.Plan.ID, DeclarationID: d.ID, StrategyID: d.StrategyID, SleeveID: d.SleeveID, Policy: forward.AssessmentPolicy, At: at.UTC(), Status: "unavailable", PlanSHA256: p.SHA256, WindowSHA256: w.SHA256, SourceSHA256: w.SourceSHA256, JournalVersion: b.Version, JournalSHA256: workerplan.Hash(b), SearchStarts: starts, SearchTotal: int64(len(starts)), FamilySHA256: workerplan.Hash(starts), FillSHA256: []string{}}
	finish := func(reason string) forward.Assessment {
		a.Reason = reason
		a.QualificationCredit = a.Status == "passed"
		a.SHA256 = workerplan.Hash(a)
		return a
	}
	if pending {
		return finish("family_accounting_pending")
	}
	if a.SearchTotal < 1 || a.SearchTotal < w.SearchTotal || a.SearchTotal > d.MaxSearchTrials {
		return finish("actual_search_total_outside_declaration")
	}
	if at.Before(d.OutcomeDeadline) {
		return finish("outcome_window_not_closed")
	}
	if len(b.Events) > 0 && b.Events[len(b.Events)-1].At.After(at) {
		return finish("assessment_precedes_journal")
	}
	plans := map[string]account.IncubationPlan{}
	observations := []account.IncubationPlan{}
	orders := map[string]account.BookOrder{}
	type observedFill struct {
		event account.BookCommand
		plan  account.IncubationPlan
	}
	var fills []observedFill
	for _, e := range b.Events {
		if e.Kind == "qualification_plan" && e.Incubation != nil && e.Incubation.Qualification != nil && e.Incubation.Qualification.PlanID == p.Plan.ID {
			plans[e.Incubation.Rebalance.ID] = *e.Incubation
			observations = append(observations, *e.Incubation)
		}
		if e.Kind == "portfolio_reserve" && e.Order != nil && e.Order.SleeveID == d.SleeveID {
			orders[e.Order.ID] = *e.Order
		}
		if e.Fill != nil {
			o, ok := orders[e.Fill.OrderID]
			if !ok {
				continue
			}
			plan, ok := plans[o.AllocationID]
			if !ok {
				return finish("incubation_or_unclassified_fill_in_window_owner")
			}
			if e.ModelFill == nil || e.Fill.At.Before(d.WindowStart) || !e.Fill.At.Before(d.OutcomeDeadline) || e.At.After(d.OutcomeDeadline) {
				return finish("fill_cost_or_original_clock_unavailable")
			}
			if plan.Qualification.Benchmark.Validate(plan.Rebalance.At) != nil || plan.Qualification.Benchmark.StrategyID != d.Benchmark.StrategyID || e.Fill.At.Sub(plan.Qualification.Benchmark.ObservedAt) > time.Minute {
				return finish("benchmark_custody_unavailable")
			}
			for _, q := range plan.Quotes {
				if q.EvidenceClass != "prospective_market_unqualified" {
					return finish("synthetic_or_unclassified_quote")
				}
			}
			fills = append(fills, observedFill{e, plan})
		}
	}
	if len(fills) == 0 {
		return finish("no_prospective_qualification_fills")
	}
	slices.SortStableFunc(fills, func(x, y observedFill) int { return x.event.Fill.At.Compare(y.event.Fill.At) })
	positions := map[string]*big.Rat{}
	cash, fees, capital := new(big.Rat), new(big.Rat), new(big.Rat)
	allCash, modelFees, benchmarkReturn := new(big.Rat), new(big.Rat), new(big.Rat)
	var opened time.Time
	var entryDay string
	var openBenchmark *big.Rat
	var eligible bool
	days := map[string]*big.Rat{}
	cumulative, high, dd := new(big.Rat), new(big.Rat), new(big.Rat)
	episodeHigh, episodeLow, episodeDD := new(big.Rat), new(big.Rat), new(big.Rat)
	observe := func(plan account.IncubationPlan) bool {
		value := new(big.Rat).Set(cash)
		for symbol, quantity := range positions {
			if quantity.Sign() == 0 {
				continue
			}
			q, ok := plan.Quotes[symbol]
			if !ok || account.ValidateIncubationQuote(q, plan.DecidedAt, plan.Rebalance.At) != nil {
				return false
			}
			price := q.Bid
			if quantity.Sign() < 0 {
				price = q.Ask
			}
			n, ok := new(big.Rat).SetString(price)
			if !ok {
				return false
			}
			value.Add(value, new(big.Rat).Mul(quantity, n))
		}
		if value.Cmp(episodeHigh) > 0 {
			episodeHigh.Set(value)
		}
		if value.Cmp(episodeLow) < 0 {
			episodeLow.Set(value)
		}
		loss := new(big.Rat).Sub(episodeHigh, value)
		if loss.Cmp(episodeDD) > 0 {
			episodeDD.Set(loss)
		}
		return true
	}
	observationIndex := 0
	for _, v := range fills {
		e, plan := v.event, v.plan
		f := e.Fill
		for observationIndex < len(observations) && !observations[observationIndex].Rebalance.At.After(f.At) {
			if !opened.IsZero() && !observe(observations[observationIndex]) {
				return finish("holding_mark_unavailable")
			}
			observationIndex++
		}
		if opened.IsZero() {
			opened = f.At
			entryDay = plan.DecidedAt.In(newYork).Format(time.DateOnly)
			eligible = !plan.DecidedAt.Before(p.Plan.HoldoutStart) && plan.DecidedAt.Before(d.WindowEnd)
			// Entry benchmark uses its retained total-return index, never raw price
			// substituted for a dividend-adjusted benchmark.
			openBenchmark, _ = new(big.Rat).SetString(plan.Qualification.Benchmark.Index)
			cash.SetInt64(0)
			capital.SetInt64(0)
			episodeHigh.SetInt64(0)
			episodeLow.SetInt64(0)
			episodeDD.SetInt64(0)
		}
		if f.At.Sub(opened) > time.Duration(d.MaxHoldingSeconds)*time.Second {
			return finish("maximum_holding_exceeded")
		}
		qty, qe := new(big.Rat).SetString(f.Quantity)
		price, pe := new(big.Rat).SetString(f.Price)
		fee, fe := new(big.Rat).SetString(f.Fee)
		effect, ce := new(big.Rat).SetString(e.ModelFill.CashEffect)
		mf, mfe := new(big.Rat).SetString(e.ModelFill.ModelFee)
		if !qe || !pe || !fe || !ce || !mfe {
			return finish("malformed_fill_amount")
		}
		cash.Add(cash, effect)
		allCash.Add(allCash, effect)
		modelFees.Add(modelFees, mf)
		fees.Add(fees, fee)
		// Sum absolute entry and resize notionals. This conservative denominator
		// cannot be reduced by deposits or optimistic venue cash.
		if f.Side == "buy" {
			capital.Add(capital, new(big.Rat).Mul(qty, price))
		} else {
			qty.Neg(qty)
		}
		if positions[f.Symbol] == nil {
			positions[f.Symbol] = new(big.Rat)
		}
		positions[f.Symbol].Add(positions[f.Symbol], qty)
		a.FillSHA256 = append(a.FillSHA256, workerplan.Hash(e))
		if !observe(plan) {
			return finish("holding_mark_unavailable")
		}
		flat := true
		for _, q := range positions {
			if q.Sign() != 0 {
				flat = false
			}
		}
		if !flat {
			continue
		}
		if eligible {
			if capital.Sign() <= 0 {
				return finish("episode_capital_unavailable")
			}
			endBenchmark, _ := new(big.Rat).SetString(plan.Qualification.Benchmark.Index)
			bench := new(big.Rat).Sub(new(big.Rat).Quo(endBenchmark, openBenchmark), big.NewRat(1, 1))
			benchmarkReturn.Add(benchmarkReturn, bench)
			net := new(big.Rat).Quo(cash, capital)
			excess := new(big.Rat).Sub(net, bench)
			day := entryDay
			if days[day] == nil {
				days[day] = new(big.Rat)
			}
			days[day].Add(days[day], excess)
			// Include all retained execution/hold quote marks, without treating them
			// as independent statistical samples. Unobserved between-quote extrema
			// remain outside this observed drawdown measure.
			boundaryLoss := new(big.Rat).Sub(high, cumulative)
			boundaryLoss.Sub(boundaryLoss, new(big.Rat).Quo(episodeLow, capital))
			withinLoss := new(big.Rat).Quo(episodeDD, capital)
			if boundaryLoss.Cmp(dd) > 0 {
				dd.Set(boundaryLoss)
			}
			if withinLoss.Cmp(dd) > 0 {
				dd.Set(withinLoss)
			}
			episodePeak := new(big.Rat).Add(cumulative, new(big.Rat).Quo(episodeHigh, capital))
			if episodePeak.Cmp(high) > 0 {
				high.Set(episodePeak)
			}
			cumulative.Add(cumulative, net)
			if cumulative.Cmp(high) > 0 {
				high.Set(cumulative)
			}
			loss := new(big.Rat).Sub(high, cumulative)
			if loss.Cmp(dd) > 0 {
				dd.Set(loss)
			}
			a.IndependentDecisions++
		}
		opened = time.Time{}
	}
	if !opened.IsZero() {
		return finish("unclosed_or_overlapping_exposure")
	}
	pendingOrders, e := account.PendingQualificationOrders(b, d.SleeveID)
	if e != nil {
		return finish("journal_replay_unavailable")
	}
	if pendingOrders {
		return finish("pending_window_orders")
	}
	a.Sessions = len(days)
	sum := new(big.Rat)
	for _, excess := range days {
		sum.Add(sum, excess)
		if excess.Sign() > 0 {
			a.PositiveDays++
		}
	}
	a.NetExcess = sum.FloatString(18)
	a.MaximumDrawdown = dd.FloatString(18)
	a.Fees = fees.FloatString(12)
	a.CashEffect = allCash.FloatString(12)
	a.NetReturn = cumulative.FloatString(18)
	a.BenchmarkReturn = benchmarkReturn.FloatString(18)
	a.ModelFees = modelFees.FloatString(12)
	if a.Sessions < d.MinSessions || a.IndependentDecisions < d.MinIndependentDecisions {
		return finish("insufficient_independent_holdout_samples")
	}
	tail := signTestTail(a.Sessions, a.PositiveDays)
	den := new(big.Int).Lsh(big.NewInt(1), uint(a.Sessions))
	pv := new(big.Rat).SetFrac(tail, den)
	a.PValue = pv.RatString()
	adjusted := new(big.Rat).Mul(pv, big.NewRat(a.SearchTotal, 1))
	if adjusted.Cmp(big.NewRat(1, 1)) > 0 {
		adjusted.SetInt64(1)
	}
	a.AdjustedPValue = adjusted.RatString()
	a.Status = "failed"
	if !signTestPasses(a.Sessions, a.PositiveDays, p.Plan.AlphaBasisPoints, int(a.SearchTotal)) {
		return finish("actual_family_corrected_sign_test_failed")
	}
	minimum, _ := new(big.Rat).SetString(d.MinimumNetExcess)
	if minimum.Sign() < 0 {
		minimum.SetInt64(0)
	}
	maximum, _ := new(big.Rat).SetString(d.MaximumDrawdown)
	if sum.Cmp(minimum) <= 0 {
		return finish("declared_benchmark_or_minimum_not_beaten")
	}
	if dd.Cmp(maximum) > 0 {
		return finish("declared_drawdown_exceeded")
	}
	// Existing owner controls cannot be overwritten by a promotion.
	if err := account.CheckQualificationControls(b, d.StrategyID); err != nil {
		return finish("owner_or_hard_risk_control")
	}
	a.Status = "passed"
	return finish("predeclared_prospective_holdout_passed")
}
