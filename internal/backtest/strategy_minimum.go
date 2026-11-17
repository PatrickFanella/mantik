package backtest

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
)

// CapitalBoundary is a financial sizing diagnostic for one frozen decision.
// It grants no gate, lifecycle, health or broker authority. WholeBookEquity is
// distinct from the subject sleeve's allocation in Distribution.
type CapitalBoundary struct {
	Account                                   account.Type
	SleeveID, Status, WholeBookEquity, Reason string
	PreviousCent                              string
	PreviousReason                            string
	Distribution                              allocation.Distribution
	Reserved                                  account.CappedSnapshot
}

// StrategyMinimum finds the first cent that sizes EVERY nonzero target in a
// frozen incubation roster, then reserves ALL generated
// orders together through the actual account and hard-risk checks. This narrow
// domain has monotone sizing: each positive target is a fixed fraction of the
// fee/gap-adjusted equity, truncated toward zero at nine decimals. The $1 order
// floor therefore provides a lower bound independent of account acceptance.
// Short rosters use equal mark/limit scenarios: every passing complete basket
// needs margin equity >= $2,000, and its fee band is searched cent by cent.
// Qualified budgets, holdings, reviews and changing decisions require a
// different proof and are explicitly refused instead of assuming monotonicity.
func StrategyMinimum(ctx context.Context, kind account.Type, req allocation.RebalanceRequest, marks map[string]string, sleeveID string, maximumCents int64) (CapitalBoundary, error) {
	out := CapitalBoundary{SleeveID: sleeveID, Account: kind}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if maximumCents < 1 || maximumCents > 100000000 || len(req.Sleeves) == 0 || (kind != account.Cash && kind != account.Margin) {
		return out, errors.New("invalid capital search bound")
	}
	found := false
	for _, s := range req.Sleeves {
		if s.Stage != "incubation" || s.ModelDriven || s.Minimum != "0" {
			return out, errors.New("capital proof requires a fresh ordinary incubation roster with zero minimum declarations")
		}
		found = found || s.ID == sleeveID
	}
	if !found {
		return out, errors.New("subject sleeve missing")
	}
	expected, subjectTargets := 0, 0
	hasShort := false
	fees := new(big.Rat)
	for _, t := range req.Targets {
		if t.Decision.KeepPositions || t.Decision.HoldExposure {
			return out, errors.New("capital proof requires explicit frozen targets")
		}
		gross := new(big.Rat)
		for _, target := range t.Decision.Targets {
			w, ok := new(big.Rat).SetString(target.Weight)
			if !ok {
				return out, errors.New("invalid capital target")
			}
			hasShort = hasShort || w.Sign() < 0
			gross.Add(gross, new(big.Rat).Abs(w))
			fee, ok := new(big.Rat).SetString(req.Fees[target.Asset])
			if !ok || fee.Sign() < 0 {
				return out, errors.New("invalid capital fee bound")
			}
			fees.Add(fees, fee)
			if w.Sign() != 0 {
				expected++
				if t.SleeveID == sleeveID {
					subjectTargets++
				}
			}
		}
		if gross.Cmp(big.NewRat(7, 10)) > 0 {
			return out, errors.New("capital proof requires target gross at most 70 percent")
		}
	}
	// Validate all declarations even for an explicit no-trade decision.
	if _, err := allocation.Distribute(req, allocation.SizingSnapshot{Equity: capitalDollars(maximumCents), Marks: marks}); err != nil {
		return out, err
	}
	if subjectTargets == 0 {
		out.Status, out.Reason = "no_trade", "no_nonzero_targets"
		return out, nil
	}
	if hasShort && kind == account.Cash {
		out.Status, out.Reason = "infeasible_cash_roster", "cash_account_cannot_open_short"
		return out, nil
	}
	if hasShort {
		for _, t := range req.Targets {
			for _, v := range t.Decision.Targets {
				if req.Limits[v.Asset] != marks[v.Asset] {
					return out, errors.New("short capital proof requires equal mark and limit scenarios")
				}
			}
		}
	}
	probe := func(cents int64) (allocation.Distribution, bool, string, error) {
		if err := ctx.Err(); err != nil {
			return allocation.Distribution{}, false, "", err
		}
		d, err := allocation.Distribute(req, allocation.SizingSnapshot{Equity: capitalDollars(cents), Marks: marks})
		if err != nil {
			return d, false, "", err
		}
		n := len(d.Orders)
		ready := n == expected
		for _, s := range d.Sleeves {
			ready = ready && s.Status == "ready"
		}
		return d, ready, fmt.Sprintf("complete_basket=%t;orders=%d/%d", ready, n, expected), nil
	}
	_, ready, reason, err := probe(maximumCents)
	if err != nil {
		return out, err
	}
	if !ready {
		out.Status, out.Reason = "infeasible_within_bound", reason
		return out, nil
	}
	lo, hi := int64(1), maximumCents
	for lo < hi {
		mid := lo + (hi-lo)/2
		_, ok, _, err := probe(mid)
		if err != nil {
			return out, err
		}
		if ok {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if hasShort && lo < 200000 {
		lo = 200000
	}
	upper := lo
	if hasShort {
		feeCents := new(big.Rat).Mul(fees, big.NewRat(100, 1))
		whole, rem := new(big.Int), new(big.Int)
		whole.QuoRem(feeCents.Num(), feeCents.Denom(), rem)
		if rem.Sign() > 0 {
			whole.Add(whole, big.NewInt(1))
		}
		if !whole.IsInt64() || whole.Int64() > 10000 {
			return out, errors.New("capital fee band exceeds 100 dollars")
		}
		upper += whole.Int64()
	}
	previousReason := ""
	for cents := lo; cents <= upper && cents <= maximumCents; cents++ {
		d, ready, _, err := probe(cents)
		if err != nil {
			return out, err
		}
		if !ready {
			return out, errors.New("capital sizing lost monotonicity")
		}
		snap, err := reserveCapital(kind, req, marks, d, capitalDollars(cents))
		if err != nil {
			previousReason = err.Error()
			continue
		}
		out.Distribution, out.Reserved, out.WholeBookEquity = d, snap, capitalDollars(cents)
		if cents > 1 {
			out.PreviousCent = capitalDollars(cents - 1)
			if cents == lo {
				var why string
				_, ready, why, err = probe(cents - 1)
				if err != nil {
					return out, err
				}
				if ready {
					prev, _, _, err := probe(cents - 1)
					if err != nil {
						return out, err
					}
					_, err = reserveCapital(kind, req, marks, prev, out.PreviousCent)
					if err == nil {
						return out, errors.New("capital predecessor admitted")
					}
					why = err.Error()
				}
				out.PreviousReason = why
			} else {
				out.PreviousReason = previousReason
			}
		}
		out.Status = "minimum_at_frozen_snapshot"
		return out, nil
	}
	// A failed financial boundary is not a proved infeasible strategy.
	out.Status, out.Reason = "account_boundary_unavailable", previousReason
	if lo > maximumCents {
		out.Status, out.Reason = "infeasible_within_bound", "margin_equity_requires_at_least_2000"
	}
	return out, nil
}

func reserveCapital(kind account.Type, req allocation.RebalanceRequest, marks map[string]string, d allocation.Distribution, equity string) (account.CappedSnapshot, error) {
	state, err := account.NewCappedState(account.CappedConfig{ID: "capital-proof", BrokerAccountID: "offline-only", Type: kind, OpenedAt: req.At, OpeningCash: equity, DepositAmount: "500", EveryWeeks: 2})
	if err != nil {
		return account.CappedSnapshot{}, err
	}
	state, err = account.ApplyCappedCommand(state, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: marks})
	if err != nil {
		return account.CappedSnapshot{}, err
	}
	for _, sized := range d.Orders {
		o := account.BookOrder{ID: sized.ID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, At: sized.At}
		state, err = account.ApplyCappedCommand(state, account.BookCommand{ID: o.ID, Kind: "portfolio_reserve", At: req.At, Order: &o, Marks: marks})
		if err != nil {
			return account.CappedSnapshot{}, err
		}
	}
	snapshot, err := account.SnapshotCapped(state, marks, req.At)
	if err != nil {
		return snapshot, err
	}
	for _, o := range snapshot.Pending {
		if err := account.CheckPortfolioSubmission(state, o, marks); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func capitalDollars(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
