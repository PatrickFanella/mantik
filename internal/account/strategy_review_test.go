package account

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func TestReviewDeclarationsReplayFullRoster(t *testing.T) {
	at := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	s, err := NewCappedState(DefaultCappedConfig("review-book", "paper", Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	req := allocation.RebalanceRequest{ID: "roster", At: at}
	for i := range 64 {
		id := fmt.Sprintf("sleeve-%d", i)
		req.Sleeves = append(req.Sleeves, allocation.Sleeve{ID: id, StrategyID: fmt.Sprintf("strategy-%064x", i), GateID: "gate-" + id, Stage: "incubation", Risk: "1", Minimum: "0"})
		req.Targets = append(req.Targets, allocation.SleeveTarget{SleeveID: id, Decision: allocation.TargetDecision{HoldExposure: true}})
	}
	s, err = ApplyCappedCommand(s, BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req})
	if err != nil {
		t.Fatal(err)
	}
	// Build a retained history directly so the assertion exercises a single
	// normal snapshot/restart replay of the supported roster size.
	for i, owner := range req.Sleeves {
		d := ReviewDeclaration{ID: fmt.Sprintf("comparison-%d", i), SleeveID: owner.ID, StrategyID: owner.StrategyID, GateID: owner.GateID, Policy: ReviewPolicy, Benchmark: "cash", BacktestUnavailable: "no retained comparison", DrawdownBudget: "0.1"}
		s.Events = append(s.Events, BookCommand{ID: "review-declare-" + d.ID, Kind: "review_declare", At: at.Add(time.Second), ReviewDeclaration: &d})
	}
	s.Version = int64(len(s.Events))
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored CappedState
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err = SnapshotCapped(restored, nil, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Ownership must still be checked at each declaration's original prefix.
	changed := *restored.Events[len(restored.Events)-1].ReviewDeclaration
	changed.StrategyID = req.Sleeves[0].StrategyID
	restored.Events[len(restored.Events)-1].ReviewDeclaration = &changed
	if _, err = SnapshotCapped(restored, nil, at.Add(2*time.Second)); err == nil {
		t.Fatal("mismatched declaration owner accepted")
	}
}

func TestReviewSizingUsesStrictDecisionClockAndOwnerControls(t *testing.T) {
	at := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	sid := "strategy-" + strings.Repeat("a", 64)
	req := allocation.RebalanceRequest{ID: "next", At: at.Add(time.Second), Sleeves: []allocation.Sleeve{{ID: "one", StrategyID: sid, Stage: "incubation", Risk: "1", Minimum: "0"}}, Targets: []allocation.SleeveTarget{{SleeveID: "one", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	for _, offset := range []time.Duration{-time.Second, 0, time.Nanosecond} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			decision := at.Add(offset)
			s := CappedState{Events: []BookCommand{{ID: "review", At: at, Review: &StrategyReview{SleeveID: "one", Scale: "0.5"}}, {ID: "plan", Incubation: &IncubationPlan{DecidedAt: decision, Rebalance: req}}}}
			r := &bookReplay{funds: New(Cash, big.NewRat(2000, 1)), positions: map[string]*big.Rat{}, orders: map[string]*openOrder{}}
			request, snap := reviewSizing(s, BookCommand{ID: req.ID, At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}}, r)
			out, e := allocation.Distribute(request, snap)
			if e != nil {
				t.Fatal(e)
			}
			want := "100.000000000"
			if offset > 0 {
				want = "50.000000000"
			}
			if out.Sleeves[0].Budget != want {
				t.Fatal(out)
			}
			if req.Targets[0].Decision.Targets[0].Weight != "1" {
				t.Fatal("retained target rewritten")
			}
		})
	}
	// Retirement is independent of a resumable breaker; neither resume nor a
	// loss-baseline reset can unretire a strategy. Reducing orders remain legal.
	s := risk.State{At: at, Equity: "2000", NAV: "1", HighNAV: "1", DayNAV: "1"}
	for _, a := range []string{"retire_strategy", "resume_strategy", "resume", "owner_clear_halt"} {
		var e error
		s, e = risk.ApplyControl(s, risk.Control{Action: a, StrategyID: sid})
		if e != nil {
			t.Fatal(e)
		}
	}
	if !s.Retired[sid] {
		t.Fatal("resurrected")
	}
	for _, reduce := range []bool{false, true} {
		reasons, e := risk.Check(s, risk.OrderView{StrategyID: sid, Equity: "2000", CashAfter: "1900", Gross: "100", Position: "100", Sleeve: "100", Reducing: reduce})
		if e != nil || (len(reasons) > 0) == reduce {
			t.Fatal(reasons, e)
		}
	}
}

func TestOwnerPausePreservesExplicitProtectiveFlatTarget(t *testing.T) {
	at := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	sid := "strategy-" + strings.Repeat("a", 64)
	req := allocation.RebalanceRequest{ID: "expiry-exit", At: at, Sleeves: []allocation.Sleeve{{ID: "one", StrategyID: sid, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "0"}}, Targets: []allocation.SleeveTarget{{SleeveID: "one", Decision: allocation.TargetDecision{}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	r := &bookReplay{funds: New(Cash, big.NewRat(2000, 1)), positions: map[string]*big.Rat{"SPY": big.NewRat(1, 10)}, orders: map[string]*openOrder{"owned": {order: BookOrder{ID: "owned", SleeveID: "one", Symbol: "SPY", Quantity: "0.1", Side: "buy"}, filled: big.NewRat(1, 10), fee: new(big.Rat)}}, risk: risk.State{Breakers: map[string]bool{sid: true}}}
	request, snap := reviewSizing(CappedState{}, BookCommand{ID: req.ID, At: at, Rebalance: &req, Marks: map[string]string{"SPY": "100"}}, r)
	out, e := allocation.Distribute(request, snap)
	if e != nil || len(out.Orders) != 1 || !out.Orders[0].ReduceOnly || out.Orders[0].Side != "sell" {
		t.Fatal("pause suppressed protective flat exit", out, e)
	}
}

func TestStrategyReviewAttributionExcludesOtherSleeves(t *testing.T) {
	at := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	state := CappedState{Events: []BookCommand{
		{Kind: "portfolio_reserve", Order: &BookOrder{ID: "mine", SleeveID: "one", Symbol: "SPY"}},
		{Kind: "portfolio_reserve", Order: &BookOrder{ID: "other", SleeveID: "two", Symbol: "QQQ"}},
		{Kind: "fill", Fill: &BookFill{OrderID: "mine", Symbol: "SPY", Quantity: "0.1", Price: "100", Fee: "0.1", Side: "buy"}, ModelFill: &IncubationFill{ModelPrice: "100.02", ModelFee: "0.1", CostPenalty: "0.2"}},
		{Kind: "fill", Fill: &BookFill{OrderID: "other", Symbol: "QQQ", Quantity: "10", Price: "1", Fee: "0", Side: "buy"}, ModelFill: &IncubationFill{ModelPrice: "1", ModelFee: "0", CostPenalty: "0"}},
	}}
	mark := ReviewMark{Prices: map[string]string{"SPY": "110"}, SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at}
	pnl, adverse, fills, ok := reviewAttribution(state, "one", mark, at)
	if !ok || pnl != "0.700000000000" || adverse != "0.000000000000" || fills != 1 {
		t.Fatal("other sleeve or optimistic penalty entered attribution", pnl, adverse, fills, ok)
	}
	mark.Prices = nil
	if _, _, _, ok = reviewAttribution(state, "one", mark, at); ok {
		t.Fatal("missing owned mark synthesized")
	}
}
