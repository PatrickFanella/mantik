package cappedbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// portfolioFixture supplies a trusted fixture admission to isolate the order
// boundary. Backtest evidence/decision validation has its own gate tests.
func portfolioFixture(t *testing.T, mode ...string) (Client, *fakePaper, allocation.RebalanceRequest, map[string]string) {
	t.Helper()
	c, b, at := paperFixture(t, mode...)
	spec := strategy.Spec{Name: "fixture", Version: "1", Data: []string{strategy.KindDailyBars}}
	sid, e := spec.ID()
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(spec)
	if e != nil {
		t.Fatal(e)
	}
	d := strategygate.Declaration{Account: "cash", ID: "gate", Family: "portfolio", StrategyID: sid, Spec: raw, Balance: "2000", Policy: "backtest-gate-v1", Benchmark: "cash", DataSHA256: strings.Repeat("a", 64)}
	store := c.Store.(strategygate.Store)
	record, e := store.DeclareStrategyGate(context.Background(), d)
	if e != nil {
		t.Fatal(e)
	}
	h, e := strategygate.Hash(d)
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.RecordStrategyGate(context.Background(), strategygate.Decision{ID: d.ID, StrategyID: sid, DeclarationSHA256: h, FamilySHA256: strings.Repeat("b", 64), Policy: d.Policy, EvidenceClass: "historical_market_unqualified", Status: "passed", Stage: "incubation", Minimum: "1", At: record.DeclaredAt, Checks: []strategygate.Check{{Code: "fixture_admission", Passed: true, Reason: "isolated_order_boundary_fixture"}}})
	if e != nil {
		t.Fatal(e)
	}
	r := allocation.RebalanceRequest{ID: "rebalance-one", At: at, Sleeves: []allocation.Sleeve{{ID: "trend", StrategyID: sid, Stage: "incubation", GateID: "gate", Risk: "0.02"}}, Targets: []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	return c, b, r, map[string]string{"SPY": "100"}
}

func TestRebalanceRetryReturnsItsOwnDistribution(t *testing.T) {
	for _, delay := range []time.Duration{0, time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			c, _, r, marks := portfolioFixture(t)
			ctx := context.Background()
			first, e := c.Rebalance(ctx, r, marks)
			if e != nil {
				t.Fatal(e)
			}
			second := r
			second.ID = "rebalance-two"
			second.At = second.At.Add(delay)
			second.Targets = []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "0.5"}}}}}
			if _, e = c.Rebalance(ctx, second, marks); e != nil {
				t.Fatal(e)
			}
			before, e := c.load(ctx)
			if e != nil {
				t.Fatal(e)
			}
			retried, e := c.Rebalance(ctx, r, marks)
			if e != nil {
				t.Fatal("retained retry failed after a later rebalance", e)
			}
			wantHash, _ := strategygate.Hash(first)
			gotHash, _ := strategygate.Hash(retried)
			if gotHash != wantHash {
				t.Fatal("retry returned another distribution", first, retried)
			}
			after, e := c.load(ctx)
			if e != nil || after.Version != before.Version {
				t.Fatal("retry changed retained book", before.Version, after.Version, e)
			}
		})
	}
}

