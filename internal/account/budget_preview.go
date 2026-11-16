package account

import (
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
)

// CurrentPortfolioMarks reads valuation evidence from the trusted book journal,
// independently of any model's asset universe or frozen feature input. A missing
// or older-than-one-minute holding/pending mark makes the preview unavailable.
func CurrentPortfolioMarks(s CappedState, at time.Time) (map[string]string, error) {
	r, err := replay(s)
	if err != nil {
		return nil, err
	}
	prices, clocks := map[string]string{}, map[string]time.Time{}
	for _, event := range s.Events {
		if event.Kind == "risk_checkpoint" && event.RiskCheckpoint != nil {
			event = event.RiskCheckpoint.Last
		}
		if event.At.After(at) {
			return nil, errors.New("valuation precedes retained book")
		}
		for symbol, price := range event.Marks {
			p, e := bookRat(price)
			if e != nil || p.Sign() <= 0 {
				return nil, errors.New("invalid retained valuation mark")
			}
			prices[symbol], clocks[symbol] = price, event.At
		}
	}
	required := map[string]bool{}
	for symbol, q := range r.positions {
		if q.Sign() != 0 {
			required[symbol] = true
		}
	}
	for _, o := range r.orders {
		full, _ := bookRat(o.order.Quantity)
		if !o.released && o.filled.Cmp(full) < 0 {
			required[o.order.Symbol] = true
		}
	}
	for symbol := range required {
		if clocks[symbol].IsZero() || at.Sub(clocks[symbol]) > time.Minute {
			return nil, errors.New("current portfolio mark unavailable: " + symbol)
		}
	}
	return prices, nil
}

