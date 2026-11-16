package account

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

// ReviewPolicy is the immutable, deliberately conservative oversight policy.
const ReviewPolicy = "daily-sleeve-review-v1"

// ReviewDeclaration freezes an operator-imported historical comparison and a
// sleeve-specific loss budget before collecting review samples. Hashes bind
// source material retained outside Git; they are not source attestations.
// Empty backtests are explicitly unavailable, including forward-only models.
type ReviewDeclaration struct {
	ID, SleeveID, StrategyID, GateID, Policy, Benchmark       string
	Backtest                                                  []strategygate.ReferenceReturn
	BacktestSourceSHA256, BacktestUnavailable, DrawdownBudget string
	BacktestEnd                                               time.Time
}

// ReviewMark is a dated prospective mark import, never broker buying power.
type ReviewMark struct {
	Prices                      map[string]string
	SourceSHA256, EvidenceClass string
	ObservedAt, ReceivedAt      time.Time
}

// ReviewBenchmark is the observed return over precisely the preceding review
// interval. Cash returns must also have a source; no benchmark is synthesized.
type ReviewBenchmark struct {
	Return, SourceSHA256, EvidenceClass string
	StartAt, EndAt, ReceivedAt          time.Time
}

// ReviewInput supplies observations only. It cannot choose an action or size.
type ReviewInput struct {
	DeclarationID string
	At            time.Time
	Mark          ReviewMark
	Benchmark     *ReviewBenchmark
}

// StrategyReview binds the exact journal prefix, observation clocks, frozen
// declaration, policy, computed attribution and triggering reasons. A research
// request is an inactive instruction for inspection, with no model authority.
type StrategyReview struct {
	Input                                                                             ReviewInput
	Policy, DeclarationSHA256, JournalSHA256                                          string
	JournalVersion                                                                    int64
	SleeveID, StrategyID, Action, Scale, BudgetCeiling                                string
	Reasons                                                                           []string
	PnL, Budget, ForwardReturn, BenchmarkReturn, BacktestMean                         string
	Drawdown, MaximumDrawdown, MeanReturn, MeanExcess, EdgeDecay, AdverseFillFraction string
	Samples, Fills                                                                    int
	ResearchRequested, QualificationCredit                                            bool
}

type reviewLifecycle struct {
	At      time.Time
	Scale   string
	Retired bool
}

func reviewPrefix(s CappedState, id string) CappedState {
	for i, e := range s.Events {
		if e.ID == id {
			s.Events, s.Version = s.Events[:i], int64(i)
			break
		}
	}
	return s
}

func reviewDeclaration(s CappedState, id string) (*ReviewDeclaration, time.Time) {
	for _, e := range s.Events {
		if e.Kind == "review_declare" && e.ReviewDeclaration != nil && e.ReviewDeclaration.ID == id {
			return e.ReviewDeclaration, e.At
		}
	}
	return nil, time.Time{}
}

func validateReviewDeclaration(s CappedState, c BookCommand) error {
	s = reviewPrefix(s, c.ID)
	d := c.ReviewDeclaration
	if d == nil || !bookID.MatchString(d.ID) || c.ID != "review-declare-"+d.ID || d.Policy != ReviewPolicy || !slices.Contains([]string{"cash", "spy"}, d.Benchmark) || len(d.Backtest) > 2520 {
		return errors.New("invalid review declaration")
	}
	roster, err := PortfolioRoster(s)
	if err != nil {
		return err
	}
	owner, ok := roster[d.SleeveID]
	if !ok || owner.StrategyID != d.StrategyID || owner.GateID != d.GateID {
		return errors.New("review declaration owner differs")
	}
	for _, e := range s.Events {
		if e.ReviewDeclaration != nil && e.ReviewDeclaration.SleeveID == d.SleeveID {
			return errors.New("review comparison already frozen")
		}
	}
	budget, err := allocationAmount(d.DrawdownBudget)
	if err != nil || budget.Sign() <= 0 || budget.Cmp(big.NewRat(15, 100)) > 0 {
		return errors.New("invalid sleeve drawdown budget")
	}
	if len(d.Backtest) == 0 {
		if d.BacktestUnavailable == "" || len(d.BacktestUnavailable) > 256 {
			return errors.New("missing backtest unavailability reason")
		}
	} else {
		if owner.ModelDriven || !hashText.MatchString(d.BacktestSourceSHA256) || d.BacktestEnd.IsZero() || !d.BacktestEnd.Before(c.At) || d.BacktestUnavailable != "" {
			return errors.New("invalid frozen backtest provenance")
		}
		last := ""
		for _, v := range d.Backtest {
			day, e := time.Parse(time.DateOnly, v.Date)
			n, ne := allocationAmount(v.Return)
			if e != nil || ne != nil || n.Cmp(big.NewRat(-1, 1)) <= 0 || n.Cmp(big.NewRat(1, 1)) > 0 || v.Date <= last || day.After(d.BacktestEnd) {
				return errors.New("invalid frozen backtest sample")
			}
			last = v.Date
		}
	}
	return nil
}

