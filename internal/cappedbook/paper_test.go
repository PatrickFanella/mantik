package cappedbook

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
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

var _ Broker = (*alpaca.PaperTrader)(nil)

type fakePaper struct {
	mu        sync.Mutex
	posts     int
	orders    map[string]alpaca.PaperOrder
	fills     []json.RawMessage
	ambiguous bool
}

func (b *fakePaper) PaperAccountID() string { return "paper-account" }
func (b *fakePaper) Submit(_ context.Context, o alpaca.StockOrder) (alpaca.PaperOrder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.posts++
	v := alpaca.PaperOrder{ID: "venue-" + o.ClientOrderID, ClientOrderID: o.ClientOrderID, Symbol: o.Symbol, Qty: o.Qty, Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce, LimitPrice: o.LimitPrice, Status: "new", FilledQty: "0"}
	b.orders[o.ClientOrderID] = v
	if b.ambiguous {
		return alpaca.PaperOrder{}, alpaca.ErrSubmissionUnknown
	}
	return v, nil
}
func (b *fakePaper) Order(_ context.Context, id string) (alpaca.PaperOrder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.orders[id]
	if !ok {
		return v, alpaca.ErrPaperNotSubmitted
	}
	return v, nil
}
func (b *fakePaper) Fills(context.Context, time.Time) ([]json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fills, nil
}
func paperFixture(t *testing.T) (Client, *fakePaper, time.Time) {
	t.Helper()
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	s := storage.NewMemory()
	b := &fakePaper{orders: map[string]alpaca.PaperOrder{}}
	_, e := s.CreateCappedBook(context.Background(), account.CappedConfig{ID: "book", BrokerAccountID: "paper-account", Type: account.Cash, OpenedAt: at, OpeningCash: "2000", DepositAmount: "500", EveryWeeks: 2})
	if e != nil {
		t.Fatal(e)
	}
	return Client{s, b, "book"}, b, at
}
func TestOwnBuyingPowerAndPendingReservations(t *testing.T) {
	c, b, at := paperFixture(t)
	o := account.BookOrder{ID: "big", Symbol: "SPY", Quantity: "50", Limit: "100", Side: "buy", Fee: "0", At: at}
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e == nil || b.posts != 0 {
		t.Fatal("broker's larger account bypassed book", e, b.posts)
	}
	o.ID = "first"
	o.Quantity = "16"
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e != nil {
		t.Fatal(e)
	}
	o.ID = "second"
	o.Quantity = "6"
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e == nil || b.posts != 1 {
		t.Fatal("pending reservation reused", e, b.posts)
	}
}
func TestDepositsMatchReplayAndApplyOnceAcrossRestarts(t *testing.T) {
	c, _, at := paperFixture(t)
	through := at.AddDate(0, 2, 0)
	s, e := c.Fund(context.Background(), through)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := account.SnapshotCapped(s, nil, through)
	if e != nil {
		t.Fatal(e)
	}
	due, e := engine.DefaultDeposits.Due(at, through)
	if e != nil || snapshot.Deposits != len(due) {
		t.Fatal(snapshot, e)
	}
	for i, v := range s.Events {
		if v.ID != due[i].ID || !v.EffectiveAt.Equal(due[i].EffectiveAt) {
			t.Fatal("paper/replay schedule mismatch", v, due[i])
		}
	}
	restarted := Client{Store: c.Store, Broker: c.Broker, BookID: "book"}
	again, e := restarted.Fund(context.Background(), through.Add(time.Hour))
	if e != nil || again.Version != s.Version {
		t.Fatal("deposit applied twice", again, e)
	}
}
func rawFill(id, order, qty string, at time.Time) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"order_id":%q,"symbol":"SPY","qty":%q,"price":"100","side":"buy","transaction_time":%q}`, id, "venue-"+order, qty, at.Format(time.RFC3339Nano)))
}
func TestPartialFillsReconcileOwnedShareAndDoNotResubmit(t *testing.T) {
	c, b, at := paperFixture(t)
	o := account.BookOrder{ID: "owned", Symbol: "SPY", Quantity: "10", Limit: "100", Side: "buy", Fee: "0", At: at}
	b.ambiguous = true
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); !errors.Is(e, alpaca.ErrSubmissionUnknown) {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("fill1", "owned", "4", at.Add(time.Minute)), rawFill("foreign", "other", "50", at.Add(time.Minute))}
	v := b.orders["owned"]
	v.FilledQty = "4"
	v.Status = "partially_filled"
	b.orders["owned"] = v
	snap, e := c.Sync(context.Background(), at.Add(2*time.Minute), map[string]string{"SPY": "100"})
	if e != nil || snap.Cash != "1600.000000000" || snap.Positions["SPY"] != "4.000000000" || len(snap.Pending) != 1 || b.posts != 1 {
		t.Fatal(snap, e, b.posts)
	}
	b.fills = append(b.fills, rawFill("fill2", "owned", "6", at.Add(3*time.Minute)))
	v.FilledQty = "10"
	v.Status = "filled"
	b.orders["owned"] = v
	snap, e = c.Sync(context.Background(), at.Add(4*time.Minute), map[string]string{"SPY": "100"})
	if e != nil || snap.Cash != "1000.000000000" || snap.Positions["SPY"] != "10.000000000" || len(snap.Pending) != 0 || b.posts != 1 {
		t.Fatal(snap, e)
	}
	again, e := c.Sync(context.Background(), at.Add(5*time.Minute), map[string]string{"SPY": "100"})
	if e != nil || again.Cash != snap.Cash {
		t.Fatal(again, e)
	}
	b.fills[0] = rawFill("fill1", "owned", "5", at.Add(time.Minute))
	if _, e = c.Sync(context.Background(), at.Add(6*time.Minute), map[string]string{"SPY": "100"}); e == nil {
		t.Fatal("rewritten fill accepted")
	}
	state, e := c.Store.CappedBook(context.Background(), "book")
	if e != nil {
		t.Fatal(e)
	}
	halted, e := account.SnapshotCapped(state, map[string]string{"SPY": "100"}, at.Add(7*time.Minute))
	if e != nil || !halted.Halted {
		t.Fatal("reconciliation breach did not halt", halted, e)
	}
}
func TestConcurrentReservationsCannotOverspend(t *testing.T) {
	c, _, at := paperFixture(t)
	s, e := c.Store.CappedBook(context.Background(), "book")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Go(func() {
			o := account.BookOrder{ID: id, Symbol: "SPY", Quantity: "15", Limit: "100", Side: "buy", Fee: "0", At: at}
			_, e := c.Store.ApplyCappedBook(context.Background(), "book", s.Version, account.BookCommand{ID: "reserve-" + id, Kind: "reserve", At: at, Order: &o, Marks: map[string]string{"SPY": "100"}})
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for e := range errs {
		if e == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatal(accepted)
	}
}

func (b *fakePaper) AbortUnsubmitted(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.orders[id]; ok {
		return alpaca.ErrSubmissionUnknown
	}
	return nil
}

func TestPreSubmissionCrashCanReleaseOnlyAfterAdapterAbort(t *testing.T) {
	c, b, at := paperFixture(t)
	s, e := c.Store.CappedBook(context.Background(), "book")
	if e != nil {
		t.Fatal(e)
	}
	o := account.BookOrder{ID: "crash", Symbol: "SPY", Quantity: "15", Limit: "100", Side: "buy", Fee: "0", At: at}
	_, e = c.Store.ApplyCappedBook(context.Background(), "book", s.Version, account.BookCommand{ID: "reserve-crash", Kind: "reserve", At: at, Order: &o, Marks: map[string]string{"SPY": "100"}})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := c.Sync(context.Background(), at.Add(time.Minute), map[string]string{"SPY": "100"})
	if e != nil || len(snap.Pending) != 0 || snap.Cash != "2000.000000000" || b.posts != 0 {
		t.Fatal("unstarted reservation stranded", snap, e)
	}
	again, e := c.Sync(context.Background(), at.Add(2*time.Minute), map[string]string{"SPY": "100"})
	if e != nil || len(again.Pending) != 0 || again.Cash != snap.Cash {
		t.Fatal("rejected reservation is not restart-idempotent", again, e)
	}
}
func TestInvalidBrokerOrderFailsBeforeReservation(t *testing.T) {
	c, b, at := paperFixture(t)
	o := account.BookOrder{ID: "bad", Symbol: "abc_def", Quantity: "1", Limit: "100", Side: "buy", Fee: "0", At: at}
	if _, e := c.submitLedgerTest(context.Background(), o, nil); e == nil {
		t.Fatal("invalid adapter symbol accepted")
	}
	s, e := c.Store.CappedBook(context.Background(), "book")
	if e != nil || s.Version != 0 || b.posts != 0 {
		t.Fatal("invalid order reserved capital", s, e)
	}
}

func TestDelayedOwnedFillIsReplayedBeforeLaterDeposit(t *testing.T) {
	c, b, at := paperFixture(t)
	o := account.BookOrder{ID: "late", Symbol: "SPY", Quantity: "10", Limit: "100", Side: "buy", Fee: "0", At: at}
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e != nil {
		t.Fatal(e)
	}
	friday := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	if _, e := c.Fund(context.Background(), friday); e != nil {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("delayed", "late", "10", at.Add(time.Minute))}
	v := b.orders["late"]
	v.FilledQty = "10"
	v.Status = "filled"
	b.orders["late"] = v
	snap, e := c.Sync(context.Background(), friday.Add(time.Minute), map[string]string{"SPY": "100"})
	if e != nil || snap.Cash != "1500.000000000" || snap.Positions["SPY"] != "10.000000000" || snap.Deposits != 1 {
		t.Fatal("late receipt could not reconcile chronologically", snap, e)
	}
}

func TestFirstInvalidOwnedFillDurablyHaltsAdmission(t *testing.T) {
	c, b, at := paperFixture(t)
	o := account.BookOrder{ID: "breach", Symbol: "SPY", Quantity: "1", Limit: "100", Side: "buy", Fee: "0", At: at}
	if _, e := c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e != nil {
		t.Fatal(e)
	}
	raw := rawFill("breach-fill", "breach", "1", at.Add(time.Minute))
	b.fills = []json.RawMessage{json.RawMessage(strings.Replace(string(raw), `"price":"100"`, `"price":"101"`, 1))}
	if _, e := c.Sync(context.Background(), at.Add(2*time.Minute), map[string]string{"SPY": "100"}); e == nil {
		t.Fatal("price breach accepted")
	}
	s, e := c.Store.CappedBook(context.Background(), "book")
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(s, map[string]string{"SPY": "100"}, at.Add(3*time.Minute))
	if e != nil || !snap.Halted {
		t.Fatal("new owned fill validation did not halt", snap, e)
	}
	o.ID = "after-breach"
	o.At = at.Add(3 * time.Minute)
	if _, e = c.submitLedgerTest(context.Background(), o, map[string]string{"SPY": "100"}); e == nil || b.posts != 1 {
		t.Fatal("halted book submitted another order", e, b.posts)
	}
}

func marginPaperFixture(t *testing.T) (Client, *fakePaper, time.Time) {
	t.Helper()
	c, b, at := paperFixture(t)
	s := storage.NewMemory()
	if _, err := s.CreateCappedBook(context.Background(), account.DefaultCappedConfig("book", "paper-account", account.Margin, at)); err != nil {
		t.Fatal(err)
	}
	c.Store = s
	return c, b, at
}
func TestIndependentPendingOrdersCannotNetMarginCapacity(t *testing.T) {
	for _, names := range [][3]string{{"a", "b", "c"}, {"z", "a", "m"}} {
		t.Run(names[0], func(t *testing.T) {
			c, b, at := marginPaperFixture(t)
			o := account.BookOrder{ID: names[0], Symbol: "SPY", Quantity: "40", Limit: "100", Side: "buy", Fee: "0", At: at}
			marks := map[string]string{"SPY": "100"}
			if _, err := c.submitLedgerTest(context.Background(), o, marks); err != nil {
				t.Fatal(err)
			}
			o.ID = names[1]
			o.Side = "sell"
			_, _ = c.submitLedgerTest(context.Background(), o, marks)
			o.ID = names[2]
			o.Side = "buy"
			if _, err := c.submitLedgerTest(context.Background(), o, marks); err == nil {
				t.Fatal("two independent buys can fill beyond the $4,000 margin limit while the offsetting sell remains unfilled")
			}
			if b.posts != 1 {
				t.Fatal("offsetting pending orders reached the broker", b.posts)
			}
		})
	}
}
func TestPendingReductionDoesNotReleaseCapacityForAnotherOrder(t *testing.T) {
	c, b, at := marginPaperFixture(t)
	marks := map[string]string{"SPY": "100"}
	o := account.BookOrder{ID: "holding", Symbol: "SPY", Quantity: "40", Limit: "100", Side: "buy", Fee: "0", At: at}
	if _, err := c.submitLedgerTest(context.Background(), o, marks); err != nil {
		t.Fatal(err)
	}
	b.fills = []json.RawMessage{rawFill("holding-fill", "holding", "40", at.Add(time.Minute))}
	status := b.orders["holding"]
	status.Status = "filled"
	status.FilledQty = "40"
	b.orders["holding"] = status
	if _, err := c.Sync(context.Background(), at.Add(2*time.Minute), marks); err != nil {
		t.Fatal(err)
	}
	o.ID = "a-reduce"
	o.Side = "sell"
	o.At = at.Add(3 * time.Minute)
	if _, err := c.submitLedgerTest(context.Background(), o, marks); err != nil {
		t.Fatal("an independent reduction must remain possible", err)
	}
	o.ID = "b-new"
	o.Side = "buy"
	o.Quantity = "1"
	o.At = at.Add(4 * time.Minute)
	if _, err := c.submitLedgerTest(context.Background(), o, marks); err == nil {
		t.Fatal("unfilled reduction released capacity for a new buy")
	}
	if b.posts != 2 {
		t.Fatal("unfunded new exposure reached the broker", b.posts)
	}
}

func TestEverySubsetOfAdmittedPendingOrdersFitsMargin(t *testing.T) {
	for _, scenario := range []struct{ cash, fee string }{{"2000", "0"}, {"5000", "5"}} {
		t.Run(scenario.cash, func(t *testing.T) {
			c, _, at := marginPaperFixture(t)
			s := storage.NewMemory()
			cfg := account.DefaultCappedConfig("book", "paper-account", account.Margin, at)
			cfg.OpeningCash = scenario.cash
			if _, err := s.CreateCappedBook(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			c.Store = s
			candidates := []account.BookOrder{}
			for i := 0; i < 8; i++ {
				symbol, qty, price := "SPY", "10", "100"
				if i%4 >= 2 {
					symbol, qty, price = "QQQ", "20", "50"
				}
				side := "buy"
				if i%2 == 1 {
					side = "sell"
				}
				candidates = append(candidates, account.BookOrder{ID: fmt.Sprintf("order-%d", i), Symbol: symbol, Quantity: qty, Limit: price, Side: side, Fee: scenario.fee, At: at})
			}
			admitted := []account.BookOrder{}
			for _, order := range candidates {
				if _, err := c.submitLedgerTest(context.Background(), order, map[string]string{"SPY": "100", "QQQ": "50"}); err == nil {
					admitted = append(admitted, order)
				}
			}
			if len(admitted) == 0 {
				t.Fatal("test admitted no orders")
			}
			for subset := 0; subset < 1<<len(admitted); subset++ {
				cash, _ := new(big.Rat).SetString(scenario.cash)
				funds := account.New(account.Margin, cash)
				positions := []account.Position{{Asset: "SPY", Quantity: new(big.Rat), Price: big.NewRat(100, 1)}, {Asset: "QQQ", Quantity: new(big.Rat), Price: big.NewRat(50, 1)}}
				for i, order := range admitted {
					if subset&(1<<i) == 0 {
						continue
					}
					q, _ := new(big.Rat).SetString(order.Quantity)
					if order.Side == "sell" {
						q.Neg(q)
					}
					price, _ := new(big.Rat).SetString(order.Limit)
					fee, _ := new(big.Rat).SetString(order.Fee)
					day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
					if code := funds.Execute(positions, account.Trade{Asset: order.Symbol, Quantity: q, Price: price, Fee: fee}, account.Overnight, day, account.SettlementDate(day)); code != "" {
						t.Fatalf("admitted subset %b cannot execute order %s independently: %s", subset, order.ID, code)
					}
					for j := range positions {
						if positions[j].Asset == order.Symbol {
							positions[j].Quantity.Add(positions[j].Quantity, q)
						}
					}
				}
				status := funds.Status(positions)
				if status.Gross.Cmp(status.OvernightBuyingPower) > 0 {
					t.Fatalf("admitted subset %b exceeded margin", subset)
				}
			}
		})
	}
}

func TestPendingCoverReservesItsAdversePriceBeforeNewExposure(t *testing.T) {
	c, b, at := marginPaperFixture(t)
	marks := map[string]string{"SPY": "100", "QQQ": "100"}
	order := account.BookOrder{ID: "short", Symbol: "SPY", Quantity: "10", Limit: "100", Side: "sell", Fee: "0", At: at}
	if _, err := c.submitLedgerTest(context.Background(), order, marks); err != nil {
		t.Fatal(err)
	}
	raw := rawFill("short-fill", "short", "10", at.Add(time.Minute))
	b.fills = []json.RawMessage{json.RawMessage(strings.Replace(string(raw), `"side":"buy"`, `"side":"sell"`, 1))}
	status := b.orders["short"]
	status.Status = "filled"
	status.FilledQty = "10"
	b.orders["short"] = status
	if _, err := c.Sync(context.Background(), at.Add(2*time.Minute), marks); err != nil {
		t.Fatal(err)
	}
	order.ID = "cover"
	order.Side = "buy"
	order.Limit = "150"
	order.At = at.Add(3 * time.Minute)
	if _, err := c.submitLedgerTest(context.Background(), order, marks); err != nil {
		t.Fatal("a reduction must remain possible", err)
	}
	order.ID = "new-exposure"
	order.Symbol = "QQQ"
	order.Quantity = "20"
	order.Limit = "100"
	order.At = at.Add(4 * time.Minute)
	if _, err := c.submitLedgerTest(context.Background(), order, marks); err == nil {
		t.Fatal("pending cover could consume $500 and put the book below margin minimum before this buy fills")
	}
	if b.posts != 2 {
		t.Fatal("exposure reached the broker without reserving adverse cover price", b.posts)
	}
}

// submitLedgerTest exercises the pre-distributor ledger contract. New public
// admission/bypass tests use Submit and Rebalance in portfolio_test.go.
func (c Client) submitLedgerTest(ctx context.Context, o account.BookOrder, marks map[string]string) (alpaca.PaperOrder, error) {
	return c.submit(ctx, o, marks, "reserve")
}
