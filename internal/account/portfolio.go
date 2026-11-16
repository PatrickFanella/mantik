package account

import (
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func retainPortfolioRoster(r *bookReplay, req allocation.RebalanceRequest) error {
	if r.sleeves == nil {
		r.sleeves = map[string]allocation.Sleeve{}
	}
	requested := map[string]allocation.Sleeve{}
	for _, s := range req.Sleeves {
		requested[s.ID] = s
	}
	net := map[string]*big.Rat{}
	active := map[string]bool{}
	for _, o := range r.orders {
		key := o.order.SleeveID + "/" + o.order.Symbol
		if net[key] == nil {
			net[key] = new(big.Rat)
		}
		q := new(big.Rat).Set(o.filled)
		if o.order.Side == "sell" {
			q.Neg(q)
		}
		net[key].Add(net[key], q)
		full, _ := bookRat(o.order.Quantity)
		if !o.released && full.Cmp(o.filled) > 0 {
			active[o.order.SleeveID] = true
		}
	}
	for key, q := range net {
		if q.Sign() != 0 {
			active[strings.SplitN(key, "/", 2)[0]] = true
		}
	}
	for id, old := range r.sleeves {
		next, included := requested[id]
		if active[id] && !included {
			return errors.New("active_sleeve_omitted")
		}
		if included && (next.StrategyID != old.StrategyID || next.Stage != old.Stage || next.ModelDriven != old.ModelDriven) {
			return errors.New("retained_sleeve_identity_changed")
		}
		for nextID, v := range requested {
			if nextID != id && v.StrategyID == old.StrategyID {
				return errors.New("strategy_version_already_owns_sleeve")
			}
		}
	}
	for id, s := range requested {
		r.sleeves[id] = s
	}
	return nil
}

// PortfolioRoster returns immutable version ownership retained by the book.
func PortfolioRoster(s CappedState) (map[string]allocation.Sleeve, error) {
	r, e := replay(s)
	if e != nil {
		return nil, e
	}
	return r.sleeves, nil
}

func sizingSnapshot(r *bookReplay, prices map[string]string) allocation.SizingSnapshot {
	ps, _ := marks(r.positions, prices) // callers validate complete marks first
	out := allocation.SizingSnapshot{Equity: r.funds.Status(ps).Equity.FloatString(9), Marks: prices, Positions: []allocation.SizingPosition{}}
	out.PreviousTargets = r.targets
	positions := map[string]*big.Rat{}
	for _, o := range r.orders {
		key := o.order.SleeveID + "/" + o.order.Symbol
		if positions[key] == nil {
			positions[key] = new(big.Rat)
		}
		q := new(big.Rat).Set(o.filled)
		if o.order.Side == "sell" {
			q.Neg(q)
		}
		positions[key].Add(positions[key], q)
		full, _ := bookRat(o.order.Quantity)
		if !o.released && o.filled.Cmp(full) < 0 {
			out.PendingSleeves = append(out.PendingSleeves, o.order.SleeveID)
		}
	}
	keys := []string{}
	for k := range positions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if positions[key].Sign() != 0 {
			parts := strings.SplitN(key, "/", 2)
			out.Positions = append(out.Positions, allocation.SizingPosition{SleeveID: parts[0], Symbol: parts[1], Quantity: positions[key].FloatString(9)})
		}
	}
	slices.Sort(out.PendingSleeves)
	out.PendingSleeves = slices.Compact(out.PendingSleeves)
	return out
}

func observePortfolio(cfg CappedConfig, r *bookReplay, prices map[string]string, at time.Time) error {
	r.funds.Advance(civil(at))
	ps, e := marks(r.positions, prices)
	if e != nil {
		return e
	}
	flow, _ := bookRat(cfg.DepositAmount)
	flow.Mul(flow, big.NewRat(int64(r.deposits), 1))
	state, e := risk.Observe(r.risk, at, r.funds.Status(ps).Equity.FloatString(9), flow.FloatString(9))
	if e == nil {
		r.risk = state
	}
	return e
}

