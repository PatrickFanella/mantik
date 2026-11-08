package allocation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// All admissions, orders and market inputs here are manufactured internal-core
// fixtures. They establish no service ownership or qualification.
func closedReleaseFixture(t *testing.T, method string) (Ledger, paperfill.Book, paperfill.Book, time.Time) {
	t.Helper()
	l, buys, at := entryFillFixture(t)
	makeBook := paperfill.NewBook
	if method == paperfill.OccurrenceBookMethod {
		makeBook = paperfill.NewOccurrenceBook
	}
	var err error
	buys, err = makeBook(buys.Model, buys.Orders)
	if err != nil {
		t.Fatal(err)
	}
	buys = entryBookQuote(t, buys, "entry-fill", at.Add(time.Second), 6)
	l, _, _, err = ApplyEntryFillBook(context.Background(), l, buys)
	if err != nil {
		t.Fatal(err)
	}
	orders := []paperfill.Order{}
	for _, buy := range buys.Orders {
		orders = append(orders, paperfill.Order{ID: buy.ID, Symbol: buy.Symbol, Currency: buy.Currency, Side: "sell", Kind: "stop_market", Quantity: buy.Quantity, StopCents: 950, PlacedAt: at.Add(2 * time.Second), ExpiresAt: at.Add(time.Minute), Mode: buy.Mode})
	}
	sells, err := makeBook(buys.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	return l, buys, sells, at
}
func TestClosedPositionReleaseConservationAndStableRetries(t *testing.T) {
	for _, method := range []string{paperfill.BookMethod, paperfill.OccurrenceBookMethod} {
		t.Run(method, func(t *testing.T) {
			ctx := context.Background()
			l, buys, sells, at := closedReleaseFixture(t, method)
			sells = exitBookQuote(t, sells, "close-a", at.Add(3*time.Second), 3, 900)
			l, _, _, err := ApplyExitFillBook(ctx, l, buys, sells)
			if err != nil {
				t.Fatal(err)
			}
			next, p, rs, err := ApplyClosedPositionReleases(ctx, l, buys, sells)
			if err != nil || len(rs) != 1 || rs[0].Duplicate || p.Reservations[0].ReservedCents != 0 || p.Reservations[0].ReleasedCents != 6995 || p.Reservations[1].ReservedCents != 6995 || p.Reservations[1].ClosedCostCents != 0 || len(p.Snapshot.Positions) != 1 {
				t.Fatal(p, rs, err)
			}
			sells = exitBookQuote(t, sells, "close-b", at.Add(4*time.Second), 3, 900)
			next, _, _, err = ApplyExitFillBook(ctx, next, buys, sells)
			if err != nil {
				t.Fatal(err)
			}
			before := workerplan.Hash(struct {
				L    Ledger
				B, S paperfill.Book
			}{next, buys, sells})
			final, fp, rs, err := ApplyClosedPositionReleases(ctx, next, buys, sells)
			if err != nil || len(rs) != 2 || !rs[0].Duplicate || rs[1].Duplicate || fp.Snapshot.AvailableCashCents != 99380 || fp.Snapshot.RealizedPnLCents != -620 || len(fp.Snapshot.Orders) != 0 || len(fp.Snapshot.Positions) != 0 || fp.ExecutionAvailable || fp.QualificationCredit {
				t.Fatal(fp, rs, err)
			}
			for _, r := range fp.Reservations {
				if r.ReservedCents != 0 || r.FilledCents != 3005 || r.ClosedCostCents != 3005 || r.ReleasedCents != 6995 {
					t.Fatal(r)
				}
			}
			if workerplan.Hash(struct {
				L    Ledger
				B, S paperfill.Book
			}{next, buys, sells}) != before {
				t.Fatal("inputs mutated")
			}
			final, _ = ledgerAppend(t, final, compareCommand("later-ledger", []Proposal{}, []Admission{}), at.Add(5*time.Second))
			sells = terminalBookCommand(t, sells, "later-exit-clock", "", at.Add(6*time.Second))
			again, _, retry, err := ApplyClosedPositionReleases(ctx, ledgerDetach(final), buys, sells)
			if err != nil || !reflect.DeepEqual(again, final) || len(retry) != 2 || !retry[0].Duplicate || !retry[1].Duplicate {
				t.Fatal(retry, err)
			}
			again.Entries[0].Plan.Decisions[0].Reasons[0] = "changed"
			if final.Entries[0].Plan.Decisions[0].Reasons[0] == "changed" {
				t.Fatal("aliased result")
			}
		})
	}
}
func TestClosedPositionReleaseKeepsUnresolvedAndPartialProtection(t *testing.T) {
	ctx := context.Background()
	l, buys, sells, at := closedReleaseFixture(t, paperfill.BookMethod)
	for _, b := range []paperfill.Book{sells, exitBookQuote(t, sells, "partial", at.Add(3*time.Second), 2, 900)} {
		posted, _, _, err := ApplyExitFillBook(ctx, l, buys, b)
		if err != nil {
			t.Fatal(err)
		}
		next, p, rs, err := ApplyClosedPositionReleases(ctx, posted, buys, b)
		if err != nil || !reflect.DeepEqual(next, posted) || len(rs) != 0 || p.Reservations[0].ReservedCents != 6995 || p.Reservations[1].ReservedCents != 6995 {
			t.Fatal(p, rs, err)
		}
	}
	sells = terminalBookCommand(t, sells, "expired-no-quote", "", at.Add(time.Minute))
	next, _, rs, err := ApplyClosedPositionReleases(ctx, l, buys, sells)
	if err != nil || len(rs) != 0 || !reflect.DeepEqual(next, l) {
		t.Fatal(rs, err)
	}
	// Terminal partial entry: release the unused amount only after its one
	// acquired share closes. A separate zero-fill release remains independent.
	l, buys, at = entryFillFixture(t)
	buys = entryBookQuote(t, buys, "one-entry", at.Add(time.Second), 1)
	buys = terminalBookCommand(t, buys, "cancel-a", "entry-a", at.Add(2*time.Second))
	buys = terminalBookCommand(t, buys, "expire-b", "", at.Add(time.Minute))
	l, _, _, err = ApplyEntryFillBook(ctx, l, buys)
	if err != nil {
		t.Fatal(err)
	}
	l, _, _, err = ApplyUnfilledEntryReleases(ctx, l, buys)
	if err != nil {
		t.Fatal(err)
	}
	sell := paperfill.Order{ID: "entry-a", Symbol: "ALPHA", Currency: "USD", Side: "sell", Kind: "stop_market", Quantity: 1, StopCents: 950, PlacedAt: at.Add(61 * time.Second), ExpiresAt: at.Add(2 * time.Minute), Mode: "fixture"}
	sells, err = paperfill.NewBook(buys.Model, []paperfill.Order{sell})
	if err != nil {
		t.Fatal(err)
	}
	sells = exitBookQuote(t, sells, "one-exit", at.Add(62*time.Second), 1, 900)
	l, _, _, err = ApplyExitFillBook(ctx, l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	next, p, rs, err := ApplyClosedPositionReleases(ctx, l, buys, sells)
	if err != nil || len(rs) != 1 || p.Reservations[0].ReleasedCents != 8997 || p.Reservations[1].ReleasedCents != 10000 || len(p.Snapshot.Orders) != 0 || len(p.Snapshot.Positions) != 0 || p.Snapshot.AvailableCashCents != 99894 {
		t.Fatal(p, rs, err)
	}
	again, _, _, err := ApplyClosedPositionReleases(ctx, next, buys, sells)
	if err != nil || !reflect.DeepEqual(again, next) {
		t.Fatal(err)
	}
}
func TestClosedPositionReleaseRejectsMissingOrChangedBindingsAtomically(t *testing.T) {
	ctx := context.Background()
	l, buys, sells, at := closedReleaseFixture(t, paperfill.BookMethod)
	sells = exitBookQuote(t, sells, "close", at.Add(3*time.Second), 6, 900)
	posted, _, _, err := ApplyExitFillBook(ctx, l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(Ledger) Ledger{
		"missing-close": func(_ Ledger) Ledger { return l },
		"unknown": func(l Ledger) Ledger {
			next, _ := ledgerAppend(t, l, effectCommand("unknown-b", "entry-b", "unknown", 0, 0), at.Add(3*time.Second))
			return next
		},
		"amount-only-release": func(l Ledger) Ledger {
			next, _ := ledgerAppend(t, l, effectCommand("manual-release", "entry-b", "release", 1, 0), at.Add(3*time.Second))
			return next
		},
		"backdated": func(l Ledger) Ledger {
			next, _ := ledgerAppend(t, l, compareCommand("late", []Proposal{}, []Admission{}), at.Add(time.Minute))
			return next
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := change(posted)
			before := workerplan.Hash(bad)
			next, p, rs, err := ApplyClosedPositionReleases(ctx, bad, buys, sells)
			if err == nil || !reflect.DeepEqual(next, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || rs != nil || workerplan.Hash(bad) != before {
				t.Fatal(next, p, rs, err)
			}
		})
	}
	released, _, _, err := ApplyClosedPositionReleases(ctx, posted, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	truncated, _ := paperfill.RootBook(sells)
	changed := ledgerDetach(sells.Orders)
	changed[0].StopCents--
	alternate, _ := paperfill.NewBook(sells.Model, changed)
	alternate, _, _, err = paperfill.ApplyBook(ctx, alternate, sells.Entries[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []paperfill.Book{truncated, alternate} {
		if _, _, _, err = ApplyClosedPositionReleases(ctx, released, buys, bad); !errors.Is(err, ErrLedgerConflict) {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err = ApplyClosedPositionReleases(cancelled, posted, buys, sells); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestClosedPositionReleaseConcurrentDetachedReplay(t *testing.T) {
	l, buys, sells, at := closedReleaseFixture(t, paperfill.BookMethod)
	sells = exitBookQuote(t, sells, "close", at.Add(3*time.Second), 6, 900)
	l, _, _, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	want, _, _, err := ApplyClosedPositionReleases(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			next, _, _, err := ApplyClosedPositionReleases(context.Background(), l, buys, sells)
			if err != nil || !reflect.DeepEqual(next, want) {
				t.Error("concurrent release", err)
			}
		})
	}
	wg.Wait()
}

func TestClosedPositionReleaseCapacityFailureAfterFirstStageIsAtomic(t *testing.T) {
	l, buys, sells, at := closedReleaseFixture(t, paperfill.BookMethod)
	sells = exitBookQuote(t, sells, "close", at.Add(3*time.Second), 6, 900)
	l, _, _, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	l, _ = ledgerAppend(t, l, compareCommand("padding-first", []Proposal{}, []Admission{}), at.Add(3*time.Second))
	template := ledgerDetach(l.Entries[len(l.Entries)-1])
	// Manufacture a valid near-capacity prefix without a thousand append/replay
	// loops. Empty comparisons at this same clock retain the identical plan.
	for len(l.Entries) < MaxLedgerEntries-1 {
		e := ledgerDetach(template)
		e.Command.ID = fmt.Sprintf("padding-%d", len(l.Entries))
		e.PreviousSHA256 = l.Entries[len(l.Entries)-1].SHA256
		e.SHA256 = ""
		e.SHA256 = workerplan.Hash(e)
		l.Entries = append(l.Entries, e)
	}
	l.SHA256 = ""
	l.SHA256 = workerplan.Hash(l)
	if _, err = ReplayLedger(context.Background(), l); err != nil {
		t.Fatal("invalid capacity fixture", err)
	}
	before := workerplan.Hash(l)
	next, p, rs, err := ApplyClosedPositionReleases(context.Background(), l, buys, sells)
	if !errors.Is(err, ErrLedgerCapacity) || !reflect.DeepEqual(next, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || rs != nil || workerplan.Hash(l) != before {
		t.Fatal("first release escaped failed second stage", err)
	}
}
func TestClosedPositionReleaseAfterFundedNegativeExitCosts(t *testing.T) {
	base, root, at := entryFillFixture(t)
	model := root.Model
	model.CommissionPerFillCents = 1000
	command := ledgerDetach(base.Policy.Command)
	command.ExecutionModel = &model
	policy, err := NewPolicyRecord(command, base.Policy.Account, at)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := NewCostLedger(policy)
	l, _ = ledgerAppend(t, l, base.Entries[0].Command, at)
	buys, _ := paperfill.NewBook(model, root.Orders)
	buys = entryBookQuote(t, buys, "entry", at.Add(time.Second), 6)
	l, _, _, err = ApplyEntryFillBook(context.Background(), l, buys)
	if err != nil {
		t.Fatal(err)
	}
	_, _, exits, _ := exitFillFixture(t)
	sells, _ := paperfill.NewBook(model, exits.Orders)
	sells = exitBookQuote(t, sells, "fee-gap", at.Add(3*time.Second), 6, 1)
	l, _, _, err = ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	_, p, rs, err := ApplyClosedPositionReleases(context.Background(), l, buys, sells)
	if err != nil || len(rs) != 2 || p.Snapshot.AvailableCashCents != 89998 || p.Snapshot.RealizedPnLCents != -10002 || len(p.Snapshot.Orders) != 0 || len(p.Snapshot.Positions) != 0 {
		t.Fatal(p, rs, err)
	}
}

func TestClosedPositionReleaseNoUnusedFundingCreatesNoEffect(t *testing.T) {
	base, root, at := entryFillFixture(t)
	command := ledgerDetach(base.Entries[0].Command)
	for i := range command.Comparison.Proposals {
		command.Comparison.Proposals[i].NotionalCents = 3005
	}
	l, _ := NewCostLedger(base.Policy)
	l, _ = ledgerAppend(t, l, command, at)
	orders := ledgerDetach(root.Orders)
	for i := range orders {
		orders[i].MaxDebitCents = 3005
	}
	buys, err := paperfill.NewBook(root.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	buys = entryBookQuote(t, buys, "entry", at.Add(time.Second), 6)
	l, _, _, err = ApplyEntryFillBook(context.Background(), l, buys)
	if err != nil {
		t.Fatal(err)
	}
	_, _, exits, _ := exitFillFixture(t)
	sells, _ := paperfill.NewBook(root.Model, exits.Orders)
	sells = exitBookQuote(t, sells, "exit", at.Add(3*time.Second), 6, 900)
	l, _, _, err = ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	next, p, rs, err := ApplyClosedPositionReleases(context.Background(), l, buys, sells)
	if err != nil || !reflect.DeepEqual(next, l) || len(rs) != 0 || p.Snapshot.AvailableCashCents != 99380 || len(p.Snapshot.Orders) != 0 || len(p.Snapshot.Positions) != 0 {
		t.Fatal(p, rs, err)
	}
}

// An exit that expires with shares left is followed by a replacement for the
// rest. The two books together close each position at its whole cost basis
// and release its unused funding once. A replacement placed while the first
// exit still works, one that sells more than remains, or a book applied
// without the closes that came before it, are all refused.
func TestReplacementExitBooksCloseTheRemainder(t *testing.T) {
	ctx := context.Background()
	l, buys, first, at := closedReleaseFixture(t, paperfill.BookMethod)
	first = exitBookQuote(t, first, "partial", at.Add(3*time.Second), 2, 900)
	first = terminalBookCommand(t, first, "first-expiry", "", at.Add(time.Minute))
	single, _, _, err := ApplyExitFillBook(ctx, l, buys, first)
	if err != nil {
		t.Fatal(err)
	}
	if seq, _, _, err := ApplyExitFillBooks(ctx, l, buys, []paperfill.Book{first}); err != nil || !reflect.DeepEqual(seq, single) {
		t.Fatal("one-book sequence differs", err)
	}
	firstProjection, err := paperfill.ReplayBook(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	replacement := func(placed time.Time, extra int64) paperfill.Book {
		orders := []paperfill.Order{}
		for i, o := range first.Orders {
			left := o.Quantity - firstProjection.Orders[i].Projection.FilledQuantity
			if left > 0 {
				orders = append(orders, paperfill.Order{ID: o.ID, Symbol: o.Symbol, Currency: o.Currency, Side: "sell", Kind: "market", Quantity: left + extra, PlacedAt: placed, ExpiresAt: placed.Add(time.Hour), Mode: o.Mode})
			}
		}
		b, err := paperfill.NewBook(first.Model, orders)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	second := exitBookQuote(t, replacement(at.Add(61*time.Second), 0), "rest", at.Add(62*time.Second), 6, 900)
	closed, _, _, err := ApplyExitFillBooks(ctx, single, buys, []paperfill.Book{first, second})
	if err != nil {
		t.Fatal(err)
	}
	final, p, rs, err := ApplyClosedPositionReleaseBooks(ctx, closed, buys, []paperfill.Book{first, second})
	if err != nil || len(rs) != 2 || len(p.Snapshot.Positions) != 0 {
		t.Fatal(p, rs, err)
	}
	for _, r := range p.Reservations {
		if r.ReservedCents != 0 || r.ClosedCostCents != r.FilledCents || r.FilledCents+r.ReleasedCents != r.Proposal.NotionalCents {
			t.Fatal(r)
		}
	}
	// Retries are stable.
	if again, _, retry, err := ApplyClosedPositionReleaseBooks(ctx, final, buys, []paperfill.Book{first, second}); err != nil || !reflect.DeepEqual(again, final) || len(retry) != 2 || !retry[0].Duplicate || !retry[1].Duplicate {
		t.Fatal("retry", retry, err)
	}
	// Placed before the first exit ended.
	early := exitBookQuote(t, replacement(at.Add(30*time.Second), 0), "early", at.Add(31*time.Second), 6, 900)
	if _, _, _, err := ApplyExitFillBooks(ctx, single, buys, []paperfill.Book{first, early}); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("replacement before the first exit ended", err)
	}
	// Sells more than the first exit left.
	if _, _, _, err := ApplyExitFillBooks(ctx, single, buys, []paperfill.Book{first, replacement(at.Add(61*time.Second), 1)}); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("oversized replacement", err)
	}
	// The replacement alone does not account for the first book's closes.
	if _, _, _, err := ApplyExitFillBook(ctx, closed, buys, second); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("replacement without its predecessor", err)
	}
}
