package papereconomics

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func study(t *testing.T, closed int64, cutAfter time.Duration) Input {
	t.Helper()
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	m := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 2, CommissionPerFillCents: 5}
	order := paperfill.Order{ID: "entry", Symbol: "SPY", Currency: "USD", Side: "buy", Kind: "limit", Quantity: 2, LimitCents: 100, MaxDebitCents: 205, PlacedAt: at, ExpiresAt: at.Add(time.Minute), Mode: "replay"}
	b, err := paperfill.NewOccurrenceBook(m, []paperfill.Order{order})
	if err != nil {
		t.Fatal(err)
	}
	quote := func(id string, at time.Time, bid, ask int64) paperfill.BookCommand {
		return paperfill.BookCommand{ID: id, At: at, Quote: &paperfill.Quote{ID: id, SourceSHA256: strings.Repeat("a", 64), Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, BidCents: bid, AskCents: ask, BidQuantity: 10, AskQuantity: 10}}
	}
	q1, q2 := quote("first", at.Add(time.Second), 99, 100), quote("second", at.Add(2*time.Second), 110, 111)
	for _, q := range []paperfill.BookCommand{q1, q2} {
		b, _, _, err = paperfill.ApplyBook(context.Background(), b, q)
		if err != nil {
			t.Fatal(err)
		}
	}
	in := Input{OpeningCashCents: 1000, EntryBook: b, CutAt: at.Add(cutAfter)}
	if closed > 0 {
		order.Side, order.Kind, order.Quantity = "sell", "market", closed
		order.LimitCents, order.MaxDebitCents = 0, 0
		order.PlacedAt = at.Add(time.Second + time.Nanosecond)
		exit, err := paperfill.NewOccurrenceBook(m, []paperfill.Order{order})
		if err != nil {
			t.Fatal(err)
		}
		exit, _, _, err = paperfill.ApplyBook(context.Background(), exit, q2)
		if err != nil {
			t.Fatal(err)
		}
		in.ExitBook = &exit
	}
	return in
}

func TestKnownClosedBookEconomics(t *testing.T) {
	in := study(t, 2, 3*time.Second)
	before := workerplan.Hash(in)
	r, err := Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Buy 2*100+5, sell 2*110-5. Initial bid liquidation marks 2*99,
	// retaining the spread and fee drawdown without inventing another sale.
	if r.EndingCashCents != 1010 || r.RealizedNetCents != 10 || r.RemainingCostCents != 0 || r.ClosedNetCents == nil || *r.ClosedNetCents != 10 || r.ObservedMaximumDrawdownCents != 7 || !r.AllEventMarksAvailable || r.TotalReturnAvailable || r.CorporateActionsApplied || r.ContinuousCoverageAvailable || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
	if workerplan.Hash(in) != before || r.InputSHA256 != before {
		t.Fatal("accounting changed original source books")
	}
	first := r.Points[1]
	if first.EquityCents == nil || *first.EquityCents != 993 || first.MarkedInventoryCents == nil || *first.MarkedInventoryCents != 198 {
		t.Fatal(first)
	}
	again, err := Evaluate(context.Background(), in)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("non-reproducible accounting", err)
	}
}

func TestPartialPositionUsesConservedWholeShareBasis(t *testing.T) {
	r, err := Evaluate(context.Background(), study(t, 1, 3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Closed basis floors 205/2 to 102; the remaining share keeps 103.
	if r.EndingCashCents != 900 || r.RemainingCostCents != 103 || r.RealizedNetCents != 3 || r.ClosedNetCents != nil || r.Points[len(r.Points)-1].EquityCents == nil || *r.Points[len(r.Points)-1].EquityCents != 1010 {
		t.Fatal(r)
	}
	stale, err := Evaluate(context.Background(), study(t, 1, 10*time.Second))
	if err != nil || stale.AllEventMarksAvailable || stale.Points[len(stale.Points)-1].EquityCents != nil || stale.ClosedNetCents != nil || stale.RemainingCostCents != 103 {
		t.Fatal("stale quote invented terminal equity or liquidation", stale, err)
	}
}

func TestInvalidBooksAndUnfundedStudyFail(t *testing.T) {
	in := study(t, 2, 3*time.Second)
	in.OpeningCashCents = 200
	if _, err := Evaluate(context.Background(), in); !errors.Is(err, ErrInvalid) {
		t.Fatal("entry funded using later exit proceeds", err)
	}
	in = study(t, 2, 3*time.Second)
	in.EntryBook.Entries[0].Outcomes[0].Outcome.Fill.DebitCents--
	if _, err := Evaluate(context.Background(), in); err == nil {
		t.Fatal("tampered fill accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, study(t, 2, 3*time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPortfolioRejectsFutureReceivedQuoteInEitherBook(t *testing.T) {
	for _, side := range []string{"entry", "exit"} {
		t.Run(side, func(t *testing.T) {
			in := study(t, 1, 3*time.Second)
			book := &in.EntryBook
			if side == "exit" {
				book = in.ExitBook
			}
			root, err := paperfill.RootBook(*book)
			if err != nil {
				t.Fatal(err)
			}
			for i, entry := range book.Entries {
				command := entry.Command
				if i == len(book.Entries)-1 {
					quote := *command.Quote
					quote.ID += "-future"
					quote.ReceivedAt = command.At.Add(time.Nanosecond)
					command.Quote = &quote
				}
				root, _, _, err = paperfill.ApplyBook(t.Context(), root, command)
				if err != nil {
					t.Fatal(err)
				}
			}
			*book = root
			// This is an authentic replayable book, including its rejected quote;
			// merely verifying its journal is insufficient for economic marking.
			if _, err = paperfill.ReplayBook(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			before := workerplan.Hash(in)
			if result, err := Evaluate(t.Context(), in); !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, Result{}) {
				t.Fatal("future-received quote became economic evidence", result, err)
			}
			if result, err := EvaluateBenchmarks(t.Context(), in); !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, Benchmarks{}) {
				t.Fatal("future-received quote entered benchmark comparison", result, err)
			}
			if workerplan.Hash(in) != before {
				t.Fatal("rejection changed retained books")
			}
		})
	}
}
