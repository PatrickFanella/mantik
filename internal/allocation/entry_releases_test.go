package allocation

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func terminalBookCommand(t *testing.T, b paperfill.Book, id, cancel string, at time.Time) paperfill.Book {
	t.Helper()
	next, _, _, err := paperfill.ApplyBook(context.Background(), b, paperfill.BookCommand{ID: id, At: at, CancelOrderID: cancel})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func TestUnfilledEntryReleaseCancellationExpiryConservationAndRetry(t *testing.T) {
	for _, method := range []string{paperfill.BookMethod, paperfill.OccurrenceBookMethod} {
		t.Run(method, func(t *testing.T) {
			l, b, at := entryFillFixture(t)
			if method == paperfill.OccurrenceBookMethod {
				var err error
				b, err = paperfill.NewOccurrenceBook(b.Model, b.Orders)
				if err != nil {
					t.Fatal(err)
				}
			}
			b = terminalBookCommand(t, b, "cancel-a", "entry-a", at.Add(time.Second))
			b = terminalBookCommand(t, b, "expire-b", "", at.Add(time.Minute))
			lh, bh := workerplan.Hash(l), workerplan.Hash(b)
			next, p, rs, err := ApplyUnfilledEntryReleases(context.Background(), l, b)
			if err != nil || len(rs) != 2 || rs[0].Duplicate || rs[1].Duplicate || p.Snapshot.AvailableCashCents != 100000 || len(p.Snapshot.Orders) != 0 || len(p.Snapshot.Positions) != 0 || p.ExecutionAvailable || p.QualificationCredit {
				t.Fatal(p, rs, err)
			}
			if workerplan.Hash(l) != lh || workerplan.Hash(b) != bh {
				t.Fatal("inputs mutated")
			}
			for _, r := range p.Reservations {
				if r.ReservedCents != 0 || r.ReleasedCents != 10000 || r.FilledCents != 0 {
					t.Fatal(r)
				}
			}
			again, ap, duplicate, err := ApplyUnfilledEntryReleases(context.Background(), next, b)
			if err != nil || !reflect.DeepEqual(again, next) || !reflect.DeepEqual(ap, p) || len(duplicate) != 2 || !duplicate[0].Duplicate || !duplicate[1].Duplicate {
				t.Fatal(duplicate, err)
			}
			// A later book head and unrelated ledger comparison preserve the exact
			// first terminal identities, historical timestamps and duplicate receipts.
			b = terminalBookCommand(t, b, "later-book-clock", "", at.Add(2*time.Minute))
			next, _ = ledgerAppend(t, next, compareCommand("later-ledger", []Proposal{}, []Admission{}), at.Add(3*time.Minute))
			again, _, duplicate, err = ApplyUnfilledEntryReleases(context.Background(), next, b)
			if err != nil || !reflect.DeepEqual(again, next) || !duplicate[0].Duplicate || !duplicate[1].Duplicate {
				t.Fatal(err, duplicate)
			}
			if workerplan.Hash(l) != lh {
				t.Fatal("ledger input mutated")
			}
			// The original book hash was unchanged before explicitly adding the clock.
			if bh == workerplan.Hash(b) {
				t.Fatal("book extension missing")
			}
			again.Entries[0].Plan.Decisions[0].Reasons[0] = "mutated"
			if next.Entries[0].Plan.Decisions[0].Reasons[0] == "mutated" {
				t.Fatal("result aliased")
			}
		})
	}
}
func TestUnfilledEntryReleaseKeepsWorkingAndFilledProtection(t *testing.T) {
	l, b, at := entryFillFixture(t)
	missing := terminalBookCommand(t, b, "no-quote", "", at.Add(time.Second))
	next, p, rs, err := ApplyUnfilledEntryReleases(context.Background(), l, missing)
	if err != nil || len(rs) != 0 || !reflect.DeepEqual(next, l) || p.Snapshot.AvailableCashCents != 80000 {
		t.Fatal(p, rs, err)
	}
	b = entryBookQuote(t, b, "partial", at.Add(time.Second), 1)
	b = terminalBookCommand(t, b, "cancel-partial", "entry-a", at.Add(2*time.Second))
	b = terminalBookCommand(t, b, "expire-rest", "", at.Add(time.Minute))
	// Missing committed entry debits must not be silently inserted by a release.
	if _, _, _, err = ApplyUnfilledEntryReleases(context.Background(), l, b); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
	l, _, _, err = ApplyEntryFillBook(context.Background(), l, b)
	if err != nil {
		t.Fatal(err)
	}
	next, p, rs, err = ApplyUnfilledEntryReleases(context.Background(), l, b)
	if err != nil || len(rs) != 1 || p.Reservations[0].FilledCents != 1003 || p.Reservations[0].ReservedCents != 8997 || p.Reservations[0].ReleasedCents != 0 || p.Reservations[1].ReleasedCents != 10000 || p.Snapshot.AvailableCashCents != 90000 {
		t.Fatal(p, rs, err)
	}
	if len(next.Entries) != 3 {
		t.Fatal(len(next.Entries))
	}
	// Even a fully filled, terminal order retains its residual safety funding.
	l, b, at = entryFillFixture(t)
	b = entryBookQuote(t, b, "full", at.Add(time.Second), 6)
	l, _, _, err = ApplyEntryFillBook(context.Background(), l, b)
	if err != nil {
		t.Fatal(err)
	}
	next, _, rs, err = ApplyUnfilledEntryReleases(context.Background(), l, b)
	if err != nil || len(rs) != 0 || !reflect.DeepEqual(next, l) {
		t.Fatal(rs, err)
	}
}
func TestUnfilledEntryReleaseConflictsAreAtomic(t *testing.T) {
	l, b, at := entryFillFixture(t)
	b = terminalBookCommand(t, b, "expire", "", at.Add(time.Minute))
	for _, kind := range []string{"unknown", "amount-only-release", "late-posting", "no-admission"} {
		t.Run(kind, func(t *testing.T) {
			bad := ledgerDetach(l)
			switch kind {
			case "unknown":
				bad, _ = ledgerAppend(t, bad, effectCommand("unknown-b", "entry-b", "unknown", 0, 0), at.Add(time.Second))
			case "amount-only-release":
				bad, _ = ledgerAppend(t, bad, effectCommand("release-b", "entry-b", "release", 10000, 0), at.Add(time.Second))
			case "late-posting":
				bad, _ = ledgerAppend(t, bad, compareCommand("late", []Proposal{}, []Admission{}), at.Add(2*time.Minute))
			case "no-admission":
				var err error
				bad, err = NewCostLedger(l.Policy)
				if err != nil {
					t.Fatal(err)
				}
			}
			lh, bh := workerplan.Hash(bad), workerplan.Hash(b)
			next, p, rs, err := ApplyUnfilledEntryReleases(context.Background(), bad, b)
			if err == nil || (kind != "late-posting" && !errors.Is(err, ErrLedgerConflict)) || !reflect.DeepEqual(next, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || rs != nil {
				t.Fatal(next, p, rs, err)
			}
			if lh != workerplan.Hash(bad) || bh != workerplan.Hash(b) {
				t.Fatal("failed batch mutated inputs")
			}
		})
	}
	released, _, _, err := ApplyUnfilledEntryReleases(context.Background(), l, b)
	if err != nil {
		t.Fatal(err)
	}
	// An alternate method/root or a valid truncated prefix cannot relabel release.
	alternate, err := paperfill.NewOccurrenceBook(b.Model, b.Orders)
	if err != nil {
		t.Fatal(err)
	}
	alternate = terminalBookCommand(t, alternate, "expire", "", at.Add(time.Minute))
	root, _ := paperfill.RootBook(b)
	for _, bad := range []paperfill.Book{alternate, root} {
		if _, _, _, err = ApplyUnfilledEntryReleases(context.Background(), released, bad); !errors.Is(err, ErrLedgerConflict) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err = ApplyUnfilledEntryReleases(ctx, l, b); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestUnfilledEntryReleaseConcurrentDetachedReplay(t *testing.T) {
	l, b, at := entryFillFixture(t)
	b = terminalBookCommand(t, b, "expire", "", at.Add(time.Minute))
	want, _, _, err := ApplyUnfilledEntryReleases(context.Background(), l, b)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			next, _, _, err := ApplyUnfilledEntryReleases(context.Background(), l, b)
			if err != nil || !reflect.DeepEqual(next, want) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
