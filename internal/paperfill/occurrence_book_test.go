package paperfill

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestOccurrenceBookCorrectionsCannotReplenishEitherSide(t *testing.T) {
	for _, method := range []string{BookMethod, OccurrenceBookMethod} {
		t.Run(method, func(t *testing.T) {
			b, err := newBook(method, model(), bookOrders())
			if err != nil {
				t.Fatal(err)
			}
			q := quote("first", 2)
			q.BidQuantity, q.AskQuantity = 4, 4
			b, _, _ = applyBook(t, b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
			correction := q
			correction.ID = "correction"
			correction.SourceSHA256 = workerplan.Hash("correction")
			correction.ReceivedAt = correction.ReceivedAt.Add(time.Second)
			correction.BidCents--
			correction.AskCents++
			correction.BidQuantity, correction.AskQuantity = 20, 20
			b, p, r := applyBook(t, b, BookCommand{ID: "correction", At: correction.ReceivedAt, Quote: &correction})
			if method == BookMethod {
				if p.Orders[0].Projection.FilledQuantity != 10 || p.Orders[2].Projection.FilledQuantity != 10 || len(p.Liquidity) != 2 {
					t.Fatal("legacy semantics changed", p)
				}
				raw, _ := json.Marshal(p)
				var wire map[string]any
				_ = json.Unmarshal(raw, &wire)
				for _, l := range wire["liquidity"].([]any) {
					if _, ok := l.(map[string]any)["capacity"]; ok {
						t.Fatal("legacy wire changed")
					}
				}
				return
			}
			for _, out := range r.Outcomes {
				if out.Outcome.Fill != nil {
					t.Fatal("correction replenished liquidity", r)
				}
			}
			if len(p.Liquidity) != 1 || p.Liquidity[0].Capacity.Ask != 4 || p.Liquidity[0].Capacity.Bid != 4 || p.Orders[0].Projection.FilledQuantity != 4 || p.Orders[2].Projection.FilledQuantity != 4 {
				t.Fatal(p)
			}
			next := quote("next", 4)
			b, p, _ = applyBook(t, b, BookCommand{ID: "next", At: next.ReceivedAt, Quote: &next})
			if p.Orders[0].Projection.FilledQuantity != 10 || p.Orders[1].Projection.FilledQuantity != 4 || p.Orders[2].Projection.FilledQuantity != 10 || p.ExecutionAttested || p.QualificationCredit {
				t.Fatal(p)
			}
			retry, pp, rr, err := ApplyBook(context.Background(), b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
			if err != nil || !rr.Duplicate || !reflect.DeepEqual(retry, b) || !reflect.DeepEqual(pp, p) {
				t.Fatal(rr, err)
			}
			raw, _ := json.Marshal(b)
			var restored Book
			_ = json.Unmarshal(raw, &restored)
			rp, err := ReplayBook(context.Background(), restored)
			if err != nil || !reflect.DeepEqual(rp, p) {
				t.Fatal("restore", err)
			}
		})
	}
}

func TestOccurrenceBookShrinksCapacityBeforeAnyFill(t *testing.T) {
	m := model()
	m.LiquidityBPS = 5000
	o := order("buy", "limit")
	o.LimitCents = 9900
	b, err := NewOccurrenceBook(m, []Order{o})
	if err != nil {
		t.Fatal(err)
	}
	q := quote("first", 2)
	q.AskQuantity = 10
	b, _, _ = applyBook(t, b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
	q.ID = "smaller"
	q.ReceivedAt = q.ReceivedAt.Add(time.Second)
	q.BidCents = 9700
	q.AskCents = 9800
	q.AskQuantity = 4
	b, p, _ := applyBook(t, b, BookCommand{ID: "smaller", At: q.ReceivedAt, Quote: &q})
	if p.Orders[0].Projection.FilledQuantity != 2 || p.Liquidity[0].Capacity.Ask != 2 {
		t.Fatal(p)
	}
	q.ID = "larger"
	q.ReceivedAt = q.ReceivedAt.Add(time.Second)
	q.AskQuantity = 100
	b, p, r := applyBook(t, b, BookCommand{ID: "larger", At: q.ReceivedAt, Quote: &q})
	if p.Orders[0].Projection.FilledQuantity != 2 || r.Outcomes[0].Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	q.ID = "zero"
	q.AskQuantity = 0
	q.ReceivedAt = q.ReceivedAt.Add(time.Second)
	b, p, r = applyBook(t, b, BookCommand{ID: "zero", At: q.ReceivedAt, Quote: &q})
	if p.Liquidity[0].Capacity.Ask != 0 || r.Outcomes[0].Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	// Changed method without replaying its hash chain cannot relabel the book.
	bad := detach(b)
	bad.Method = BookMethod
	bad.SHA256 = ""
	bad.SHA256 = workerplan.Hash(bad)
	if _, err = ReplayBook(context.Background(), bad); err == nil {
		t.Fatal("method relabel accepted")
	}
	before := workerplan.Hash(b)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := ReplayBook(context.Background(), b); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if workerplan.Hash(b) != before {
		t.Fatal("replay mutated input")
	}
}

func TestOccurrenceBookFutureAndQuarantinedQuotesDoNotSetCapacity(t *testing.T) {
	b, err := NewOccurrenceBook(model(), bookOrders())
	if err != nil {
		t.Fatal(err)
	}
	q := quote("future", 5)
	b, p, _ := applyBook(t, b, BookCommand{ID: "future", At: start.Add(2 * time.Second), Quote: &q})
	if len(p.Liquidity) != 0 {
		t.Fatal("future quote set capacity", p)
	}
	q = quote("quarantined", 3)
	q.Quality = "quarantined"
	q.BidCents, q.AskCents, q.BidQuantity, q.AskQuantity = 0, 0, 0, 0
	_, p, _ = applyBook(t, b, BookCommand{ID: "quarantined", At: q.ReceivedAt, Quote: &q})
	if len(p.Liquidity) != 0 {
		t.Fatal("quarantine set capacity", p)
	}
	if _, err = RootBook(Book{Method: "unknown", Model: model(), Orders: bookOrders()}); err == nil {
		t.Fatal("unknown root accepted")
	}
}
