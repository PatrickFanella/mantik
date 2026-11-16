package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

type supervisionStore struct {
	*storage.Memory
	gateReads atomic.Int64
}

func (s *supervisionStore) StrategyGate(ctx context.Context, id string) (strategygate.Record, error) {
	s.gateReads.Add(1)
	return s.Memory.StrategyGate(ctx, id)
}

type supervisionBroker struct{ fills, posts atomic.Int64 }

func (*supervisionBroker) PaperAccountID() string                         { return "supervision-fixture" }
func (*supervisionBroker) AbortUnsubmitted(context.Context, string) error { return nil }
func (b *supervisionBroker) Submit(context.Context, alpaca.StockOrder) (alpaca.PaperOrder, error) {
	b.posts.Add(1)
	return alpaca.PaperOrder{}, errors.New("fixture must not submit")
}
func (*supervisionBroker) Order(context.Context, string) (alpaca.PaperOrder, error) {
	return alpaca.PaperOrder{}, alpaca.ErrPaperNotSubmitted
}
func (b *supervisionBroker) Fills(context.Context, time.Time) ([]json.RawMessage, error) {
	b.fills.Add(1)
	return nil, nil
}

func privateSupervisionJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIncubationSupervisionKeepsSafetyRunningAfterCycleRefusal(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-independent-quotes", true: "current-independent-quotes"}[available], func(t *testing.T) {
			root := t.TempDir()
			quotesPath, configPath, cyclePath := filepath.Join(root, "quotes.json"), filepath.Join(root, "safety.json"), filepath.Join(root, "cycle.json")
			at := time.Now().UTC().Add(-time.Second)
			quotes := map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", OccurredAt: at, ReceivedAt: at, SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "prospective_market_unqualified"}}
			if available {
				privateSupervisionJSON(t, quotesPath, quotes)
			}
			privateSupervisionJSON(t, configPath, incubationSafetyInput{QuotesFile: quotesPath, Symbols: []string{"SPY"}, Fees: map[string]string{"SPY": "1"}, MaximumHoldingSeconds: 3600, IntervalSeconds: 1})
			cfg := library.Default(library.Trend)
			privateSupervisionJSON(t, cyclePath, incubationInput{ID: "refused-cycle", DecidedAt: at.Add(-time.Second), ExecuteAt: at, Quotes: quotes, Strategies: []incubationStrategyInput{{SleeveID: "trend", GateID: "missing-gate", Library: &cfg, Assets: []string{"SPY"}}}})
			store := &supervisionStore{Memory: storage.NewMemory()}
			if _, err := store.CreateCappedBook(context.Background(), account.DefaultCappedConfig("supervised-book", "supervision-fixture", account.Cash, at.Add(-time.Minute))); err != nil {
				t.Fatal(err)
			}
			broker := &supervisionBroker{}
			runner := service.IncubationRunner{Store: store, Broker: broker, BookID: "supervised-book"}
			ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
			defer cancel()
			var out bytes.Buffer
			err := runIncubationSupervision(ctx, runner, cyclePath, configPath, &out)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			var events []incubationSupervisionEvent
			decoder := json.NewDecoder(&out)
			for decoder.More() {
				var e incubationSupervisionEvent
				if err := decoder.Decode(&e); err != nil {
					t.Fatal(err)
				}
				events = append(events, e)
			}
			if len(events) == 0 || events[len(events)-1].Kind != "supervision_stopped" || broker.posts.Load() != 0 {
				t.Fatal(events, broker.posts.Load())
			}
			cycles := 0
			for _, e := range events {
				if e.Kind == "cycle" {
					cycles++
					if e.CycleID != "refused-cycle" || e.Error == "" {
						t.Fatal(e)
					}
				}
			}
			if available {
				if store.gateReads.Load() != 1 || cycles != 1 || broker.fills.Load() < 2 {
					t.Fatal("entry refusal stopped safety or replayed unchanged input", events, store.gateReads.Load(), broker.fills.Load())
				}
			} else if store.gateReads.Load() != 0 || cycles != 0 || broker.fills.Load() != 0 {
				t.Fatal("missing safety data reached entry or reconciliation", events)
			}
		})
	}
}

