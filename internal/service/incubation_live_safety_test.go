package service

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type review90FinalLoadStore struct {
	*storage.Memory
	finalLoads    int
	armed         bool
	afterCoverage func()
}

func (s *review90FinalLoadStore) CappedBook(ctx context.Context, id string) (account.CappedState, error) {
	if s.armed {
		s.finalLoads++
		if s.finalLoads == 2 {
			s.armed = false
			s.afterCoverage()
		}
	}
	return s.Memory.CappedBook(ctx, id)
}

func TestIncubationSafetyRegressionWithdrawnSafetyAtFinalLoad(t *testing.T) {
	for _, mode := range []string{"cancel", "failed-cycle"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, req, _, b := incubationFixture(t, false)
			store := &review90FinalLoadStore{Memory: r.Store.(*storage.Memory)}
			r.Store = store
			clock := &incubationSafetyFixture{at: req.DecidedAt, next: make(chan time.Time), waiting: make(chan struct{})}
			worker, err := NewIncubationSafetyWorker(r, clock, IncubationSafetyPolicy{Symbols: []string{"SPY"}, MaximumHolding: time.Hour, Fees: map[string]string{"SPY": "1"}}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			workerCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); _ = worker.Run(workerCtx, clock) }()
			t.Cleanup(func() { cancel(); <-done })
			<-clock.waiting
			r.Safety = worker
			store.afterCoverage = func() {
				if mode == "cancel" {
					cancel()
					<-done
					return
				}
				clock.mu.Lock()
				clock.fail = true
				clock.mu.Unlock()
				clock.next <- req.ExecuteAt
				<-clock.waiting
			}
			r.Broker = boundaryHealthBroker{incubationTestBroker: b, prepare: func() { store.armed = true }}
			result, err := r.Run(ctx, req)
			state, loadErr := r.Store.CappedBook(ctx, r.BookID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			live := worker.coverage(r.Store, state, req.ExecuteAt, incubationMarks(req.Quotes))
			t.Logf("mode=%s final_loads=%d live=%s liveReady=%v cachedReady=%v posts=%d err=%v", mode, store.finalLoads, live.Code, live.Ready, result.Admission.Checks["worker_liveness"].Ready, b.posts, err)
			if b.posts != 0 {
				t.Fatal("new risk POST after safety coverage was withdrawn")
			}
		})
	}
}

func TestIncubationSafetyRegressionForwardDeadlineCoverage(t *testing.T) {
	ctx := context.Background()
	r, req, _, b, record := forwardRunnerFixture(t)
	out, err := r.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	entry := out.Distribution.Orders[0]
	filled := req.ExecuteAt.Add(time.Second)
	b.fill(t, entry.ID, "review90-forward-fill", entry.Quantity, "100", "0", filled, true)
	if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
		t.Fatal(err)
	}
	expiry := filled.Add(time.Duration(record.Declaration.MaxHoldingSeconds) * time.Second)
	r.Safety = startIncubationTestSafety(t, r, expiry.Add(-time.Second))
	state, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := account.ForwardHoldingOpened(state, "trend", expiry)
	if err != nil {
		t.Fatal(err)
	}
	clocks := record.Clocks(expiry, record.SearchTotal, opened)
	check := r.Safety.coverage(r.Store, state, expiry, map[string]string{"SPY": "100"})
	t.Logf("forwardHoldExpired=%v workerCoverageReady=%v code=%s posts=%d", clocks.HoldExpired, check.Ready, check.Code, b.posts)
	if clocks.HoldExpired && check.Ready {
		t.Fatal("due forward holding protection accepted live coverage")
	}
}

func TestIncubationSafetyRegressionForwardDeadlineAllowsUnrelatedEntry(t *testing.T) {
	ctx := context.Background()
	r, req, strat, b, record := forwardRunnerFixture(t)
	d := record.Declaration
	d.ID = "review90-short-hold"
	d.MaxHoldingSeconds = 5
	short, err := r.Store.(forward.Store).DeclareForward(ctx, d, func() time.Time { return req.DecidedAt.Add(-time.Second / 2) })
	if err != nil {
		t.Fatal(err)
	}
	record = short
	req.Strategies[0].ForwardDeclarationID = d.ID
	strat.decide = func(strategy.View) (strategy.Decision, error) {
		return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: "0.1"}}}, nil
	}
	second := review90Second(t, r, req)
	second.ForwardDeclarationID = ""
	out, err := r.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	entry := out.Distribution.Orders[0]
	filled := req.ExecuteAt.Add(time.Second)
	b.fill(t, entry.ID, "review90-forward-real-fill", entry.Quantity, "100", "0", filled, true)
	if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
		t.Fatal(err)
	}
	expiry := filled.Add(time.Duration(record.Declaration.MaxHoldingSeconds) * time.Second)
	r.Safety = startIncubationTestSafety(t, r, expiry.Add(-4*time.Second))
	strat.decide = func(strategy.View) (strategy.Decision, error) { return strategy.Decision{}, nil }
	next := nextForwardCycle(req, "review90-after-hold-expiry", expiry.Add(-3*time.Second))
	next.Strategies = append(next.Strategies, second)
	now := next.ExecuteAt
	r.now = func() time.Time { return now }
	r.Broker = boundaryHealthBroker{incubationTestBroker: b, prepare: func() { now = expiry }}
	result, err := r.Run(ctx, next)
	newPosts := 0
	for _, o := range result.Orders {
		if o.Side == "buy" {
			newPosts++
		}
	}
	t.Logf("afterForwardExpiry newBuyPOSTs=%d totalPosts=%d workerReady=%v err=%v", newPosts, b.posts, result.Admission.Checks["worker_liveness"].Ready, err)
	if newPosts != 0 {
		t.Fatal("unrelated new risk POST while retained forward holding protection is due")
	}
}

