package papereconomics

import (
	"context"
	"math/big"
	"regexp"
	"slices"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const TotalReturnMethod = "usd-counterfactual-observed-bid-reconciled-total-return-v1"

// MaxActions bounds one instrument's reconciled actions for a window.
const MaxActions = 64

// Action is one reconciled corporate action for the book's symbol. Amounts and
// ratios are exact decimal strings; nothing passes through floating point.
type Action struct {
	Kind        string `json:"kind"` // cash_dividend or split
	ExDate      string `json:"ex_date"`
	PayableDate string `json:"payable_date,omitempty"`
	Amount      string `json:"amount,omitempty"` // USD per share before the ex-date
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
}

// Actions are the complete reconciled actions for one symbol over an inclusive
// ex-date window. Positions held across a date outside the window make total
// return unavailable, because an action on that date would be unknown.
type Actions struct {
	Symbol      string   `json:"symbol"`
	WindowStart string   `json:"window_start"`
	WindowEnd   string   `json:"window_end"`
	Actions     []Action `json:"actions"`
}

type AppliedAction struct {
	Action
	At               time.Time  `json:"at"`
	QuantityBefore   string     `json:"quantity_before"`
	QuantityAfter    string     `json:"quantity_after"`
	EntitlementCents string     `json:"entitlement_cents,omitempty"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
}

// TotalReturn values are exact: cents and share quantities render as a
// terminating decimal or, when a split leaves a repeating fraction, as a/b.
type TotalReturn struct {
	Method                  string          `json:"method"`
	InputSHA256             string          `json:"input_sha256"`
	ActionsSHA256           string          `json:"actions_sha256"`
	Status                  string          `json:"status"`
	Applied                 []AppliedAction `json:"applied"`
	EndingCashCents         string          `json:"ending_cash_cents"`
	DividendReceivableCents string          `json:"dividend_receivable_cents"`
	RemainingCostCents      string          `json:"remaining_cost_cents"`
	RealizedNetCents        string          `json:"realized_net_cents"`
	HeldQuantity            string          `json:"held_quantity"`
	MarkQuoteID             string          `json:"mark_quote_id,omitempty"`
	MarkSourceSHA256        string          `json:"mark_source_sha256,omitempty"`
	EquityCents             *string         `json:"equity_cents,omitempty"`
	NetCents                *string         `json:"net_cents,omitempty"`
	Authority               string          `json:"authority"`
	AdmissionAvailable      bool            `json:"admission_available"`
	ExecutionAvailable      bool            `json:"execution_available"`
	QualificationCredit     bool            `json:"qualification_credit"`
	SHA256                  string          `json:"sha256"`
}

// Available reports whether terminal equity and net were derived.
func (r TotalReturn) Available() bool { return r.Status == "observed_total_return" }

var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

func date(s string) (time.Time, bool) {
	d, err := time.ParseInLocation(time.DateOnly, s, newYork)
	return d, err == nil && d.Format(time.DateOnly) == s
}

// Plain decimals, optionally with a two-digit exponent as a JSON number may
// carry. Hex floats, signs, fractions and unbounded exponents are refused.
var decimalText = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]{1,2})?$`)

func positive(s string) (*big.Rat, bool) {
	if !decimalText.MatchString(s) {
		return nil, false
	}
	v, ok := new(big.Rat).SetString(s)
	return v, ok && v.Sign() > 0
}

// The US market trades ex-dividend and ex-split from the start of the ex-date:
// a position held at 00:00 New York time on that date is entitled, and the
// split applies to it. Cash arrives at 00:00 on the payable date.
func boundary(d time.Time) time.Time { return d.UTC() }

type step struct {
	at     time.Time
	order  int // 0 ex-date, 1 payable date, 2 book event
	action int
	event  *event
}

type lot struct{ held, basis *big.Rat }

// EvaluateTotalReturn replays the same books as Evaluate with exact rational
// accounting and applies reconciled actions: dividend entitlement accrues as a
// receivable on the ex-date and becomes cash on the payable date; a split
// multiplies held quantity and preserves cost basis. A mark observed before
// the latest applied action cannot value the position after it. Malformed
// owner input is an error; uncovered holdings and marks are explicit statuses.
func EvaluateTotalReturn(ctx context.Context, in Input, actions Actions) (TotalReturn, error) {
	events, symbol, err := replayEvents(ctx, in)
	if err != nil {
		return TotalReturn{}, err
	}
	if actions.Symbol != symbol {
		return TotalReturn{}, ErrInvalid
	}
	values, seen, windowStart, windowEnd, err := parseActions(actions)
	if err != nil {
		return TotalReturn{}, err
	}
	out := TotalReturn{Method: TotalReturnMethod, InputSHA256: workerplan.Hash(in), ActionsSHA256: workerplan.Hash(actions), Status: "observed_total_return", Applied: []AppliedAction{}, Authority: "owner_selected_reconciled_total_return_accounting_only"}
	// Dividends on one ex-date commute. A split's order relative to another
	// action on its ex-date is undocumented; it matters only for shares held
	// at that boundary, where no value is then derived.
	conflict := map[string]bool{}
	for date, kinds := range seen {
		conflict[date] = len(kinds) > 1 && slices.Contains(kinds, "split")
	}
	start, cut := events[0].at, in.CutAt.UTC()
	steps := []step{}
	for _, e := range events {
		steps = append(steps, step{at: e.at, order: 2, event: e})
	}
	applied := map[int]int{}
	for i, v := range values {
		at := boundary(v.ex)
		if !at.After(start) || at.After(cut) {
			continue // no position can exist at or before the first placement
		}
		steps = append(steps, step{at: at, order: 0, action: i})
		if !v.pay.IsZero() && !boundary(v.pay).After(cut) {
			steps = append(steps, step{at: boundary(v.pay), order: 1, action: i})
		}
	}
	slices.SortStableFunc(steps, func(a, b step) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return a.order - b.order
	})

	zero := func() *big.Rat { return new(big.Rat) }
	cash, receivable := new(big.Rat).SetInt64(in.OpeningCashCents), zero()
	entitled := map[int]*big.Rat{}
	lots := map[string]*lot{}
	held := func() *big.Rat {
		q := zero()
		for _, l := range lots {
			q.Add(q, l.held)
		}
		return q
	}
	var mark *paperfill.Quote
	lastAction, previous := time.Time{}, start
	finish := func(status string) (TotalReturn, error) {
		remaining, realized := zero(), zero()
		for _, l := range lots {
			remaining.Add(remaining, l.basis)
		}
		// cash + receivable + remaining basis = opening + realized + paid and
		// accrued entitlements; realized is the closed-lot difference only.
		realized.Add(cash, remaining)
		realized.Sub(realized, new(big.Rat).SetInt64(in.OpeningCashCents))
		for _, v := range entitled {
			realized.Sub(realized, v)
		}
		realized.Add(realized, receivable)
		out.Status = status
		out.EndingCashCents, out.DividendReceivableCents = exactText(cash), exactText(receivable)
		out.RemainingCostCents, out.RealizedNetCents, out.HeldQuantity = exactText(remaining), exactText(realized), exactText(held())
		out.SHA256 = ""
		out.SHA256 = workerplan.Hash(out)
		return out, nil
	}
	for _, s := range steps {
		if err := ctx.Err(); err != nil {
			return TotalReturn{}, err
		}
		if held().Sign() > 0 && !covered(previous, s.at, windowStart, windowEnd) {
			return finish("holding_outside_corporate_coverage_window")
		}
		previous = s.at
		switch s.order {
		case 0:
			v, quantity := values[s.action], held()
			if conflict[actions.Actions[s.action].ExDate] && quantity.Sign() > 0 {
				return finish("same_ex_date_split_and_other_action_unsupported")
			}
			a := AppliedAction{Action: actions.Actions[s.action], At: s.at, QuantityBefore: exactText(quantity)}
			if v.ratio != nil {
				for _, l := range lots {
					l.held.Mul(l.held, v.ratio)
				}
			} else {
				amount := new(big.Rat).Mul(v.amount, quantity)
				entitled[s.action] = amount
				receivable.Add(receivable, amount)
				a.EntitlementCents = exactText(amount)
			}
			a.QuantityAfter = exactText(held())
			applied[s.action] = len(out.Applied)
			out.Applied = append(out.Applied, a)
			lastAction = s.at
		case 1:
			amount := entitled[s.action]
			receivable.Sub(receivable, amount)
			cash.Add(cash, amount)
			at := s.at
			out.Applied[applied[s.action]].PaidAt = &at
		default:
			sortFills(s.event.fills)
			for _, f := range s.event.fills {
				l := lots[f.OrderID]
				if l == nil {
					l = &lot{held: zero(), basis: zero()}
					lots[f.OrderID] = l
				}
				q := new(big.Rat).SetInt64(f.Quantity)
				if f.Side == "buy" {
					cash.Sub(cash, new(big.Rat).SetInt64(f.DebitCents))
					l.held.Add(l.held, q)
					l.basis.Add(l.basis, new(big.Rat).SetInt64(f.DebitCents))
					continue
				}
				// A reverse split can leave fewer shares than an original exit
				// order, whose quantity was fixed before the action.
				if q.Cmp(l.held) > 0 {
					return finish("fill_exceeds_split_adjusted_position")
				}
				closed := new(big.Rat).Mul(l.basis, q)
				closed.Quo(closed, l.held)
				l.basis.Sub(l.basis, closed)
				l.held.Sub(l.held, q)
				cash.Add(cash, new(big.Rat).SetInt64(f.NetProceedsCents))
			}
			if s.event.quote != nil {
				mark = s.event.quote
			}
		}
	}
	quantity := held()
	equity := new(big.Rat).Add(cash, receivable)
	if quantity.Sign() > 0 {
		if mark == nil || mark.Quality != "usable" || mark.Symbol != symbol || mark.Currency != "USD" || mark.BidCents < 1 || mark.BidCents > paperfill.MaxPriceCents || mark.OccurredAt.After(cut) || cut.Sub(mark.OccurredAt) > time.Duration(in.EntryBook.Model.MaxQuoteAgeSeconds)*time.Second {
			return finish("terminal_mark_unavailable")
		}
		// A pre-action quote is in pre-action units and still carries the
		// dividend that the receivable now holds.
		if mark.OccurredAt.Before(lastAction) {
			return finish("terminal_mark_predates_corporate_action")
		}
		equity.Add(equity, new(big.Rat).Mul(quantity, new(big.Rat).SetInt64(mark.BidCents)))
		out.MarkQuoteID, out.MarkSourceSHA256 = mark.ID, mark.SourceSHA256
	}
	e, n := exactText(equity), exactText(new(big.Rat).Sub(equity, new(big.Rat).SetInt64(in.OpeningCashCents)))
	out.EquityCents, out.NetCents = &e, &n
	return finish("observed_total_return")
}

