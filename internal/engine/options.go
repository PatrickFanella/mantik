package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

// OptionSource supplies point-in-time chains and retained daily OHLC. It has no
// order authority. OptionBars can delegate to options.ReadBars on the same store
// used by OptionChain. No provider, broker or database is opened by this engine.
type OptionSource interface {
	OptionChain(context.Context, string, time.Time) (options.Chain, error)
	OptionBars(context.Context, string, time.Time) (map[string]options.IndicativeBar, error)
}

// OptionConfig opens a standalone, unqualified simulation sleeve. Stocks are
// imported fully paid shares, separately from SettledCash; they cannot be sold
// or pledged twice. Prices/Greeks always come from Source. Fees are dollars per
// contract per side and slippage is basis points, both strictly positive.
// This path grants no qualification, capital allocation or broker authority.
type OptionConfig struct {
	Source                      OptionSource
	Account                     account.Type
	StrategyID, SettledCash     string
	Stocks                      map[string]int
	FeePerContract, SlippageBps float64
}

// OptionFill records actual simulated cash movement in dollars. Contracts are
// signed, and the full OHLC observation and clocks remain inspectable.
type OptionFill struct {
	OrderID, AssetID string
	At               time.Time
	Leg              int
	Contracts        int
	Price, Fee       string
	Bar              options.IndicativeBar
	Closing          bool
}

// OptionResult is an admission/execution result. Refusals consume no cash or
// contracts. A failed execution retains the outstanding order and reservation.
type OptionResult struct {
	Reasons []string
	Risk    options.StructureRisk
	Fills   []OptionFill
}

// OptionSnapshot is a detached readback; dollar strings use up to Scale places.
// Reserved includes pending AND filled lots. Cash collateral is kept in addition
// to already-paid premiums; sale proceeds are unavailable until T+1 settlement.
type OptionSnapshot struct {
	Version, EvidenceStatus             string
	At                                  time.Time
	Cash, SettledCash, Equity, Reserved string
	Risk                                risk.State
	Fills                               []OptionFill
	Positions                           []OptionHolding
	Pending                             []string
}

// OptionHolding identifies a filled contract lot, including partially filled orders.
type OptionHolding struct {
	OrderID, AssetID, Symbol string
	Contracts                int
}

type optionLot struct {
	id, asset         string
	decided           time.Time
	structure         options.Structure
	filled            []bool
	cancelled, closed bool
	reserve           *big.Rat
	coveredShares     int
	reserveSpot       float64
}
type optionCredit struct {
	date   time.Time
	amount *big.Rat
}

// OptionBook is a single-owner indicative simulator. Submit reserves an opening
// structure; Step fills at most one whole leg on a later session, in protected
// order. Close atomically unwinds filled legs, shorts first, on later bars.
// State is in memory; issue #85 owns the durable paper-broker path.
type OptionBook struct {
	cfg     OptionConfig
	cash    *big.Rat
	credits []optionCredit
	lots    []*optionLot
	at      time.Time
	brake   risk.State
	fills   []OptionFill
}

// NewOptionBook validates its local funding and marks imported paid stock. It
// observes the initial hard-risk baseline from this book, never a broker balance.
func NewOptionBook(ctx context.Context, cfg OptionConfig, at time.Time) (*OptionBook, error) {
	cash, ok := decimal(cfg.SettledCash)
	if !ok || cash.Sign() < 0 || cfg.Source == nil || !cfg.Account.Valid() || cfg.StrategyID == "" || at.IsZero() || !optionFinite(cfg.FeePerContract) || cfg.FeePerContract <= 0 || !optionFinite(cfg.SlippageBps) || cfg.SlippageBps <= 0 || cfg.SlippageBps >= 10000 {
		return nil, ErrInvalidConfig
	}
	stocks := map[string]int{}
	for a, q := range cfg.Stocks {
		if a == "" || q <= 0 || q > 100000000 {
			return nil, ErrInvalidConfig
		}
		stocks[a] = q
	}
	cfg.Stocks = stocks
	b := &OptionBook{cfg: cfg, cash: cash}
	if _, err := b.observe(ctx, at, ""); err != nil {
		return nil, err
	}
	return b, nil
}

