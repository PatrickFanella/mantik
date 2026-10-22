package paperfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func bookOrders() []Order {
	a, b, c := order("buy", "market"), order("buy", "market"), order("sell", "market")
	a.ID, b.ID, c.ID = "a-buy", "b-buy", "c-sell"
	return []Order{a, b, c}
}
func mustBook(t *testing.T, m Model, orders []Order) Book {
	t.Helper()
	b, err := NewBook(m, orders)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func applyBook(t *testing.T, b Book, c BookCommand) (Book, BookProjection, BookReceipt) {
	t.Helper()
	updated, p, r, err := ApplyBook(context.Background(), b, c)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ReplayBook(context.Background(), updated)
	if err != nil || !reflect.DeepEqual(replayed, p) {
		t.Fatal("replay", err, p, replayed)
	}
	return updated, p, r
}
func TestBookSharedSideLiquidityAndUnmodifiedQuote(t *testing.T) {
	m := model()
	m.LiquidityBPS = 5000
	m.FeeBPS = 10
	m.CommissionPerFillCents = 1
	b := mustBook(t, m, bookOrders())
	original := detach(b)
	q := quote("first", 2)
	b, p, r := applyBook(t, b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
	if !reflect.DeepEqual(original.Entries, []BookEntry{}) || p.Orders[0].Projection.FilledQuantity != 5 || p.Orders[1].Projection.FilledQuantity != 0 || p.Orders[2].Projection.FilledQuantity != 5 || p.Liquidity[0].AskUsed != 5 || p.Liquidity[0].BidUsed != 5 {
		t.Fatal(p)
	}
	if !reflect.DeepEqual(*b.Entries[0].Command.Quote, q) || r.Outcomes[0].Outcome.Fill.MarketUpdateSHA256 != marketUpdate(q) || r.Outcomes[0].Outcome.Fill.QuoteSHA256 != q.SourceSHA256 {
		t.Fatal("quote changed", r)
	}
	// Capture aliases (including another evidence label) cannot replenish liquidity.
	alias := q
	alias.ID = "alias"
	alias.SourceSHA256 = strings.Repeat("b", 64)
	alias.ReceivedAt = alias.ReceivedAt.Add(time.Second)
	alias.EvidenceClass = "prospective_market_unqualified"
	b, p, r = applyBook(t, b, BookCommand{ID: "alias", At: alias.ReceivedAt, Quote: &alias})
	for _, out := range r.Outcomes {
		if out.Outcome.Fill != nil {
			t.Fatal("alias replenished", r)
		}
	}
	if len(p.Liquidity) != 1 || p.Orders[1].Projection.FilledQuantity != 0 {
		t.Fatal(p)
	}
	next := quote("next", 4)
	b, p, _ = applyBook(t, b, BookCommand{ID: "next", At: next.ReceivedAt, Quote: &next})
	if p.Orders[0].Projection.Status != "filled" || p.Orders[1].Projection.FilledQuantity != 0 || p.Orders[2].Projection.Status != "filled" {
		t.Fatal(p)
	}
	next = quote("third", 5)
	_, p, _ = applyBook(t, b, BookCommand{ID: "third", At: next.ReceivedAt, Quote: &next})
	if p.Orders[1].Projection.FilledQuantity != 5 || p.ExecutionAttested || p.QualificationCredit || p.Authority != "owner_supplied_shared_paper_fill_method_only" {
		t.Fatal(p)
	}
}
func TestBookIneligibleAndBudgetLimitedOrdersDoNotSpendOtherLiquidity(t *testing.T) {
	orders := bookOrders()
	orders[0].Kind = "limit"
	orders[0].LimitCents = 9999
	orders[1].MaxDebitCents = 20003
	m := model()
	m.CommissionPerFillCents = 3
	b := mustBook(t, m, orders)
	q := quote("first", 2)
	_, p, r := applyBook(t, b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
	if p.Orders[0].Projection.FilledQuantity != 0 || p.Orders[1].Projection.FilledQuantity != 2 || p.Liquidity[0].AskUsed != 2 || r.Outcomes[1].Outcome.Fill.DebitCents != 20003 {
		t.Fatal(p, r)
	}
	// An untriggered stop has no claim on displayed liquidity.
	orders = bookOrders()
	orders[0].Kind = "stop_limit"
	orders[0].StopCents = 11000
	orders[0].LimitCents = 11000
	b = mustBook(t, model(), orders)
	_, p, _ = applyBook(t, b, BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q})
	if p.Orders[0].Projection.Triggered || p.Orders[1].Projection.FilledQuantity != 10 {
		t.Fatal(p)
	}
}
func TestBookSingleOrderUsesLegacyArithmetic(t *testing.T) {
	for _, mode := range []string{"fixture", "replay", "ongoing_paper"} {
		for _, side := range []string{"buy", "sell"} {
			for _, kind := range []string{"market", "limit", "stop_market", "stop_limit"} {
				t.Run(mode+side+kind, func(t *testing.T) {
					m := model()
					m.SlippageBPS = 5
					m.FeeBPS = 20
					m.CommissionPerFillCents = 3
					o := order(side, kind)
					o.Mode = mode
					j := mustNew(t, m, o)
					b := mustBook(t, m, []Order{o})
					for i := 1; i <= 4; i++ {
						q := quote(fmt.Sprintf("quote-%d", i), i+1)
						q.AskQuantity = 2
						q.BidQuantity = 2
						q.BidCents = 9400
						q.AskCents = 9410
						c := command(fmt.Sprintf("step-%d", i), q)
						var legacy Projection
						var legacyReceipt Receipt
						j, legacy, legacyReceipt = apply(t, j, c)
						var p BookProjection
						var r BookReceipt
						b, p, r = applyBook(t, b, BookCommand{ID: c.ID, At: c.At, Quote: c.Quote})
						if !reflect.DeepEqual(legacy, p.Orders[0].Projection) || len(r.Outcomes) != 1 || !reflect.DeepEqual(legacyReceipt.Outcome, r.Outcomes[0].Outcome) {
							t.Fatal("legacy mismatch", legacy, p)
						}
					}
				})
			}
		}
	}
}
func TestBookRetryCancellationCorruptionAndCapacity(t *testing.T) {
	b := mustBook(t, model(), bookOrders())
	q := quote("quote", 2)
	c := BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q}
	b, _, first := applyBook(t, b, c)
	b, p, _ := applyBook(t, b, BookCommand{ID: "cancel-b", At: start.Add(3 * time.Second), CancelOrderID: "b-buy"})
	if p.Orders[1].Projection.Status != "cancelled" || p.Orders[0].Projection.Status != "filled" {
		t.Fatal(p)
	}
	unchanged, latest, retry, err := ApplyBook(context.Background(), b, c)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Outcomes, first.Outcomes) || !reflect.DeepEqual(latest, p) || !reflect.DeepEqual(unchanged, b) {
		t.Fatal(retry, err)
	}
	bad := c
	bad.Quote = detach(c.Quote)
	bad.Quote.AskCents++
	if _, _, _, err = ApplyBook(context.Background(), b, bad); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad.ID = "new-conflict"
	bad.At = start.Add(4 * time.Second)
	if _, _, _, err = ApplyBook(context.Background(), b, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("quote id rebound", err)
	}
	for _, cancel := range []string{"missing", "a-buy", "b-buy"} {
		if _, _, _, err = ApplyBook(context.Background(), b, BookCommand{ID: "invalid-cancel", At: start.Add(4 * time.Second), CancelOrderID: cancel}); !errors.Is(err, ErrConflict) {
			t.Fatal(cancel, err)
		}
	}
	forged := detach(b)
	forged.Entries[0].Outcomes[0].Outcome.Fill.Quantity++
	forged.Entries[0].SHA256 = ""
	forged.Entries[0].SHA256 = workerplan.Hash(forged.Entries[0])
	forged.SHA256 = ""
	forged.SHA256 = workerplan.Hash(forged)
	if _, err = ReplayBook(context.Background(), forged); err == nil {
		t.Fatal("counterfeit fill")
	}
	if _, _, _, err = ApplyBook(context.Background(), forged, c); err == nil {
		t.Fatal("retry hid counterfeit fill")
	}
	raw, _ := json.Marshal(b)
	var restored Book
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("json")
	}
	got, err := ReplayBook(context.Background(), restored)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatal(err)
	}
	for i := len(b.Entries); i < MaxEntries; i++ {
		b, _, _ = applyBook(t, b, BookCommand{ID: fmt.Sprintf("tick-%03d", i), At: start.Add(time.Duration(i+3) * time.Second)})
	}
	if _, _, _, err = ApplyBook(context.Background(), b, BookCommand{ID: "overflow", At: start.Add(time.Hour)}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, _, r, err := ApplyBook(context.Background(), b, c); err != nil || !r.Duplicate {
		t.Fatal(err)
	}
}
func TestBookValidationAndConcurrency(t *testing.T) {
	orders := bookOrders()
	for _, invalid := range [][]Order{nil, {}, {orders[1], orders[0]}, {orders[0], orders[0]}} {
		if _, err := NewBook(model(), invalid); err == nil {
			t.Fatal("invalid order list")
		}
	}
	mixed := detach(orders)
	mixed[1].Mode = "replay"
	if _, err := NewBook(model(), mixed); err == nil {
		t.Fatal("mixed modes")
	}
	b := mustBook(t, model(), orders)
	before := detach(b)
	q := quote("quote", 2)
	c := BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := ApplyBook(canceled, b, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			updated, _, r, err := ApplyBook(context.Background(), b, c)
			if err != nil || len(updated.Entries) != 1 || r.Outcomes[0].Outcome.Fill.Quantity != 10 {
				t.Error(err)
			}
			if len(updated.Orders) > 0 {
				updated.Orders[0].ID = "detached"
			}
		})
	}
	wg.Wait()
	if !reflect.DeepEqual(b, before) || !reflect.DeepEqual(q, *c.Quote) {
		t.Fatal("input mutated")
	}
}

