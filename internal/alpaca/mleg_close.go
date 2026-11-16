package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

// MLegClose fixes one whole-close attempt and the fallback limit for each opening
// leg, in opening leg order. Limits cannot be raised under a new close identity.
// Repeating this exact plan reconciles pending orders and resumes safe progress.
type MLegClose struct {
	OpenClientOrderID string
	LimitPrice        string
	LegLimits         []string
}

// MLegCloseResult exposes owned order/leg identities and cumulative quantities.
// Pending never means canceled or filled. Raw source responses stay in custody.
type MLegCloseResult struct {
	State     string
	Open      OptionPaperOrder
	Whole     OptionPaperOrder
	Fallback  []OptionPaperOrder
	Remaining []int
}

func optionCloseID(open string) string { return "mleg-close-" + Digest(open)[:32] }
func optionLegID(open string, i int) string {
	return fmt.Sprintf("mleg-leg-%s-%d", Digest(open)[:32], i)
}

func (p *PaperTrader) retainedOpen() (MLegOpen, error) {
	var o MLegOpen
	raw, err := p.journal.read(optionLifecycle, MaxBytes)
	if err != nil {
		return o, err
	}
	if json.Unmarshal(raw, &o) != nil || o.Order.Validate() != nil || len(o.Structure.Legs) != len(o.Order.Legs) {
		return o, errors.New("invalid retained option lifecycle")
	}
	return o, nil
}

