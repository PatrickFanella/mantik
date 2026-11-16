package account

import (
	"errors"
	"math/big"
	"slices"
	"time"
)

// ForwardBindings returns the first retained declaration for each sleeve.
// A later request cannot silently detach a running sleeve from its clocks.
func ForwardBindings(s CappedState) map[string]string {
	out := map[string]string{}
	for _, e := range s.Events {
		if IsStrategyPlan(e) && e.Incubation != nil {
			for sleeve, id := range e.Incubation.ForwardDeclarations {
				if out[sleeve] == "" {
					out[sleeve] = id
				}
			}
		}
	}
	return out
}

// ForwardHoldingOpened derives the oldest continuously open exposure clock from
// owned fills. Adding/reducing a position cannot reset it; only fully flattening
// the sleeve resets it. This is deliberately conservative across assets.
func ForwardHoldingOpened(s CappedState, sleeve string, at time.Time) (time.Time, error) {
	orders := map[string]bool{}
	fills := []BookFill{}
	for _, e := range s.Events {
		if e.At.After(at) {
			return time.Time{}, errors.New("forward clock precedes retained book state")
		}
		if e.Kind == "portfolio_reserve" && e.Order != nil && e.Order.SleeveID == sleeve {
			orders[e.Order.ID] = true
		}
		if e.Kind != "fill" || e.Fill == nil || !orders[e.Fill.OrderID] {
			continue
		}
		f := e.Fill
		if f.At.After(at) {
			return time.Time{}, errors.New("forward fill is in the future")
		}
		fills = append(fills, *f)
	}
	// Sync receipts can discover an earlier execution after a later one. Keep
	// receipt checks above, but derive continuous exposure in economic order.
	// Equal execution clocks retain their original receipt order.
	slices.SortStableFunc(fills, func(a, b BookFill) int { return a.At.Compare(b.At) })
	positions := map[string]*big.Rat{}
	var opened time.Time
	for _, f := range fills {
		q, err := bookRat(f.Quantity)
		if err != nil {
			return time.Time{}, err
		}
		if f.Side == "sell" {
			q.Neg(q)
		}
		if positions[f.Symbol] == nil {
			positions[f.Symbol] = new(big.Rat)
		}
		positions[f.Symbol].Add(positions[f.Symbol], q)
		flat := true
		for _, v := range positions {
			if v.Sign() != 0 {
				flat = false
			}
		}
		if flat {
			opened = time.Time{}
		} else if opened.IsZero() {
			opened = f.At
		}
	}
	return opened, nil
}
