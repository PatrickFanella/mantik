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

func entryFillFixture(t *testing.T) (Ledger, paperfill.Book, time.Time) {
	t.Helper()
	p, s, at := fixture(t)
	model := paperfill.Model{Method: paperfill.Method, FeeBPS: 10, CommissionPerFillCents: 2, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	policy, err := NewPolicyRecord(PolicyCommand{ReceiverID: s.Account.ReceiverID, AccountID: s.Account.Config.ID, Policy: p, ExecutionModel: &model}, s.Account, at)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewCostLedger(policy)
	if err != nil {
		t.Fatal(err)
	}
	qs := []Proposal{proposal("entry-a", "baseline-v1", "ALPHA", 10000, at), proposal("entry-b", "baseline-v1", "ALPHA", 10000, at)}
	ledger, _ = ledgerAppend(t, ledger, compareCommand("reserve", qs, s.Admissions), at)
	orders := []paperfill.Order{}
	for _, q := range qs {
		orders = append(orders, paperfill.Order{ID: q.ID, Symbol: q.Symbol, Currency: "USD", Side: "buy", Kind: "limit", Quantity: 3, LimitCents: 1100, MaxDebitCents: 5000, PlacedAt: at, ExpiresAt: at.Add(time.Minute), Mode: "fixture"})
	}
	book, err := paperfill.NewBook(model, orders)
	if err != nil {
		t.Fatal(err)
	}
	return ledger, book, at
}
func entryBookQuote(t *testing.T, book paperfill.Book, id string, at time.Time, quantity int64) paperfill.Book {
	t.Helper()
	q := paperfill.Quote{ID: id, SourceSHA256: workerplan.Hash(id), Symbol: "ALPHA", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, BidCents: 999, AskCents: 1000, BidQuantity: quantity, AskQuantity: quantity}
	next, _, _, err := paperfill.ApplyBook(context.Background(), book, paperfill.BookCommand{ID: id, At: at, Quote: &q})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func TestEntryFillBookSharedDebitsConservationAndExactRetries(t *testing.T) {
	l, b, at := entryFillFixture(t)
	original := workerplan.Hash(l)
	b = entryBookQuote(t, b, "first", at.Add(time.Second), 4)
	source := workerplan.Hash(b)
	next, p, receipts, err := ApplyEntryFillBook(context.Background(), l, b)
	if err != nil || len(receipts) != 2 || receipts[0].Duplicate || receipts[1].Duplicate {
		t.Fatal(receipts, err)
	}
	// A has three shares, B only one: the displayed offer was consumed once.
	if p.Reservations[0].FilledCents != 3005 || p.Reservations[1].FilledCents != 1003 || p.Snapshot.AvailableCashCents != 80000 || len(p.Snapshot.Positions) != 2 {
		t.Fatal(p)
	}
	var reserved, filled int64
	for _, r := range p.Reservations {
		reserved += r.ReservedCents
		filled += r.FilledCents
	}
	if reserved+filled+p.Snapshot.AvailableCashCents != 100000 || p.ExecutionAvailable || p.QualificationCredit {
		t.Fatal(p)
	}
	if workerplan.Hash(l) != original || workerplan.Hash(b) != source {
		t.Fatal("inputs mutated")
	}
	again, againP, duplicate, err := ApplyEntryFillBook(context.Background(), next, b)
	if err != nil || !reflect.DeepEqual(again, next) || !reflect.DeepEqual(againP, p) || len(duplicate) != 2 || !duplicate[0].Duplicate || !duplicate[1].Duplicate {
		t.Fatal(duplicate, err)
	}
	// Additional book entries preserve earlier fill identities and only append
	// the newly available two-share partial with its actual cost and fee.
	b = entryBookQuote(t, b, "second", at.Add(2*time.Second), 2)
	final, fp, rs, err := ApplyEntryFillBook(context.Background(), next, b)
	if err != nil || len(rs) != 3 || !rs[0].Duplicate || !rs[1].Duplicate || rs[2].Duplicate || fp.Reservations[1].FilledCents != 3007 {
		t.Fatal(fp, rs, err)
	}
	if len(final.Entries) != 4 {
		t.Fatal("duplicate debit", len(final.Entries))
	}
	// An exact earlier book still replays after a later unrelated comparison.
	final, _ = ledgerAppend(t, final, compareCommand("later", []Proposal{}, []Admission{}), at.Add(3*time.Second))
	retry, _, _, err := ApplyEntryFillBook(context.Background(), final, b)
	if err != nil || !reflect.DeepEqual(retry, final) {
		t.Fatal(err)
	}
	// Detached results cannot modify retained order/decision evidence.
	retry.Entries[0].Plan.Decisions[0].Reasons[0] = "changed"
	if final.Entries[0].Plan.Decisions[0].Reasons[0] == "changed" {
		t.Fatal("aliased result")
	}
}
func TestEntryFillBookUnavailableQuotesAndNoAdmission(t *testing.T) {
	l, b, at := entryFillFixture(t)
	noFill, _, _, err := paperfill.ApplyBook(context.Background(), b, paperfill.BookCommand{ID: "missing", At: at.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	next, _, receipts, err := ApplyEntryFillBook(context.Background(), l, noFill)
	if err != nil || !reflect.DeepEqual(next, l) || len(receipts) != 0 {
		t.Fatal(receipts, err)
	}
	// The fill model cannot create a reservation or turn a deferral into one.
	empty, err := NewCostLedger(l.Policy)
	if err != nil {
		t.Fatal(err)
	}
	ps := []Proposal{proposal("entry-a", "baseline-v1", "ALPHA", 10000, at), proposal("entry-b", "baseline-v1", "ALPHA", 10000, at)}
	empty, _ = ledgerAppend(t, empty, compareCommand("defer", ps, []Admission{}), at)
	b = entryBookQuote(t, b, "fill", at.Add(time.Second), 4)
	if _, _, _, err = ApplyEntryFillBook(context.Background(), empty, b); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("fill manufactured admission", err)
	}
}
func TestEntryFillBookBindingsAndRehashedCorruption(t *testing.T) {
	l, b, at := entryFillFixture(t)
	b = entryBookQuote(t, b, "fill", at.Add(time.Second), 4)
	for name, change := range map[string]func(*paperfill.Book){
		"model":       func(b *paperfill.Book) { b.Model.FeeBPS++ },
		"symbol":      func(b *paperfill.Book) { b.Orders[0].Symbol = "BETA" },
		"order":       func(b *paperfill.Book) { b.Orders[0].ID = "aaa-unreserved" },
		"side":        func(b *paperfill.Book) { b.Orders[0].Side = "sell"; b.Orders[0].MaxDebitCents = 0 },
		"budget":      func(b *paperfill.Book) { b.Orders[0].MaxDebitCents = 10001 },
		"expiry":      func(b *paperfill.Book) { b.Orders[0].ExpiresAt = at.Add(2 * time.Hour) },
		"placement":   func(b *paperfill.Book) { b.Orders[0].PlacedAt = at.Add(-time.Second) },
		"fill amount": func(b *paperfill.Book) { b.Entries[0].Outcomes[0].Outcome.Fill.DebitCents++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := ledgerDetach(b)
			change(&bad)
			// Rebuild from changed source orders for valid but mismatched envelopes;
			// leave forged outcome arithmetic in place for replay corruption.
			if name != "fill amount" {
				root, err := paperfill.NewBook(bad.Model, bad.Orders)
				if err != nil {
					t.Fatal(err)
				}
				bad, _, _, err = paperfill.ApplyBook(context.Background(), root, b.Entries[0].Command)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				f := bad.Entries[0].Outcomes[0].Outcome.Fill
				f.SHA256 = ""
				f.SHA256 = workerplan.Hash(f)
				bad.Entries[0].SHA256 = ""
				bad.Entries[0].SHA256 = workerplan.Hash(bad.Entries[0])
				bad.SHA256 = ""
				bad.SHA256 = workerplan.Hash(bad)
			}
			before := workerplan.Hash(l)
			n, p, r, err := ApplyEntryFillBook(context.Background(), l, bad)
			if err == nil || !reflect.DeepEqual(n, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || r != nil || workerplan.Hash(l) != before {
				t.Fatal("mismatched source changed ledger", err)
			}
		})
	}
	next, _, _, err := ApplyEntryFillBook(context.Background(), l, b)
	if err != nil {
		t.Fatal(err)
	}
	// A revised immutable book root cannot relabel existing effects, and a
	// truncated prefix cannot hide a debit it previously committed.
	changed := ledgerDetach(b)
	changed.Orders[0].LimitCents = 1101
	root, _ := paperfill.NewBook(changed.Model, changed.Orders)
	changed, _, _, err = paperfill.ApplyBook(context.Background(), root, b.Entries[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []paperfill.Book{root, changed} {
		if _, _, _, err = ApplyEntryFillBook(context.Background(), next, bad); !errors.Is(err, ErrLedgerConflict) {
			t.Fatal("changed root or truncated history accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err = ApplyEntryFillBook(ctx, next, b); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Amount-only legacy effects have no book binding and must not be relabeled.
	legacy, _ := ledgerAppend(t, l, effectCommand("manual", "entry-a", "fill", 1, 0), at)
	if _, _, _, err = ApplyEntryFillBook(context.Background(), legacy, b); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
}
func TestEntryFillBookFailedLaterDebitHasNoPartialResult(t *testing.T) {
	l, b, at := entryFillFixture(t)
	b = entryBookQuote(t, b, "fill", at.Add(time.Second), 4)
	// B's full hold is released; A could settle but B cannot. Neither input is
	// changed and no partial A debit escapes the combined preflight.
	l, _ = ledgerAppend(t, l, effectCommand("release-b", "entry-b", "release", 10000, 0), at)
	before := workerplan.Hash(l)
	n, p, r, err := ApplyEntryFillBook(context.Background(), l, b)
	if !errors.Is(err, ErrLedgerConflict) || !reflect.DeepEqual(n, Ledger{}) || !reflect.DeepEqual(p, LedgerProjection{}) || r != nil || workerplan.Hash(l) != before {
		t.Fatal(n, p, r, err)
	}
}

func TestEntryFillBookUnknownAndConcurrentImmutablePreflight(t *testing.T) {
	l, b, at := entryFillFixture(t)
	b = entryBookQuote(t, b, "fill", at.Add(time.Second), 4)
	unknown, _ := ledgerAppend(t, l, effectCommand("unknown-b", "entry-b", "unknown", 0, 0), at)
	if _, _, _, err := ApplyEntryFillBook(context.Background(), unknown, b); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("unknown remainder settled", err)
	}
	// A model-free policy cannot opt into a fill model by supplying a book.
	legacyPolicy, err := NewPolicyRecord(PolicyCommand{ReceiverID: l.Policy.Command.ReceiverID, AccountID: l.Policy.Command.AccountID, Policy: l.Policy.Command.Policy}, l.Policy.Account, at)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := NewLedger(legacyPolicy)
	if _, _, _, err = ApplyEntryFillBook(context.Background(), legacy, b); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal("unbound model accepted", err)
	}
	before, source := workerplan.Hash(l), workerplan.Hash(b)
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			n, _, _, err := ApplyEntryFillBook(context.Background(), l, b)
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
			t.Fatal("nondeterministic preflight")
		}
		want = h
	}
	if workerplan.Hash(l) != before || workerplan.Hash(b) != source {
		t.Fatal("concurrent preflight mutated input")
	}
}