// CloseMLeg first closes as one mleg. A confirmed refusal, rejection, expiry or
// cancellation permits fallback ONLY after GET reconciles terminal children.
// All shorts must be filled closed before any protection can be sold. A partial,
// unknown or failed short close leaves every long untouched. No polling occurs.
func (p *PaperTrader) CloseMLeg(ctx context.Context, plan MLegClose) (MLegCloseResult, error) {
	out := MLegCloseResult{State: "pending"}
	if !paperID.MatchString(plan.OpenClientOrderID) || !signedOptionLimit(plan.LimitPrice) {
		return out, errors.New("invalid option close plan")
	}
	if err := p.enter(); err != nil {
		return out, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return out, err
	}
	open, err := p.retainedOpen()
	if err != nil {
		return out, err
	}
	if open.Order.ClientOrderID != plan.OpenClientOrderID || len(plan.LegLimits) != len(open.Order.Legs) {
		return out, ErrJournalConflict
	}
	for _, v := range plan.LegLimits {
		if !positivePaperDecimal(v) {
			return out, errors.New("fallback requires positive per-leg limits")
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return out, err
	}
	if old, e := p.journal.read(optionClosePlan, MaxBytes); e == nil {
		if !bytes.Equal(raw, old) {
			return out, ErrJournalConflict
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return out, e
	}
	opening := optionRequest{MLegOrder: open.Order}
	out.Open, err = p.optionLookup(ctx, opening)
	if err != nil {
		return out, err
	}
	if err = p.optionMutationAuthority(); err != nil {
		return out, err
	}
	if !optionTerminal(out.Open.Status) {
		out.Open, err = p.optionCancelTerminal(ctx, opening, out.Open)
		if err != nil {
			return out, err
		}
	}
	if !optionChildrenTerminal(out.Open) {
		return out, ErrOptionPending
	}
	qty, _ := optionInteger(open.Order.Qty)
	units := -1
	initial := make([]int, len(open.Order.Legs))
	for i, l := range open.Order.Legs {
		n, _ := filledContracts(out.Open.Legs[i])
		ratio, _ := optionInteger(l.RatioQty)
		if n%ratio != 0 || units >= 0 && units != n/ratio {
			return out, errors.New("opening leg fills are not proportional; manual reconciliation required")
		}
		units = n / ratio
		initial[i] = n
	}
	if units > qty {
		return out, errors.New("opening fills exceed custody")
	}
	if units == 0 {
		if err = p.optionInventory(ctx, open.Order.Legs, make([]int, len(open.Order.Legs))); err != nil {
			return out, err
		}
		out.State = "closed"
		return out, nil
	}
	if err = p.optionCloseFunding(open, out.Open, plan, initial); err != nil {
		return out, err
	}
	if err = p.journal.writeOnce(optionClosePlan, raw); err != nil {
		return out, err
	}
	whole := optionRequest{MLegOrder: open.Order}
	whole.ClientOrderID = optionCloseID(open.Order.ClientOrderID)
	whole.Qty = strconv.Itoa(units)
	whole.LimitPrice = plan.LimitPrice
	whole.Legs = append([]MLeg(nil), open.Order.Legs...)
	for i := range whole.Legs {
		l := &whole.Legs[i]
		if l.Side == "buy" {
			l.Side = "sell"
		} else {
			l.Side = "buy"
		}
		l.PositionIntent = l.Side + "_to_close"
	}
	// Every generated close must be backed by this lifecycle's actual fills.
	want := signedRemaining(open.Order.Legs, initial)
	if _, e := p.journal.read(optionIntentName(whole.ClientOrderID), MaxBytes); errors.Is(e, os.ErrNotExist) {
		if err = p.optionInventory(ctx, open.Order.Legs, want); err != nil {
			return out, err
		}
	} else if e != nil {
		return out, e
	}
	out.Whole, err = p.optionSubmit(ctx, whole)
	if err != nil && !errors.Is(err, ErrOptionRefused) {
		return out, err
	}
	refused := errors.Is(err, ErrOptionRefused)
	if !refused && !optionTerminal(out.Whole.Status) {
		out.Whole, err = p.optionCancelTerminal(ctx, whole, out.Whole)
		if err != nil {
			return out, err
		}
	}
	if !refused && !optionChildrenTerminal(out.Whole) {
		return out, ErrOptionPending
	}
	remaining := append([]int(nil), initial...)
	if !refused {
		for i, l := range out.Whole.Legs {
			n, _ := filledContracts(l)
			remaining[i] -= n
			if remaining[i] < 0 {
				return out, errors.New("whole close exceeds owned fills")
			}
		}
	}
	out.Remaining = remaining
	// A successful whole close needs each child's quantity; parent status alone
	// never authorizes removing protection or declaring the position flat.
	done := true
	for _, n := range remaining {
		done = done && n == 0
	}
	if done {
		if err = p.optionInventory(ctx, open.Order.Legs, signedRemaining(open.Order.Legs, remaining)); err != nil {
			return out, err
		}
		out.State = "closed"
		return out, nil
	}
	if !refused && out.Whole.Status == "filled" {
		return out, errors.New("filled whole order has inconsistent child fills")
	}
	// Deterministic failure is now known and all whole-order partials reconciled.
	// Recover ALL earlier fallback orders before inventory comparison on restart.
	recovered := make(map[int]OptionPaperOrder)
	for i, l := range open.Order.Legs {
		id := optionLegID(open.Order.ClientOrderID, i)
		request := closingLeg(whole, id, l, remaining[i], plan.LegLimits[i])
		intent, e := p.journal.read(optionIntentName(id), MaxBytes)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return out, e
		}
		// Original quantities remain immutable even after some of that order fills.
		if json.Unmarshal(intent, &request) != nil || request.ClientOrderID != id || request.Symbol != l.Symbol || request.Side == l.Side || request.PositionIntent != request.Side+"_to_close" || request.LimitPrice != plan.LegLimits[i] {
			return out, ErrJournalConflict
		}
		original, _ := optionInteger(request.Qty)
		if original != remaining[i] {
			return out, errors.New("fallback intent exceeds remaining whole-close custody")
		}
		o, e := p.optionLookup(ctx, request)
		if e != nil {
			return out, e
		}
		recovered[i] = o
		out.Fallback = append(out.Fallback, o)
		n, _ := filledContracts(o)
		remaining[i] -= n
	}
	if err = p.optionInventoryForFallback(ctx, open.Order.Legs, remaining, recovered); err != nil {
		return out, err
	}
	// Buy every short to completion first. Failed/partial/ambiguous attempts stop
	// here; a different plan or fresh ID cannot bypass the original attempt.
	for _, shorts := range []bool{true, false} {
		if !shorts {
			for i, l := range open.Order.Legs {
				if l.Side == "sell" && remaining[i] != 0 {
					return out, ErrOptionPending
				}
			}
		}
		for i, l := range open.Order.Legs {
			if (l.Side == "sell") != shorts {
				continue
			}
			if prev, ok := recovered[i]; ok {
				if !optionTerminal(prev.Status) || remaining[i] != 0 {
					return out, ErrOptionPending
				}
				continue
			}
			if remaining[i] == 0 {
				continue
			}
			// Reconcile live quantities/pending orders before each new POST. An earlier
			// successful short fill is a required witness before long-sale authority.
			if err = p.optionInventory(ctx, open.Order.Legs, signedRemaining(open.Order.Legs, remaining)); err != nil {
				return out, err
			}
			request := closingLeg(whole, optionLegID(open.Order.ClientOrderID, i), l, remaining[i], plan.LegLimits[i])
			if request.Side == "buy" {
				if err = p.optionFallbackFunding(open, out.Open, out.Whole, recovered, request); err != nil {
					return out, err
				}
			}
			o, e := p.optionSubmit(ctx, request)
			if e != nil {
				return out, e
			}
			out.Fallback = append(out.Fallback, o)
			recovered[i] = o
			n, _ := filledContracts(o)
			remaining[i] -= n
			if !optionTerminal(o.Status) || remaining[i] != 0 {
				return out, ErrOptionPending
			}
		}
	}
	if err = p.optionInventory(ctx, open.Order.Legs, signedRemaining(open.Order.Legs, remaining)); err != nil {
		return out, err
	}
	out.State = "closed"
	return out, nil
}

func closingLeg(whole optionRequest, id string, l MLeg, n int, limit string) optionRequest {
	side := "buy"
	if l.Side == "buy" {
		side = "sell"
	}
	return optionRequest{MLegOrder: MLegOrder{ClientOrderID: id, Qty: strconv.Itoa(n), Type: "limit", TimeInForce: whole.TimeInForce, LimitPrice: limit, OrderClass: "simple"}, Symbol: l.Symbol, Side: side, PositionIntent: side + "_to_close"}
}
func signedRemaining(legs []MLeg, qty []int) []int {
	out := append([]int(nil), qty...)
	for i, l := range legs {
		if l.Side == "sell" {
			out[i] = -out[i]
		}
	}
	return out
}
func (p *PaperTrader) optionCancelTerminal(ctx context.Context, r optionRequest, o OptionPaperOrder) (OptionPaperOrder, error) {
	if err := p.optionMutationAuthority(); err != nil {
		return o, err
	}
	if err := p.optionMutationCapacity(); err != nil {
		return o, err
	}
	_, status, err := p.request(ctx, http.MethodDelete, "/v2/orders/"+url.PathEscape(o.ID), nil)
	if err != nil || status != 204 && status != 422 {
		return o, ErrOptionPending
	}
	next, err := p.optionLookup(ctx, r)
	if err != nil {
		return o, err
	}
	if !optionTerminal(next.Status) {
		return next, ErrOptionPending
	}
	return next, nil
}
func (p *PaperTrader) optionInventoryForFallback(ctx context.Context, legs []MLeg, remaining []int, orders map[int]OptionPaperOrder) error {
	// Pending reductions must not be mistaken for other custody. Stop without
	// generating anything until their terminal result has been observed.
	for _, o := range orders {
		if !optionTerminal(o.Status) {
			return ErrOptionPending
		}
	}
	return p.optionInventory(ctx, legs, signedRemaining(legs, remaining))
}
func (p *PaperTrader) optionCloseFunding(open MLegOpen, filled OptionPaperOrder, plan MLegClose, qty []int) error {
	cash, err := p.optionCloseCash(open, filled)
	if err != nil {
		return err
	}
	buys := new(big.Rat)
	for i, l := range open.Order.Legs {
		if l.Side == "sell" {
			limit, _ := new(big.Rat).SetString(plan.LegLimits[i])
			buys.Add(buys, new(big.Rat).Mul(limit, big.NewRat(int64(qty[i]*100), 1)))
		}
	}
	ratio, _ := optionInteger(open.Order.Legs[0].RatioQty)
	whole := optionPremiumAllowance(plan.LimitPrice, qty[0]/ratio)
	if whole.Cmp(buys) > 0 {
		buys.Set(whole)
	}
	if cash.Cmp(buys) < 0 {
		return errors.New("option close exceeds dedicated settled funding; protection retained")
	}
	return nil
}

func (p *PaperTrader) optionCloseCash(open MLegOpen, filled OptionPaperOrder) (*big.Rat, error) {
	localCash, err := open.LocalFunding(p.account)
	if err != nil {
		return nil, err
	}
	cash, _ := new(big.Rat).SetString(localCash)
	// A terminal package consumes its transmitted positive limit for filled units.
	// Improvements and credits never replenish cash for a separate order.
	n, _ := filledContracts(filled)
	cash.Sub(cash, optionPremiumAllowance(open.Order.LimitPrice, n))
	cash.Sub(cash, optionFeePool(open))
	return cash, nil
}

// A terminal whole close may have spent part of the dedicated cash. Reconcile
// its package allowance and every previous fallback buy before authorizing another.
// Observed sales remain unavailable; the full modeled fee pool stays reserved.
func (p *PaperTrader) optionFallbackFunding(open MLegOpen, filled OptionPaperOrder, whole OptionPaperOrder, previous map[int]OptionPaperOrder, request optionRequest) error {
	cash, err := p.optionCloseCash(open, filled)
	if err != nil {
		return err
	}
	if whole.ID != "" {
		n, ok := filledContracts(whole)
		if !ok || !optionChildrenTerminal(whole) {
			return errors.New("whole-close consumption unknown; protection retained")
		}
		cash.Sub(cash, optionPremiumAllowance(whole.LimitPrice, n))
	}
	for _, order := range previous {
		if order.Side == "buy" {
			n, ok := filledContracts(order)
			if !ok || !optionTerminal(order.Status) {
				return errors.New("fallback consumption unknown; protection retained")
			}
			cash.Sub(cash, optionPremiumAllowance(order.LimitPrice, n))
		}
	}
	n, ok := optionInteger(request.Qty)
	if !ok {
		return errors.New("invalid option fallback quantity")
	}
	limit, _ := new(big.Rat).SetString(request.LimitPrice)
	need := new(big.Rat).Mul(limit, big.NewRat(int64(n*100), 1))
	if cash.Cmp(need) < 0 {
		return errors.New("option close exceeds dedicated settled funding; protection retained")
	}
	return nil
}

func optionChildrenTerminal(o OptionPaperOrder) bool {
	if !optionTerminal(o.Status) {
		return false
	}
	for _, l := range o.Legs {
		if !optionTerminal(l.Status) {
			return false
		}
	}
	return true
}