func TestIncubationSafetyPendingFamilyStillDeliversExit(t *testing.T) {
	ctx := context.Background()
	r, req, strat, broker, record := forwardRunnerFixture(t)
	entry, err := r.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	order := entry.Distribution.Orders[0]
	filled := req.ExecuteAt.Add(time.Second)
	broker.fill(t, order.ID, "pending-safety-entry", order.Quantity, "100", "0", filled, true)
	if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
		t.Fatal(err)
	}
	if err = r.Store.(*storage.Memory).BeginStrategyTrialBatch(ctx, "pending-worker-family", record.Declaration.Family); err != nil {
		t.Fatal(err)
	}
	clock := r.Safety.source.(*incubationSafetyFixture)
	clock.mu.Lock()
	clock.step = time.Millisecond
	clock.mu.Unlock()
	clock.next <- filled.Add(3 * time.Second)
	<-clock.waiting
	if broker.posts != 2 || strat.calls != 1 {
		t.Fatal("pending family stranded worker protection", broker.posts, strat.calls)
	}
	state, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	exits := 0
	for _, event := range state.Events {
		if event.Order != nil && event.Order.ReduceOnly {
			exits++
		}
	}
	if exits != 1 {
		t.Fatal("pending family did not retain exactly one protective order", exits)
	}
	if check := r.Safety.coverage(r.Store, state, clock.Now(), incubationMarks(req.Quotes)); check.Ready {
		t.Fatal("unfilled exit reported as completed protection", check)
	}
}

type acceptance38LateOwnerStore struct {
	*storage.Memory
	armed  bool
	loads  int
	inject func()
}

func (s *acceptance38LateOwnerStore) CappedBook(ctx context.Context, id string) (account.CappedState, error) {
	if s.armed {
		s.loads++
	}
	return s.Memory.CappedBook(ctx, id)
}
func (s *acceptance38LateOwnerStore) ForwardDeclaration(ctx context.Context, id string) (forward.Record, error) {
	if s.armed && s.loads >= 2 {
		s.armed = false
		s.inject()
	}
	return s.Memory.ForwardDeclaration(ctx, id)
}

func TestIncubationAtomicOwnerRetirementDuringCoverageRead(t *testing.T) {
	ctx := context.Background()
	r, req, _, _, _ := forwardRunnerFixture(t)
	m := r.Store.(*storage.Memory)
	store := &acceptance38LateOwnerStore{Memory: m}
	r.Store = store
	r.Safety = startIncubationTestSafety(t, r, req.DecidedAt)
	sid, _ := req.Strategies[0].Strategy.Spec().ID()
	committed := false
	store.inject = func() {
		state, err := m.CappedBook(ctx, r.BookID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "owner-retired-during-coverage", Kind: "risk_control", At: req.ExecuteAt, RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: sid}})
		if err != nil {
			t.Fatal(err)
		}
		committed = true
	}
	posts := 0
	old := httpDefaultAcceptance38(t, func() { store.armed = true }, &posts)
	r.Broker = old
	_, err := r.Run(ctx, req)
	state, loadErr := m.CappedBook(ctx, r.BookID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	snap, snapErr := account.SnapshotCapped(state, incubationMarks(req.Quotes), req.ExecuteAt)
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	t.Logf("actual-adapter POSTs=%d committed-retirement=%v retained-retired=%v final-loads=%d err=%v", posts, committed, snap.Risk.Retired[sid], store.loads, err)
	if !committed {
		t.Fatal("injection not reached")
	}
	if posts != 0 {
		t.Fatal("opening POST after retirement committed during final coverage read")
	}
}

// Use the real adapter, with every HTTP request intercepted in process.
func httpDefaultAcceptance38(t *testing.T, arm func(), posts *int) *alpaca.PaperTrader {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
		raw := ""
		switch h.URL.Path {
		case "/v2/account/activities/FILL":
			raw = `[]`
		case "/v2/account":
			raw = `{"id":"paper-fixture","status":"ACTIVE"}`
		case "/v2/assets/SPY":
			arm()
			raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
		case "/v2/orders":
			*posts++
			var o alpaca.StockOrder
			if err := json.NewDecoder(h.Body).Decode(&o); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(alpaca.PaperOrder{ID: "venue-fixture", ClientOrderID: o.ClientOrderID, Symbol: o.Symbol, Qty: o.Qty, Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce, LimitPrice: o.LimitPrice, Status: "new", FilledQty: "0"})
			raw = string(b)
		default:
			t.Fatalf("unexpected intercepted request %s", h.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return review90NewTrader(t)
}