// allocationAmount accepts bounded signed decimal returns as well as amounts.
func allocationAmount(v string) (*big.Rat, error) {
	if len(v) == 0 || len(v) > 40 {
		return nil, errors.New("invalid review decimal")
	}
	n := v
	if n[0] == '-' {
		n = n[1:]
	}
	// risk precision is sufficient for returns; disallow fractions and exponents.
	dots := 0
	for _, c := range n {
		if c == '.' {
			dots++
		} else if c < '0' || c > '9' {
			return nil, errors.New("invalid review decimal")
		}
	}
	if dots > 1 {
		return nil, errors.New("invalid review decimal")
	}
	r, ok := new(big.Rat).SetString(v)
	if !ok {
		return nil, errors.New("invalid review decimal")
	}
	return r, nil
}

func reviewDay(at time.Time) string {
	zone, _ := time.LoadLocation("America/New_York") // tzdata is embedded by capped.go
	return at.In(zone).Format(time.DateOnly)
}

func validReviewMark(m ReviewMark, at time.Time) bool {
	if !hashText.MatchString(m.SourceSHA256) || m.EvidenceClass != "prospective_market_unqualified" || m.ObservedAt.IsZero() || m.ObservedAt.After(m.ReceivedAt) || m.ReceivedAt.After(at) || at.Sub(m.ObservedAt) > time.Minute || len(m.Prices) > 256 {
		return false
	}
	for symbol, price := range m.Prices {
		p, e := bookRat(price)
		if !bookSymbol.MatchString(symbol) || e != nil || p.Sign() <= 0 {
			return false
		}
	}
	return true
}

// reviewAttribution derives cash and signed holdings ONLY from this sleeve's
// retained owned fills. It includes the existing worse-of-venue/model penalty.
func reviewAttribution(s CappedState, sleeve string, mark ReviewMark, at time.Time) (string, string, int, bool) {
	if !validReviewMark(mark, at) {
		return "", "", 0, false
	}
	orders := map[string]BookOrder{}
	positions := map[string]*big.Rat{}
	pnl, adverse, notional := new(big.Rat), new(big.Rat), new(big.Rat)
	fills := 0
	for _, e := range s.Events {
		if e.Order != nil && e.Kind == "portfolio_reserve" {
			orders[e.Order.ID] = *e.Order
		}
		if e.Kind != "fill" || e.Fill == nil {
			continue
		}
		f := e.Fill
		o, ok := orders[f.OrderID]
		if !ok || o.SleeveID != sleeve {
			continue
		}
		if e.ModelFill == nil {
			return "", "", fills, false
		}
		q, _ := bookRat(f.Quantity)
		price, _ := bookRat(f.Price)
		fee, _ := bookRat(f.Fee)
		cash := new(big.Rat).Mul(q, price)
		notional.Add(notional, cash)
		if f.Side == "buy" {
			cash.Neg(cash)
		}
		cash.Sub(cash, fee)
		modelPrice, pe := allocationAmount(e.ModelFill.ModelPrice)
		modelFee, fe := allocationAmount(e.ModelFill.ModelFee)
		penalty, ce := allocationAmount(e.ModelFill.CostPenalty)
		if pe != nil || fe != nil || ce != nil {
			return "", "", fills, false
		}
		modeled := new(big.Rat).Mul(q, modelPrice)
		if f.Side == "buy" {
			modeled.Neg(modeled)
		}
		modeled.Sub(modeled, modelFee)
		if cash.Cmp(modeled) < 0 {
			adverse.Add(adverse, new(big.Rat).Sub(modeled, cash))
		}
		pnl.Add(pnl, new(big.Rat).Sub(cash, penalty))
		if positions[f.Symbol] == nil {
			positions[f.Symbol] = new(big.Rat)
		}
		if f.Side == "sell" {
			q.Neg(q)
		}
		positions[f.Symbol].Add(positions[f.Symbol], q)
		fills++
	}
	for symbol, q := range positions {
		if q.Sign() == 0 {
			continue
		}
		p, err := bookRat(mark.Prices[symbol])
		if err != nil || p.Sign() <= 0 {
			return "", "", fills, false
		}
		pnl.Add(pnl, new(big.Rat).Mul(q, p))
	}
	if notional.Sign() > 0 {
		adverse.Quo(adverse, notional)
	}
	return pnl.FloatString(12), adverse.FloatString(12), fills, true
}

