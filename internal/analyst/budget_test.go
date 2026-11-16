package analyst

import (
	"context"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	compiled "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"strings"
	"testing"
	"time"
)

func reviewApply(t *testing.T, s *storage.Memory, c account.BookCommand) account.CappedState {
	t.Helper()
	state, e := s.CappedBook(context.Background(), "fixture-book")
	if e != nil {
		t.Fatal(e)
	}
	state, e = s.ApplyCappedBook(context.Background(), "fixture-book", state.Version, c)
	if e != nil {
		t.Fatal(c.Kind, e)
	}
	return state
}

// All incumbent commands pass the real memory store's gate and book validation.
func reviewIncumbent(t *testing.T, s *storage.Memory, q Request, symbol, weight string, filled bool) account.CappedState {
	t.Helper()
	ctx := context.Background()
	at := q.AsOf.Add(-5 * time.Second)
	spec := strategy.Spec{Name: "incumbent", Version: "1", Data: []string{strategy.KindDailyBars}}
	sid, e := spec.ID()
	if e != nil {
		t.Fatal(e)
	}
	g, e := s.DeclareStrategyGate(ctx, strategygate.Declaration{ID: "incumbent-gate", Family: "incumbent-family", StrategyID: sid, Spec: marshal(spec), Policy: "backtest-gate-v1", Benchmark: "cash", Minimum: "1", Account: "cash", Balance: "2000", DataSHA256: strings.Repeat("b", 64), Assets: []string{symbol}})
	if e != nil {
		t.Fatal(e)
	}
	h, _ := strategygate.Hash(g.Declaration)
	_, e = s.RecordStrategyGate(ctx, strategygate.Decision{ID: g.Declaration.ID, StrategyID: sid, DeclarationSHA256: h, FamilySHA256: strings.Repeat("c", 64), Policy: "backtest-gate-v1", Status: "passed", Stage: "incubation", EvidenceClass: "historical_market_unqualified", Minimum: "1", At: g.DeclaredAt, Checks: []strategygate.Check{{Code: "fixture", Passed: true, Reason: "isolated acceptance reproduction"}}})
	if e != nil {
		t.Fatal(e)
	}
	sleeve := allocation.Sleeve{ID: "incumbent", StrategyID: sid, GateID: g.Declaration.ID, Stage: "incubation", Risk: "1", Minimum: "1"}
	req := allocation.RebalanceRequest{ID: "incumbent-allocation", At: at, Sleeves: []allocation.Sleeve{sleeve}, Targets: []allocation.SleeveTarget{{SleeveID: sleeve.ID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: symbol, Weight: weight}}}}}, Limits: map[string]string{symbol: "100"}, Fees: map[string]string{symbol: "0"}}
	state := reviewApply(t, s, account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: map[string]string{symbol: "100"}})
	snap, e := account.SnapshotCapped(state, map[string]string{symbol: "100"}, at)
	if e != nil || len(snap.Distribution.Orders) != 1 {
		t.Fatal(snap, e)
	}
	order := snap.Distribution.Orders[0]
	o := account.BookOrder{ID: order.ID, AllocationID: order.AllocationID, SleeveID: order.SleeveID, StrategyID: order.StrategyID, Symbol: order.Symbol, Quantity: order.Quantity, Limit: order.Limit, Side: order.Side, Fee: order.Fee, At: at}
	state = reviewApply(t, s, account.BookCommand{ID: "incumbent-reserve", Kind: "portfolio_reserve", At: at, Order: &o, Marks: map[string]string{symbol: "100"}})
	if !filled {
		return state
	}
	reviewApply(t, s, account.BookCommand{ID: "incumbent-bind", Kind: "bind", At: at, OrderID: o.ID, VenueOrderID: "owned-venue"})
	f := account.BookFill{ID: "incumbent-fill", OrderID: o.ID, VenueOrderID: "owned-venue", Symbol: symbol, Quantity: o.Quantity, Price: "100", Fee: "0", Side: o.Side, SourceSHA256: strings.Repeat("d", 64), At: at}
	return reviewApply(t, s, account.BookCommand{ID: f.ID, Kind: "fill", At: at, Fill: &f, Marks: map[string]string{symbol: "100"}})
}

