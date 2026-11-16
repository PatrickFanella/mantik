package allocation

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func sizedFixture() (RebalanceRequest, SizingSnapshot) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	r := RebalanceRequest{ID: "day-one", At: at, Sleeves: []Sleeve{{ID: "trend", StrategyID: "strategy-" + strings.Repeat("a", 64), Stage: "incubation", Risk: "0.01", Minimum: "1"}}, Targets: []SleeveTarget{{SleeveID: "trend", Decision: TargetDecision{Targets: []WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "333"}, Fees: map[string]string{"SPY": "0"}}
	return r, SizingSnapshot{Equity: "2000", Marks: map[string]string{"SPY": "333"}}
}
func TestDistributorFractionalWaitAndDepositRebalance(t *testing.T) {
	r, s := sizedFixture()
	r.Sleeves[0].Minimum = "110"
	a, e := Distribute(r, s)
	if e != nil || len(a.Orders) != 0 || a.Sleeves[0].Reason != "minimum_balance" {
		t.Fatal(a, e)
	}
	s.Equity = "2500"
	a, e = Distribute(r, s)
	if e != nil || len(a.Orders) != 1 || a.Sleeves[0].Budget != "125.000000000" || a.Orders[0].Quantity != "0.375375375" {
		t.Fatal(a, e)
	}
	s.PendingSleeves = []string{"trend"}
	a, e = Distribute(r, s)
	if e != nil || len(a.Orders) != 0 || a.Sleeves[0].Reason != "pending_orders" {
		t.Fatal(a, e)
	}
}
func TestQualifiedSleevesStartEqualByRiskWithoutSpendingReserve(t *testing.T) {
	r, s := sizedFixture()
	r.Sleeves = nil
	r.Targets = nil
	s.Equity = "10000"
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("s%d", i)
		vol := "2"
		if i == 0 {
			vol = "1"
		}
		r.Sleeves = append(r.Sleeves, Sleeve{ID: id, StrategyID: "strategy-" + fmt.Sprintf("%064x", i), Stage: "qualified", Risk: vol, Minimum: "1"})
		r.Targets = append(r.Targets, SleeveTarget{SleeveID: id, Decision: TargetDecision{KeepPositions: true}})
	}
	a, e := Distribute(r, s)
	if e != nil {
		t.Fatal(e)
	}
	first, _ := new(big.Rat).SetString(a.Sleeves[0].Budget)
	second, _ := new(big.Rat).SetString(a.Sleeves[1].Budget)
	delta := new(big.Rat).Sub(first, new(big.Rat).Mul(second, big.NewRat(2, 1)))
	if new(big.Rat).Abs(delta).Cmp(big.NewRat(1, 1_000_000)) > 0 {
		t.Fatal("not equal by risk", a)
	}
	total := new(big.Rat)
	for _, v := range a.Sleeves {
		b, _ := new(big.Rat).SetString(v.Budget)
		total.Add(total, b)
		if b.Cmp(big.NewRat(2000, 1)) > 0 {
			t.Fatal("sleeve cap", v)
		}
	}
	if total.Cmp(big.NewRat(6501, 1)) >= 0 {
		t.Fatal("spent reserved pool/cash", total)
	}
}
func TestDistributorRejectsUnboundedOrDuplicateTargetsAndUsesSmallerForwardPool(t *testing.T) {
	r, s := sizedFixture()
	r.Sleeves[0].ModelDriven = true
	r.Sleeves[0].Stage = "forward_only"
	a, e := Distribute(r, s)
	if e != nil || a.Sleeves[0].Budget != "20.000000000" {
		t.Fatal(a, e)
	}
	r.Targets[0].Decision.Targets = append(r.Targets[0].Decision.Targets, WeightTarget{Asset: "SPY", Weight: "1"})
	if _, e = Distribute(r, s); e == nil {
		t.Fatal("duplicate target accepted")
	}
}

func TestUnderfundedAndInsolventSleevesCanStillFlatten(t *testing.T) {
	r, s := sizedFixture()
	r.Sleeves[0].Minimum = "1000"
	s.Positions = []SizingPosition{{SleeveID: "trend", Symbol: "SPY", Quantity: "1"}}
	for _, equity := range []string{"2000", "0", "-100"} {
		s.Equity = equity
		d, e := Distribute(r, s)
		if e != nil || len(d.Orders) != 1 || d.Orders[0].Side != "sell" || d.Orders[0].Quantity != "1.000000000" {
			t.Fatal("underfunding trapped exit", equity, d, e)
		}
	}
}

func TestForwardExposureHoldDoesNotResizeAfterDeposit(t *testing.T) {
	req, snap := sizedFixture()
	snap.PreviousTargets = map[string]TargetDecision{"trend": req.Targets[0].Decision}
	snap.Positions = []SizingPosition{{SleeveID: "trend", Symbol: "SPY", Quantity: "0.1"}}
	snap.Equity = "3000"
	req.Targets[0].Decision = TargetDecision{HoldExposure: true}
	held, e := Distribute(req, snap)
	if e != nil || len(held.Orders) != 0 || FlatTarget(req.Targets[0].Decision) {
		t.Fatal(held, e)
	}
	req.Targets[0].Decision = TargetDecision{KeepPositions: true}
	legacy, e := Distribute(req, snap)
	if e != nil || len(legacy.Orders) != 1 {
		t.Fatal("legacy weight reuse changed", legacy, e)
	}
	for _, bad := range []TargetDecision{{HoldExposure: true, KeepPositions: true}, {HoldExposure: true, Targets: []WeightTarget{{Asset: "SPY", Weight: "1"}}}} {
		req.Targets[0].Decision = bad
		if _, e = Distribute(req, snap); e == nil {
			t.Fatal("ambiguous exposure hold accepted", bad)
		}
	}
}

func TestLegacyTargetJSONRetainsHashInput(t *testing.T) {
	raw, e := json.Marshal(TargetDecision{})
	if e != nil || string(raw) != `{"Targets":null,"KeepPositions":false}` {
		t.Fatal(string(raw), e)
	}
}
