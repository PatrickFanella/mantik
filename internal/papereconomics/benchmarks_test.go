package papereconomics

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func benchmarkStudy(t *testing.T, liquidity int64, cut time.Duration) Input {
	t.Helper()
	in := study(t, 2, cut)
	commands := []paperfill.BookCommand{}
	for _, entry := range in.EntryBook.Entries {
		q := entry.Command
		if len(commands) == 1 {
			copy := *q.Quote
			copy.BidCents = 98
			copy.AskCents = 99
			copy.AskQuantity = liquidity
			q.Quote = &copy
		}
		commands = append(commands, q)
	}
	entry, err := paperfill.RootBook(in.EntryBook)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range commands {
		entry, _, _, err = paperfill.ApplyBook(t.Context(), entry, q)
		if err != nil {
			t.Fatal(err)
		}
	}
	exit, err := paperfill.RootBook(*in.ExitBook)
	if err != nil {
		t.Fatal(err)
	}
	exit, _, _, err = paperfill.ApplyBook(t.Context(), exit, commands[1])
	if err != nil {
		t.Fatal(err)
	}
	in.EntryBook, in.ExitBook = entry, &exit
	return in
}

func TestObservedBenchmarksConserveFiniteCashAndDoNotFillReference(t *testing.T) {
	in := benchmarkStudy(t, 10, 3*time.Second)
	before := workerplan.Hash(in)
	b, err := EvaluateBenchmarks(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	// The first quote prices a nine-share order, but cannot fill it. The later
	// quote buys 9*99+5, leaving104 cash and 9*98 observed bid inventory.
	if b.CashEndingCents != 1000 || b.CashNetCents != 0 || b.PassiveFilledQuantity != 9 || b.Passive == nil || b.Passive.EndingCashCents != 104 || b.Passive.RemainingCostCents != 896 || b.PassiveObservedNetCents == nil || *b.PassiveObservedNetCents != -14 || b.StrategyObservedNetCents == nil || *b.StrategyObservedNetCents != -14 || b.ObservedExcessOverPassiveCents == nil || *b.ObservedExcessOverPassiveCents != 0 {
		t.Fatal(b)
	}
	if b.PassiveBook.Orders[0].PlacedAt != in.EntryBook.Entries[0].Command.At.Add(time.Nanosecond) || b.PassiveBook.Entries[0].Command.Quote.ID != "second" || b.Passive.ClosedNetCents != nil || b.TotalReturnComparisonAvailable || b.CorporateCoverageReconciled || b.AdmissionAvailable || b.ExecutionAvailable || b.QualificationCredit {
		t.Fatal("reference filled, terminal sale invented or authority granted", b)
	}
	if workerplan.Hash(in) != before {
		t.Fatal("benchmark changed actual source books")
	}
	again, err := EvaluateBenchmarks(t.Context(), in)
	if err != nil || !reflect.DeepEqual(b, again) {
		t.Fatal("benchmark not reproducible", err)
	}
}

func TestObservedBenchmarksPreservePartialUnfilledAndMissingMarks(t *testing.T) {
	b, err := EvaluateBenchmarks(t.Context(), benchmarkStudy(t, 4, 3*time.Second))
	if err != nil || b.PassiveFilledQuantity != 4 || b.PassiveStatus != "partial_passive_entry_price_path_only" || b.Passive.EndingCashCents != 599 || b.PassiveObservedNetCents == nil || *b.PassiveObservedNetCents != -9 {
		t.Fatal(b, err)
	}
	b, err = EvaluateBenchmarks(t.Context(), benchmarkStudy(t, 4, 10*time.Second))
	if err != nil || b.PassiveObservedNetCents != nil || b.ObservedExcessOverPassiveCents != nil || b.Passive.ClosedNetCents != nil {
		t.Fatal("stale mark or sale invented", b, err)
	}
	b, err = EvaluateBenchmarks(t.Context(), study(t, 2, 3*time.Second))
	if err != nil || b.PassiveFilledQuantity != 0 || b.PassiveStatus != "no_passive_fill_at_source_cut" || b.Passive.EndingCashCents != 1000 {
		t.Fatal("future rising quote changed fixed limit", b, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := EvaluateBenchmarks(ctx, benchmarkStudy(t, 10, 3*time.Second)); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestObservedBenchmarksDoNotBorrowCashOrInventAnotherQuote(t *testing.T) {
	in := study(t, 0, 3*time.Second)
	root, err := paperfill.RootBook(in.EntryBook)
	if err != nil {
		t.Fatal(err)
	}
	first := in.EntryBook.Entries[0].Command
	root, _, _, err = paperfill.ApplyBook(t.Context(), root, first)
	if err != nil {
		t.Fatal(err)
	}
	in.EntryBook = root
	in.CutAt = first.At
	b, err := EvaluateBenchmarks(t.Context(), in)
	if err != nil || b.Passive != nil || b.PassiveStatus != "no_post_reference_window" {
		t.Fatal("same observation supplied a retroactive fill", b, err)
	}
	orders := append([]paperfill.Order{}, root.Orders...)
	orders[0].LimitCents = 1
	root, err = paperfill.NewOccurrenceBook(root.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	root, _, _, err = paperfill.ApplyBook(t.Context(), root, first)
	if err != nil {
		t.Fatal(err)
	}
	in.EntryBook = root
	in.OpeningCashCents = 1
	in.CutAt = first.At.Add(time.Second)
	b, err = EvaluateBenchmarks(t.Context(), in)
	if err != nil || b.Passive != nil || b.PassiveStatus != "opening_cash_below_one_share_ceiling" || b.CashEndingCents != 1 {
		t.Fatal("borrowed cash for passive baseline", b, err)
	}
}