func TestRetainedRebalanceRetrySurvivesNewFamilySearch(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	first, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal(e)
	}
	if e = startExtraSearch(ctx, c.Store.(*storage.Memory)); e != nil {
		t.Fatal(e)
	}
	before, e := c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	retried, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal("lost-response retry rechecked stale research", e)
	}
	wantHash, _ := strategygate.Hash(first)
	gotHash, _ := strategygate.Hash(retried)
	if gotHash != wantHash {
		t.Fatal("lost-response retry changed distribution", first, retried)
	}
	after, e := c.load(ctx)
	if e != nil || after.Version != before.Version {
		t.Fatal("retained retry changed journal", e)
	}
	changed := r
	changed.Targets = []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "0.5"}}}}}
	if _, e = c.Rebalance(ctx, changed, marks); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed retained request accepted", e)
	}
	if _, e = c.Rebalance(ctx, r, map[string]string{"SPY": "101"}); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed retained marks accepted", e)
	}
	for _, change := range []string{"clock", "risk", "gate", "fee"} {
		changed = r
		changed.Sleeves = append([]allocation.Sleeve(nil), r.Sleeves...)
		switch change {
		case "clock":
			changed.At = changed.At.Add(time.Minute)
		case "risk":
			changed.Sleeves[0].Risk = "0.03"
		case "gate":
			changed.Sleeves[0].GateID = "another-gate"
		case "fee":
			changed.Fees = map[string]string{"SPY": "1"}
		}
		if _, e = c.Rebalance(ctx, changed, marks); !errors.Is(e, account.ErrBookConflict) {
			t.Fatal("changed retained request accepted", change, e)
		}
	}
	next := r
	next.ID = "new-after-search"
	if _, e = c.Rebalance(ctx, next, marks); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") {
		t.Fatal("new rebalance bypassed freshness", e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(first.Orders[0]), marks); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") || b.posts != 0 {
		t.Fatal("old distribution opened exposure after new search", e, b.posts)
	}
}

func TestRetainedFlatRebalanceRetryUsesOriginalRoster(t *testing.T) {
	c, _, r, marks := portfolioFixture(t)
	ctx := context.Background()
	if _, e := c.Rebalance(ctx, r, marks); e != nil {
		t.Fatal(e)
	}
	flat := r
	flat.ID = "flat"
	flat.Sleeves = append([]allocation.Sleeve(nil), r.Sleeves...)
	flat.Sleeves[0].Risk = "9" // flat requests restore the retained sleeve
	flat.Targets = []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{}}}
	want, e := c.Rebalance(ctx, flat, marks)
	if e != nil {
		t.Fatal(e)
	}
	next := r
	next.ID = "changed-roster"
	next.Sleeves = append([]allocation.Sleeve(nil), r.Sleeves...)
	next.Sleeves[0].Risk = "0.03"
	if _, e = c.Rebalance(ctx, next, marks); e != nil {
		t.Fatal(e)
	}
	if e = startExtraSearch(ctx, c.Store.(*storage.Memory)); e != nil {
		t.Fatal(e)
	}
	got, e := c.Rebalance(ctx, flat, marks)
	if e != nil {
		t.Fatal("retained flat retry used current roster or research", e)
	}
	wantHash, _ := strategygate.Hash(want)
	gotHash, _ := strategygate.Hash(got)
	if gotHash != wantHash {
		t.Fatal("flat retry changed distribution", got, want)
	}
}

type gateClassBook struct {
	*storage.Memory
	evidenceClass string
}

func (s *gateClassBook) StrategyGate(ctx context.Context, id string) (strategygate.Record, error) {
	r, e := s.Memory.StrategyGate(ctx, id)
	if e == nil && r.Decision != nil {
		r.Decision.EvidenceClass = s.evidenceClass
	}
	return r, e
}

