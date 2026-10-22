package paperfill

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var start = time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)

func model() Model {
	return Model{Method: Method, LiquidityBPS: 10000, LatencyMillis: 1000, MaxQuoteAgeSeconds: 60}
}
func order(side, kind string) Order {
	o := Order{ID: "order", Symbol: "SPY", Currency: "USD", Side: side, Kind: kind, Quantity: 10, PlacedAt: start, ExpiresAt: start.Add(time.Hour), Mode: "fixture"}
	if side == "buy" {
		o.MaxDebitCents = 1000000
	}
	if kind == "limit" || kind == "stop_limit" {
		o.LimitCents = 10000
	}
	if kind == "stop_market" || kind == "stop_limit" {
		o.StopCents = 9500
	}
	return o
}
func quote(id string, seconds int) Quote {
	return Quote{ID: id, SourceSHA256: strings.Repeat("a", 64), Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: start.Add(time.Duration(seconds) * time.Second), ReceivedAt: start.Add(time.Duration(seconds) * time.Second), BidCents: 9990, AskCents: 10000, BidQuantity: 10, AskQuantity: 10}
}
func command(id string, q Quote) Command { return Command{ID: id, At: q.ReceivedAt, Quote: &q} }
func mustNew(t *testing.T, m Model, o Order) Journal {
	t.Helper()
	j, err := New(m, o)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func apply(t *testing.T, j Journal, c Command) (Journal, Projection, Receipt) {
	t.Helper()
	next, p, r, err := Apply(context.Background(), j, c)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(context.Background(), next)
	if err != nil || !reflect.DeepEqual(p, replayed) {
		t.Fatal("replay diverged", err, p, replayed)
	}
	return next, p, r
}
func TestSpreadFeesSlippageAndModeContract(t *testing.T) {
	m := model()
	m.SlippageBPS = 25
	m.FeeBPS = 15
	m.CommissionPerFillCents = 7
	for _, tc := range []struct {
		side                          string
		price, gross, fee, debit, net int64
	}{{"buy", 10025, 20050, 38, 20088, 0}, {"sell", 9965, 19930, 37, 0, 19893}} {
		var previous *Fill
		for _, mode := range []string{"fixture", "replay", "ongoing_paper"} {
			o := order(tc.side, "market")
			o.Quantity = 2
			o.Mode = mode
			j := mustNew(t, m, o)
			_, p, r := apply(t, j, command("fill", quote("q", 2)))
			f := r.Outcome.Fill
			if f == nil || f.PriceCents != tc.price || f.GrossCents != tc.gross || f.FeeCents != tc.fee || f.DebitCents != tc.debit || f.NetProceedsCents != tc.net || p.Status != "filled" || p.ExecutionAttested || p.QualificationCredit || f.Method != Method || f.ModelSHA256 != workerplan.Hash(m) || f.Mode != mode || f.EvidenceClass != "synthetic_unqualified" {
				t.Fatal(p, r)
			}
			if previous != nil && (previous.GrossCents != f.GrossCents || previous.FeeCents != f.FeeCents) {
				t.Fatal("mode changed fill mechanics")
			}
			previous = f
		}
	}
}
func TestPartialsConsumeMarketUpdateOnceAcrossCaptureAliases(t *testing.T) {
	j := mustNew(t, model(), order("buy", "market"))
	q := quote("q1", 2)
	q.AskQuantity = 4
	j, p, r := apply(t, j, command("one", q))
	if p.FilledQuantity != 4 || p.RemainingQuantity != 6 || r.Outcome.Fill.Quantity != 4 {
		t.Fatal(p, r)
	}
	alias := q
	alias.ID = "poll-again"
	alias.SourceSHA256 = strings.Repeat("b", 64)
	alias.ReceivedAt = alias.ReceivedAt.Add(time.Second)
	j, p, r = apply(t, j, command("alias", alias))
	if r.Outcome.Code != "market_update_already_consumed" || r.Outcome.Fill != nil || p.FilledQuantity != 4 {
		t.Fatal(p, r)
	}
	next := quote("q2", 4)
	next.AskQuantity = 3
	j, p, _ = apply(t, j, command("two", next))
	if p.FilledQuantity != 7 {
		t.Fatal(p)
	}
	final := quote("q3", 5)
	j, p, _ = apply(t, j, command("three", final))
	if p.Status != "filled" || p.RemainingQuantity != 0 || p.FilledQuantity != 10 || p.DebitCents != 100000 {
		t.Fatal(p)
	}
	// Exact earlier retries return their historical outcome and current projection.
	restored := Journal{}
	raw, _ := json.Marshal(j)
	if workerplan.ReadBoundedShape(raw, &restored, MaxJournalBytes) != nil {
		t.Fatal("restore decode")
	}
	again, projection, receipt, err := Apply(context.Background(), restored, command("one", q))
	if err != nil || !receipt.Duplicate || receipt.Outcome.Fill.Quantity != 4 || !reflect.DeepEqual(projection, p) || !reflect.DeepEqual(again, j) {
		t.Fatal(receipt, err)
	}
}
func TestMissingStaleFutureLatencyAndSupersession(t *testing.T) {
	o := order("buy", "market")
	j := mustNew(t, model(), o)
	early := quote("early", 0)
	early.OccurredAt = start.Add(500 * time.Millisecond)
	early.ReceivedAt = early.OccurredAt
	j, _, r := apply(t, j, command("early", early))
	if r.Outcome.Code != "latency_pending" || r.Outcome.Fill != nil {
		t.Fatal(r)
	}
	c := command("pre-eligible", early)
	c.At = start.Add(2 * time.Second)
	j, _, r = apply(t, j, c)
	if r.Outcome.Code != "quote_before_eligible" {
		t.Fatal(r)
	}
	j, _, r = apply(t, j, Command{ID: "missing", At: start.Add(3 * time.Second)})
	if r.Outcome.Code != "missing_quote" || r.Outcome.Fill != nil {
		t.Fatal(r)
	}
	missing := quote("missing-state", 4)
	missing.Quality = "missing"
	missing.BidCents = 0
	missing.AskCents = 0
	missing.BidQuantity = 0
	missing.AskQuantity = 0
	j, _, r = apply(t, j, command("missing-state", missing))
	if r.Outcome.Code != "quote_missing" {
		t.Fatal(r)
	}
	older := command("older-good", quote("old", 2))
	older.At = start.Add(5 * time.Second)
	j, _, r = apply(t, j, older)
	if r.Outcome.Code != "superseded_quote" || r.Outcome.Fill != nil {
		t.Fatal(r)
	}
	future := command("future", quote("future", 10))
	future.At = start.Add(6 * time.Second)
	j, _, r = apply(t, j, future)
	if r.Outcome.Code != "future_quote" {
		t.Fatal(r)
	}
	stale := command("stale", quote("stale", 7))
	stale.At = start.Add(68 * time.Second)
	j, p, r := apply(t, j, stale)
	if r.Outcome.Code != "stale_quote" || p.FilledQuantity != 0 || p.DebitCents != 0 || r.Outcome.Fill != nil {
		t.Fatal(p, r)
	}
}
func TestStopsGapsLimitsAndStickyTrigger(t *testing.T) {
	m := model()
	m.SlippageBPS = 100
	o := order("sell", "stop_market")
	j := mustNew(t, m, o)
	gap := quote("gap", 2)
	gap.BidCents = 9000
	gap.AskCents = 9010
	_, _, r := apply(t, j, command("gap", gap))
	if !r.Outcome.Triggered || r.Outcome.Fill == nil || r.Outcome.Fill.PriceCents != 8910 {
		t.Fatal(r)
	}
	o = order("sell", "stop_limit")
	o.LimitCents = 9400
	j = mustNew(t, model(), o)
	j, p, r := apply(t, j, command("trigger", gap))
	if !p.Triggered || r.Outcome.Code != "limit_not_marketable" || r.Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	recovery := quote("recovery", 3)
	recovery.BidCents = 9450
	recovery.AskCents = 9460
	recovery.BidQuantity = 4
	j, p, r = apply(t, j, command("partial", recovery))
	if !p.Triggered || p.FilledQuantity != 4 || r.Outcome.Fill.PriceCents != 9450 {
		t.Fatal(p, r)
	}
	above := quote("above-stop", 4)
	_, p, r = apply(t, j, command("rest", above))
	if !p.Triggered || p.Status != "filled" || r.Outcome.Fill.PriceCents != 9990 {
		t.Fatal(p, r)
	}
	buy := order("buy", "stop_market")
	buy.StopCents = 10100
	j = mustNew(t, model(), buy)
	j, _, r = apply(t, j, command("not-triggered", quote("low", 2)))
	if r.Outcome.Code != "stop_not_triggered" {
		t.Fatal(r)
	}
	high := quote("high", 3)
	high.AskCents = 10200
	_, _, r = apply(t, j, command("triggered", high))
	if r.Outcome.Fill.PriceCents != 10200 {
		t.Fatal(r)
	}
	for _, side := range []string{"buy", "sell"} {
		o = order(side, "limit")
		if side == "sell" {
			o.LimitCents = 9990
		}
		j = mustNew(t, m, o)
		_, _, r = apply(t, j, command("adverse", quote("q", 2)))
		if r.Outcome.Code != "limit_not_marketable" || r.Outcome.Fill != nil {
			t.Fatal(side, r)
		}
	}
}
func TestBudgetLiquidityExpiryCancelAndSignedFees(t *testing.T) {
	m := model()
	m.LiquidityBPS = 5000
	o := order("buy", "market")
	o.MaxDebitCents = 25000
	j := mustNew(t, m, o)
	q := quote("q", 2)
	q.AskQuantity = 10
	j, p, r := apply(t, j, command("partial", q))
	if r.Outcome.Fill.Quantity != 2 || p.DebitCents != 20000 {
		t.Fatal(p, r)
	}
	next := quote("next", 3)
	next.AskCents = 5000
	next.BidCents = 4990
	j, p, r = apply(t, j, command("budget", next))
	if r.Outcome.Fill.Quantity != 1 || p.DebitCents != 25000 {
		t.Fatal(p, r)
	}
	last := quote("last", 4)
	j, p, r = apply(t, j, command("exhausted", last))
	if r.Outcome.Code != "debit_budget_exhausted" || r.Outcome.Fill != nil || p.FilledQuantity != 3 {
		t.Fatal(p, r)
	}
	j, p, r = apply(t, j, Command{ID: "cancel", At: start.Add(5 * time.Second), Cancel: true})
	if p.Status != "cancelled" || p.RemainingQuantity != 7 || p.DebitCents != 25000 || r.Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	if _, _, _, err := Apply(context.Background(), j, Command{ID: "late", At: start.Add(6 * time.Second)}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	o = order("buy", "market")
	j = mustNew(t, model(), o)
	_, p, r = apply(t, j, Command{ID: "expire", At: o.ExpiresAt})
	if p.Status != "expired" || p.FilledQuantity != 0 || r.Outcome.Fill != nil {
		t.Fatal(p, r)
	}
	o = order("sell", "market")
	o.Quantity = 1
	m = model()
	m.CommissionPerFillCents = 100
	q = quote("penny", 2)
	q.BidCents = 1
	q.AskCents = 1
	_, p, r = apply(t, mustNew(t, m, o), command("fee", q))
	if r.Outcome.Fill.NetProceedsCents != -99 || p.NetProceedsCents != p.GrossCents-p.FeeCents {
		t.Fatal(p, r)
	}
	m.SlippageBPS = 1
	_, p, r = apply(t, mustNew(t, m, o), command("subcent", q))
	if r.Outcome.Code != "model_price_out_of_range" || p.FilledQuantity != 0 {
		t.Fatal(p, r)
	}
}
func TestInvalidConflictCorruptionCapacityAndCancellation(t *testing.T) {
	for _, change := range []func(*Model){func(m *Model) { m.Method = "unknown" }, func(m *Model) { m.SlippageBPS = 1001 }, func(m *Model) { m.FeeBPS = -1 }, func(m *Model) { m.LiquidityBPS = 0 }, func(m *Model) { m.LatencyMillis = 60001 }, func(m *Model) { m.MaxQuoteAgeSeconds = 0 }} {
		m := model()
		change(&m)
		if _, err := New(m, order("buy", "market")); err == nil {
			t.Fatal("invalid model accepted")
		}
	}
	j := mustNew(t, model(), order("buy", "market"))
	q := quote("q", 2)
	q.AskQuantity = 1
	original := detach(j)
	j, _, _ = apply(t, j, command("one", q))
	if !reflect.DeepEqual(original.Entries, []Entry{}) {
		t.Fatal("input mutated")
	}
	bad := command("one", q)
	bad.At = bad.At.Add(time.Second)
	if _, _, _, err := Apply(context.Background(), j, bad); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad = command("new", q)
	bad.Quote.AskQuantity = 2
	bad.At = bad.At.Add(time.Second)
	if _, _, _, err := Apply(context.Background(), j, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("quote identity rebound", err)
	}
	corrupted := detach(j)
	corrupted.Entries[0].Outcome.Fill.PriceCents++
	corrupted.Entries[0].Outcome.Fill.SHA256 = ""
	corrupted.Entries[0].Outcome.Fill.SHA256 = workerplan.Hash(*corrupted.Entries[0].Outcome.Fill)
	corrupted.Entries[0].SHA256 = ""
	corrupted.Entries[0].SHA256 = workerplan.Hash(corrupted.Entries[0])
	corrupted.SHA256 = ""
	corrupted.SHA256 = workerplan.Hash(corrupted)
	if _, err := Replay(context.Background(), corrupted); err == nil {
		t.Fatal("self-hashed false fill accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Replay(ctx, j); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Capacity never evicts historical attempts; an exact retry remains readable.
	j = mustNew(t, model(), order("buy", "market"))
	var first Command
	for i := 0; i < MaxEntries; i++ {
		c := Command{ID: strings.Repeat("a", 1) + string(rune('A'+i/26)) + string(rune('a'+i%26)), At: start.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			first = c
		}
		j, _, _ = apply(t, j, c)
	}
	if _, _, _, err := Apply(context.Background(), j, Command{ID: "overflow", At: start.Add(200 * time.Second)}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	_, _, receipt, err := Apply(context.Background(), j, first)
	if err != nil || !receipt.Duplicate {
		t.Fatal(receipt, err)
	}
}
func TestSeededConservationAndDetachedConcurrentCalls(t *testing.T) {
	rng := rand.New(rand.NewPCG(20, 21))
	for n := 0; n < 100; n++ {
		m := model()
		m.SlippageBPS = int64(rng.IntN(101))
		m.FeeBPS = int64(rng.IntN(31))
		m.CommissionPerFillCents = int64(rng.IntN(10))
		m.LiquidityBPS = int64(rng.IntN(10000) + 1)
		side := "buy"
		if n%2 == 1 {
			side = "sell"
		}
		o := order(side, "market")
		o.Quantity = int64(rng.IntN(100) + 1)
		j := mustNew(t, m, o)
		for k := 0; k < 20; k++ {
			q := quote("q"+string(rune('A'+k)), k+2)
			q.AskQuantity = int64(rng.IntN(40))
			q.BidQuantity = q.AskQuantity
			var p Projection
			var r Receipt
			j, p, r = apply(t, j, command("step"+string(rune('A'+k)), q))
			if p.FilledQuantity+p.RemainingQuantity != o.Quantity || p.FeeCents < 0 || side == "buy" && (p.DebitCents != p.GrossCents+p.FeeCents || p.DebitCents > o.MaxDebitCents) || side == "sell" && p.NetProceedsCents != p.GrossCents-p.FeeCents {
				t.Fatal(n, k, p)
			}
			if r.Outcome.Fill != nil && r.Outcome.Fill.Quantity > q.AskQuantity*m.LiquidityBPS/10000 {
				t.Fatal("invented liquidity")
			}
			if p.Status == "filled" {
				break
			}
			_ = j
		}
	}
	j := mustNew(t, model(), order("buy", "market"))
	c := command("same", quote("q", 2))
	want, p, receipt := apply(t, j, c)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			next, got, r, err := Apply(context.Background(), j, c)
			if err != nil || !reflect.DeepEqual(next, want) || !reflect.DeepEqual(got, p) || !reflect.DeepEqual(r, receipt) {
				t.Error("concurrent pure call differed", err)
			}
			if r.Outcome.Fill != nil {
				r.Outcome.Fill.Quantity = 99
			}
			if len(next.Entries) > 0 {
				next.Entries[0].Outcome.Code = "mutated"
			}
		})
	}
	wg.Wait()
	if len(j.Entries) != 0 || c.Quote.AskQuantity != 10 {
		t.Fatal("caller data mutated")
	}
}
