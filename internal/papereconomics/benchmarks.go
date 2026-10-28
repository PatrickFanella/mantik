package papereconomics

import (
	"context"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const BenchmarkMethod = "owner-observed-first-quote-whole-share-passive-v1"

// Benchmarks are alternative studies with the same opening cash and model.
// They do not consume strategy liquidity or supply total-return coverage.
type Benchmarks struct {
	Method                         string          `json:"method"`
	InputSHA256                    string          `json:"input_sha256"`
	CashMethod                     string          `json:"cash_method"`
	CashEndingCents                int64           `json:"cash_ending_cents"`
	CashNetCents                   int64           `json:"cash_net_cents"`
	PassiveStatus                  string          `json:"passive_status"`
	PassiveFilledQuantity          int64           `json:"passive_filled_quantity"`
	ReferenceQuoteID               string          `json:"reference_quote_id,omitempty"`
	ReferenceSourceSHA256          string          `json:"reference_source_sha256,omitempty"`
	PlacedAt                       time.Time       `json:"placed_at"`
	PassiveBook                    *paperfill.Book `json:"passive_book,omitempty"`
	Passive                        *Result         `json:"passive,omitempty"`
	StrategyObservedNetCents       *int64          `json:"strategy_observed_net_cents,omitempty"`
	PassiveObservedNetCents        *int64          `json:"passive_observed_net_cents,omitempty"`
	ObservedExcessOverCashCents    *int64          `json:"observed_excess_over_cash_cents,omitempty"`
	ObservedExcessOverPassiveCents *int64          `json:"observed_excess_over_passive_cents,omitempty"`
	TotalReturnComparisonAvailable bool            `json:"total_return_comparison_available"`
	CorporateCoverageReconciled    bool            `json:"corporate_coverage_reconciled"`
	Authority                      string          `json:"authority"`
	AdmissionAvailable             bool            `json:"admission_available"`
	ExecutionAvailable             bool            `json:"execution_available"`
	QualificationCredit            bool            `json:"qualification_credit"`
	SHA256                         string          `json:"sha256"`
}

// The first usable retained quote supplies a reference price, not a retroactive
// fill. The order is placed one nanosecond after its availability. Only later
// eligible observations can fill it through the shared occurrence-book method.
// Limit and quantity are fixed once; no reinvestment or forced terminal sale.
func EvaluateBenchmarks(ctx context.Context, in Input) (Benchmarks, error) {
	strategy, err := Evaluate(ctx, in)
	if err != nil {
		return Benchmarks{}, err
	}
	out := Benchmarks{Method: BenchmarkMethod, InputSHA256: workerplan.Hash(in), CashMethod: "nominal-usd-cash-no-interest-v1", CashEndingCents: in.OpeningCashCents, PassiveStatus: "reference_quote_unavailable", Authority: "owner_selected_observed_price_benchmarks_only"}
	finish := func() (Benchmarks, error) { out.SHA256 = ""; out.SHA256 = workerplan.Hash(out); return out, nil }
	last := strategy.Points[len(strategy.Points)-1]
	if last.EquityCents != nil {
		net := *last.EquityCents - in.OpeningCashCents
		out.StrategyObservedNetCents = &net
		excess := net
		out.ObservedExcessOverCashCents = &excess
	}
	quotes := map[string]paperfill.BookCommand{}
	books := []paperfill.Book{in.EntryBook}
	if in.ExitBook != nil {
		books = append(books, *in.ExitBook)
	}
	for _, book := range books {
		for _, entry := range book.Entries {
			if err := ctx.Err(); err != nil {
				return Benchmarks{}, err
			}
			q := entry.Command.Quote
			if q == nil || q.Symbol != in.EntryBook.Orders[0].Symbol {
				continue
			}
			if old, ok := quotes[q.ID]; ok {
				if !reflect.DeepEqual(old.Quote, q) {
					return Benchmarks{}, ErrInvalid
				}
				if !entry.Command.At.Before(old.At) {
					continue
				}
			}
			quotes[q.ID] = paperfill.BookCommand{At: entry.Command.At, Quote: q}
		}
	}
	path := make([]paperfill.BookCommand, 0, len(quotes))
	for _, q := range quotes {
		path = append(path, q)
	}
	slices.SortFunc(path, func(a, b paperfill.BookCommand) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		if a.Quote.ID < b.Quote.ID {
			return -1
		}
		if a.Quote.ID > b.Quote.ID {
			return 1
		}
		return 0
	})
	model := in.EntryBook.Model
	var reference *paperfill.BookCommand
	for i := range path {
		q := path[i]
		if q.Quote.Quality == "usable" && !q.Quote.ReceivedAt.After(q.At) && !q.Quote.OccurredAt.After(q.At) && q.At.Sub(q.Quote.OccurredAt) <= time.Duration(model.MaxQuoteAgeSeconds)*time.Second {
			reference = &path[i]
			break
		}
	}
	if reference == nil {
		return finish()
	}
	out.ReferenceQuoteID, out.ReferenceSourceSHA256 = reference.Quote.ID, reference.Quote.SourceSHA256
	out.PlacedAt = reference.At.Add(time.Nanosecond)
	if !in.CutAt.After(out.PlacedAt) {
		out.PassiveStatus = "no_post_reference_window"
		return finish()
	}
	limit := (reference.Quote.AskCents*(10000+model.SlippageBPS) + 9999) / 10000
	if limit > paperfill.MaxPriceCents {
		out.PassiveStatus = "reference_model_price_out_of_range"
		return finish()
	}
	// One-share partials bound fixed commissions and upward fee rounding.
	perShare := limit + (limit*model.FeeBPS+9999)/10000 + model.CommissionPerFillCents
	quantity := min(paperfill.MaxQuantity, in.OpeningCashCents/perShare)
	if quantity < 1 {
		out.PassiveStatus = "opening_cash_below_one_share_ceiling"
		return finish()
	}
	expires := minTime(out.PlacedAt.Add(24*time.Hour), in.CutAt)
	order := paperfill.Order{ID: "passive-benchmark", Symbol: reference.Quote.Symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: quantity, LimitCents: limit, MaxDebitCents: quantity * perShare, PlacedAt: out.PlacedAt, ExpiresAt: expires, Mode: "replay"}
	book, err := paperfill.NewOccurrenceBook(model, []paperfill.Order{order})
	if err != nil {
		return Benchmarks{}, err
	}
	count := 0
	for _, command := range path {
		if command.At.Before(out.PlacedAt) {
			continue
		}
		command.ID = "benchmark-" + workerplan.Digest([]byte(command.Quote.ID))
		book, _, _, err = paperfill.ApplyBook(ctx, book, command)
		if err == paperfill.ErrCapacity {
			out.PassiveStatus = "passive_source_capacity_exceeded"
			return finish()
		}
		if err != nil {
			return Benchmarks{}, err
		}
		count++
	}
	// Use the original cut even when no subsequent quote exists. Missing or
	// stale marks remain unavailable rather than creating a liquidation.
	if count == 0 {
		out.PassiveStatus = "no_post_reference_quote"
	}
	value, err := Evaluate(ctx, Input{OpeningCashCents: in.OpeningCashCents, EntryBook: book, CutAt: in.CutAt})
	if err != nil {
		return Benchmarks{}, err
	}
	out.PassiveBook, out.Passive = &book, &value
	if count > 0 {
		out.PassiveStatus = "observed_price_path_only_total_return_unavailable"
	}
	projection, err := paperfill.ReplayBook(ctx, book)
	if err != nil {
		return Benchmarks{}, err
	}
	out.PassiveFilledQuantity = projection.Orders[0].Projection.FilledQuantity
	if out.PassiveFilledQuantity == 0 {
		out.PassiveStatus = "no_passive_fill_at_source_cut"
	} else if out.PassiveFilledQuantity < quantity {
		out.PassiveStatus = "partial_passive_entry_price_path_only"
	}
	terminal := value.Points[len(value.Points)-1]
	if terminal.EquityCents != nil {
		net := *terminal.EquityCents - in.OpeningCashCents
		out.PassiveObservedNetCents = &net
		if out.StrategyObservedNetCents != nil {
			excess := *out.StrategyObservedNetCents - net
			out.ObservedExcessOverPassiveCents = &excess
		}
	}
	return finish()
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