func checkSizedOrder(r *bookReplay, o BookOrder) error {
	if r.distribution == nil || o.AllocationID != r.distribution.ID {
		return errors.New("distributor_order_required")
	}
	for _, s := range r.distribution.Orders {
		if s.ID != o.ID {
			continue
		}
		if s.ReduceOnly == o.ReduceOnly && s.SleeveID == o.SleeveID && s.StrategyID == o.StrategyID && s.Symbol == o.Symbol && s.Quantity == o.Quantity && s.Limit == o.Limit && s.Fee == o.Fee && s.Side == o.Side && s.At.Equal(o.At) {
			return nil
		}
		return errors.New("distributor_order_changed")
	}
	return errors.New("distributor_order_required")
}

// CheckPortfolioSubmission rechecks the current brake immediately before POST,
// including retries of already-reserved orders. Clearing a kill switch cannot
// erase a persistent hard halt. The reservation remains after a refusal.
func CheckPortfolioSubmission(s CappedState, o BookOrder, prices map[string]string) error {
	r, e := replay(s)
	if e != nil {
		return e
	}
	if e = checkSizedOrder(r, o); e != nil {
		return e
	}
	at := o.At
	if len(s.Events) > 0 && s.Events[len(s.Events)-1].At.After(at) {
		at = s.Events[len(s.Events)-1].At
	}
	if e = observePortfolio(s.Config, r, prices, at); e != nil {
		return e
	}
	return checkPortfolioRisk(r, o, prices)
}