func TestIncubationSafetyFileRefusesUnsafeOrExpiredSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quotes.json")
	at := time.Now().UTC()
	q := account.IncubationQuote{Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", OccurredAt: at, ReceivedAt: at, SourceSHA256: strings.Repeat("b", 64), EvidenceClass: "prospective_market_unqualified"}
	privateSupervisionJSON(t, path, map[string]account.IncubationQuote{"SPY": q})
	source := incubationSafetyFile(path)
	if _, err := source.Quotes(context.Background(), []string{"SPY"}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Quotes(context.Background(), []string{"SPY"}, at.Add(time.Minute+time.Nanosecond)); err == nil {
		t.Fatal("expired independent quote accepted")
	}
	if _, err := source.Quotes(context.Background(), []string{"QQQ"}, at); err == nil {
		t.Fatal("uncovered symbol accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Quotes(context.Background(), []string{"SPY"}, at); err == nil {
		t.Fatal("non-private safety source accepted")
	}
}

// This isolated ledger is synthetic contract evidence. No venue is contacted.
type protectiveFileStore struct {
	*storage.Memory
	mu    sync.Mutex
	state account.CappedState
}

func (s *protectiveFileStore) CappedBook(context.Context, string) (account.CappedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}
func (s *protectiveFileStore) ApplyCappedBook(_ context.Context, _ string, v int64, c account.BookCommand) (account.CappedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Version != v {
		return s.state, account.ErrBookConflict
	}
	next, e := account.ApplyCappedCommand(s.state, c)
	if e == nil {
		s.state = next
	}
	return next, e
}
func (s *protectiveFileStore) CheckCappedSubmission(_ context.Context, _ string, o account.BookOrder, m map[string]string) (account.CappedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, account.CheckPortfolioSubmission(s.state, o, m)
}

type protectiveFileBroker struct {
	order  alpaca.PaperOrder
	fill   json.RawMessage
	posted chan struct{}
	posts  atomic.Int64
}

func (b *protectiveFileBroker) PaperAccountID() string                         { return "file-account" }
func (b *protectiveFileBroker) AbortUnsubmitted(context.Context, string) error { return nil }
func (b *protectiveFileBroker) Order(context.Context, string) (alpaca.PaperOrder, error) {
	return b.order, nil
}
func (b *protectiveFileBroker) Fills(context.Context, time.Time) ([]json.RawMessage, error) {
	return []json.RawMessage{b.fill}, nil
}
func (b *protectiveFileBroker) Submit(_ context.Context, o alpaca.StockOrder) (alpaca.PaperOrder, error) {
	b.posts.Add(1)
	select {
	case b.posted <- struct{}{}:
	default:
	}
	return alpaca.PaperOrder{ID: "protective-venue", ClientOrderID: o.ClientOrderID, Symbol: o.Symbol, Qty: o.Qty, Side: o.Side, LimitPrice: o.LimitPrice, Type: o.Type, TimeInForce: o.TimeInForce, Status: "new", FilledQty: "0"}, nil
}
func (b *protectiveFileBroker) SubmitChecked(ctx context.Context, o alpaca.StockOrder, check func() error) (alpaca.PaperOrder, error) {
	if e := check(); e != nil {
		return alpaca.PaperOrder{}, e
	}
	return b.Submit(ctx, o)
}

type protectiveFileClock struct{ awaiting chan struct{} }

func (c protectiveFileClock) Now() time.Time { return time.Now().UTC() }
func (c protectiveFileClock) Wait(ctx context.Context, d time.Duration) error {
	if d < time.Second {
		select {
		case c.awaiting <- struct{}{}:
		default:
		}
	}
	return (service.WallClock{}).Wait(ctx, d)
}

func TestIncubationActualFileWaitsForPostDecisionProtectionQuote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	at := time.Now().UTC().Add(-10 * time.Second)
	state, e := account.NewCappedState(account.DefaultCappedConfig("file-book", "file-account", account.Cash, at.Add(-time.Minute)))
	if e != nil {
		t.Fatal(e)
	}
	apply := func(c account.BookCommand) {
		t.Helper()
		var err error
		state, err = account.ApplyCappedCommand(state, c)
		if err != nil {
			t.Fatal(c.Kind, err)
		}
	}
	sid := "strategy-" + strings.Repeat("a", 64)
	req := allocation.RebalanceRequest{ID: "file-entry", At: at, Sleeves: []allocation.Sleeve{{ID: "trend", StrategyID: sid, GateID: "fixture-gate", Stage: "incubation", Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	q := account.IncubationQuote{Symbol: "SPY", Bid: "100", Ask: "100", BidQuantity: "100", AskQuantity: "100", OccurredAt: at, ReceivedAt: at, SourceSHA256: strings.Repeat("b", 64), EvidenceClass: "prospective_market_unqualified"}
	plan := account.IncubationPlan{InputSHA256: strings.Repeat("c", 64), DecidedAt: at.Add(-time.Second), Rebalance: req, Quotes: map[string]account.IncubationQuote{"SPY": q}, Costs: map[string]executioncost.Model{"trend": {Version: "fixture-v1", FeeBps: "0", SlippageBps: "0"}}, Decisions: []account.IncubationDecision{{SleeveID: "trend", StrategyID: sid}}}
	apply(account.BookCommand{ID: "file-plan", Kind: "incubation_plan", At: at, Incubation: &plan})
	apply(account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: req.Limits})
	snap, e := account.SnapshotCapped(state, req.Limits, at)
	if e != nil {
		t.Fatal(e)
	}
	v := snap.Distribution.Orders[0]
	o := account.BookOrder{ID: v.ID, AllocationID: v.AllocationID, SleeveID: v.SleeveID, StrategyID: v.StrategyID, Symbol: v.Symbol, Quantity: v.Quantity, Limit: v.Limit, Side: v.Side, Fee: v.Fee, At: v.At}
	apply(account.BookCommand{ID: "reserve-entry", Kind: "portfolio_reserve", At: at, Order: &o, Marks: req.Limits})
	apply(account.BookCommand{ID: "bind-entry", Kind: "bind", At: at, OrderID: o.ID, VenueOrderID: "file-venue"})
	fillRaw, _ := json.Marshal(map[string]any{"id": "file-entry-fill", "order_id": "file-venue", "symbol": "SPY", "side": "buy", "qty": o.Quantity, "price": "100", "commission": "0", "transaction_time": at})
	fill := account.BookFill{ID: "file-entry-fill", OrderID: o.ID, VenueOrderID: "file-venue", Symbol: "SPY", Quantity: o.Quantity, Price: "100", Fee: "0", Side: "buy", At: at, SourceSHA256: alpaca.Digest(string(fillRaw))}
	model, e := account.ModelIncubationFill(state, fill)
	if e != nil {
		t.Fatal(e)
	}
	apply(account.BookCommand{ID: fill.ID, Kind: "fill", At: at, Fill: &fill, ModelFill: model, Marks: req.Limits})
	store := &protectiveFileStore{Memory: storage.NewMemory(), state: state}
	broker := &protectiveFileBroker{order: alpaca.PaperOrder{ID: "file-venue", ClientOrderID: o.ID, Symbol: o.Symbol, Qty: o.Quantity, Side: o.Side, LimitPrice: o.Limit, Type: "limit", TimeInForce: "day", Status: "filled", FilledQty: o.Quantity}, fill: fillRaw, posted: make(chan struct{}, 1)}
	path := filepath.Join(t.TempDir(), "quotes.json")
	q.OccurredAt = time.Now().UTC().Add(-time.Second)
	q.ReceivedAt = q.OccurredAt
	privateSupervisionJSON(t, path, map[string]account.IncubationQuote{"SPY": q})
	worker, e := service.NewIncubationSafetyWorker(service.IncubationRunner{Store: store, Broker: broker, BookID: "file-book"}, incubationSafetyFile(path), service.IncubationSafetyPolicy{Symbols: []string{"SPY"}, MaximumHolding: time.Second, Fees: map[string]string{"SPY": "0"}}, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	clock := protectiveFileClock{awaiting: make(chan struct{}, 1)}
	go func() { done <- worker.Run(ctx, clock) }()
	defer func() { cancel(); <-done }()
	// Wait until the worker has finalized its decision and rejected the old
	// snapshot. Only then does the independent collector publish a newer quote.
	select {
	case <-clock.awaiting:
	case <-ctx.Done():
		t.Fatal("worker did not wait for a new quote", ctx.Err())
	}
	q.OccurredAt = time.Now().UTC()
	q.ReceivedAt = q.OccurredAt
	next := path + ".next"
	privateSupervisionJSON(t, next, map[string]account.IncubationQuote{"SPY": q})
	if e = os.Rename(next, path); e != nil {
		t.Fatal(e)
	}
	select {
	case <-broker.posted:
	case <-ctx.Done():
		t.Error("file-backed safety never delivered after a genuinely newer quote", ctx.Err())
	}
	cancel()
	if broker.posts.Load() != 1 {
		t.Fatal("protective delivery did not retain exactly one POST", broker.posts.Load())
	}
}
