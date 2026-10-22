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

func exitFillFixture(t *testing.T) (Ledger, paperfill.Book, paperfill.Book, time.Time) {
	t.Helper()
	l, buys, at := entryFillFixture(t)
	buys = entryBookQuote(t, buys, "entry-fill", at.Add(time.Second), 6)
	l, _, _, err := ApplyEntryFillBook(context.Background(), l, buys)
	if err != nil {
		t.Fatal(err)
	}
	orders := []paperfill.Order{}
	for _, buy := range buys.Orders {
		orders = append(orders, paperfill.Order{ID: buy.ID, Symbol: buy.Symbol, Currency: buy.Currency, Side: "sell", Kind: "stop_market", Quantity: buy.Quantity, StopCents: 950, PlacedAt: at.Add(2 * time.Second), ExpiresAt: at.Add(time.Minute), Mode: buy.Mode})
	}
	sells, err := paperfill.NewBook(buys.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	return l, buys, sells, at
}
func exitBookQuote(t *testing.T, book paperfill.Book, id string, at time.Time, quantity, bid int64) paperfill.Book {
	t.Helper()
	q := paperfill.Quote{ID: id, SourceSHA256: workerplan.Hash(id), Symbol: "ALPHA", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, BidCents: bid, AskCents: bid + 1, BidQuantity: quantity, AskQuantity: quantity}
	next, _, _, err := paperfill.ApplyBook(context.Background(), book, paperfill.BookCommand{ID: id, At: at, Quote: &q})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func TestExitFillBookPartialBasisAndNetProceeds(t *testing.T) {
	l, buys, sells, at := exitFillFixture(t)
	before, source := workerplan.Hash(l), workerplan.Hash(buys)
	sells = exitBookQuote(t, sells, "gap", at.Add(3*time.Second), 2, 900)
	next, p, receipts, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil || len(receipts) != 1 || receipts[0].Duplicate {
		t.Fatal(receipts, err)
	}
	// Two of A's three shares close after a stop gap. Entry cost is 3005;
	// the two-share close gets 2003 cents; the last share keeps the remainder.
	if p.Reservations[0].ClosedCostCents != 2003 || p.Reservations[0].ProceedsCents != 1796 || p.Snapshot.RealizedPnLCents != -207 || p.Reservations[1].ClosedCostCents != 0 || p.ExecutionAvailable || p.QualificationCredit {
		t.Fatal(p)
	}
	again, pp, rs, err := ApplyExitFillBook(context.Background(), next, buys, sells)
	if err != nil || !reflect.DeepEqual(next, again) || !reflect.DeepEqual(p, pp) || len(rs) != 1 || !rs[0].Duplicate {
		t.Fatal(rs, err)
	}
	sells = exitBookQuote(t, sells, "gap-two", at.Add(4*time.Second), 4, 901)
	final, fp, rs, err := ApplyExitFillBook(context.Background(), next, buys, sells)
	if err != nil || len(rs) != 3 || !rs[0].Duplicate || rs[1].Duplicate || rs[2].Duplicate {
		t.Fatal(rs, err)
	}
	var closed, proceeds int64
	for _, r := range fp.Reservations {
		closed += r.ClosedCostCents
		proceeds += r.ProceedsCents
		if r.ClosedCostCents != 3005 || r.FilledCents != 3005 {
			t.Fatal(r)
		}
	}
	if closed != 6010 || len(fp.Snapshot.Positions) != 0 || fp.Snapshot.RealizedPnLCents != proceeds-closed || fp.Snapshot.AvailableCashCents != 80000+proceeds || workerplan.Hash(l) != before || workerplan.Hash(buys) != source {
		t.Fatal(fp)
	}
	if _, err := ReplayLedger(context.Background(), final); err != nil {
		t.Fatal(err)
	}
	// Inputs and detached output never share command/proposal storage.
	again.Entries[0].Plan.Decisions[0].Reasons[0] = "changed"
	if next.Entries[0].Plan.Decisions[0].Reasons[0] == "changed" {
		t.Fatal("aliased result")
	}
}
func TestExitFillBookNoQuoteAndUnfilledStopLimit(t *testing.T) {
	l, buys, sells, at := exitFillFixture(t)
	orders := ledgerDetach(sells.Orders)
	for i := range orders {
		orders[i].Kind = "stop_limit"
		orders[i].LimitCents = 940
	}
	sells, _ = paperfill.NewBook(sells.Model, orders)
	sells = exitBookQuote(t, sells, "gap", at.Add(3*time.Second), 6, 900)
	sells, _, _, _ = paperfill.ApplyBook(context.Background(), sells, paperfill.BookCommand{ID: "missing", At: at.Add(4 * time.Second)})
	n, p, rs, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil || !reflect.DeepEqual(n, l) || len(rs) != 0 || p.Snapshot.RealizedPnLCents != 0 {
		t.Fatal(rs, err)
	}
}
func TestExitFillBookRejectsMissingEntryAndChangedBindings(t *testing.T) {
	l, buys, sells, at := exitFillFixture(t)
	sells = exitBookQuote(t, sells, "gap", at.Add(3*time.Second), 6, 900)
	for name, change := range map[string]func(*paperfill.Book){
		"unknown position": func(b *paperfill.Book) { b.Orders[0].ID = "aaa-unowned" },
		"symbol":           func(b *paperfill.Book) { b.Orders[0].Symbol = "BETA" },
		"mode": func(b *paperfill.Book) {
			for i := range b.Orders {
				b.Orders[i].Mode = "replay"
			}
		},
		"model":           func(b *paperfill.Book) { b.Model.FeeBPS++ },
		"early placement": func(b *paperfill.Book) { b.Orders[0].PlacedAt = at },
		"oversell":        func(b *paperfill.Book) { b.Orders[0].Quantity = 4 },
		"buy": func(b *paperfill.Book) {
			b.Orders[0].Side = "buy"
			b.Orders[0].Kind = "limit"
			b.Orders[0].StopCents = 0
			b.Orders[0].LimitCents = 1000
			b.Orders[0].MaxDebitCents = 5000
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := ledgerDetach(sells)
			change(&bad)
			bad, err := paperfill.NewBook(bad.Model, bad.Orders)
			if err != nil {
				t.Fatal(err)
			}
			n, p, rs, err := ApplyExitFillBook(context.Background(), l, buys, bad)
			if err == nil || !reflect.DeepEqual(n, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || rs != nil {
				t.Fatal("invalid binding accepted", err)
			}
		})
	}
	// An entry fill present in the book but absent from the ledger is not
	// posted as a side effect of asking to close a position.
	missing := ledgerDetach(l)
	missing.Entries = missing.Entries[:1]
	missing.SHA256 = ""
	missing.SHA256 = workerplan.Hash(missing)
	if _, _, _, err := ApplyExitFillBook(context.Background(), missing, buys, sells); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
	working, _ := paperfill.NewBook(buys.Model, buys.Orders)
	working = entryBookQuote(t, working, "partial", at.Add(time.Second), 2)
	base, _, _ := entryFillFixture(t)
	base, _, _, _ = ApplyEntryFillBook(context.Background(), base, working)
	if _, _, _, err := ApplyExitFillBook(context.Background(), base, working, sells); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("working entry accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := ApplyExitFillBook(ctx, l, buys, sells); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestExitFillBookRefusesRelabelledCloseAndTruncatedHistory(t *testing.T) {
	l, buys, sells, at := exitFillFixture(t)
	sells = exitBookQuote(t, sells, "gap", at.Add(3*time.Second), 6, 900)
	next, _, _, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := paperfill.NewBook(sells.Model, sells.Orders)
	if _, _, _, err = ApplyExitFillBook(context.Background(), next, buys, root); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("truncated close accepted", err)
	}
	changed := ledgerDetach(sells.Orders)
	changed[0].StopCents--
	other, _ := paperfill.NewBook(sells.Model, changed)
	other, _, _, _ = paperfill.ApplyBook(context.Background(), other, sells.Entries[0].Command)
	if _, _, _, err = ApplyExitFillBook(context.Background(), next, buys, other); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("changed exit root accepted", err)
	}
	manual, _ := ledgerAppend(t, l, effectCommand("manual-close", "entry-a", "close", 1, 1), at.Add(2*time.Second))
	if _, _, _, err = ApplyExitFillBook(context.Background(), manual, buys, sells); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("amount-only close relabelled", err)
	}
	bad := ledgerDetach(sells)
	bad.Entries[0].Outcomes[0].Outcome.Fill.NetProceedsCents++
	if _, _, _, err = ApplyExitFillBook(context.Background(), l, buys, bad); err == nil {
		t.Fatal("forged proceeds accepted")
	}
}
func TestExitFillBookConcurrentImmutablePreflight(t *testing.T) {
	l, buys, sells, at := exitFillFixture(t)
	sells = exitBookQuote(t, sells, "gap", at.Add(3*time.Second), 6, 900)
	before := workerplan.Hash(struct {
		L    Ledger
		B, S paperfill.Book
	}{l, buys, sells})
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			n, _, _, err := ApplyExitFillBook(context.Background(), l, buys, sells)
			if err != nil {
				t.Error(err)
				return
			}
			results <- n.SHA256
		})
	}
	wg.Wait()
	close(results)
	want := ""
	for h := range results {
		if want != "" && h != want {
			t.Fatal("nondeterministic close")
		}
		want = h
	}
	if workerplan.Hash(struct {
		L    Ledger
		B, S paperfill.Book
	}{l, buys, sells}) != before {
		t.Fatal("inputs mutated")
	}
}