// Submit admits an independently collateralized opening order. IDs cannot be
// reused, including after cancellation. Shape, cash-account policy, collateral,
// Greek caps and Wave9 hard-risk checks all run before it becomes pending.
func (b *OptionBook) Submit(ctx context.Context, at time.Time, id, asset string, s options.Structure) (OptionResult, error) {
	if id == "" || len(id) > 128 || asset == "" {
		return refused("option_invalid_order"), nil
	}
	for _, l := range b.lots {
		if l.id == id {
			return refused("option_duplicate_order"), nil
		}
	}
	chains, err := b.observe(ctx, at, asset)
	if err != nil {
		return OptionResult{}, err
	}
	c := chains[asset]
	r, err := options.Analyze(c, s, b.cfg.FeePerContract)
	if err != nil {
		return optionRefusal(err), nil
	}
	// A margin book below its own $2,000 equity uses cash-account permissions.
	eq := b.equity(chains)
	cashMode := b.cfg.Account != account.Margin || eq.Cmp(big.NewRat(2000, 1)) < 0
	if cashMode && s.Kind != "long_options" && s.Kind != "covered_call" && s.Kind != "cash_secured_put" {
		return refused("option_cash_structure_not_allowed"), nil
	}
	used := 0
	for _, l := range b.lots {
		if l.asset == asset && !l.closed && (!l.cancelled || hasOptionFill(l)) {
			used += l.coveredShares
		}
	}
	if r.CoveredShares > b.cfg.Stocks[asset]-used {
		return refused("option_uncovered_call"), nil
	}
	l := &optionLot{id: id, asset: asset, decided: at, structure: s, filled: make([]bool, len(s.Legs))}
	l.structure.Legs = slices.Clone(s.Legs)
	l.reserve, l.coveredShares = b.hold(l, r, c), r.CoveredShares
	l.reserveSpot = c.Spot.Price
	b.lots = append(b.lots, l)
	reasons, err := b.check(chains)
	if err != nil || len(reasons) > 0 {
		b.lots = b.lots[:len(b.lots)-1]
		return OptionResult{Reasons: reasons, Risk: r}, err
	}
	return OptionResult{Risk: r}, nil
}

// Step executes the next protected leg at high+slippage for buys or
// low-slippage for sells, never at the decision close or midpoint. Limits bound
// entry losses; a gap beyond a limit leaves the order pending with a reason.
// Marks, all independent reservations, equity and brakes are rechecked first.
func (b *OptionBook) Step(ctx context.Context, at time.Time, id string) (OptionResult, error) {
	l := b.find(id)
	if l == nil || l.closed || l.cancelled {
		return refused("option_order_not_pending"), nil
	}
	chains, err := b.observe(ctx, at, l.asset)
	if err != nil {
		return OptionResult{}, err
	}
	r, err := options.Analyze(chains[l.asset], l.structure, b.cfg.FeePerContract)
	if err != nil {
		return optionRefusal(err), nil
	}
	index := -1
	for _, i := range r.FillOrder {
		if !l.filled[i] {
			index = i
			break
		}
	}
	if index < 0 {
		return refused("option_order_not_pending"), nil
	}
	reasons, err := b.check(chains)
	if err != nil || len(reasons) > 0 {
		return OptionResult{Reasons: reasons, Risk: r}, err
	}
	fills, err := b.price(ctx, at, l, []int{index}, false, chains[l.asset])
	if err != nil {
		return optionRefusal(err), nil
	}
	f := fills[0]
	b.apply(f)
	l.filled[index] = true
	b.fills = append(b.fills, f)
	return OptionResult{Risk: r, Fills: fills}, nil
}

// Cancel releases an untouched order. A partial fill keeps its full reservation
// and dedicated stock collateral until Close; cancellation cannot unprotect it.
func (b *OptionBook) Cancel(id string) error {
	l := b.find(id)
	if l == nil || l.closed || l.cancelled {
		return errors.New("option_order_not_pending")
	}
	l.cancelled = true
	return nil
}

