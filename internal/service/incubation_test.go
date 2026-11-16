package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// These are isolated contract fixtures, not real market admissions or evidence.
type incubationTestStrategy struct {
	model  bool
	calls  int
	decide func(strategy.View) (strategy.Decision, error)
}

func (s *incubationTestStrategy) Spec() strategy.Spec {
	return strategy.Spec{Name: "incubation-fixture", Version: "1", Data: []string{strategy.KindDailyBars}, ModelDriven: s.model}
}
func (s *incubationTestStrategy) Decide(v strategy.View) (strategy.Decision, error) {
	s.calls++
	if s.decide != nil {
		return s.decide(v)
	}
	return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: "1"}}, State: []byte("next")}, nil
}

type incubationTestSource struct{ future bool }

func (s incubationTestSource) AdjustedBars(_ context.Context, a string, at time.Time) (marketdata.Series, error) {
	day := at.UTC().AddDate(0, 0, -1)
	published := time.Date(day.Year(), day.Month(), day.Day(), 21, 15, 0, 0, time.UTC)
	raw := storage.DailyBar{AssetID: a, Symbol: a, Date: day.Format(time.DateOnly), Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 100, ObservedAt: published, PublishedAt: published, AvailableAt: published}
	out, _ := marketdata.Adjust(a, []storage.DailyBar{raw}, nil, at)
	if s.future {
		out.Bars = []marketdata.AdjustedBar{{Raw: storage.DailyBar{AvailableAt: at.Add(time.Second)}}}
	}
	return out, nil
}

type incubationTestBroker struct {
	mu        sync.Mutex
	orders    map[string]alpaca.PaperOrder
	fills     []json.RawMessage
	posts     int
	ambiguous bool
}

