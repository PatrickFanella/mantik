// Package account enforces the capped book's equity-account rules without a
// broker or model. Callers supply marked positions and payment provenance.
package account

import (
	"math/big"
	"time"
)

// Type selects the account rules. The zero value means Cash.
type Type string

const (
	Cash   Type = "cash"
	Margin Type = "margin"
)

// Valid reports whether t names a supported account type.
func (t Type) Valid() bool { return t == "" || t == Cash || t == Margin }

// Period selects overnight Reg T or intraday maintenance-excess checks.
type Period string

const (
	Overnight Period = "overnight"
	Intraday  Period = "intraday"
)

// Code is a stable refusal reason. An empty code means accepted.
type Code string

const (
	InvalidTrade      Code = "invalid_trade"
	UnsettledFunds    Code = "unsettled_funds"
	FreeRiding        Code = "free_riding"
	GoodFaith         Code = "good_faith_violation"
	ShortNotAllowed   Code = "short_not_allowed"
	BelowMinimum      Code = "margin_equity_below_minimum"
	InitialMargin     Code = "initial_margin_exceeded"
	MaintenanceMargin Code = "maintenance_margin_shortfall"
	IntradayMargin    Code = "intraday_margin_deficit"
)

// Position is marked at the current price. Unpaid is the long quantity still
// awaiting payment, for cash-account imports/reconciliation. Normal cash buys
// cannot create unpaid holdings. Nil means zero. All numbers are exact.
type Position struct {
	Asset           string
	Quantity, Price *big.Rat
	Unpaid          *big.Rat
}

// Trade is a signed quantity at its execution price, including fees. Negative
// quantities sell. Positions passed to Execute must include its asset, even
// when its current quantity is zero.
type Trade struct {
	Asset                string
	Quantity, Price, Fee *big.Rat
}

// Status reports total capacity (not additional order capacity), gross
// exposure, maintenance requirement and excess. Market losses can make excess
// negative without creating an intraday transaction deficit.
type Status struct {
	EffectiveType                                                       Type
	Equity, Gross, OvernightBuyingPower, Maintenance, Excess, Shortfall *big.Rat
}

type credit struct {
	date   time.Time
	amount *big.Rat
}

// Book owns trade-date cash and its unsettled sale credits. It is single-owner
// state; callers retain positions and must update them after accepted trades.
type Book struct {
	kind    Type
	cash    *big.Rat
	credits []credit
}

// New opens an empty book with settled cash. Invalid input returns nil.
func New(kind Type, cash *big.Rat) *Book {
	if !kind.Valid() || cash == nil || cash.Sign() < 0 {
		return nil
	}
	if kind == "" {
		kind = Cash
	}
	return &Book{kind: kind, cash: new(big.Rat).Set(cash)}
}

// Cash returns a detached trade-date cash balance, including unsettled sales.
func (b *Book) Cash() *big.Rat { return new(big.Rat).Set(b.cash) }

// Settled returns cash after excluding sale credits not yet settled. Margin
// debits can make this negative.
func (b *Book) Settled() *big.Rat {
	r := b.Cash()
	for _, c := range b.credits {
		r.Sub(r, c.amount)
	}
	return r
}

// Advance releases credits whose settlement date has arrived. Dates are UTC
// civil dates supplied by the caller, rather than a wall clock.
func (b *Book) Advance(date time.Time) {
	kept := b.credits[:0]
	for _, c := range b.credits {
		if c.date.After(date) {
			kept = append(kept, c)
		}
	}
	b.credits = kept
}

// AddCash applies settled deposits or dividend payments (negative for a short
// dividend obligation). It does not release unsettled sale credits.
func (b *Book) AddCash(amount *big.Rat) { b.cash.Add(b.cash, amount) }

// Status marks the supplied positions. Callers must supply distinct assets,
// positive prices and non-nil quantities; Execute validates these inputs.
func (b *Book) Status(positions []Position) Status {
	r := Status{EffectiveType: b.kind, Equity: b.Cash(), Gross: new(big.Rat), Maintenance: new(big.Rat)}
	for _, p := range positions {
		value := new(big.Rat).Mul(p.Quantity, p.Price)
		r.Equity.Add(r.Equity, value)
		r.Gross.Add(r.Gross, new(big.Rat).Abs(value))
		r.Maintenance.Add(r.Maintenance, maintenance(p))
	}
	if r.Equity.Cmp(big.NewRat(2000, 1)) < 0 {
		r.EffectiveType = Cash
	}
	r.OvernightBuyingPower = new(big.Rat).Set(r.Equity)
	if r.EffectiveType == Margin {
		r.OvernightBuyingPower.Mul(r.OvernightBuyingPower, big.NewRat(2, 1))
	}
	if r.OvernightBuyingPower.Sign() < 0 {
		r.OvernightBuyingPower.SetInt64(0)
	}
	r.Excess = new(big.Rat).Sub(r.Equity, r.Maintenance)
	r.Shortfall = new(big.Rat).Neg(r.Excess)
	if r.Shortfall.Sign() < 0 {
		r.Shortfall.SetInt64(0)
	}
	return r
}