// Close unwinds only this lot's filled quantities. It needs no opening-risk
// permission, so a kill/halt cannot prevent an exit. All prices are validated
// before any mutation; no cross-lot netting or collateral reuse is possible.
func (b *OptionBook) Close(ctx context.Context, at time.Time, id string) (OptionResult, error) {
	l := b.find(id)
	if l == nil || l.closed || !hasOptionFill(l) {
		return refused("option_position_unavailable"), nil
	}
	chains, err := b.observe(ctx, at, l.asset)
	if err != nil {
		return OptionResult{}, err
	}
	indexes := []int{}
	for i, leg := range l.structure.Legs {
		if l.filled[i] && leg.Contracts < 0 {
			indexes = append(indexes, i)
		}
	}
	for i, leg := range l.structure.Legs {
		if l.filled[i] && leg.Contracts > 0 {
			indexes = append(indexes, i)
		}
	}
	fills, err := b.price(ctx, at, l, indexes, true, chains[l.asset])
	if err != nil {
		return optionRefusal(err), nil
	}
	debit := new(big.Rat)
	for _, f := range fills {
		debit.Add(debit, optionDebit(f))
	}
	// No spending unsettled sale proceeds, including proceeds within the close.
	buys := new(big.Rat)
	for _, f := range fills {
		if d := optionDebit(f); d.Sign() > 0 {
			buys.Add(buys, d)
		}
	}
	available := b.settled()
	for _, other := range b.lots {
		if other == l || other.closed || other.cancelled && !hasOptionFill(other) {
			continue
		}
		available.Sub(available, optionLotHold(other, chains[other.asset]))
	}
	if buys.Cmp(available) > 0 || debit.Cmp(b.cash) > 0 {
		return refused("option_close_unfunded"), nil
	}
	for _, f := range fills {
		b.apply(f)
	}
	b.fills = append(b.fills, fills...)
	l.closed = true
	return OptionResult{Fills: fills}, nil
}

// Control applies an explicit operator brake control. Strategies cannot call
// through their read-only view; callers authenticate operator access.
func (b *OptionBook) Control(c risk.Control) error {
	next, err := risk.ApplyControl(b.brake, c)
	if err == nil {
		b.brake = next
	}
	return err
}

// Snapshot marks all holdings with known chains and returns detached state.
func (b *OptionBook) Snapshot(ctx context.Context, at time.Time) (OptionSnapshot, error) {
	chains, err := b.observe(ctx, at, "")
	if err != nil {
		return OptionSnapshot{}, err
	}
	out := OptionSnapshot{Version: options.StructureVersion, EvidenceStatus: "indicative_unqualified", At: b.at, Cash: render(b.cash), SettledCash: render(b.settled()), Equity: render(b.equity(chains)), Risk: b.brake, Fills: slices.Clone(b.fills)}
	out.Risk.Breakers = map[string]bool{}
	for k, v := range b.brake.Breakers {
		out.Risk.Breakers[k] = v
	}
	reserved := new(big.Rat)
	for _, l := range b.lots {
		if l.closed || l.cancelled && !hasOptionFill(l) {
			continue
		}
		reserved.Add(reserved, optionLotHold(l, chains[l.asset]))
		pending := false
		for i, v := range l.filled {
			if v {
				out.Positions = append(out.Positions, OptionHolding{l.id, l.asset, l.structure.Legs[i].Symbol, l.structure.Legs[i].Contracts})
			} else if !l.cancelled {
				pending = true
			}
		}
		if pending {
			out.Pending = append(out.Pending, l.id)
		}
	}
	out.Reserved = render(reserved)
	return out, nil
}

func (b *OptionBook) find(id string) *optionLot {
	for _, l := range b.lots {
		if l.id == id {
			return l
		}
	}
	return nil
}
func hasOptionFill(l *optionLot) bool    { return slices.Contains(l.filled, true) }
func refused(reason string) OptionResult { return OptionResult{Reasons: []string{reason}} }
func optionRefusal(err error) OptionResult {
	var e options.StructureError
	if errors.As(err, &e) {
		return refused(string(e))
	}
	return refused("option_evidence_unavailable: " + err.Error())
}
func optionFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func optionRat(v float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	return r
}

