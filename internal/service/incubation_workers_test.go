package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

// This clock pauses a real service worker after its actual cycle. Test entry
// requests cannot assert coverage or manufacture a successful worker receipt.
type incubationSafetyFixture struct {
	mu      sync.Mutex
	at      time.Time
	next    chan time.Time
	waiting chan struct{}
	step    time.Duration
	fail    bool
}

func (f *incubationSafetyFixture) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.at }
func (f *incubationSafetyFixture) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case f.waiting <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case at := <-f.next:
		f.mu.Lock()
		f.at = at
		f.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *incubationSafetyFixture) Quotes(_ context.Context, symbols []string, at time.Time) (map[string]account.IncubationQuote, error) {
	f.mu.Lock()
	step, fail := f.step, f.fail
	f.at = f.at.Add(step)
	at = f.at
	f.mu.Unlock()
	if fail {
		return nil, errors.New("independent quote source unavailable")
	}
	out := map[string]account.IncubationQuote{}
	for _, symbol := range symbols {
		out[symbol] = account.IncubationQuote{Symbol: symbol, Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: incubationDigest("safety-quotes" + at.Format(time.RFC3339Nano)), EvidenceClass: "prospective_market_unqualified", OccurredAt: at, ReceivedAt: at}
	}
	return out, nil
}
func startIncubationTestSafety(t *testing.T, r IncubationRunner, at time.Time) *IncubationSafetyWorker {
	t.Helper()
	return startIncubationTestSafetyWithPolicy(t, r, at, IncubationSafetyPolicy{Symbols: []string{"SPY"}, MaximumHolding: 60 * 24 * time.Hour, Fees: map[string]string{"SPY": "1"}})
}
func startIncubationTestSafetyWithPolicy(t *testing.T, r IncubationRunner, at time.Time, policy IncubationSafetyPolicy) *IncubationSafetyWorker {
	t.Helper()
	clock := &incubationSafetyFixture{at: at, next: make(chan time.Time), waiting: make(chan struct{})}
	worker, err := NewIncubationSafetyWorker(r, clock, policy, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(ctx, clock) }()
	t.Cleanup(func() { cancel(); <-done })
	<-clock.waiting
	state, err := r.Store.CappedBook(context.Background(), r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if c := worker.coverage(r.Store, state, at, map[string]string{"SPY": "100"}); !c.Ready {
		t.Fatal(c)
	}
	return worker
}
func advanceIncubationTestSafety(t *testing.T, w *IncubationSafetyWorker, at time.Time) {
	t.Helper()
	clock := w.source.(*incubationSafetyFixture)
	clock.next <- at
	<-clock.waiting
	state, err := w.runner.Store.CappedBook(context.Background(), w.runner.BookID)
	if err != nil {
		t.Fatal(err)
	}
	if c := w.coverage(w.runner.Store, state, at, map[string]string{"SPY": "100"}); !c.Ready {
		t.Fatal(c)
	}
}

func TestIncubationSafetyCoverageAssociationAndLifecycle(t *testing.T) {
	for _, name := range []string{"different-store", "different-book", "different-account", "uncovered-symbol", "expired", "future", "source-failure", "not-started", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			r, req, _, b := incubationFixture(t, false)
			ctx := context.Background()
			state, err := r.Store.CappedBook(ctx, r.BookID)
			if err != nil {
				t.Fatal(err)
			}
			at := req.ExecuteAt
			marks := incubationMarks(req.Quotes)
			worker := r.Safety
			switch name {
			case "different-store":
				// Same exact config and journal in a different storage instance
				// must not borrow the original worker's live responsibility.
				other := storage.NewMemory()
				if _, err := other.CreateCappedBook(ctx, state.Config); err != nil {
					t.Fatal(err)
				}
				r.Store = other
			case "different-book":
				state.Config.ID = "other-book"
			case "different-account":
				state.Config.BrokerAccountID = "other-account"
			case "uncovered-symbol":
				marks["QQQ"] = "100"
			case "expired":
				at = at.Add(4 * time.Minute)
			case "future":
				at = state.Config.OpenedAt
			case "source-failure":
				clock := worker.source.(*incubationSafetyFixture)
				clock.mu.Lock()
				clock.fail = true
				clock.mu.Unlock()
				clock.next <- at
				<-clock.waiting
			case "not-started":
				worker, err = NewIncubationSafetyWorker(r, worker.source, worker.policy, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				// The owner's context, not a caller-authored stopped report, withdraws
				// live responsibility even before the worker finishes its shutdown.
				worker.mu.Lock()
				cancelled, cancel := context.WithCancel(worker.ctx)
				cancel()
				worker.ctx = cancelled
				worker.mu.Unlock()
			}
			if check := worker.coverage(r.Store, state, at, marks); check.Ready {
				t.Fatal(name, check)
			}
			if b.posts != 0 {
				t.Fatal("coverage inspection submitted work")
			}
		})
	}
}

