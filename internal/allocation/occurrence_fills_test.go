package allocation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestOccurrenceBookFinancialBindingsRetainMethod(t *testing.T) {
	l, old, at := entryFillFixture(t)
	buys, err := paperfill.NewOccurrenceBook(old.Model, old.Orders)
	if err != nil {
		t.Fatal(err)
	}
	buys = entryBookQuote(t, buys, "buy", at.Add(time.Second), 6)
	next, _, _, err := ApplyEntryFillBook(context.Background(), l, buys)
	if err != nil {
		t.Fatal(err)
	}
	root, err := paperfill.RootBook(buys)
	if err != nil || root.Method != paperfill.OccurrenceBookMethod {
		t.Fatal(root, err)
	}
	fill := buys.Entries[0].Outcomes[0].Outcome.Fill
	binding := workerplan.Hash(struct{ Method, BookRootSHA256, FillSHA256 string }{EntryFillBindingMethod, root.SHA256, fill.SHA256})
	if next.Entries[1].Command.Effect.SHA256 != binding {
		t.Fatal("entry lost method binding")
	}
	orders := ledgerDetach(buys.Orders)
	for i := range orders {
		orders[i].Side = "sell"
		orders[i].Kind = "market"
		orders[i].LimitCents = 0
		orders[i].MaxDebitCents = 0
		orders[i].PlacedAt = at.Add(2 * time.Second)
	}
	sells, err := paperfill.NewOccurrenceBook(buys.Model, orders)
	if err != nil {
		t.Fatal(err)
	}
	sells = exitBookQuote(t, sells, "sell", at.Add(3*time.Second), 6, 900)
	closed, p, _, err := ApplyExitFillBook(context.Background(), next, buys, sells)
	if err != nil || len(p.Snapshot.Positions) != 0 || p.ExecutionAvailable || p.QualificationCredit {
		t.Fatal(p, err)
	}
	exitRoot, _ := paperfill.RootBook(sells)
	f := sells.Entries[0].Outcomes[0].Outcome.Fill
	want := workerplan.Hash(struct{ Method, EntryBookSHA256, ExitBookRootSHA256, FillSHA256 string }{ExitFillBindingMethod, buys.SHA256, exitRoot.SHA256, f.SHA256})
	if closed.Entries[3].Command.Effect.SHA256 != want {
		t.Fatal("exit lost method binding")
	}
	again, _, _, err := ApplyExitFillBook(context.Background(), closed, buys, sells)
	if err != nil || !reflect.DeepEqual(again, closed) {
		t.Fatal("retry", err)
	}
}
