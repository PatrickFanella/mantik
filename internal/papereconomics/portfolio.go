// Package papereconomics accounts for already replayed counterfactual books.
// It has no I/O, account writer, admission issuer or execution authority.
package papereconomics

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Method = "usd-counterfactual-observed-bid-equity-v1"
const maxEquityCents = 2 * paperfill.MaxMoneyCents

var ErrInvalid = errors.New("invalid owner-selected portfolio books")

// The owner supplies the same finite opening capital and original entry/exit
// books used by its counterfactual. No result, fill or mark is supplied separately.
type Input struct {
	OpeningCashCents int64           `json:"opening_cash_cents"`
	EntryBook        paperfill.Book  `json:"entry_book"`
	ExitBook         *paperfill.Book `json:"exit_book,omitempty"`
	CutAt            time.Time       `json:"cut_at"`
}

type Point struct {
	At                   time.Time `json:"at"`
	CashCents            int64     `json:"cash_cents"`
	RemainingCostCents   int64     `json:"remaining_cost_cents"`
	RealizedNetCents     int64     `json:"realized_net_cents"`
	MarkedInventoryCents *int64    `json:"marked_inventory_cents,omitempty"`
	EquityCents          *int64    `json:"equity_cents,omitempty"`
	MarkSourceSHA256     string    `json:"mark_source_sha256,omitempty"`
	MarkQuoteID          string    `json:"mark_quote_id,omitempty"`
	Status               string    `json:"status"`
}

type Result struct {
	Method                       string  `json:"method"`
	InputSHA256                  string  `json:"input_sha256"`
	Points                       []Point `json:"points"`
	EndingCashCents              int64   `json:"ending_cash_cents"`
	RemainingCostCents           int64   `json:"remaining_cost_cents"`
	RealizedNetCents             int64   `json:"realized_net_cents"`
	ClosedNetCents               *int64  `json:"closed_net_cents,omitempty"`
	ObservedMaximumDrawdownCents int64   `json:"observed_maximum_drawdown_cents"`
	AllEventMarksAvailable       bool    `json:"all_event_marks_available"`
	ContinuousCoverageAvailable  bool    `json:"continuous_coverage_available"`
	CorporateActionsApplied      bool    `json:"corporate_actions_applied"`
	TotalReturnAvailable         bool    `json:"total_return_available"`
	Authority                    string  `json:"authority"`
	AdmissionAvailable           bool    `json:"admission_available"`
	ExecutionAvailable           bool    `json:"execution_available"`
	QualificationCredit          bool    `json:"qualification_credit"`
	SHA256                       string  `json:"sha256"`
}

type position struct{ entered, closed, debit, proceeds int64 }
type event struct {
	at    time.Time
	quote *paperfill.Quote
	fills []paperfill.Fill
}