// PreviewPortfolioBudgets reports current capacity, never operational admission.
// It uses the normal review/distribution rules, then bounds each sleeve by the
// pool capacity remaining after every other owner's filled and pending opening
// commitment. Pending reductions never release capacity before a fill. This is
// separate from replay sizing so historical distributions and hashes stay fixed.
// A clipped nominal budget waits: the eventual fixed target must fit without
// assuming another owner will resize or release its commitments.
func PreviewPortfolioBudgets(s CappedState, req allocation.RebalanceRequest, prices map[string]string, at time.Time) ([]allocation.SleeveAllocation, error) {
	if !req.At.Equal(at) || at.IsZero() || len(s.Events) > 0 && s.Events[len(s.Events)-1].At.After(at) {
		return nil, errors.New("budget preview clock differs from current book")
	}
	r, err := replay(s)
	if err != nil {
		return nil, err
	}
	if err = observePortfolio(s.Config, r, prices, at); err != nil {
		return nil, err
	}
	if r.halted || r.risk.Halted || r.risk.Kill || r.risk.Paused || r.risk.ReduceOnly {
		return nil, errors.New("current portfolio risk blocks budget")
	}
	req.Targets = nil
	req.Limits, req.Fees = prices, map[string]string{}
	for symbol := range prices {
		req.Fees[symbol] = "0"
	}
	for _, sleeve := range req.Sleeves {
		req.Targets = append(req.Targets, allocation.SleeveTarget{SleeveID: sleeve.ID, Decision: allocation.TargetDecision{HoldExposure: true}})
	}
	// Include every review already known now, including equal-clock receipts.
	// Do not reuse a historical decision cutoff or a synthetic command ID that
	// could collide with an existing plan. Replay sizing remains unchanged.
	snap := sizingSnapshot(r, prices)
	snap.BudgetScales, snap.BudgetCeilings = map[string]string{}, map[string]string{}
	for _, event := range s.Events {
		if review := event.Review; review != nil {
			snap.BudgetScales[review.SleeveID] = review.Scale
			if review.BudgetCeiling != "" {
				snap.BudgetCeilings[review.SleeveID] = review.BudgetCeiling
			} else {
				delete(snap.BudgetCeilings, review.SleeveID)
			}
		}
	}
	for _, sleeve := range req.Sleeves {
		if r.risk.Retired[sleeve.StrategyID] {
			snap.BudgetScales[sleeve.ID] = "0"
		}
	}
	exposure, held := map[string]*big.Rat{}, map[string]*big.Rat{}
	add := func(key string, value *big.Rat) {
		if exposure[key] == nil {
			exposure[key] = new(big.Rat)
		}
		exposure[key].Add(exposure[key], value)
	}
	for _, p := range snap.Positions {
		q, quantityErr := allocationAmount(p.Quantity)
		if quantityErr != nil {
			return nil, errors.New("invalid position quantity")
		}
		price, e := bookRat(prices[p.Symbol])
		if e != nil || price.Sign() <= 0 {
			return nil, errors.New("position mark unavailable")
		}
		key := p.SleeveID + "/" + p.Symbol
		held[key] = new(big.Rat).Set(q)
		add(key, new(big.Rat).Mul(new(big.Rat).Abs(q), price))
	}
	equity, _ := bookRat(snap.Equity)
	ids := []string{}
	for id := range r.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		q.Sub(q, o.filled)
		if o.released || q.Sign() == 0 {
			continue
		}
		price, e := bookRat(prices[o.order.Symbol])
		if e != nil || price.Sign() <= 0 {
			return nil, errors.New("pending mark unavailable")
		}
		limit, _ := bookRat(o.order.Limit)
		fee, _ := bookRat(o.order.Fee)
		fee.Sub(fee, o.fee)
		equity.Sub(equity, fee)
		gap := new(big.Rat).Sub(limit, price)
		if o.order.Side == "sell" {
			gap.Neg(gap)
		}
		if gap.Sign() > 0 {
			equity.Sub(equity, new(big.Rat).Mul(q, gap))
		}
		key := o.order.SleeveID + "/" + o.order.Symbol
		h := held[key]
		if h == nil {
			h = new(big.Rat)
			held[key] = h
		}
		opening := new(big.Rat).Set(q)
		if h.Sign() > 0 && o.order.Side == "sell" || h.Sign() < 0 && o.order.Side == "buy" {
			reduction := new(big.Rat).Abs(h)
			if reduction.Cmp(q) > 0 {
				reduction.Set(q)
			}
			opening.Sub(opening, reduction)
			if h.Sign() > 0 {
				h.Sub(h, reduction)
			} else {
				h.Add(h, reduction)
			}
		}
		if limit.Cmp(price) > 0 {
			price.Set(limit)
		}
		add(key, new(big.Rat).Mul(opening, price))
	}
	// Pending fees and adverse execution gaps reduce every stage budget before
	// review scales/ceilings, matching the final guard's conservative equity.
	snap.Equity = equity.FloatString(9)
	dist, err := allocation.Distribute(req, snap)
	if err != nil {
		return nil, err
	}
	owners := map[string]allocation.Sleeve{}
	for id, owner := range r.sleeves {
		owners[id] = owner
	}
	for _, owner := range req.Sleeves {
		owners[owner.ID] = owner
	}
	pool, committed := new(big.Rat), map[string]*big.Rat{}
	for key, value := range exposure {
		id := strings.SplitN(key, "/", 2)[0]
		owner, ok := owners[id]
		if !ok || owner.Stage != "qualified" {
			pool.Add(pool, value)
		}
		if committed[id] == nil {
			committed[id] = new(big.Rat)
		}
		committed[id].Add(committed[id], value)
	}
	for i, a := range dist.Sleeves {
		owner := owners[a.SleeveID]
		budget, _ := bookRat(a.Budget)
		nominal := new(big.Rat).Set(budget)
		if owner.Stage != "qualified" {
			available := new(big.Rat).Sub(new(big.Rat).Mul(equity, big.NewRat(5, 100)), pool)
			if own := committed[a.SleeveID]; own != nil {
				available.Add(available, own)
			}
			if available.Sign() < 0 {
				available.SetInt64(0)
			}
			if budget.Cmp(available) > 0 {
				budget.Set(available)
			}
		}
		if owner.ModelDriven || owner.Stage == "forward_only" {
			cap := new(big.Rat).Mul(equity, big.NewRat(1, 100))
			if cap.Sign() < 0 {
				cap.SetInt64(0)
			}
			if budget.Cmp(cap) > 0 {
				budget.Set(cap)
			}
		}
		a.Budget = budget.FloatString(9)
		minimum, _ := bookRat(owner.Minimum)
		if budget.Sign() <= 0 || budget.Cmp(minimum) < 0 || budget.Cmp(nominal) < 0 {
			a.Status, a.Reason = "wait", "committed_capacity"
		}
		if r.risk.Retired[owner.StrategyID] || r.risk.Breakers[owner.StrategyID] {
			a.Status, a.Reason = "wait", "owner_control"
		}
		dist.Sleeves[i] = a
	}
	return dist.Sleeves, nil
}