// optionRounded rounds outward at the report quantum, so serialization never
// improves a price or erases a positive fee. Inputs have already been validated.
func optionRounded(v float64, up bool) *big.Rat {
	r := optionRat(v)
	scale := big.NewInt(1000000000000)
	numerator := new(big.Int).Mul(r.Num(), scale)
	units, remainder := new(big.Int), new(big.Int)
	units.QuoRem(numerator, r.Denom(), remainder)
	if up && remainder.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return new(big.Rat).SetFrac(units, scale)
}
func optionDollars(v float64) string { return strconv.FormatFloat(math.Ceil(v*100)/100, 'f', 2, 64) }
func optionEntry(c options.Chain, symbol string) options.Entry {
	for _, e := range c.Entries {
		if e.Symbol == symbol {
			return e
		}
	}
	return options.Entry{}
}
func (b *OptionBook) settled() *big.Rat {
	v := new(big.Rat).Set(b.cash)
	for _, c := range b.credits {
		v.Sub(v, c.amount)
	}
	return v
}
func (b *OptionBook) equity(chains map[string]options.Chain) *big.Rat {
	e := new(big.Rat).Set(b.cash)
	for a, q := range b.cfg.Stocks {
		e.Add(e, new(big.Rat).Mul(big.NewRat(int64(q), 1), optionRat(chains[a].Spot.Price)))
	}
	for _, l := range b.lots {
		if l.closed {
			continue
		}
		for i, filled := range l.filled {
			if filled {
				e.Add(e, new(big.Rat).Mul(big.NewRat(int64(l.structure.Legs[i].Contracts*100), 1), optionRat(optionEntry(chains[l.asset], l.structure.Legs[i].Symbol).Close)))
			}
		}
	}
	return e
}
func (b *OptionBook) observe(ctx context.Context, at time.Time, extra string) (map[string]options.Chain, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at.IsZero() || at.Before(b.at) {
		return nil, errors.New("option_clock_reversed")
	}
	assets := map[string]bool{}
	for a := range b.cfg.Stocks {
		assets[a] = true
	}
	for _, l := range b.lots {
		if !l.closed && (!l.cancelled || hasOptionFill(l)) {
			assets[l.asset] = true
		}
	}
	if extra != "" {
		assets[extra] = true
	}
	ids := []string{}
	for a := range assets {
		ids = append(ids, a)
	}
	slices.Sort(ids)
	chains := map[string]options.Chain{}
	underlyings := map[string]string{}
	for _, a := range ids {
		c, err := b.cfg.Source.OptionChain(ctx, a, at)
		if err != nil {
			return nil, fmt.Errorf("option chain %s: %w", a, err)
		}
		if err := c.Validate(at, a); err != nil {
			return nil, err
		}
		if prior, ok := underlyings[c.Underlying]; ok && prior != a {
			return nil, errors.New("option_underlying_identity_conflict")
		}
		underlyings[c.Underlying] = a
		chains[a] = c
	}
	for _, l := range b.lots {
		if l.closed || l.cancelled && !hasOptionFill(l) {
			continue
		}
		for i, filled := range l.filled {
			if !filled {
				continue
			}
			c := chains[l.asset]
			e := optionEntry(c, l.structure.Legs[i].Symbol)
			if e.Symbol == "" || !optionFinite(e.Close) || e.Close <= 0 || e.PriceOccurredAt.IsZero() || e.PriceObservedAt.IsZero() || e.PriceOccurredAt.After(c.Spot.At) || e.PriceOccurredAt.In(newYork).Format(time.DateOnly) != c.Spot.Date || e.FeedSelection != "provider_default_unverified" {
				return nil, errors.New("option_held_price_unavailable")
			}
		}
	}
	eq := b.equity(chains)
	next, err := risk.Observe(b.brake, at, render(eq), "0")
	if err != nil {
		return nil, err
	}
	b.brake = next
	b.at = at
	date := optionCivil(at)
	kept := b.credits[:0]
	for _, c := range b.credits {
		if c.date.After(date) {
			kept = append(kept, c)
		}
	}
	b.credits = kept
	return chains, nil
}
func optionCivil(at time.Time) time.Time {
	z := newYork
	v := at.In(z)
	return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)
}
func (b *OptionBook) hold(l *optionLot, r options.StructureRisk, c options.Chain) *big.Rat {
	hold := r.Reserve
	// Put collateral never assumes that an unfilled premium will arrive.
	if l.structure.Kind == "cash_secured_put" {
		leg := l.structure.Legs[0]
		e := optionEntry(c, leg.Symbol)
		hold = math.Max(hold, float64(-leg.Contracts)*(100*e.Strike+2*b.cfg.FeePerContract))
	}
	return optionRat(hold)
}