func (b *incubationTestBroker) PaperAccountID() string { return "paper-fixture" }
func (b *incubationTestBroker) AbortUnsubmitted(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.orders[id]; ok {
		return alpaca.ErrSubmissionUnknown
	}
	return nil
}
func (b *incubationTestBroker) Submit(_ context.Context, o alpaca.StockOrder) (alpaca.PaperOrder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.orders[o.ClientOrderID]; ok {
		return v, nil
	}
	b.posts++
	v := alpaca.PaperOrder{ID: "venue-" + o.ClientOrderID, ClientOrderID: o.ClientOrderID, Symbol: o.Symbol, Qty: o.Qty, Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce, LimitPrice: o.LimitPrice, Status: "new", FilledQty: "0"}
	b.orders[o.ClientOrderID] = v
	if b.ambiguous {
		return alpaca.PaperOrder{}, alpaca.ErrSubmissionUnknown
	}
	return v, nil
}
func (b *incubationTestBroker) Order(_ context.Context, id string) (alpaca.PaperOrder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.orders[id]
	if !ok {
		return v, alpaca.ErrPaperNotSubmitted
	}
	return v, nil
}
func (b *incubationTestBroker) Fills(context.Context, time.Time) ([]json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]json.RawMessage(nil), b.fills...), nil
}
func (b *incubationTestBroker) fill(t *testing.T, id, activity, qty, price, fee string, at time.Time, terminal bool) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.orders[id]
	q, _ := new(big.Rat).SetString(qty)
	prior, _ := new(big.Rat).SetString(o.FilledQty)
	o.FilledQty = new(big.Rat).Add(q, prior).FloatString(9)
	if terminal {
		o.Status = "filled"
	}
	b.orders[id] = o
	raw, e := json.Marshal(map[string]any{"id": activity, "order_id": o.ID, "symbol": o.Symbol, "side": o.Side, "qty": qty, "price": price, "commission": fee, "transaction_time": at})
	if e != nil {
		t.Fatal(e)
	}
	b.fills = append(b.fills, raw)
}
func incubationFixture(t *testing.T, model bool) (IncubationRunner, IncubationRequest, *incubationTestStrategy, *incubationTestBroker) {
	t.Helper()
	ctx := context.Background()
	m := storage.NewMemory()
	s := &incubationTestStrategy{model: model}
	sid, e := s.Spec().ID()
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(s.Spec())
	cost, _ := json.Marshal(engine.DefaultCost)
	d := strategygate.Declaration{Assets: []string{"SPY"}, ID: "incubation-gate", Family: "incubation-fixture", Account: "cash", StrategyID: sid, Spec: raw, BaseCost: cost, Balance: "2000", Minimum: "1", Policy: "backtest-gate-v1", Benchmark: "cash", DataSHA256: strings.Repeat("a", 64)}
	g, e := m.DeclareStrategyGate(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := strategygate.Hash(d)
	status, class := "passed", "historical_market_unqualified"
	if model {
		status, class = "forward_only", "forward_only"
	}
	_, e = m.RecordStrategyGate(ctx, strategygate.Decision{ID: d.ID, StrategyID: sid, DeclarationSHA256: hash, FamilySHA256: strings.Repeat("b", 64), Policy: d.Policy, EvidenceClass: class, Status: status, Stage: "incubation", Minimum: "1", At: g.DeclaredAt, Checks: []strategygate.Check{{Code: "fixture", Passed: true, Reason: "isolated_runner_contract_fixture"}}})
	if e != nil {
		t.Fatal(e)
	}
	decision := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
	execution := decision.Add(time.Second)
	_, e = m.CreateCappedBook(ctx, account.DefaultCappedConfig("incubation-book", "paper-fixture", account.Cash, decision.Add(-time.Second)))
	if e != nil {
		t.Fatal(e)
	}
	b := &incubationTestBroker{orders: map[string]alpaca.PaperOrder{}}
	r := IncubationRunner{Store: m, Broker: b, BookID: "incubation-book", now: func() time.Time { return execution }}
	req := IncubationRequest{ID: "cycle-one", InputSHA256: strings.Repeat("c", 64), DecidedAt: decision, ExecuteAt: execution, Strategies: []IncubatingStrategy{{SleeveID: "trend", GateID: d.ID, Risk: "0.02", Strategy: s, Source: incubationTestSource{}, Assets: []string{"SPY"}}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: strings.Repeat("d", 64), EvidenceClass: "prospective_market_unqualified", OccurredAt: execution, ReceivedAt: execution}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
	req.Calendar = incubationTestCalendar(decision)
	r.Safety = startIncubationTestSafety(t, r, execution.Add(-time.Second))
	return r, req, s, b
}
func TestIncubationRunAndConservativeReturns(t *testing.T) {
	for _, model := range []bool{false, true} {
		t.Run(fmt.Sprint(model), func(t *testing.T) {
			r, req, s, b := incubationFixture(t, model)
			ctx := context.Background()
			out, e := r.Run(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			if b.posts != 1 || s.calls != 1 || len(out.Distribution.Orders) != 1 || out.QualificationCredit {
				t.Fatal(out, b.posts, s.calls)
			}
			budget, _ := new(big.Rat).SetString(out.Distribution.Sleeves[0].Budget)
			cap := big.NewRat(100, 1)
			if model {
				cap.SetInt64(20)
			}
			if budget.Cmp(cap) > 0 {
				t.Fatal("fixed pool exceeded", budget)
			}
			order := out.Distribution.Orders[0]
			b.fill(t, order.ID, "venue-part-one", "0.01", "100", "0", req.ExecuteAt.Add(time.Second), false)
			syncAt := req.ExecuteAt.Add(2 * time.Second)
			perf, e := r.Sync(ctx, syncAt, req.Quotes)
			if e != nil {
				t.Fatal(e)
			}
			if perf.Fills != 1 || perf.QualificationCredit || perf.NetReturnOnContributions == "0.000000000000" || perf.CostPenalty == "0.000000000000" {
				t.Fatal(perf)
			}
			state, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil {
				t.Fatal(e)
			}
			var retained bool
			for _, event := range state.Events {
				if event.Fill != nil {
					retained = true
					if event.Fill.Price != "100" || event.ModelFill == nil || event.ModelFill.ModelPrice != "100.060005000000" || event.ModelFill.CashEffect != "-1.000600050000" {
						t.Fatal(event)
					}
				}
			}
			if !retained {
				t.Fatal("fill missing")
			}
			before := state.Version
			// Restart after fills and later clocks: no Decide and no second POST.
			r.now = func() time.Time { return syncAt.Add(time.Hour) }
			retry, e := r.Run(ctx, req)
			if e != nil || b.posts != 1 || s.calls != 1 || retry.Distribution.InputSHA256 != out.Distribution.InputSHA256 {
				t.Fatal("restart changed run", retry, e, b.posts, s.calls)
			}
			after, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil || after.Version != before {
				t.Fatal("retry mutated journal", e)
			}
			if _, e = r.Sync(ctx, syncAt, req.Quotes); e != nil {
				t.Fatal("sync replay", e)
			}
			trials, e := r.Store.(*storage.Memory).StrategyTrials(ctx, "incubation-fixture")
			if e != nil || len(trials) != 0 {
				t.Fatal("incubation earned trial evidence", trials, e)
			}
		})
	}
}
func TestIncubationAmbiguousSubmissionNeverPostsAgain(t *testing.T) {
	r, req, s, b := incubationFixture(t, false)
	b.ambiguous = true
	if _, e := r.Run(context.Background(), req); !errors.Is(e, alpaca.ErrSubmissionUnknown) {
		t.Fatal(e)
	}
	state, _ := r.Store.CappedBook(context.Background(), r.BookID)
	snap, e := account.SnapshotCapped(state, incubationMarks(req.Quotes), req.ExecuteAt)
	if e != nil || len(snap.Pending) != 1 {
		t.Fatal("ambiguous reserve lost", snap, e)
	}
	r.now = func() time.Time { return req.ExecuteAt.Add(24 * time.Hour) }
	if _, e = r.Run(context.Background(), req); e != nil || b.posts != 1 || s.calls != 1 {
		t.Fatal("ambiguous restart resubmitted", e, b.posts, s.calls)
	}
	if _, e = r.Sync(context.Background(), req.ExecuteAt.Add(time.Second), req.Quotes); e != nil {
		t.Fatal(e)
	}
}

type changedIncubationGate struct {
	IncubationStore
	change func(*strategygate.Record)
}

func (s changedIncubationGate) StrategyGate(ctx context.Context, id string) (strategygate.Record, error) {
	g, e := s.IncubationStore.StrategyGate(ctx, id)
	if e == nil {
		s.change(&g)
	}
	return g, e
}
func TestIncubationRefusesIneligibleInputsBeforePost(t *testing.T) {
	for _, name := range []string{"failed", "synthetic", "unclassified", "gate-clock", "model-route", "gate-assets", "future-bars", "quote-clock", "stale-quote", "recorded-quote", "model-limit", "model-liquidity", "kill", "expired"} {
		t.Run(name, func(t *testing.T) {
			r, req, s, b := incubationFixture(t, false)
			switch name {
			case "failed", "synthetic", "unclassified", "gate-clock", "model-route", "gate-assets":
				r.Store = changedIncubationGate{r.Store, func(g *strategygate.Record) {
					switch name {
					case "failed":
						g.Decision.Status = "failed"
					case "synthetic":
						g.Decision.EvidenceClass = "synthetic_fixture"
					case "unclassified":
						g.Decision.EvidenceClass = ""
					case "gate-clock":
						g.Decision.At = req.ExecuteAt
					case "gate-assets":
						g.Declaration.Assets = []string{"QQQ"}
					case "model-route":
						g.Decision.Status = "forward_only"
						g.Decision.EvidenceClass = "forward_only"
					}
				}}
			case "future-bars":
				req.Strategies[0].Source = incubationTestSource{future: true}
			case "quote-clock":
				q := req.Quotes["SPY"]
				q.OccurredAt = req.DecidedAt
				req.Quotes["SPY"] = q
			case "stale-quote":
				req.ExecuteAt = req.ExecuteAt.Add(2 * time.Minute)
			case "recorded-quote":
				q := req.Quotes["SPY"]
				q.EvidenceClass = "recorded_market_unqualified"
				req.Quotes["SPY"] = q
			case "model-limit":
				req.Limits["SPY"] = "100"
			case "model-liquidity":
				q := req.Quotes["SPY"]
				q.AskQuantity = "0"
				req.Quotes["SPY"] = q
			case "kill":
				c := cappedbook.Client{Store: r.Store, Broker: b, BookID: r.BookID}
				if _, e := c.Control(context.Background(), "kill", req.DecidedAt, risk.Control{Action: "kill"}); e != nil {
					t.Fatal(e)
				}
			case "expired":
				r.now = func() time.Time { return req.ExecuteAt.Add(2 * time.Minute) }
			}
			if _, e := r.Run(context.Background(), req); e == nil || b.posts != 0 {
				t.Fatal("unsafe input posted", name, e, b.posts)
			}
			if (name == "failed" || name == "synthetic" || name == "unclassified" || name == "gate-clock" || name == "model-route") && s.calls != 0 {
				t.Fatal("ineligible strategy executed", s.calls)
			}
		})
	}
}
func TestIncubationChangedRetryAndStateOwnership(t *testing.T) {
	r, req, s, b := incubationFixture(t, false)
	ctx := context.Background()
	out, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	changed := req
	changed.InputSHA256 = strings.Repeat("f", 64)
	if _, e = r.Run(ctx, changed); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal(e)
	}
	b.fill(t, out.Orders[0].ClientOrderID, "complete", out.Orders[0].Qty, "100", "0", req.ExecuteAt.Add(time.Second), true)
	syncAt := req.ExecuteAt.Add(2 * time.Second)
	if _, e = r.Sync(ctx, syncAt, req.Quotes); e != nil {
		t.Fatal(e)
	}
	s.decide = func(v strategy.View) (strategy.Decision, error) {
		if string(v.State()) != "next" || len(v.Positions()) != 1 || len(v.Fills()) != 1 {
			t.Fatal("state/owned fills not restored", v.State(), v.Positions(), v.Fills())
		}
		return strategy.Decision{KeepPositions: true, State: []byte("continued")}, nil
	}
	req.ID = "cycle-two"
	req.DecidedAt = syncAt
	req.ExecuteAt = syncAt.Add(time.Second)
	req.InputSHA256 = strings.Repeat("e", 64)
	q := req.Quotes["SPY"]
	q.OccurredAt = req.ExecuteAt
	q.ReceivedAt = req.ExecuteAt
	q.SourceSHA256 = strings.Repeat("e", 64)
	req.Quotes["SPY"] = q
	r.now = func() time.Time { return req.ExecuteAt }
	if _, e = r.Run(ctx, req); e != nil || s.calls != 2 {
		t.Fatal("state continuation", e, s.calls)
	}
}

type interleavedIncubation struct {
	IncubationStore
	before func(context.Context) error
}

func (s *interleavedIncubation) ApplyCappedBook(ctx context.Context, id string, v int64, c account.BookCommand) (account.CappedState, error) {
	if c.Kind == "incubation_plan" && s.before != nil {
		f := s.before
		s.before = nil
		if e := f(ctx); e != nil {
			return account.CappedState{}, e
		}
	}
	return s.IncubationStore.ApplyCappedBook(ctx, id, v, c)
}
func TestIncubationAtomicSearchFreshness(t *testing.T) {
	r, req, _, b := incubationFixture(t, false)
	m := r.Store.(*storage.Memory)
	r.Store = &interleavedIncubation{IncubationStore: m, before: func(ctx context.Context) error {
		spec := req.Strategies[0].Strategy.Spec()
		raw, _ := json.Marshal(spec)
		sid, _ := spec.ID()
		_, e := m.StartStrategyTrial(ctx, strategytrial.Start{ID: "new-search", Family: "incubation-fixture", StrategyID: sid, Spec: raw, Config: json.RawMessage(`{"variant":1}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
		return e
	}}
	if _, e := r.Run(context.Background(), req); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") || b.posts != 0 {
		t.Fatal("stale search admitted", e, b.posts)
	}
	state, _ := m.CappedBook(context.Background(), r.BookID)
	for _, event := range state.Events {
		if event.Kind != "risk_observe" {
			t.Fatal("stale plan retained", state)
		}
	}
}

func TestIncubationFinalSubmissionClockIsChecked(t *testing.T) {
	r, req, _, b := incubationFixture(t, false)
	calls := 0
	r.now = func() time.Time {
		calls++
		if calls < 3 {
			return req.ExecuteAt
		}
		return req.ExecuteAt.Add(2 * time.Minute)
	}
	if _, e := r.Run(context.Background(), req); e == nil || b.posts != 0 {
		t.Fatal("expired during reservation posted", e, b.posts)
	}
	// A deterministic pre-POST refusal safely aborts and releases the reserve.
	state, e := r.Store.CappedBook(context.Background(), r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, incubationMarks(req.Quotes), req.ExecuteAt)
	if e != nil || len(snap.Pending) != 0 {
		t.Fatal(snap, e)
	}
	if _, e = r.Run(context.Background(), req); e != nil || b.posts != 0 {
		t.Fatal("rejected ID reopened", e, b.posts)
	}
}

func incubationTestCalendar(at time.Time) *IncubationCalendar {
	day := at.UTC().AddDate(0, 0, -1)
	open := time.Date(day.Year(), day.Month(), day.Day(), 14, 30, 0, 0, time.UTC)
	return &IncubationCalendar{From: day.Format(time.DateOnly), Through: at.Add(24 * time.Hour).Format(time.DateOnly), SourceSHA256: strings.Repeat("e", 64), ObservedAt: at.Add(-time.Second), Sessions: []alpaca.Session{{Date: day.Format(time.DateOnly), OpenAt: open, CloseAt: open.Add(6*time.Hour + 30*time.Minute)}}}
}

func healthyIncubationWorkers(t *testing.T, r IncubationRunner, at time.Time) {
	t.Helper()
	// Advance the same running worker so runner copies share evidence from
	// a newly executed service cycle.
	if r.Safety == nil {
		t.Fatal("fixture safety worker missing")
	}
	advanceIncubationTestSafety(t, r.Safety, at.Add(-time.Second))
}

func (b *incubationTestBroker) SubmitChecked(ctx context.Context, o alpaca.StockOrder, check func() error) (alpaca.PaperOrder, error) {
	if e := check(); e != nil {
		return alpaca.PaperOrder{}, e
	}
	return b.Submit(ctx, o)
}