func TestExitFillBookNegativeProceedsAndAtomicFundingFailure(t *testing.T) {
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
	_, _, originalExits, _ := exitFillFixture(t)
	sells, _ := paperfill.NewBook(model, originalExits.Orders)
	sells = exitBookQuote(t, sells, "fee-gap", at.Add(3*time.Second), 6, 1)
	next, p, rs, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if err != nil || len(rs) != 2 || p.Reservations[0].ProceedsCents != -998 || p.Reservations[1].ProceedsCents != -998 || p.Snapshot.AvailableCashCents != 78004 || p.Snapshot.RealizedPnLCents != -10002 {
		t.Fatal(p, rs, err)
	}
	// Serialization/reconstruction retains both the signed proceeds and the
	// source-bound commands, so a reconnect-style retry cannot duplicate them.
	restored := ledgerDetach(next)
	if again, _, _, err := ApplyExitFillBook(context.Background(), restored, buys, sells); err != nil || !reflect.DeepEqual(again, next) {
		t.Fatal(err)
	}
	// A separate pre-existing loss leaves enough free cash for A's fee debit
	// but not B's. Held reservations must not fund the second close.
	_, snapshot, _ := fixture(t)
	extra := proposal("other-position", "baseline-v1", "ALPHA", 1000, at)
	l, _ = ledgerAppend(t, l, compareCommand("other-reserve", []Proposal{extra}, snapshot.Admissions), at.Add(2*time.Second))
	l, _ = ledgerAppend(t, l, effectCommand("other-fill", extra.ID, "fill", 1000, 0), at.Add(2*time.Second))
	l, _ = ledgerAppend(t, l, effectCommand("other-loss", extra.ID, "close", 1000, -77500), at.Add(2*time.Second))
	before := workerplan.Hash(l)
	n, projection, receipts, err := ApplyExitFillBook(context.Background(), l, buys, sells)
	if !errors.Is(err, ErrLedgerConflict) || !reflect.DeepEqual(n, Ledger{}) || !reflect.DeepEqual(projection, LedgerProjection{}) || receipts != nil || workerplan.Hash(l) != before {
		t.Fatal("partial close escaped failed batch", err)
	}
}