// Evaluate preserves costs from actual verified fill journals. Marks are the
// observed bid at each retained event clock, never invented end-of-window sales.
// Drawdown describes this discrete path only. Corporate entitlements and gaps
// require additional owner evidence before a total-return evaluation can pass.
func Evaluate(ctx context.Context, in Input) (Result, error) {
	if in.OpeningCashCents < 1 || in.OpeningCashCents > paperfill.MaxMoneyCents || in.CutAt.IsZero() {
		return Result{}, ErrInvalid
	}
	if _, err := paperfill.ReplayBook(ctx, in.EntryBook); err != nil {
		return Result{}, err
	}
	books := []paperfill.Book{in.EntryBook}
	if in.ExitBook != nil {
		if in.ExitBook.Model != in.EntryBook.Model || in.ExitBook.Method != in.EntryBook.Method {
			return Result{}, ErrInvalid
		}
		if _, err := paperfill.ReplayBook(ctx, *in.ExitBook); err != nil {
			return Result{}, err
		}
		books = append(books, *in.ExitBook)
	}
	orders := map[string]paperfill.Order{}
	symbol := ""
	start := time.Time{}
	for i, b := range books {
		for _, o := range b.Orders {
			if o.Mode != "replay" || o.Currency != "USD" || i == 0 && o.Side != "buy" || i == 1 && o.Side != "sell" || o.PlacedAt.After(in.CutAt) {
				return Result{}, ErrInvalid
			}
			if symbol == "" {
				symbol = o.Symbol
			}
			if o.Symbol != symbol {
				return Result{}, ErrInvalid
			}
			if i == 0 {
				orders[o.ID] = o
				if start.IsZero() || o.PlacedAt.Before(start) {
					start = o.PlacedAt
				}
			} else if _, ok := orders[o.ID]; !ok {
				return Result{}, ErrInvalid
			}
		}
	}
	if start.IsZero() || in.CutAt.Before(start) {
		return Result{}, ErrInvalid
	}
	byTime := map[time.Time]*event{}
	quotesByID := map[string]paperfill.Quote{}
	get := func(at time.Time) *event {
		at = at.UTC()
		if byTime[at] == nil {
			byTime[at] = &event{at: at, fills: []paperfill.Fill{}}
		}
		return byTime[at]
	}
	get(start)
	get(in.CutAt)
	for _, b := range books {
		for _, e := range b.Entries {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if e.Command.At.Before(start) || e.Command.At.After(in.CutAt) {
				return Result{}, ErrInvalid
			}
			point := get(e.Command.At)
			if q := e.Command.Quote; q != nil {
				if old, ok := quotesByID[q.ID]; ok && !reflect.DeepEqual(old, *q) {
					return Result{}, ErrInvalid
				}
				quotesByID[q.ID] = *q
				// One observation per event clock; quote-ID order resolves equal
				// availability deterministically. This is not an intratick path.
				if point.quote == nil || q.ID > point.quote.ID {
					copy := *q
					point.quote = &copy
				}
			}
			for _, outcome := range e.Outcomes {
				if outcome.Outcome.Fill != nil {
					point.fills = append(point.fills, *outcome.Outcome.Fill)
				}
			}
		}
	}
	events := make([]*event, 0, len(byTime))
	for _, e := range byTime {
		events = append(events, e)
	}
	slices.SortFunc(events, func(a, b *event) int { return a.at.Compare(b.at) })
	out := Result{Method: Method, InputSHA256: workerplan.Hash(in), Points: []Point{}, AllEventMarksAvailable: true, Authority: "owner_selected_counterfactual_book_accounting_only"}
	cash, peak := in.OpeningCashCents, in.OpeningCashCents
	positions := map[string]*position{}
	var mark *paperfill.Quote
	for _, e := range events {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		// Entry debits precede exit credits at an equal clock; future proceeds
		// cannot finance a buy. Equal-side order is fixed by original order ID.
		slices.SortFunc(e.fills, func(a, b paperfill.Fill) int {
			if a.Side != b.Side {
				if a.Side == "buy" {
					return -1
				}
				return 1
			}
			return strings.Compare(a.OrderID, b.OrderID)
		})
		for _, f := range e.fills {
			if positions[f.OrderID] == nil {
				positions[f.OrderID] = &position{}
			}
			p := positions[f.OrderID]
			if f.Side == "buy" {
				if f.DebitCents > cash || p.closed > 0 {
					return Result{}, ErrInvalid
				}
				cash -= f.DebitCents
				p.entered += f.Quantity
				p.debit += f.DebitCents
			} else {
				if f.Quantity > p.entered-p.closed || f.NetProceedsCents > maxEquityCents-cash || f.NetProceedsCents < -cash {
					return Result{}, ErrInvalid
				}
				cash += f.NetProceedsCents
				p.closed += f.Quantity
				p.proceeds += f.NetProceedsCents
			}
		}
		if e.quote != nil {
			mark = e.quote
		}
		var remaining, realized, quantity int64
		for _, p := range positions {
			if p.entered < 1 || p.closed > p.entered {
				return Result{}, ErrInvalid
			}
			closedBasis := p.debit/p.entered*p.closed + p.debit%p.entered*p.closed/p.entered
			remaining += p.debit - closedBasis
			realized += p.proceeds - closedBasis
			quantity += p.entered - p.closed
		}
		if cash+remaining != in.OpeningCashCents+realized {
			return Result{}, ErrInvalid
		}
		point := Point{At: e.at, CashCents: cash, RemainingCostCents: remaining, RealizedNetCents: realized, Status: "mark_unavailable"}
		usable := quantity == 0 || mark != nil && mark.Quality == "usable" && mark.Symbol == symbol && mark.Currency == "USD" && mark.BidCents >= 1 && mark.BidCents <= paperfill.MaxPriceCents && !mark.OccurredAt.After(e.at) && e.at.Sub(mark.OccurredAt) <= time.Duration(in.EntryBook.Model.MaxQuoteAgeSeconds)*time.Second
		if usable {
			value := int64(0)
			if quantity > 0 {
				if quantity > maxEquityCents/mark.BidCents {
					return Result{}, ErrInvalid
				}
				value = quantity * mark.BidCents
				point.MarkSourceSHA256 = mark.SourceSHA256
				point.MarkQuoteID = mark.ID
			}
			if value > maxEquityCents-cash {
				return Result{}, ErrInvalid
			}
			equity := cash + value
			point.MarkedInventoryCents, point.EquityCents = &value, &equity
			point.Status = "observed_bid_mark"
			peak = max(peak, equity)
			out.ObservedMaximumDrawdownCents = max(out.ObservedMaximumDrawdownCents, peak-equity)
		} else {
			out.AllEventMarksAvailable = false
		}
		out.Points = append(out.Points, point)
		out.EndingCashCents, out.RemainingCostCents, out.RealizedNetCents = cash, remaining, realized
		if quantity == 0 {
			net := cash - in.OpeningCashCents
			out.ClosedNetCents = &net
		} else {
			out.ClosedNetCents = nil
		}
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
