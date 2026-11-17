package allocation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func financialEntryFixture(t *testing.T) (Ledger, paperfill.Book, time.Time) {
	t.Helper()
	old, book, at := entryFillFixture(t)
	l, err := NewFinancialLedger(old.Policy)
	if err != nil {
		t.Fatal(err)
	}
	l, _, _, err = AppendLedger(context.Background(), l, old.Entries[0].Command, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	return l, book, at
}

func TestFinancialLedgerRetainsPartialFillAndCloseEconomics(t *testing.T) {
	ctx := context.Background()
	l, buys, at := financialEntryFixture(t)
	buys = entryBookQuote(t, buys, "first", at.Add(time.Second), 4)
	buys = entryBookQuote(t, buys, "second", at.Add(2*time.Second), 2)
	next, _, receipts, err := ApplyEntryFillBook(ctx, l, buys)
	if err != nil || len(receipts) != 3 {
		t.Fatal(receipts, err)
	}
	wantQuantity := []int64{3, 1, 2}
	wantFees := []int64{5, 3, 4}
	for i, e := range next.Entries[1:] {
		f := e.Command.Effect.Execution
		if f == nil || f.Quantity != wantQuantity[i] || f.GrossCents != 1000*wantQuantity[i] || f.FeeCents != wantFees[i] || f.DebitCents != f.GrossCents+f.FeeCents || !f.At.Equal(e.At) {
			t.Fatal("missing exact partial-fill economics", e)
		}
	}
	orders := []paperfill.Order{}
	for _, buy := range buys.Orders {
		orders = append(orders, paperfill.Order{ID: buy.ID, Symbol: buy.Symbol, Currency: buy.Currency, Side: "sell", Kind: "market", Quantity: buy.Quantity, PlacedAt: at.Add(3 * time.Second), ExpiresAt: at.Add(time.Minute), Mode: buy.Mode})
	}
	sells, err := paperfill.NewBook(buys.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	sells = exitBookQuote(t, sells, "close-partial", at.Add(4*time.Second), 2, 900)
	closed, projection, _, err := ApplyExitFillBook(ctx, next, buys, sells)
	if err != nil {
		t.Fatal(err)
	}
	e := closed.Entries[len(closed.Entries)-1].Command.Effect
	f := e.Execution
	if f == nil || f.Quantity != 2 || f.GrossCents != 1800 || f.FeeCents != 4 || f.NetProceedsCents != 1796 || e.AmountCents != 2003 || projection.Snapshot.RealizedPnLCents != -207 {
		t.Fatal("gross, net proceeds and cost basis must stay distinct", e, projection)
	}
	again, _, rs, err := ApplyExitFillBook(ctx, closed, buys, sells)
	if err != nil || !reflect.DeepEqual(again, closed) || len(rs) != 1 || !rs[0].Duplicate {
		t.Fatal("retry changed economics", rs, err)
	}
	// Derived results must not share the immutable source fill object.
	again.Entries[len(again.Entries)-1].Command.Effect.Execution.FeeCents++
	if closed.Entries[len(closed.Entries)-1].Command.Effect.Execution.FeeCents != 4 {
		t.Fatal("aliased fill")
	}
}

func TestFinancialLedgerRejectsForgedFillArithmeticAndLegacyRelabeling(t *testing.T) {
	l, book, at := financialEntryFixture(t)
	book = entryBookQuote(t, book, "fill", at.Add(time.Second), 2)
	next, _, _, err := ApplyEntryFillBook(context.Background(), l, book)
	if err != nil {
		t.Fatal(err)
	}
	e := *next.Entries[len(next.Entries)-1].Command.Effect
	for name, change := range map[string]func(*Effect){
		"missing":        func(e *Effect) { e.Execution = nil },
		"fee":            func(e *Effect) { e.Execution.FeeCents++ },
		"gross":          func(e *Effect) { e.Execution.GrossCents++ },
		"quantity":       func(e *Effect) { e.Execution.Quantity++ },
		"order":          func(e *Effect) { e.Execution.OrderID = "other-order" },
		"side":           func(e *Effect) { e.Execution.Side = "sell" },
		"effect binding": func(e *Effect) { e.ID = "entry-fill-" + workerplan.Hash("wrong") },
	} {
		t.Run(name, func(t *testing.T) {
			bad := ledgerDetach(e)
			change(&bad)
			if bad.Execution != nil {
				bad.Execution.SHA256 = ""
				bad.Execution.SHA256 = workerplan.Hash(*bad.Execution)
			}
			if bad.ValidateExecution(FinancialLedgerSchema) == nil {
				t.Fatal("self-rehashed forged detail accepted")
			}
		})
	}
	if e.ValidateExecution(CostLedgerSchema) == nil || e.ValidateExecution(LedgerSchema) == nil {
		t.Fatal("old schema accepted added fill detail")
	}
	old, oldBook, oldAt := entryFillFixture(t)
	oldBook = entryBookQuote(t, oldBook, "old-fill", oldAt.Add(time.Second), 2)
	old, _, _, err = ApplyEntryFillBook(context.Background(), old, oldBook)
	if err != nil || old.Entries[len(old.Entries)-1].Command.Effect.Execution != nil {
		t.Fatal("historical schema acquired invented metadata", err)
	}
	// Changing a locally consistent fee cannot rewrite the source-derived fill.
	bad := ledgerDetach(next)
	fill := bad.Entries[len(bad.Entries)-1].Command.Effect.Execution
	fill.ModelSHA256 = workerplan.Hash("replacement-model")
	fill.SHA256 = ""
	fill.SHA256 = workerplan.Hash(*fill)
	bad.Entries[len(bad.Entries)-1].SHA256 = ""
	bad.Entries[len(bad.Entries)-1].SHA256 = workerplan.Hash(bad.Entries[len(bad.Entries)-1])
	bad.SHA256 = ""
	bad.SHA256 = workerplan.Hash(bad)
	if _, _, _, err := ApplyEntryFillBook(context.Background(), bad, book); err == nil {
		t.Fatal("self-consistent replacement model bypassed source replay")
	}
}

func TestFinancialLedgerRetainsFeeGreaterThanSaleGross(t *testing.T) {
	base, admissions, at := ledgerFixture(t)
	ledger, err := NewFinancialLedger(base.Policy)
	if err != nil {
		t.Fatal(err)
	}
	buy := modeledFill(t, "buy", 10000, 3, at)
	sell := modeledFill(t, "sell", 1, 20, at.Add(2*time.Second))
	// These are pure accounting inputs. Owner replay separately verifies the
	// exact order/book/quote source; this core test selects one proposal ID.
	for _, f := range []*paperfill.Fill{&buy, &sell} {
		f.OrderID = "modeled"
		f.SHA256 = ""
		f.SHA256 = workerplan.Hash(*f)
	}
	q := proposal("modeled", "baseline-v1", "ALPHA", buy.DebitCents, at)
	ledger, _ = ledgerAppend(t, ledger, compareCommand("reserve", []Proposal{q}, admissions), at)
	bh, sh := workerplan.Hash("entry-binding"), workerplan.Hash("exit-binding")
	entry := Effect{ID: "entry-fill-" + bh, SHA256: bh, DecisionID: q.ID, Kind: "fill", AmountCents: buy.DebitCents, Execution: &buy}
	close := Effect{ID: "exit-fill-" + sh, SHA256: sh, DecisionID: q.ID, Kind: "close", AmountCents: buy.DebitCents, ProceedsCents: sell.NetProceedsCents, Execution: &sell}
	ledger, _ = ledgerAppend(t, ledger, LedgerCommand{ID: entry.ID, Effect: &entry}, buy.At)
	ledger, projection := ledgerAppend(t, ledger, LedgerCommand{ID: close.ID, Effect: &close}, sell.At)
	if projection.Snapshot.AvailableCashCents != 89978 || projection.Snapshot.RealizedPnLCents != -10022 || ledger.Entries[2].Command.Effect.Execution.GrossCents != 1 || ledger.Entries[2].Command.Effect.Execution.FeeCents != 20 || ledger.Entries[2].Command.Effect.ProceedsCents != -19 {
		t.Fatal("exit fee was clamped or funding arithmetic changed", projection)
	}
}