func TestReviewHealthyRiskReceiptDuringCall(t *testing.T) {
	r, q, s, _, c := fixture(t, "1")
	q.Data.EvidenceClass = "prospective_market_unqualified"
	c.q = q
	now := q.AsOf
	r.now = func() time.Time { return now }
	c.after = func(n int) {
		if n == 1 {
			now = now.Add(time.Second)
			state := reviewApply(t, s, account.BookCommand{ID: "healthy-safety-tick", Kind: "risk_observe", At: now, Marks: map[string]string{"SPY": "101"}})
			snap, e := account.SnapshotCapped(state, map[string]string{"SPY": "101"}, now)
			if e != nil || snap.Risk.Halted || snap.Risk.Kill || snap.Risk.Paused || snap.Risk.ReduceOnly {
				t.Fatal("unhealthy setup", snap, e)
			}
		}
	}
	c.transport = func(record *model.Record) { record.FinishedAt = now }
	out, e := r.Run(context.Background(), q)
	t.Logf("healthy book receipt at input+1s: status=%s reason=%s calls=%d hold=%v err=%v", out.Status, out.Reason, len(c.calls), out.Target.HoldExposure, e)
	if e != nil || out.Status != "target" || len(c.calls) != 4 {
		t.Fatalf("healthy safety observation must not abort frozen-data workflow: %+v, %v", out, e)
	}
}

func TestReviewOtherAssetOwnedPosition(t *testing.T) {
	r, q, s, _, c := fixture(t, "1")
	q.Data.EvidenceClass = "prospective_market_unqualified"
	c.q = q
	state := reviewIncumbent(t, s, q, "QQQ", "0.1", true)
	snap, e := account.SnapshotCapped(state, map[string]string{"SPY": "101", "QQQ": "100"}, q.AsOf)
	if e != nil || snap.Equity != "2000.000000000" || snap.Positions["QQQ"] != "0.100000000" {
		t.Fatal("invalid retained holding", snap, e)
	}
	direct := r.admission(context.Background(), q, q.AsOf)
	out, e := r.Run(context.Background(), q)
	t.Logf("valid QQQ=0.1 holding; complete-mark equity=%s; runner admission=%v status=%s calls=%d", snap.Equity, direct, out.Status, len(c.calls))
	if e != nil || out.Status != "target" || len(c.calls) != 4 {
		t.Fatalf("unrelated valid holding must permit funded analyst: %+v, %v", out, e)
	}
}

func TestReviewPoolAlreadyCommitted(t *testing.T) {
	for _, filled := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "filled"}[filled], func(t *testing.T) {
			r, q, s, _, c := fixture(t, "1")
			q.Data.EvidenceClass = "prospective_market_unqualified"
			q.Data.Bars[0].Close = "100"
			c.q = q
			state := reviewIncumbent(t, s, q, "SPY", "1", filled)
			_, e := account.SnapshotCapped(state, map[string]string{"SPY": "100"}, q.AsOf)
			if e != nil {
				t.Fatal(e)
			}
			out, e := r.Run(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			if len(c.calls) != 0 || out.Status != "no_target" || !out.Target.HoldExposure {
				t.Fatalf("all 5%% incubation capacity already committed; runner still spent %d calls", len(c.calls))
			}
		})
	}
}