// BuildStrategyReview computes a deterministic event from a complete retained
// prefix. Replaying the event recomputes it, rejecting forged actions/evidence.
func BuildStrategyReview(s CappedState, in ReviewInput) (StrategyReview, error) {
	r, err := replay(s)
	if err != nil {
		return StrategyReview{}, err
	}
	return buildStrategyReview(s, in, r)
}

func buildStrategyReview(s CappedState, in ReviewInput, r *bookReplay) (StrategyReview, error) {
	out, err := evaluateStrategyReview(s, in, r)
	if err != nil || out.Action == "grow" {
		return out, err
	}
	// Missing or insufficient evidence cannot expand dollar risk through a later
	// deposit, rising equity or pool redistribution. Reductions also retain an
	// absolute ceiling until a complete review explicitly authorizes growth.
	budget, e := allocationAmount(out.Budget)
	if e != nil {
		out.BudgetCeiling = "0"
		return out, nil
	}
	applied := big.NewRat(1, 1)
	if value, ok := r.reviewScales[out.SleeveID]; ok {
		applied, _ = allocationAmount(value)
	}
	scale, _ := allocationAmount(out.Scale)
	if applied.Sign() > 0 && scale.Cmp(applied) < 0 {
		budget.Mul(budget, new(big.Rat).Quo(scale, applied))
	}
	// A plan decided before a review may size later with more capital. That
	// distribution cannot loosen an outstanding ceiling without a grow review.
	for i := len(s.Events) - 1; i >= 0; i-- {
		prior := s.Events[i].Review
		if prior == nil || prior.SleeveID != out.SleeveID {
			continue
		}
		if prior.BudgetCeiling != "" {
			ceiling, _ := allocationAmount(prior.BudgetCeiling)
			priorScale, _ := allocationAmount(prior.Scale)
			if priorScale.Sign() > 0 && scale.Cmp(priorScale) < 0 {
				ceiling.Mul(ceiling, new(big.Rat).Quo(scale, priorScale))
			}
			if ceiling.Cmp(budget) < 0 {
				budget = ceiling
			}
		}
		break
	}
	out.BudgetCeiling = budget.FloatString(9)
	return out, nil
}