func TestRebalanceRefusesSyntheticAndLegacyUnclassifiedGates(t *testing.T) {
	for _, class := range []string{"", "synthetic_fixture"} {
		t.Run(class, func(t *testing.T) {
			c, b, r, marks := portfolioFixture(t)
			c.Store = &gateClassBook{Memory: c.Store.(*storage.Memory), evidenceClass: class}
			before, e := c.load(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if _, e = c.Rebalance(context.Background(), r, marks); e == nil || !strings.Contains(e.Error(), "gate_evidence_ineligible") {
				t.Fatal("ineligible gate admitted", class, e)
			}
			after, e := c.load(context.Background())
			if e != nil || after.Version != before.Version || b.posts != 0 {
				t.Fatal("refused gate changed book or reached broker", e, b.posts)
			}
		})
	}
}

func TestPublicReservationsReconcileAmbiguousAndCanceledPartialFills(t *testing.T) {
	for _, kind := range []string{"ambiguous", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			c, b, r, marks := portfolioFixture(t)
			ctx := context.Background()
			d, e := c.Rebalance(ctx, r, marks)
			if e != nil {
				t.Fatal(e)
			}
			o := portfolioOrder(d.Orders[0])
			b.ambiguous = kind == "ambiguous"
			_, e = c.Submit(ctx, o, marks)
			if kind == "ambiguous" {
				if e == nil {
					t.Fatal("missing unknown submission")
				}
			} else if e != nil {
				t.Fatal(e)
			}
			qty, status := "1", "filled"
			if kind == "canceled" {
				qty, status = "0.4", "canceled"
			}
			b.fills = []json.RawMessage{rawFill("public-fill", o.ID, qty, r.At.Add(time.Minute))}
			v := b.orders[o.ID]
			v.FilledQty, v.Status = qty, status
			b.orders[o.ID] = v
			snap, e := c.Sync(ctx, r.At.Add(2*time.Minute), marks)
			if e != nil || len(snap.Pending) != 0 || snap.Positions["SPY"] == "" || b.posts != 1 {
				t.Fatal("public reservation was not reconciled", snap, e, b.posts)
			}
			if kind == "ambiguous" && snap.Cash != "1900.000000000" {
				t.Fatal("owned ambiguous fill treated as foreign", snap)
			}
		})
	}
}

func TestReconciliationCanHaltAgainAfterExplicitOwnerClear(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal(e)
	}
	o := portfolioOrder(d.Orders[0])
	if _, e = c.Submit(ctx, o, marks); e != nil {
		t.Fatal(e)
	}
	bad := strings.Replace(string(rawFill("bad-fill", o.ID, "1", r.At.Add(time.Minute))), `"price":"100"`, `"price":"101"`, 1)
	b.fills = []json.RawMessage{json.RawMessage(bad)}
	if _, e = c.Sync(ctx, r.At.Add(2*time.Minute), marks); e == nil {
		t.Fatal("invalid price accepted")
	}
	if _, e = c.Control(ctx, "owner-clear", r.At.Add(3*time.Minute), risk.Control{Action: "owner_clear_halt"}); e != nil {
		t.Fatal(e)
	}
	state, e := c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, marks, r.At.Add(3*time.Minute))
	if e != nil || snap.Halted {
		t.Fatal("owner clear failed", snap, e)
	}
	if _, e = c.Sync(ctx, r.At.Add(4*time.Minute), marks); e == nil {
		t.Fatal("repeated discrepancy ignored")
	}
	state, e = c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	snap, e = account.SnapshotCapped(state, marks, r.At.Add(4*time.Minute))
	if e != nil || !snap.Halted {
		t.Fatal("second halt identity conflicted", snap, e)
	}
}

func TestDistributorLimitBoundFitsItsOwnRiskReservation(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	r.Limits["SPY"] = "101"
	d, e := c.Rebalance(context.Background(), r, marks)
	if e != nil || len(d.Orders) != 1 {
		t.Fatal(d, e)
	}
	if d.Orders[0].Quantity == "1.000000000" {
		t.Fatal("sized using mark rather than execution bound", d)
	}
	if _, e = c.Submit(context.Background(), portfolioOrder(d.Orders[0]), marks); e != nil || b.posts != 1 {
		t.Fatal("distributor produced unexecutable risk size", e)
	}
}

type interleavedBook struct {
	*storage.Memory
	trigger string
	before  func(context.Context) error
}