func TestCurrentBudgetMarksAreIndependentAndUnavailableExplicitly(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "future", "trusted", "features_frozen"} {
		t.Run(mode, func(t *testing.T) {
			r, q, s, _, c := fixture(t, "1")
			state := reviewIncumbent(t, s, q, "QQQ", "0.1", true)
			var err error
			switch mode {
			case "missing":
				// Accepted older financial receipts can lack a newer valuation. They are
				// not permission to borrow a mark from the model's daily input.
				r.now = func() time.Time { return q.AsOf.Add(56 * time.Second) }
			case "stale":
				r.now = func() time.Time { return q.AsOf.Add(time.Minute) }
			case "future":
				reviewApply(t, s, account.BookCommand{ID: "future-observation", Kind: "risk_observe", At: q.AsOf.Add(time.Second), Marks: map[string]string{"QQQ": "100"}})
			case "trusted", "features_frozen":
				reviewApply(t, s, account.BookCommand{ID: "complete-current-observation", Kind: "risk_observe", At: q.AsOf, Marks: map[string]string{"QQQ": "100", "SPY": "102"}})
			}
			if mode == "missing" {
				// Exercise an explicit missing-mark state without rewriting retained events:
				// a view-only store may return a valid book with no marks for empty cash;
				// a held book must refuse empty marks at the bounded budget helper.
				roster, e := account.PortfolioRoster(state)
				if e != nil {
					t.Fatal(e)
				}
				req := allocation.RebalanceRequest{ID: "missing-preview", At: q.AsOf}
				for _, owner := range roster {
					req.Sleeves = append(req.Sleeves, owner)
				}
				if _, e = account.PreviewPortfolioBudgets(state, req, nil, q.AsOf); e == nil {
					t.Fatal("missing held mark guessed as zero")
				}
				return
			}
			out, err := r.Run(context.Background(), q)
			if mode == "stale" || mode == "future" {
				if err != nil || out.Status != "no_target" || len(c.calls) != 0 {
					t.Fatal("unavailable valuation spent calls", out, err)
				}
				return
			}
			if err != nil || out.Status != "target" || len(c.calls) != 4 {
				t.Fatal(out, err)
			}
			v, _ := view(context.Background(), q)
			input, _ := compiled.Input(v)
			for _, call := range c.calls {
				if strings.Contains(call.Prompt, `"QQQ"`) || !strings.Contains(call.Prompt, "Input: "+string(input)+"\nPrior") {
					t.Fatal("current valuation expanded or changed frozen features")
				}
			}
		})
	}
}

func TestCurrentBudgetRechecksCommitmentsAndControlsDuringEveryStage(t *testing.T) {
	for _, step := range []int{1, 2, 3, 4} {
		for _, mode := range []string{"pending", "filled", "kill", "future_book", "healthy"} {
			t.Run(fmt.Sprintf("%d/%s", step, mode), func(t *testing.T) {
				r, q, s, _, c := fixture(t, "1")
				now := q.AsOf
				r.now = func() time.Time { return now }
				c.after = func(n int) {
					if n != step {
						return
					}
					now = now.Add(time.Second)
					switch mode {
					case "pending", "filled":
						later := q
						later.AsOf = now.Add(5 * time.Second)
						reviewIncumbent(t, s, later, "SPY", "1", mode == "filled")
					case "kill":
						reviewApply(t, s, account.BookCommand{ID: "current-kill", Kind: "risk_control", At: now, RiskControl: &risk.Control{Action: "kill"}})
					case "future_book":
						reviewApply(t, s, account.BookCommand{ID: "future-book", Kind: "risk_observe", At: now.Add(time.Second), Marks: map[string]string{"SPY": "100"}})
					case "healthy":
						reviewApply(t, s, account.BookCommand{ID: "healthy", Kind: "risk_observe", At: now, Marks: map[string]string{"SPY": "100"}})
					}
				}
				out, err := r.Run(context.Background(), q)
				if mode == "healthy" {
					if err != nil || out.Status != "target" || len(c.calls) != 4 {
						t.Fatal(out, err)
					}
					return
				}
				if err != nil || out.Status != "no_target" || out.Reason != "admission_unavailable" || len(c.calls) != step {
					t.Fatal("current fence bypassed", out, err, len(c.calls))
				}
			})
		}
	}
}

func TestCurrentBudgetDoesNotAssumeOtherOwnersWillResize(t *testing.T) {
	for _, filled := range []bool{false, true} {
		for _, weight := range []string{"0.8", "0.9", "0.995"} {
			t.Run(fmt.Sprintf("%v/%s", filled, weight), func(t *testing.T) {
				r, q, s, _, c := fixture(t, "1")
				q.Data.Bars[0].Close = "100"
				c.q = q
				reviewIncumbent(t, s, q, "SPY", weight, filled)
				out, err := r.Run(context.Background(), q)
				// A full unit analyst target needs its nominal 1% version budget ($20).
				// The old owner retains $80/$90/$99.50, including pending openings.
				want := 0
				if weight == "0.8" {
					want = 4
				}
				if err != nil || len(c.calls) != want || (out.Status == "target") != (want == 4) {
					t.Fatal(out, err, len(c.calls))
				}
			})
		}
	}
}