func evaluateStrategyReview(s CappedState, in ReviewInput, r *bookReplay) (StrategyReview, error) {
	out := StrategyReview{Input: in, Policy: ReviewPolicy, JournalVersion: s.Version, Reasons: []string{}, Action: "investigate", Scale: "1", ResearchRequested: true}
	if in.At.IsZero() || len(in.DeclarationID) > 64 {
		return out, errors.New("invalid review input")
	}
	d, declaredAt := reviewDeclaration(s, in.DeclarationID)
	if d == nil || !in.At.After(declaredAt) {
		return out, errors.New("review declaration unavailable")
	}
	var err error
	out.SleeveID, out.StrategyID = d.SleeveID, d.StrategyID
	out.DeclarationSHA256, err = strategygate.Hash(*d)
	if err != nil {
		return out, err
	}
	out.JournalSHA256, err = strategygate.Hash(s)
	if err != nil {
		return out, err
	}
	var previous *StrategyReview
	var history []StrategyReview
	for _, e := range s.Events {
		if e.At.After(in.At) {
			return out, errors.New("review precedes retained information")
		}
		if e.Review != nil && e.Review.SleeveID == d.SleeveID {
			if reviewDay(e.At) == reviewDay(in.At) {
				return out, ErrBookConflict
			}
			previous = e.Review
			history = append(history, *e.Review)
			out.Scale = e.Review.Scale
		}
	}
	if r.distribution != nil {
		for _, a := range r.distribution.Sleeves {
			if a.SleeveID == d.SleeveID {
				out.Budget = a.Budget
			}
		}
	}
	out.PnL, out.AdverseFillFraction, out.Fills, _ = reviewAttribution(s, d.SleeveID, in.Mark, in.At)
	if len(d.Backtest) >= 20 {
		mean := new(big.Rat)
		for _, v := range d.Backtest {
			n, _ := allocationAmount(v.Return)
			mean.Add(mean, n)
		}
		out.BacktestMean = mean.Quo(mean, big.NewRat(int64(len(d.Backtest)), 1)).FloatString(12)
	}
	unavailable := func(reason string) (StrategyReview, error) {
		out.Reasons = append(out.Reasons, reason)
		return out, nil
	}
	if previous == nil {
		return unavailable("initial_attribution_baseline")
	}
	if in.At.Sub(previous.Input.At) < 20*time.Hour || in.At.Sub(previous.Input.At) > 96*time.Hour {
		return unavailable("daily_interval_outside_20_to_96_hours")
	}
	if previous.PnL == "" || out.PnL == "" {
		return unavailable("owned_fills_or_fresh_marks_unavailable")
	}
	budget, be := allocationAmount(previous.Budget)
	if be != nil || budget.Sign() <= 0 {
		return unavailable("previous_sleeve_budget_unavailable")
	}
	pnl, _ := allocationAmount(out.PnL)
	prior, _ := allocationAmount(previous.PnL)
	out.ForwardReturn = new(big.Rat).Quo(new(big.Rat).Sub(pnl, prior), budget).FloatString(12)
	b := in.Benchmark
	if b != nil && b.StartAt.Equal(previous.Input.At) && b.EndAt.Equal(in.At) && !b.ReceivedAt.Before(b.EndAt) && !b.ReceivedAt.After(in.At) && hashText.MatchString(b.SourceSHA256) && b.EvidenceClass == "prospective_market_unqualified" {
		v, e := allocationAmount(b.Return)
		if e == nil && v.Cmp(big.NewRat(-1, 1)) > 0 && v.Cmp(big.NewRat(1, 1)) <= 0 {
			out.BenchmarkReturn = v.FloatString(12)
		}
	}
	// Drawdown is the running loss from peak of cumulative budget-normalized
	// increments. This is attributed return on prior budgets, explicitly not TWR.
	cumulative, high, dd := new(big.Rat), new(big.Rat), new(big.Rat)
	for _, v := range append(history, out) {
		n, e := allocationAmount(v.ForwardReturn)
		if e != nil {
			continue
		}
		cumulative.Add(cumulative, n)
		if cumulative.Cmp(high) > 0 {
			high.Set(cumulative)
		}
		drop := new(big.Rat).Sub(high, cumulative)
		if drop.Cmp(dd) > 0 {
			dd.Set(drop)
		}
	}
	out.MaximumDrawdown = dd.FloatString(12)
	out.Drawdown = new(big.Rat).Sub(high, cumulative).FloatString(12)
	dd = new(big.Rat).Sub(high, cumulative)
	lossBudget, _ := allocationAmount(d.DrawdownBudget)
	action := func(a, scale, reason string) (StrategyReview, error) {
		out.Action, out.Scale = a, scale
		out.ResearchRequested = a == "investigate"
		out.Reasons = append(out.Reasons, reason)
		return out, nil
	}
	// A retirement is terminal; later positive observations cannot resurrect it.
	if life := r.reviews[d.SleeveID]; life.Retired || r.risk.Retired[d.StrategyID] {
		return action("retire", "0", "retirement_is_terminal")
	}
	if dd.Cmp(new(big.Rat).Mul(lossBudget, big.NewRat(2, 1))) >= 0 {
		return action("retire", "0", "drawdown_twice_sleeve_budget")
	}
	if dd.Cmp(new(big.Rat).Mul(lossBudget, big.NewRat(3, 2))) >= 0 {
		return action("demote", minReviewScale(out.Scale, "0.25"), "drawdown_quarantine")
	}
	if dd.Cmp(lossBudget) >= 0 {
		return action("cut", minReviewScale(out.Scale, "0.5"), "drawdown_sleeve_budget")
	}
	if out.BenchmarkReturn == "" {
		return unavailable("interval_benchmark_unavailable")
	}
	// Only consecutive complete observed intervals qualify. Missing days, absent
	// imports, long gaps and empty execution evidence cannot become zero returns.
	window := []StrategyReview{}
	all := append(history, out)
	for i := len(all) - 1; i >= 0 && len(window) < 20; i-- {
		v := all[i]
		if v.ForwardReturn == "" || v.BenchmarkReturn == "" || v.PnL == "" {
			break
		}
		if i > 0 && v.Input.At.Sub(all[i-1].Input.At) > 96*time.Hour {
			break
		}
		window = append(window, v)
	}
	slices.Reverse(window)
	out.Samples = len(window)
	sum, excess, early, late := new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)
	for i, v := range window {
		n, _ := allocationAmount(v.ForwardReturn)
		bench, _ := allocationAmount(v.BenchmarkReturn)
		x := new(big.Rat).Sub(n, bench)
		sum.Add(sum, n)
		excess.Add(excess, x)
		if i < len(window)/2 {
			early.Add(early, x)
		} else {
			late.Add(late, x)
		}
	}
	if len(window) > 0 {
		out.MeanReturn = sum.Quo(sum, big.NewRat(int64(len(window)), 1)).FloatString(12)
		out.MeanExcess = excess.Quo(excess, big.NewRat(int64(len(window)), 1)).FloatString(12)
	}
	if len(window) < 20 || out.Fills < 5 {
		out.Action, out.ResearchRequested = "hold", false
		return unavailable("minimum_20_intervals_and_5_owned_fills")
	}
	out.EdgeDecay = new(big.Rat).Quo(new(big.Rat).Sub(late, early), big.NewRat(10, 1)).FloatString(12)
	if out.BacktestMean == "" {
		return unavailable("frozen_backtest_unavailable_or_short")
	}
	adverse, _ := allocationAmount(out.AdverseFillFraction)
	if adverse.Cmp(big.NewRat(1, 1000)) >= 0 {
		return unavailable("venue_fill_model_gap_at_least_10bps")
	}
	mean, _ := allocationAmount(out.MeanReturn)
	back, _ := allocationAmount(out.BacktestMean)
	edge, _ := allocationAmount(out.EdgeDecay)
	if excess.Sign() < 0 && mean.Cmp(new(big.Rat).Quo(back, big.NewRat(2, 1))) < 0 {
		return action("demote", minReviewScale(out.Scale, "0.25"), "negative_excess_and_backtest_decay")
	}
	if excess.Cmp(big.NewRat(-1, 1000)) <= 0 {
		return action("cut", minReviewScale(out.Scale, "0.5"), "sustained_benchmark_lag_at_least_10bps")
	}
	if edge.Cmp(big.NewRat(-1, 1000)) <= 0 {
		return unavailable("edge_decay_at_least_10bps")
	}
	if r.risk.Breakers[d.StrategyID] || r.risk.Kill || r.risk.Paused || r.risk.Halted || r.risk.ReduceOnly || r.halted {
		return action("hold", out.Scale, "owner_or_hard_risk_control")
	}
	if excess.Cmp(big.NewRat(5, 10000)) >= 0 && back.Sign() >= 0 && mean.Cmp(new(big.Rat).Quo(back, big.NewRat(2, 1))) >= 0 {
		scale, _ := allocationAmount(out.Scale)
		scale.Add(scale, big.NewRat(1, 4))
		if scale.Cmp(big.NewRat(1, 1)) > 0 {
			scale.SetInt64(1)
		}
		return action("grow", scale.FloatString(2), "complete_positive_excess_without_decay")
	}
	return action("hold", out.Scale, "no_policy_threshold_crossed")
}

