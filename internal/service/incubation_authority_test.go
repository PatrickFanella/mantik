package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

func review90Adapter(t *testing.T, beforeFills func(), posts *int, orderGets *int) *alpaca.PaperTrader {
	t.Helper()
	orders := map[string]alpaca.PaperOrder{}
	old := http.DefaultTransport
	http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
		raw := ""
		switch h.URL.Path {
		case "/v2/account/activities/FILL":
			if beforeFills != nil {
				beforeFills()
			}
			raw = `[]`
		case "/v2/account":
			raw = `{"id":"paper-fixture","status":"ACTIVE"}`
		case "/v2/assets/SPY":
			raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
		case "/v2/orders":
			if h.Method != "POST" {
				t.Fatal(h.Method)
			}
			*posts++
			var sent alpaca.StockOrder
			if e := json.NewDecoder(h.Body).Decode(&sent); e != nil {
				t.Fatal(e)
			}
			v := alpaca.PaperOrder{ID: "venue-" + sent.ClientOrderID, ClientOrderID: sent.ClientOrderID, Symbol: sent.Symbol, Qty: sent.Qty, Side: sent.Side, Type: sent.Type, TimeInForce: sent.TimeInForce, LimitPrice: sent.LimitPrice, Status: "new", FilledQty: "0"}
			orders[sent.ClientOrderID] = v
			b, _ := json.Marshal(v)
			raw = string(b)
		case "/v2/orders:by_client_order_id":
			if orderGets != nil {
				*orderGets++
			}
			v, ok := orders[h.URL.Query().Get("client_order_id")]
			if !ok {
				t.Fatal("unknown order lookup")
			}
			b, _ := json.Marshal(v)
			raw = string(b)
		default:
			t.Fatalf("unexpected intercepted request %s %s", h.Method, h.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return review90NewTrader(t)
}
func review90NewTrader(t *testing.T) *alpaca.PaperTrader {
	t.Helper()
	j, e := alpaca.OpenJournal(filepath.Join(t.TempDir(), "paper"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := j.Close(); e != nil {
			t.Error(e)
		}
	})
	b, e := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fixture-only"}, "paper-fixture", j)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// The health reports are genuine successes from the existing workers' fixture
// domain, but that domain has no relationship to the capped book being traded.
func TestIncubationGenericWorkersCannotAuthorizeBook(t *testing.T) {
	r, req, _, _ := incubationFixture(t, false)
	r.Safety = nil // Only the unrelated, actual demo workers remain.
	m := r.Store.(*storage.Memory)
	before, e := m.Snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	clock := review90WorkerClock{req.ExecuteAt, waiting}
	go func() {
		RunTimerWorker(workerCtx, m, "existing-timer-worker", clock, time.Second, time.Minute)
		done <- struct{}{}
	}()
	go func() {
		RunActionWorker(workerCtx, m, "existing-action-worker", clock, time.Second, time.Minute)
		done <- struct{}{}
	}()
	t.Cleanup(func() { cancel(); <-done; <-done })
	<-waiting
	<-waiting
	posts := 0
	r.Broker = review90Adapter(t, nil, &posts, nil)
	out, e := r.Run(context.Background(), req)
	if e == nil || posts != 0 || out.Admission.Checks["worker_liveness"].Ready {
		t.Fatal(e, posts, out.Admission)
	}
	t.Logf("POSTs=%d worker_domain_case=%s capped_book=%s liveness=%s", posts, before.State.Case.ID, r.BookID, out.Admission.Checks["worker_liveness"].Code)
}

type review90WorkerClock struct {
	at      time.Time
	waiting chan struct{}
}

func (c review90WorkerClock) Now() time.Time { return c.at }
func (c review90WorkerClock) Wait(ctx context.Context, _ time.Duration) error {
	c.waiting <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

type review90SecondStrategy struct{ *incubationTestStrategy }

func (s review90SecondStrategy) Spec() strategy.Spec {
	v := s.incubationTestStrategy.Spec()
	v.Name += "-second"
	return v
}
func review90Second(t *testing.T, r IncubationRunner, req IncubationRequest) IncubatingStrategy {
	t.Helper()
	m := r.Store.(*storage.Memory)
	ctx := context.Background()
	s := review90SecondStrategy{&incubationTestStrategy{}}
	g, e := m.StrategyGate(ctx, req.Strategies[0].GateID)
	if e != nil {
		t.Fatal(e)
	}
	d := g.Declaration
	d.ID += "-second"
	d.Family += "-second"
	d.StrategyID, _ = s.Spec().ID()
	d.Spec, _ = json.Marshal(s.Spec())
	record, e := m.DeclareStrategyGate(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	dec := *g.Decision
	dec.ID = d.ID
	dec.StrategyID = d.StrategyID
	dec.DeclarationSHA256, _ = strategygate.Hash(d)
	dec.At = record.DeclaredAt
	if _, e = m.RecordStrategyGate(ctx, dec); e != nil {
		t.Fatal(e)
	}
	v := req.Strategies[0]
	v.SleeveID = "second"
	v.GateID = d.ID
	v.Strategy = s
	return v
}

func TestIncubationConcurrentAllocationDuringFillsCannotInheritReconciliation(t *testing.T) {
	ctx := context.Background()
	r, inner, _, _ := incubationFixture(t, false)
	inner.Strategies[0].Strategy.(*incubationTestStrategy).decide = func(strategy.View) (strategy.Decision, error) {
		return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: "0.1"}}}, nil
	}
	outer := nextForwardCycle(inner, "outer-cycle", inner.ExecuteAt)
	second := review90Second(t, r, inner)
	hold := *inner.Strategies[0].Strategy.(*incubationTestStrategy)
	hold.decide = func(strategy.View) (strategy.Decision, error) { return strategy.Decision{KeepPositions: true}, nil }
	outer.Strategies = []IncubatingStrategy{inner.Strategies[0], second}
	outer.Strategies[0].Strategy = &hold
	at := outer.ExecuteAt
	r.now = func() time.Time { return at }
	innerRunner := r
	innerRunner.Broker = review90NewTrader(t)
	posts := 0
	fills := 0
	orderGets := 0
	injected := false
	innerVersion := int64(0)
	r.Broker = review90Adapter(t, func() {
		fills++
		if injected {
			return
		}
		injected = true
		at = inner.ExecuteAt
		result, e := innerRunner.Run(ctx, inner)
		if e != nil {
			t.Fatal("concurrent inner run", e)
		}
		if len(result.Orders) != 1 {
			t.Fatal(result)
		}
		s, e := r.Store.CappedBook(ctx, r.BookID)
		if e != nil {
			t.Fatal(e)
		}
		innerVersion = s.Version
		at = outer.ExecuteAt
	}, &posts, &orderGets)
	out, e := r.Run(ctx, outer)
	if e == nil || posts != 1 {
		t.Fatal("outer", e, posts, out.Admission, out.Distribution)
	}
	c := out.Plan.Health.Checks["reconciliation"]
	t.Logf("POSTs=%d fill_GETs=%d order_GETs=%d concurrent_allocation_version=%d claimed_reconciliation=%v", posts, fills, orderGets, innerVersion, c)
	if c.Ready {
		t.Fatal("concurrent allocation was incorrectly reconciled", c)
	}
	// The outer Sync loaded the book before the inner allocation existed.
	// No order GET inspected its reservation in that reconciliation; the outer
	// cycle must refuse new entry until another complete Sync covers it.
	if orderGets != 0 {
		t.Fatal("unexpected owned order reconciliation", orderGets)
	}
}