func checkPortfolioRisk(r *bookReplay, order BookOrder, prices map[string]string) error {
	ps, e := marks(r.positions, prices)
	if e != nil {
		return e
	}
	equity := r.funds.Status(ps).Equity
	cash := r.funds.Settled()
	positions := map[string]*big.Rat{}
	available := map[string]*big.Rat{}
	for _, p := range sizingSnapshot(r, prices).Positions {
		q, _ := new(big.Rat).SetString(p.Quantity)
		available[p.SleeveID+"/"+p.Symbol] = q
		mark, _ := bookRat(prices[p.Symbol])
		positions[p.SleeveID+"/"+p.Symbol] = new(big.Rat).Mul(new(big.Rat).Abs(q), mark)
	}
	ids := []string{}
	for id := range r.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	candidateReducing := false
	// Test the candidate against reductions reserved by ALL other orders,
	// irrespective of ID sort order. Pending opening orders cannot supply stock.
	held := new(big.Rat)
	if q := available[order.SleeveID+"/"+order.Symbol]; q != nil {
		held.Set(q)
	}
	want, _ := bookRat(order.Quantity)
	if old := r.orders[order.ID]; old != nil {
		want.Sub(want, old.filled)
	}
	for _, id := range ids {
		o := r.orders[id]
		if id == order.ID || o.released || o.order.SleeveID != order.SleeveID || o.order.Symbol != order.Symbol || o.order.Side != order.Side {
			continue
		}
		q, _ := bookRat(o.order.Quantity)
		q.Sub(q, o.filled)
		if held.Sign() > 0 && order.Side == "sell" {
			held.Sub(held, q)
			if held.Sign() < 0 {
				held.SetInt64(0)
			}
		} else if held.Sign() < 0 && order.Side == "buy" {
			held.Add(held, q)
			if held.Sign() > 0 {
				held.SetInt64(0)
			}
		}
	}
	if want.Sign() == 0 || order.Side == "sell" && held.Sign() > 0 && want.Cmp(held) <= 0 || order.Side == "buy" && held.Sign() < 0 && want.Cmp(new(big.Rat).Neg(held)) <= 0 {
		candidateReducing = true
	}
	if order.ReduceOnly && !candidateReducing {
		return errors.New("risk_reduce_only")
	}
	for _, id := range ids {
		o := r.orders[id]
		q, _ := bookRat(o.order.Quantity)
		q.Sub(q, o.filled)
		if o.released || q.Sign() == 0 {
			continue
		}
		price, pe := bookRat(prices[o.order.Symbol])
		if pe != nil || price.Sign() <= 0 {
			return errors.New("missing positive pending-order mark")
		}
		limit, _ := bookRat(o.order.Limit)
		fee, _ := bookRat(o.order.Fee)
		fee.Sub(fee, o.fee)
		equity.Sub(equity, fee)
		cash.Sub(cash, fee)
		if o.order.Side == "buy" {
			cash.Sub(cash, new(big.Rat).Mul(q, limit))
		}
		gap := new(big.Rat).Sub(limit, price)
		if o.order.Side == "sell" {
			gap.Neg(gap)
		}
		if gap.Sign() > 0 {
			equity.Sub(equity, new(big.Rat).Mul(q, gap))
		}
		key := o.order.SleeveID + "/" + o.order.Symbol
		h := available[key]
		if h == nil {
			h = new(big.Rat)
			available[key] = h
		}
		opening := new(big.Rat).Set(q)
		if h.Sign() > 0 && o.order.Side == "sell" {
			reduce := new(big.Rat).Set(q)
			if reduce.Cmp(h) > 0 {
				reduce.Set(h)
			}
			opening.Sub(opening, reduce)
			h.Sub(h, reduce)
		} else if h.Sign() < 0 && o.order.Side == "buy" {
			reduce := new(big.Rat).Neg(h)
			if reduce.Cmp(q) > 0 {
				reduce.Set(q)
			}
			opening.Sub(opening, reduce)
			h.Add(h, reduce)
		}
		if limit.Cmp(price) > 0 {
			price.Set(limit)
		}
		if positions[key] == nil {
			positions[key] = new(big.Rat)
		}
		positions[key].Add(positions[key], new(big.Rat).Mul(opening, price))
	}
	gross, pos, sleeve := new(big.Rat), new(big.Rat), new(big.Rat)
	incubation := new(big.Rat)
	models := map[string]*big.Rat{}
	assetExposure, sleeveExposure := map[string]*big.Rat{}, map[string]*big.Rat{}
	for key, v := range positions {
		parts := strings.SplitN(key, "/", 2)
		gross.Add(gross, v)
		owner, known := r.sleeves[parts[0]]
		if !known || owner.Stage == "incubation" || owner.Stage == "forward_only" {
			incubation.Add(incubation, v)
		}
		if owner.ModelDriven || owner.Stage == "forward_only" {
			if models[owner.StrategyID] == nil {
				models[owner.StrategyID] = new(big.Rat)
			}
			models[owner.StrategyID].Add(models[owner.StrategyID], v)
		}
		if assetExposure[parts[1]] == nil {
			assetExposure[parts[1]] = new(big.Rat)
		}
		assetExposure[parts[1]].Add(assetExposure[parts[1]], v)
		if sleeveExposure[parts[0]] == nil {
			sleeveExposure[parts[0]] = new(big.Rat)
		}
		sleeveExposure[parts[0]].Add(sleeveExposure[parts[0]], v)
	}
	for _, v := range assetExposure {
		if v.Cmp(pos) > 0 {
			pos.Set(v)
		}
	}
	for _, v := range sleeveExposure {
		if v.Cmp(sleeve) > 0 {
			sleeve.Set(v)
		}
	}
	modelExposure := new(big.Rat)
	for _, v := range models {
		if v.Cmp(modelExposure) > 0 {
			modelExposure.Set(v)
		}
	}
	reasons, e := risk.Check(r.risk, risk.OrderView{StrategyID: order.StrategyID, Equity: equity.FloatString(18), CashAfter: cash.FloatString(18), Gross: gross.FloatString(18), Position: pos.FloatString(18), Sleeve: sleeve.FloatString(18), Incubation: incubation.FloatString(18), ModelVersion: modelExposure.FloatString(18), Reducing: candidateReducing})
	if e != nil {
		return e
	}
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, ","))
	}
	return nil
}