// Keep the approved hold even when a cancelled, unused contract has no estimate.
// Covered stock can appreciate; both complete and partial loss can grow by at
// most shares times that change. Never release collateral merely on a mark.
func optionLotHold(l *optionLot, c options.Chain) *big.Rat {
	hold := new(big.Rat).Set(l.reserve)
	if l.coveredShares > 0 && c.Spot.Price > l.reserveSpot {
		change := new(big.Rat).Sub(optionRat(c.Spot.Price), optionRat(l.reserveSpot))
		hold.Add(hold, change.Mul(change, big.NewRat(int64(2*l.coveredShares), 1)))
	}
	return hold
}

func (b *OptionBook) check(chains map[string]options.Chain) ([]string, error) {
	markedEquity := b.equity(chains)
	eq := new(big.Rat).Set(markedEquity)
	cash := b.settled()
	gross := new(big.Rat)
	perAsset := map[string]*big.Rat{}
	exposures := []risk.OptionExposure{}
	for a, q := range b.cfg.Stocks {
		v := new(big.Rat).Mul(big.NewRat(int64(q), 1), optionRat(chains[a].Spot.Price))
		gross.Add(gross, v)
		perAsset[a] = new(big.Rat).Set(v)
		exposures = append(exposures, risk.OptionExposure{Underlying: a, Delta: render(v), Gamma: "0", Vega: "0", Theta: "0", Reserve: "0"})
	}
	for _, l := range b.lots {
		if l.closed || l.cancelled && !hasOptionFill(l) {
			continue
		}
		c := chains[l.asset]
		hold := optionLotHold(l, c)
		cash.Sub(cash, hold)
		gross.Add(gross, hold)
		if perAsset[l.asset] == nil {
			perAsset[l.asset] = new(big.Rat)
		}
		perAsset[l.asset].Add(perAsset[l.asset], hold)
		d, g, v, t := 0.0, 0.0, 0.0, 0.0
		for i, leg := range l.structure.Legs {
			if l.cancelled && !l.filled[i] {
				continue
			}
			e := optionEntry(c, leg.Symbol)
			if err := options.UsableGreeks(e); err != nil {
				return nil, err
			}
			n := math.Abs(float64(leg.Contracts * 100))
			d += n * math.Abs(e.Greeks.Delta) * c.Spot.Price
			g += n * e.Greeks.Gamma * .5 * c.Spot.Price * c.Spot.Price
			v += n * e.Greeks.Vega * .01
			t += n * math.Abs(e.Greeks.Theta)
			if !l.filled[i] {
				adverse := math.Max(0, float64(leg.Contracts*100)*(leg.Limit-e.Close)) + float64(absOptionContracts(leg.Contracts))*b.cfg.FeePerContract
				if !optionFinite(adverse) {
					return nil, errors.New("option_nonfinite_projection")
				}
				eq.Sub(eq, optionRat(adverse))
				// Filled lots keep their reserve. Pending premiums and fees need
				// separate cash, and unreceived sale proceeds cannot fund them.
				fee := float64(absOptionContracts(leg.Contracts)) * b.cfg.FeePerContract
				cash.Sub(cash, optionRounded(fee, true))
				if leg.Contracts > 0 {
					cash.Sub(cash, new(big.Rat).Mul(big.NewRat(int64(leg.Contracts*100), 1), optionRat(leg.Limit)))
				}
			}
		}
		exposures = append(exposures, risk.OptionExposure{Underlying: l.asset, Delta: optionDollars(d), Gamma: optionDollars(g), Vega: optionDollars(v), Theta: optionDollars(t), Reserve: render(hold)})
	}
	position := new(big.Rat)
	for _, v := range perAsset {
		if v.Cmp(position) > 0 {
			position.Set(v)
		}
	}
	reasons, err := risk.Check(b.brake, risk.OrderView{StrategyID: b.cfg.StrategyID, Equity: render(eq), CashAfter: render(cash), Gross: render(gross), Position: render(position), Sleeve: render(gross)})
	if err != nil {
		return nil, err
	}
	// A possible execution loss cannot lower the cash floor. Conservative bar
	// fills cannot improve current marked equity, so cover its full 20% first.
	if new(big.Rat).Mul(cash, big.NewRat(5, 1)).Cmp(markedEquity) < 0 && !slices.Contains(reasons, "risk_cash_reserve") {
		reasons = append(reasons, "risk_cash_reserve")
	}
	if cash.Sign() < 0 {
		reasons = append(reasons, "option_insufficient_settled_reserve")
	}
	if eq.Sign() > 0 {
		greek, err := risk.CheckOptions(render(eq), exposures)
		if err != nil {
			return nil, err
		}
		reasons = append(reasons, greek...)
	}
	// Re-evaluate cash fallback immediately before EACH fill, not just admission.
	if b.cfg.Account != account.Margin || eq.Cmp(big.NewRat(2000, 1)) < 0 {
		for _, l := range b.lots {
			if l.closed || l.cancelled {
				continue
			}
			if l.structure.Kind != "long_options" && l.structure.Kind != "covered_call" && l.structure.Kind != "cash_secured_put" {
				reasons = append(reasons, "option_cash_structure_not_allowed")
				break
			}
		}
	}
	return reasons, nil
}
func absOptionContracts(q int) int {
	if q < 0 {
		return -q
	}
	return q
}
func (b *OptionBook) price(ctx context.Context, at time.Time, l *optionLot, indexes []int, closing bool, c options.Chain) ([]OptionFill, error) {
	bars, err := b.cfg.Source.OptionBars(ctx, l.asset, at)
	if err != nil {
		return nil, err
	}
	out := []OptionFill{}
	for _, i := range indexes {
		leg := l.structure.Legs[i]
		q := leg.Contracts
		if closing {
			q = -q
		}
		bar, ok := bars[leg.Symbol]
		if !ok {
			return nil, errors.New("option_fill_bar_unavailable")
		}
		if err := bar.Validate(at); err != nil {
			return nil, err
		}
		entry := optionEntry(c, leg.Symbol)
		if bar.Symbol != leg.Symbol || bar.Date != c.Spot.Date || bar.Close != entry.Close || !bar.OccurredAt.Equal(entry.PriceOccurredAt) || !bar.AvailableAt.Equal(entry.PriceAvailableAt) || !bar.ObservedAt.Equal(entry.PriceObservedAt) {
			return nil, errors.New("option_fill_chain_bar_mismatch")
		}
		// The entire bar (not merely its acquisition clock) must follow decision
		// and any earlier execution of this lot, to avoid within-bar lookahead.
		after := l.decided
		for _, f := range b.fills {
			if f.OrderID == l.id && f.At.After(after) {
				after = f.At
			}
		}
		if !bar.OccurredAt.After(after) {
			return nil, errors.New("option_fill_requires_later_bar")
		}
		price := bar.High * (1 + b.cfg.SlippageBps/10000)
		if q < 0 {
			price = bar.Low * (1 - b.cfg.SlippageBps/10000)
		}
		if !optionFinite(price) || price <= 0 {
			return nil, errors.New("option_invalid_fill")
		}
		rounded := optionRounded(price, q > 0)
		if rounded.Sign() <= 0 {
			return nil, errors.New("option_fill_below_precision")
		}
		if !closing && (q > 0 && rounded.Cmp(optionRat(leg.Limit)) > 0 || q < 0 && rounded.Cmp(optionRat(leg.Limit)) < 0) {
			return nil, options.StructureError("option_limit_not_reached")
		}
		out = append(out, OptionFill{OrderID: l.id, AssetID: l.asset, At: at, Leg: i, Contracts: q, Price: render(rounded), Fee: render(optionRounded(float64(absOptionContracts(q))*b.cfg.FeePerContract, true)), Bar: bar, Closing: closing})
	}
	return out, nil
}
func optionDebit(f OptionFill) *big.Rat {
	p, _ := new(big.Rat).SetString(f.Price)
	fee, _ := new(big.Rat).SetString(f.Fee)
	return new(big.Rat).Add(new(big.Rat).Mul(p, big.NewRat(int64(f.Contracts*100), 1)), fee)
}
func (b *OptionBook) apply(f OptionFill) {
	d := optionDebit(f)
	b.cash.Sub(b.cash, d)
	if d.Sign() < 0 {
		b.credits = append(b.credits, optionCredit{account.SettlementDate(optionCivil(f.At)), new(big.Rat).Neg(d)})
	}
}

// String renders refusal reasons for logs without including source records.
func (r OptionResult) String() string { return strings.Join(r.Reasons, ",") }
