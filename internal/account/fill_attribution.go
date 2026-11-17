package account

import (
	"errors"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
)

// BookFillEconomics projects one original broker activity and its owned order.
// Gross and CashDelta are exact USD decimals, not rounded display cents.
type BookFillEconomics struct {
	Fill                               BookFill
	RecordedAt                         time.Time
	AllocationID, SleeveID, StrategyID string
	Gross, CashDelta                   string
}

// BookExecutionAttribution reconciles only original fills and retained deposits.
// It grants no current order admission, benchmark or qualification credit.
type BookExecutionAttribution struct {
	Method, Status, GrossBuys, GrossSells, Fees, CashDelta string
	OpeningCash, Deposits, ReconciledCash                  string
	Fills                                                  []BookFillEconomics
}

var attributionAmount = regexp.MustCompile(`^(0|[1-9][0-9]{0,23})(\.[0-9]{1,18})?$`)

// Validate checks a portable readback's identities and exact cash arithmetic.
// Only the authenticated service's original journal establishes source custody.
func (e BookExecutionAttribution) Validate(cash string) error {
	invalid := errors.New("invalid book fill attribution")
	if e.Method != "retained-broker-fill-cash-v1" || e.Fills == nil || len(e.Fills) > 4096 || !attributionAmount.MatchString(e.OpeningCash) || !attributionAmount.MatchString(e.Deposits) {
		return invalid
	}
	opening, ok := new(big.Rat).SetString(e.OpeningCash)
	if !ok || opening.Sign() <= 0 {
		return invalid
	}
	deposits, ok := new(big.Rat).SetString(e.Deposits)
	if !ok {
		return invalid
	}
	buys, sells, fees := new(big.Rat), new(big.Rat), new(big.Rat)
	seen := map[string]bool{}
	var previous time.Time
	for _, x := range e.Fills {
		f := x.Fill
		q, qe := bookRat(f.Quantity)
		p, pe := bookRat(f.Price)
		fee, fe := bookRat(f.Fee)
		// Activity IDs retain the journal's nonempty <=128-byte contract;
		// only locally generated order IDs use the narrower bookID pattern.
		if f.ID == "" || len(f.ID) > 128 || !bookID.MatchString(f.OrderID) || f.VenueOrderID == "" || !bookSymbol.MatchString(f.Symbol) || !hashText.MatchString(f.SourceSHA256) || f.At.IsZero() || f.At.Before(previous) || x.RecordedAt.Before(f.At) || seen[f.ID] || qe != nil || pe != nil || fe != nil || q.Sign() <= 0 || p.Sign() <= 0 || f.Side != "buy" && f.Side != "sell" {
			return invalid
		}
		seen[f.ID], previous = true, f.At
		gross := new(big.Rat).Mul(q, p)
		delta := new(big.Rat).Set(gross)
		if f.Side == "buy" {
			buys.Add(buys, gross)
			delta.Neg(delta)
		} else {
			sells.Add(sells, gross)
		}
		delta.Sub(delta, fee)
		fees.Add(fees, fee)
		if x.Gross != exactBookDecimal(gross) || x.CashDelta != exactBookDecimal(delta) {
			return invalid
		}
	}
	delta := new(big.Rat).Sub(sells, buys)
	delta.Sub(delta, fees)
	reconciled := new(big.Rat).Add(opening, deposits)
	reconciled.Add(reconciled, delta)
	status := "no_fills"
	if len(e.Fills) > 0 {
		status = "fills_reconciled"
	}
	if e.Status != status || e.GrossBuys != exactBookDecimal(buys) || e.GrossSells != exactBookDecimal(sells) || e.Fees != exactBookDecimal(fees) || e.CashDelta != exactBookDecimal(delta) || e.ReconciledCash != exactBookDecimal(reconciled) || cash != reconciled.FloatString(9) {
		return invalid
	}
	return nil
}

func exactBookDecimal(r *big.Rat) string {
	// Inputs have at most nine decimal places; quantity*price has at most 18.
	// Sums and signs preserve that denominator bound. No cents rounding occurs.
	s := strings.TrimRight(strings.TrimRight(r.FloatString(18), "0"), ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// bookFillAttribution runs only after the original capped journal was replayed.
// Every row keeps both the actual fill clock and local application clock.
func bookFillAttribution(s CappedState, orders map[string]*openOrder, currentCash *big.Rat) (BookExecutionAttribution, error) {
	out := BookExecutionAttribution{Method: "retained-broker-fill-cash-v1", Status: "no_fills", OpeningCash: s.Config.OpeningCash, Fills: []BookFillEconomics{}}
	buys, sells, fees, deposits := new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)
	for _, c := range s.Events {
		if c.Kind == "deposit" {
			amount, err := bookRat(c.Amount)
			if err != nil {
				return BookExecutionAttribution{}, err
			}
			deposits.Add(deposits, amount)
		}
		if c.Kind != "fill" {
			continue
		}
		f := *c.Fill
		o := orders[f.OrderID]
		q, qe := bookRat(f.Quantity)
		p, pe := bookRat(f.Price)
		fee, fe := bookRat(f.Fee)
		if o == nil || qe != nil || pe != nil || fe != nil {
			return BookExecutionAttribution{}, errors.New("fill economics lack original owned terms")
		}
		gross := new(big.Rat).Mul(q, p)
		delta := new(big.Rat).Set(gross)
		if f.Side == "buy" {
			buys.Add(buys, gross)
			delta.Neg(delta)
		} else {
			sells.Add(sells, gross)
		}
		fees.Add(fees, fee)
		delta.Sub(delta, fee)
		out.Fills = append(out.Fills, BookFillEconomics{Fill: f, RecordedAt: c.At, AllocationID: o.order.AllocationID, SleeveID: o.order.SleeveID, StrategyID: o.order.StrategyID, Gross: exactBookDecimal(gross), CashDelta: exactBookDecimal(delta)})
	}
	slices.SortStableFunc(out.Fills, func(a, b BookFillEconomics) int { return a.Fill.At.Compare(b.Fill.At) })
	delta := new(big.Rat).Sub(sells, buys)
	delta.Sub(delta, fees)
	opening, err := bookRat(s.Config.OpeningCash)
	if err != nil {
		return BookExecutionAttribution{}, err
	}
	cash := new(big.Rat).Add(opening, deposits)
	cash.Add(cash, delta)
	if cash.Cmp(currentCash) != 0 {
		return BookExecutionAttribution{}, errors.New("fill economics do not reconcile to book cash")
	}
	if len(out.Fills) > 0 {
		out.Status = "fills_reconciled"
	}
	out.GrossBuys, out.GrossSells, out.Fees, out.CashDelta = exactBookDecimal(buys), exactBookDecimal(sells), exactBookDecimal(fees), exactBookDecimal(delta)
	out.Deposits, out.ReconciledCash = exactBookDecimal(deposits), exactBookDecimal(cash)
	return out, nil
}