type parsedAction struct {
	ex, pay       time.Time
	amount, ratio *big.Rat
}

// Validate reports whether the actions are well formed, independent of any
// book. EvaluateTotalReturn additionally requires the book's symbol.
func (a Actions) Validate() error {
	_, _, _, _, err := parseActions(a)
	return err
}

func parseActions(actions Actions) ([]parsedAction, map[string][]string, time.Time, time.Time, error) {
	windowStart, ok1 := date(actions.WindowStart)
	windowEnd, ok2 := date(actions.WindowEnd)
	if !ok1 || !ok2 || windowEnd.Before(windowStart) || actions.Symbol == "" || actions.Actions == nil || len(actions.Actions) > MaxActions {
		return nil, nil, time.Time{}, time.Time{}, ErrInvalid
	}
	values := make([]parsedAction, len(actions.Actions))
	seen := map[string][]string{}
	bad := func() ([]parsedAction, map[string][]string, time.Time, time.Time, error) {
		return nil, nil, time.Time{}, time.Time{}, ErrInvalid
	}
	for i, a := range actions.Actions {
		ex, ok := date(a.ExDate)
		if !ok || ex.Before(windowStart) || ex.After(windowEnd) {
			return bad()
		}
		v := parsedAction{ex: ex}
		switch a.Kind {
		case "cash_dividend":
			amount, ok := positive(a.Amount)
			if !ok || a.From != "" || a.To != "" {
				return bad()
			}
			v.amount = amount.Mul(amount, big.NewRat(100, 1))
			if a.PayableDate != "" {
				pay, ok := date(a.PayableDate)
				if !ok || pay.Before(ex) {
					return bad()
				}
				v.pay = pay
			}
		case "split":
			from, ok1 := positive(a.From)
			to, ok2 := positive(a.To)
			if !ok1 || !ok2 || a.Amount != "" || a.PayableDate != "" {
				return bad()
			}
			v.ratio = to.Quo(to, from)
		default:
			return bad()
		}
		seen[a.ExDate] = append(seen[a.ExDate], a.Kind)
		values[i] = v
	}
	return values, seen, windowStart, windowEnd, nil
}

// covered reports whether every ex-date boundary in (from, to] is a date in
// the window. Dates are contiguous, so checking the first and last suffices.
func covered(from, to, start, end time.Time) bool {
	first := from.In(newYork)
	first = time.Date(first.Year(), first.Month(), first.Day()+1, 0, 0, 0, 0, newYork)
	last := to.In(newYork)
	last = time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, newYork)
	if last.Before(first) {
		return true
	}
	return !first.Before(start) && !last.After(end)
}

// exactText renders a terminating decimal exactly and any other rational as a
// reduced a/b fraction, so no value is rounded.
func exactText(r *big.Rat) string {
	d := new(big.Int).Set(r.Denom())
	digits := 0
	for _, p := range []int64{2, 5} {
		n := 0
		for m := new(big.Int); ; n++ {
			q, rem := new(big.Int).QuoRem(d, big.NewInt(p), m)
			if rem.Sign() != 0 {
				break
			}
			d = q
		}
		digits = max(digits, n)
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return r.RatString()
	}
	return r.FloatString(digits)
}