func TestBookSeededConservation(t *testing.T) {
	random := rand.New(rand.NewPCG(73, 91))
	for range 100 {
		m := model()
		m.LiquidityBPS = 1 + random.Int64N(10000)
		m.FeeBPS = random.Int64N(1000)
		m.CommissionPerFillCents = random.Int64N(100)
		orders := bookOrders()
		for i := range orders {
			orders[i].Quantity = 1 + random.Int64N(100)
			if orders[i].Side == "buy" {
				orders[i].MaxDebitCents = 1 + random.Int64N(1000000)
			}
		}
		q := quote("sample", 2)
		q.BidQuantity = random.Int64N(100)
		q.AskQuantity = random.Int64N(100)
		b := mustBook(t, m, orders)
		_, p, r := applyBook(t, b, BookCommand{ID: "sample", At: q.ReceivedAt, Quote: &q})
		bid, ask := int64(0), int64(0)
		for i, out := range r.Outcomes {
			state := p.Orders[i].Projection
			if state.FilledQuantity+state.RemainingQuantity != orders[i].Quantity || state.RemainingQuantity < 0 {
				t.Fatal("quantity conservation", state)
			}
			if fill := out.Outcome.Fill; fill != nil {
				if fill.GrossCents != fill.PriceCents*fill.Quantity || fill.FeeCents != ceilDiv(fill.GrossCents*m.FeeBPS, 10000)+m.CommissionPerFillCents {
					t.Fatal("cost conservation", fill)
				}
				if fill.Side == "buy" {
					ask += fill.Quantity
					if fill.DebitCents != fill.GrossCents+fill.FeeCents || state.DebitCents > orders[i].MaxDebitCents {
						t.Fatal("debit", fill, state)
					}
				} else {
					bid += fill.Quantity
					if fill.NetProceedsCents != fill.GrossCents-fill.FeeCents {
						t.Fatal("proceeds", fill)
					}
				}
			}
		}
		if bid > q.BidQuantity*m.LiquidityBPS/10000 || ask > q.AskQuantity*m.LiquidityBPS/10000 {
			t.Fatal("shared liquidity conservation", bid, ask)
		}
	}
}
func TestBookUnusableTimingAndSymbolIsolation(t *testing.T) {
	orders := bookOrders()
	orders[1].Symbol = "QQQ"
	orders[0].Quantity = 20
	b := mustBook(t, model(), orders)
	q := quote("too-early", 0)
	b, p, r := applyBook(t, b, BookCommand{ID: "too-early", At: start.Add(time.Second), Quote: &q})
	if len(p.Liquidity) != 0 || len(r.Outcomes) != 2 || r.Outcomes[0].Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	q = quote("usable", 2)
	b, p, _ = applyBook(t, b, BookCommand{ID: "usable", At: q.ReceivedAt, Quote: &q})
	if p.Orders[0].Projection.FilledQuantity != 10 || p.Orders[1].Projection.FilledQuantity != 0 {
		t.Fatal(p)
	}
	missing := Quote{ID: "missing", SourceSHA256: strings.Repeat("c", 64), Symbol: "SPY", Currency: "USD", Quality: "missing", EvidenceClass: "synthetic_unqualified", ReceivedAt: start.Add(4 * time.Second)}
	b, _, r = applyBook(t, b, BookCommand{ID: "missing", At: missing.ReceivedAt, Quote: &missing})
	if r.Outcomes[0].Outcome.Code != "quote_missing" {
		t.Fatal(r)
	}
	old := quote("older-receipt", 3)
	b, p, r = applyBook(t, b, BookCommand{ID: "older-receipt", At: start.Add(5 * time.Second), Quote: &old})
	if r.Outcomes[0].Outcome.Code != "superseded_quote" || p.Orders[0].Projection.FilledQuantity != 10 {
		t.Fatal(r, p)
	}
	q = quote("qqq", 6)
	q.Symbol = "QQQ"
	_, p, _ = applyBook(t, b, BookCommand{ID: "qqq", At: q.ReceivedAt, Quote: &q})
	if p.Orders[1].Projection.FilledQuantity != 10 || p.Orders[0].Projection.FilledQuantity != 10 {
		t.Fatal(p)
	}
}