// Execute refuses or records a trade atomically. Refusals never change cash.
// settlesOn must be after date. Intraday uses current maintenance excess and a
// 4x ceiling; Overnight also requires 50% initial margin. A reduction of an
// existing position remains possible during a shortfall or cash fallback.
func (b *Book) Execute(positions []Position, t Trade, period Period, date, settlesOn time.Time) Code {
	if t.Asset == "" || t.Quantity == nil || t.Quantity.Sign() == 0 || t.Price == nil || t.Price.Sign() <= 0 || t.Fee == nil || t.Fee.Sign() < 0 || date.IsZero() || !settlesOn.After(date) || (period != Overnight && period != Intraday) {
		return InvalidTrade
	}
	seen := map[string]bool{}
	index := -1
	for i, p := range positions {
		if p.Asset == "" || seen[p.Asset] || p.Quantity == nil || p.Price == nil || p.Price.Sign() <= 0 || p.Unpaid != nil && (p.Unpaid.Sign() < 0 || p.Unpaid.Cmp(p.Quantity) > 0) {
			return InvalidTrade
		}
		seen[p.Asset] = true
		if p.Asset == t.Asset {
			index = i
		}
	}
	if index < 0 {
		return InvalidTrade
	}
	p := positions[index]
	target := new(big.Rat).Add(p.Quantity, t.Quantity)
	reducing := p.Quantity.Sign()*t.Quantity.Sign() < 0 && target.Sign()*p.Quantity.Sign() >= 0 && new(big.Rat).Abs(target).Cmp(new(big.Rat).Abs(p.Quantity)) < 0
	before := b.Status(positions)
	notional := new(big.Rat).Mul(t.Quantity, t.Price)
	debit := new(big.Rat).Add(notional, t.Fee)
	post := &Book{kind: b.kind, cash: new(big.Rat).Sub(b.cash, debit)}
	marked := append([]Position(nil), positions...)
	marked[index].Quantity = target
	after := post.Status(marked)

	if b.kind == Cash || before.EffectiveType == Cash || after.EffectiveType == Cash {
		if target.Sign() < 0 && !reducing {
			if b.kind == Margin {
				return BelowMinimum
			}
			return ShortNotAllowed
		}
		if b.kind == Cash && t.Quantity.Sign() < 0 && p.Unpaid != nil && new(big.Rat).Neg(t.Quantity).Cmp(new(big.Rat).Sub(p.Quantity, p.Unpaid)) > 0 {
			return GoodFaith
		}
		// Existing margin positions may be managed down even with a debit.
		if !reducing || b.kind == Cash {
			available := b.Settled()
			// Short proceeds remain collateral, even after settlement. A cash
			// fallback cannot spend the money needed to cover existing shorts.
			unencumbered := b.Cash()
			for _, p := range positions {
				if p.Quantity.Sign() < 0 {
					unencumbered.Add(unencumbered, new(big.Rat).Mul(p.Quantity, p.Price))
				}
			}
			if unencumbered.Cmp(available) < 0 {
				available = unencumbered
			}
			if debit.Sign() > 0 && debit.Cmp(available) > 0 {
				if b.kind == Margin {
					return BelowMinimum
				}
				if debit.Cmp(b.cash) <= 0 {
					return UnsettledFunds
				}
				return FreeRiding
			}
		}
	} else if !reducing {
		if period == Overnight && after.Gross.Cmp(after.OvernightBuyingPower) > 0 {
			return InitialMargin
		}
		if period == Intraday && (after.Gross.Cmp(new(big.Rat).Mul(after.Equity, big.NewRat(4, 1))) > 0 || after.Excess.Sign() < 0) {
			return IntradayMargin
		}
		if after.Shortfall.Sign() > 0 {
			return MaintenanceMargin
		}
	}
	b.cash.Set(post.cash)
	if debit.Sign() < 0 {
		b.credits = append(b.credits, credit{settlesOn, new(big.Rat).Neg(debit)})
	}
	return ""
}

// Alpaca's ordinary-equity house maintenance schedule. Leveraged ETFs and
// non-marginable securities need separate instrument policy before use.
func maintenance(p Position) *big.Rat {
	q := new(big.Rat).Abs(p.Quantity)
	v := new(big.Rat).Mul(q, p.Price)
	if p.Quantity.Sign() < 0 {
		perShare, rate := big.NewRat(5, 1), big.NewRat(3, 10)
		if p.Price.Cmp(big.NewRat(5, 1)) < 0 {
			perShare, rate = big.NewRat(5, 2), big.NewRat(1, 1)
		}
		a, b := new(big.Rat).Mul(q, perShare), new(big.Rat).Mul(v, rate)
		if a.Cmp(b) > 0 {
			return a
		}
		return b
	}
	rate := big.NewRat(3, 10)
	if p.Price.Cmp(big.NewRat(5, 2)) < 0 {
		rate = big.NewRat(1, 1)
	} else if p.Price.Cmp(big.NewRat(6, 1)) <= 0 {
		rate = big.NewRat(1, 2)
	}
	return new(big.Rat).Mul(v, rate)
}