func minReviewScale(a, b string) string {
	x, _ := allocationAmount(a)
	y, _ := allocationAmount(b)
	if x.Cmp(y) < 0 {
		return a
	}
	return b
}

func replayStrategyReview(s CappedState, c BookCommand, r *bookReplay) error {
	if c.Review == nil || !c.At.Equal(c.Review.Input.At) {
		return errors.New("invalid strategy review event")
	}
	prefix := reviewPrefix(s, c.ID)
	if c.Review.JournalVersion != prefix.Version {
		return ErrBookConflict
	}
	expected, e := buildStrategyReview(prefix, c.Review.Input, r)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(expected, *c.Review) {
		return errors.New("review action or evidence differs from immutable inputs")
	}
	if raw, e := json.Marshal(c.Review); e != nil || len(raw) > 128<<10 {
		return errors.New("review event too large")
	}
	if r.reviews == nil {
		r.reviews = map[string]reviewLifecycle{}
	}
	old := r.reviews[expected.SleeveID]
	r.reviews[expected.SleeveID] = reviewLifecycle{At: c.At, Scale: expected.Scale, Retired: old.Retired || expected.Action == "retire"}
	return nil
}

func reviewSizing(s CappedState, c BookCommand, r *bookReplay) (allocation.RebalanceRequest, allocation.SizingSnapshot) {
	req := *c.Rebalance
	req.Targets = slices.Clone(req.Targets)
	snap := sizingSnapshot(r, c.Marks)
	snap.BudgetScales = map[string]string{}
	snap.BudgetCeilings = map[string]string{}
	cutoff := c.At
	for _, e := range s.Events {
		if e.ID == c.ID {
			break
		}
		if e.Incubation != nil && e.Incubation.Rebalance.ID == c.ID {
			cutoff = e.Incubation.DecidedAt
			break
		}
	}
	// Only preceding reviews strictly before the decision clock apply. Never
	// mutate the retained request, roster, target history, plans or old orders.
	for _, e := range s.Events {
		if e.ID == c.ID {
			break
		}
		if e.Review != nil && e.At.Before(cutoff) {
			snap.BudgetScales[e.Review.SleeveID] = e.Review.Scale
			if e.Review.BudgetCeiling != "" {
				snap.BudgetCeilings[e.Review.SleeveID] = e.Review.BudgetCeiling
			} else {
				delete(snap.BudgetCeilings, e.Review.SleeveID)
			}
		}
	}
	// A retained review declaration enables this policy for its sleeve. Legacy
	// journals have no declaration and must keep their original pause sizing.
	managed := map[string]bool{}
	for _, e := range s.Events {
		if e.ID == c.ID {
			break
		}
		if e.Kind == "review_declare" && e.ReviewDeclaration != nil {
			managed[e.ReviewDeclaration.SleeveID] = true
		}
	}
	for i, t := range req.Targets {
		var sid string
		for _, sleeve := range req.Sleeves {
			if sleeve.ID == t.SleeveID {
				sid = sleeve.StrategyID
				break
			}
		}
		if r.risk.Retired[sid] {
			snap.BudgetScales[t.SleeveID] = "0"
		}
		if snap.BudgetScales[t.SleeveID] == "0" {
			req.Targets[i].Decision = allocation.TargetDecision{}
		} else if managed[t.SleeveID] && r.risk.Breakers[sid] && !allocation.FlatTarget(t.Decision) {
			req.Targets[i].Decision = allocation.TargetDecision{HoldExposure: true}
		}
	}
	return req, snap
}

// ReviewExitRequired lets the runner make an explicit same-owner flat decision
// before evaluating a retired strategy, preserving stale-gate exit admission.
func ReviewExitRequired(s CappedState, sleeve, sid string, decidedAt time.Time) bool {
	for _, e := range s.Events {
		if e.RiskControl != nil && e.RiskControl.Action == "retire_strategy" && e.RiskControl.StrategyID == sid {
			return true
		}
		if e.Review != nil && e.Review.SleeveID == sleeve && e.Review.StrategyID == sid && e.Review.Action == "retire" && e.At.Before(decidedAt) {
			return true
		}
	}
	return false
}