func TestIncubationSafetyWorkerActuallyDeliversAndReconcilesExit(t *testing.T) {
	for _, name := range []string{"owner-kill", "holding-deadline"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r, req, strat, b := incubationFixture(t, false)
			if name == "holding-deadline" {
				r.Safety = startIncubationTestSafetyWithPolicy(t, r, req.DecidedAt, IncubationSafetyPolicy{Symbols: []string{"SPY"}, MaximumHolding: 3 * time.Second, Fees: map[string]string{"SPY": "1"}})
			}
			out, err := r.Run(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			entry := out.Distribution.Orders[0]
			filled := req.ExecuteAt.Add(time.Second)
			b.fill(t, entry.ID, "safety-entry-fill", entry.Quantity, "100", "0", filled, true)
			if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
				t.Fatal(err)
			}
			c := cappedbook.Client{Store: r.Store, Broker: b, BookID: r.BookID}
			at := filled.Add(3 * time.Second)
			if name == "owner-kill" {
				if _, err = c.Control(ctx, "safety-owner-kill", at, risk.Control{Action: "kill"}); err != nil {
					t.Fatal(err)
				}
			}
			clock := r.Safety.source.(*incubationSafetyFixture)
			clock.mu.Lock()
			clock.step = time.Millisecond
			clock.mu.Unlock()
			// A worker cycle, without another entry/model request, must route the exit.
			clock.next <- at.Add(time.Second)
			<-clock.waiting
			if b.posts != 2 || strat.calls != 1 {
				t.Fatal("worker did not deliver independent protective exit", b.posts, strat.calls)
			}
			state, err := r.Store.CappedBook(ctx, r.BookID)
			if err != nil {
				t.Fatal(err)
			}
			var exit *account.BookOrder
			for _, event := range state.Events {
				if event.Order != nil && event.Order.ReduceOnly {
					exit = event.Order
				}
			}
			if exit == nil || exit.Side != "sell" {
				t.Fatal("missing protective reservation")
			}
			if check := r.Safety.coverage(r.Store, state, clock.Now(), incubationMarks(req.Quotes)); check.Ready {
				t.Fatal("queued exit reported fulfilled", check)
			}
			b.fill(t, exit.ID, "safety-exit-fill", exit.Quantity, "99.99", "0", clock.Now().Add(time.Second), true)
			clock.next <- clock.Now().Add(2 * time.Second)
			<-clock.waiting
			state, err = r.Store.CappedBook(ctx, r.BookID)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := account.SnapshotCapped(state, incubationMarks(req.Quotes), clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			if snap.Positions["SPY"] != "0.000000000" || snap.Risk.Kill != (name == "owner-kill") || b.posts != 2 || strat.calls != 1 {
				t.Fatal("exit not reconciled or owner controls lost", snap, b.posts, strat.calls)
			}
			if check := r.Safety.coverage(r.Store, state, clock.Now(), incubationMarks(req.Quotes)); !check.Ready {
				t.Fatal(check)
			}
			for _, event := range state.Events {
				if event.Incubation != nil && event.Incubation.Health != nil && event.Incubation.Health.QualificationCredit {
					t.Fatal("safety work awarded qualification")
				}
			}
		})
	}
}

func TestIncubationSafetyWorkerRejectsInvalidPolicy(t *testing.T) {
	for _, fee := range []string{"", "-1", "1/2", "nan", strings.Repeat("1", 20)} {
		t.Run(fee, func(t *testing.T) {
			r, _, _, _ := incubationFixture(t, false)
			if _, err := NewIncubationSafetyWorker(r, r.Safety.source, IncubationSafetyPolicy{Symbols: []string{"SPY"}, MaximumHolding: time.Hour, Fees: map[string]string{"SPY": fee}}, time.Second); err == nil {
				t.Fatal("invalid safety fee accepted", fee)
			}
		})
	}
}