func (s *interleavedBook) ApplyCappedBook(ctx context.Context, id string, v int64, c account.BookCommand) (account.CappedState, error) {
	if c.Kind == s.trigger && s.before != nil {
		f := s.before
		s.before = nil
		if e := f(ctx); e != nil {
			return account.CappedState{}, e
		}
	}
	return s.Memory.ApplyCappedBook(ctx, id, v, c)
}
func startExtraSearch(ctx context.Context, s *storage.Memory) error {
	g, e := s.StrategyGate(ctx, "gate")
	if e != nil {
		return e
	}
	_, e = s.StartStrategyTrial(ctx, strategytrial.Start{ID: "additional-search", Family: g.Declaration.Family, StrategyID: g.Declaration.StrategyID, Spec: g.Declaration.Spec, Config: json.RawMessage(`{"search":"additional"}`), DataSHA256: g.Declaration.DataSHA256, EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
	return e
}

func TestFamilyStartCannotRaceRebalanceOrNewReservation(t *testing.T) {
	for _, kind := range []string{"rebalance", "portfolio_reserve"} {
		t.Run(kind, func(t *testing.T) {
			c, b, r, marks := portfolioFixture(t)
			ctx := context.Background()
			var order account.BookOrder
			if kind == "portfolio_reserve" {
				d, e := c.Rebalance(ctx, r, marks)
				if e != nil {
					t.Fatal(e)
				}
				order = portfolioOrder(d.Orders[0])
			}
			m := c.Store.(*storage.Memory)
			c.Store = &interleavedBook{Memory: m, trigger: kind, before: func(ctx context.Context) error { return startExtraSearch(ctx, m) }}
			var e error
			if kind == "rebalance" {
				_, e = c.Rebalance(ctx, r, marks)
			} else {
				_, e = c.Submit(ctx, order, marks)
			}
			if e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") || b.posts != 0 {
				t.Fatal("interleaved search bypassed admission", e, b.posts)
			}
		})
	}
}

func TestStaleSearchHistoryDoesNotTrapAnExplicitFlatExit(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal(e)
	}
	o := portfolioOrder(d.Orders[0])
	if _, e = c.Submit(ctx, o, marks); e != nil {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("entry", o.ID, "1", r.At.Add(time.Minute))}
	v := b.orders[o.ID]
	v.FilledQty, v.Status = "1", "filled"
	b.orders[o.ID] = v
	if _, e = c.Sync(ctx, r.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	if e = startExtraSearch(ctx, c.Store.(*storage.Memory)); e != nil {
		t.Fatal(e)
	}
	r.ID = "stale-exit"
	r.At = r.At.Add(3 * time.Minute)
	r.Targets[0].Decision = allocation.TargetDecision{}
	d, e = c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 1 || !d.Orders[0].ReduceOnly {
		t.Fatal("stale gate trapped flattening", d, e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(d.Orders[0]), marks); e != nil {
		t.Fatal("research availability blocked exit", e)
	}
}

func admitPortfolioVersion(t *testing.T, c Client, name string, model bool) (string, string) {
	t.Helper()
	spec := strategy.Spec{Name: name, Version: "1", Data: []string{strategy.KindDailyBars}, ModelDriven: model}
	sid, e := spec.ID()
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(spec)
	if e != nil {
		t.Fatal(e)
	}
	d := strategygate.Declaration{ID: name, Family: name, Account: "cash", StrategyID: sid, Spec: raw, Balance: "2000", Minimum: "1", Policy: "backtest-gate-v1", Benchmark: "cash", DataSHA256: strings.Repeat("a", 64)}
	store := c.Store.(strategygate.Store)
	r, e := store.DeclareStrategyGate(context.Background(), d)
	if e != nil {
		t.Fatal(e)
	}
	h, e := strategygate.Hash(d)
	if e != nil {
		t.Fatal(e)
	}
	status, evidenceClass := "passed", "historical_market_unqualified"
	if model {
		status = "forward_only"
		evidenceClass = "forward_only"
	}
	_, e = store.RecordStrategyGate(context.Background(), strategygate.Decision{ID: name, StrategyID: sid, DeclarationSHA256: h, FamilySHA256: strings.Repeat("b", 64), Policy: d.Policy, EvidenceClass: evidenceClass, Status: status, Stage: "incubation", Minimum: "1", At: r.DeclaredAt, Checks: []strategygate.Check{{Code: "fixture_admission", Passed: true, Reason: "isolated_order_boundary_fixture"}}})
	if e != nil {
		t.Fatal(e)
	}
	return name, sid
}

func TestIncubationCannotOmitHeldSleevesOrSpendPendingReduction(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal(e)
	}
	entry := portfolioOrder(d.Orders[0])
	if _, e = c.Submit(ctx, entry, marks); e != nil {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("pool-entry", entry.ID, "1", r.At.Add(time.Minute))}
	v := b.orders[entry.ID]
	v.FilledQty, v.Status = "1", "filled"
	b.orders[entry.ID] = v
	if _, e = c.Sync(ctx, r.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	gate, sid := admitPortfolioVersion(t, c, "other-version", false)
	next := r
	next.ID = "roster-next"
	next.At = r.At.Add(3 * time.Minute)
	next.Limits = map[string]string{"SPY": "100", "QQQ": "100"}
	next.Fees = map[string]string{"SPY": "0", "QQQ": "0"}
	marks["QQQ"] = "100"
	next.Sleeves = []allocation.Sleeve{{ID: "other", StrategyID: sid, Stage: "incubation", GateID: gate, Risk: "0.02"}}
	next.Targets = []allocation.SleeveTarget{{SleeveID: "other", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "QQQ", Weight: "1"}}}}}
	if _, e = c.Rebalance(ctx, next, marks); e == nil || !strings.Contains(e.Error(), "active_sleeve_omitted") {
		t.Fatal("omitted holdings multiplied incubation pool", e)
	}
	next.Sleeves = append([]allocation.Sleeve{r.Sleeves[0]}, next.Sleeves...)
	next.Targets = append([]allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{KeepPositions: true}}}, next.Targets...)
	d, e = c.Rebalance(ctx, next, marks)
	if e != nil || len(d.Orders) != 2 {
		t.Fatal("roster did not resize retained signal", d, e)
	}
	var trim, add account.BookOrder
	for _, o := range d.Orders {
		if o.SleeveID == "trend" {
			trim = portfolioOrder(o)
		} else {
			add = portfolioOrder(o)
		}
	}
	if _, e = c.Submit(ctx, add, marks); e == nil || !strings.Contains(e.Error(), "risk_incubation_pool") || b.posts != 1 {
		t.Fatal("pending reduction funded pool expansion", e, b.posts)
	}
	if _, e = c.Submit(ctx, trim, marks); e != nil {
		t.Fatal("pool reduction blocked", e)
	}
	fill := strings.Replace(string(rawFill("pool-trim", trim.ID, trim.Quantity, next.At.Add(time.Minute))), `"side":"buy"`, `"side":"sell"`, 1)
	b.fills = append(b.fills, json.RawMessage(fill))
	v = b.orders[trim.ID]
	v.FilledQty, v.Status = trim.Quantity, "filled"
	b.orders[trim.ID] = v
	if _, e = c.Sync(ctx, next.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	next.ID = "roster-after-trim"
	next.At = next.At.Add(3 * time.Minute)
	d, e = c.Rebalance(ctx, next, marks)
	if e != nil || len(d.Orders) != 1 {
		t.Fatal(d, e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(d.Orders[0]), marks); e != nil {
		t.Fatal("settled pool capacity could not be used", e)
	}
}

func TestVersionOwnershipSurvivesOmissionAndForwardOnlyUsesOnePercent(t *testing.T) {
	for _, model := range []bool{false, true} {
		t.Run(fmt.Sprint(model), func(t *testing.T) {
			c, _, r, marks := portfolioFixture(t)
			if model {
				gate, sid := admitPortfolioVersion(t, c, "model-version", true)
				r.Sleeves[0].GateID, r.Sleeves[0].StrategyID = gate, sid
			}
			d, e := c.Rebalance(context.Background(), r, marks)
			if e != nil {
				t.Fatal(e)
			}
			if model && d.Sleeves[0].Budget != "20.000000000" {
				t.Fatal("forward-only cap", d)
			}
			r.ID = "alias-plan"
			r.Sleeves[0].ID = "alias"
			r.Targets[0].SleeveID = "alias"
			if _, e = c.Rebalance(context.Background(), r, marks); e == nil || !strings.Contains(e.Error(), "strategy_version_already_owns_sleeve") {
				t.Fatal("one version acquired another sleeve", e)
			}
		})
	}
}

func TestConcurrentPublicReservationsShareOneIncubationPool(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	gate, sid := admitPortfolioVersion(t, c, "concurrent-version", false)
	r.Sleeves = append(r.Sleeves, allocation.Sleeve{ID: "other", StrategyID: sid, Stage: "incubation", GateID: gate, Risk: "0.02"})
	r.Targets = append(r.Targets, allocation.SleeveTarget{SleeveID: "other", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}})
	d, e := c.Rebalance(context.Background(), r, marks)
	if e != nil || len(d.Orders) != 2 {
		t.Fatal(d, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, order := range d.Orders {
		wg.Go(func() { _, e := c.Submit(context.Background(), portfolioOrder(order), marks); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	state, e := c.load(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, marks, r.At)
	if e != nil || len(snap.Pending) != 2 || snap.Pending[0].Quantity != "0.500000000" || snap.Pending[1].Quantity != "0.500000000" || b.posts != 2 {
		t.Fatal("concurrent admissions changed pool", snap, e, b.posts)
	}
}
func portfolioOrder(v allocation.SizedOrder) account.BookOrder {
	return account.BookOrder{ReduceOnly: v.ReduceOnly, ID: v.ID, AllocationID: v.AllocationID, SleeveID: v.SleeveID, StrategyID: v.StrategyID, Symbol: v.Symbol, Quantity: v.Quantity, Limit: v.Limit, Side: v.Side, Fee: v.Fee, At: v.At}
}

func TestPublicSubmitRequiresExactDistributorOrderBeforeAnyBrokerPost(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	if _, e := c.Submit(ctx, account.BookOrder{ID: "raw", Symbol: "SPY", Quantity: "1", Limit: "100", Side: "buy", Fee: "0", At: r.At}, marks); e == nil || b.posts != 0 {
		t.Fatal("raw target bypassed distributor", e)
	}
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 1 {
		t.Fatal(d, e)
	}
	o := portfolioOrder(d.Orders[0])
	changed := o
	changed.Quantity = "2"
	if _, e = c.Submit(ctx, changed, marks); e == nil || b.posts != 0 {
		t.Fatal("quantity bypassed distributor", e)
	}
	if _, e = c.Submit(ctx, o, marks); e != nil || b.posts != 1 {
		t.Fatal("valid sized order failed", e, b.posts)
	}
	r.ID = "second"
	r.At = r.At.Add(time.Minute)
	d, e = c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 0 || d.Sleeves[0].Reason != "pending_orders" {
		t.Fatal("pending order reissued", d, e)
	}
}
func TestDepositSpreadsAtNextRebalanceAndFeesStillFitHardPositionCap(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil || d.Orders[0].Quantity != "1.000000000" {
		t.Fatal(d, e)
	}
	r.ID = "after-deposit"
	r.At = r.At.AddDate(0, 0, 4)
	r.Fees["SPY"] = "1"
	d, e = c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 1 || d.Orders[0].Quantity != "1.249500000" {
		t.Fatal(d, e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(d.Orders[0]), marks); e != nil || b.posts != 1 {
		t.Fatal("own fee made sized order exceed hard cap", e)
	}
}
func TestKillAppliesToAlreadyReservedButNotSubmittedOrderAndResumeDoesNotClearHalt(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil {
		t.Fatal(e)
	}
	o := portfolioOrder(d.Orders[0])
	s, e := c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s, e = c.Store.ApplyCappedBook(ctx, c.BookID, s.Version, account.BookCommand{ID: "reserve-" + o.ID, Kind: "portfolio_reserve", At: o.At, Order: &o, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Control(ctx, "kill", r.At, risk.Control{Action: "kill"}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Submit(ctx, o, marks); e == nil || !strings.Contains(e.Error(), "risk_kill_switch") || b.posts != 0 {
		t.Fatal("reserved order escaped kill", e, b.posts)
	}
	if _, e = c.append(ctx, account.BookCommand{ID: "halt", Kind: "halt", At: r.At}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Control(ctx, "resume", r.At, risk.Control{Action: "resume"}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Control(ctx, "unkill", r.At, risk.Control{Action: "unkill"}); e != nil {
		t.Fatal(e)
	}
	restarted := Client{Store: c.Store, Broker: c.Broker, BookID: c.BookID}
	if _, e = restarted.Submit(ctx, o, marks); e == nil || !strings.Contains(e.Error(), "risk_hard_halt") {
		t.Fatal("restart/resume cleared halt", e)
	}
}
func TestHaltStillAllowsSizedReductionAndCannotTurnItIntoAFlip(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	if _, e := c.Rebalance(ctx, r, marks); e != nil {
		t.Fatal(e)
	}
	// Existing unmanaged Wave8 holdings are retained during the upgrade. A risk
	// breach must allow closing those holdings rather than trapping the book.
	o := account.BookOrder{ID: "legacy", SleeveID: "trend", StrategyID: r.Sleeves[0].StrategyID, Symbol: "SPY", Quantity: "20", Limit: "100", Side: "buy", Fee: "0", At: r.At}
	if _, e := c.submitLedgerTest(ctx, o, marks); e != nil {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("legacy-fill", o.ID, "20", r.At.Add(time.Minute))}
	v := b.orders[o.ID]
	v.FilledQty = "20"
	v.Status = "filled"
	b.orders[o.ID] = v
	if _, e := c.Sync(ctx, r.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	marks["SPY"] = "85"
	snap, e := c.Sync(ctx, r.At.Add(3*time.Minute), marks)
	if e != nil || !snap.Halted {
		t.Fatal("loss halt not retained", snap, e)
	}
	r.ID = "emergency-exit"
	r.At = r.At.Add(4 * time.Minute)
	r.Limits["SPY"] = "85"
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 1 || d.Orders[0].Side != "sell" {
		t.Fatal(d, e)
	}
	exit := portfolioOrder(d.Orders[0])
	flip := exit
	flip.Quantity = "21"
	if _, e = c.Submit(ctx, flip, marks); e == nil {
		t.Fatal("reduce-only order became a short")
	}
	if _, e = c.Submit(ctx, exit, marks); e != nil {
		t.Fatal("halt trapped reducing exit", e)
	}
	s, e := c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	snap, e = account.SnapshotCapped(s, marks, r.At)
	if e != nil || !snap.Halted {
		t.Fatal("exit reset halt", snap, e)
	}
}

func TestNewAssetCannotIncreaseRiskWhileAnotherPositionBreachesItsCap(t *testing.T) {
	c, b, r, marks := portfolioFixture(t)
	ctx := context.Background()
	o := account.BookOrder{ID: "existing", SleeveID: "legacy", Symbol: "SPY", Quantity: "2", Limit: "100", Side: "buy", Fee: "0", At: r.At}
	if _, e := c.submitLedgerTest(ctx, o, marks); e != nil {
		t.Fatal(e)
	}
	b.fills = []json.RawMessage{rawFill("existing-fill", o.ID, "2", r.At.Add(time.Minute))}
	v := b.orders[o.ID]
	v.FilledQty = "2"
	v.Status = "filled"
	b.orders[o.ID] = v
	if _, e := c.Sync(ctx, r.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	r.At = r.At.Add(3 * time.Minute)
	r.Targets[0].Decision.Targets[0].Asset = "QQQ"
	r.Limits["QQQ"] = "100"
	r.Fees["QQQ"] = "0"
	marks["QQQ"] = "100"
	d, e := c.Rebalance(ctx, r, marks)
	if e != nil || len(d.Orders) != 1 {
		t.Fatal(d, e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(d.Orders[0]), marks); e == nil || !strings.Contains(e.Error(), "risk_position_limit") || b.posts != 1 {
		t.Fatal("unrelated position breach ignored", e, b.posts)
	}
}
