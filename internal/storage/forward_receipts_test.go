package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
)

func forwardLateReceiptContract(t *testing.T, s forwardBackend) {
	t.Helper()
	ctx := context.Background()
	d, at := forwardFixture(t, s)
	if _, e := s.DeclareForward(ctx, d, func() time.Time { return at }); e != nil {
		t.Fatal(e)
	}
	execution := d.WindowStart.Add(time.Second)
	state, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	plan := account.IncubationPlan{ForwardDeclarations: map[string]string{d.SleeveID: d.ID}, InputSHA256: strings.Repeat("c", 64), DecidedAt: d.WindowStart, Costs: map[string]executioncost.Model{d.SleeveID: executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: d.SleeveID, StrategyID: d.StrategyID}}, Rebalance: allocation.RebalanceRequest{ID: "late-fill-allocation", At: execution, Sleeves: []allocation.Sleeve{{ID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: strings.Repeat("d", 64), EvidenceClass: "prospective_market_unqualified", OccurredAt: execution, ReceivedAt: execution}}}
	marks := map[string]string{"SPY": "100"}
	for _, c := range []account.BookCommand{
		{ID: "late-fill-plan", Kind: "incubation_plan", At: execution, Incubation: &plan},
		{ID: plan.Rebalance.ID, Kind: "rebalance", At: execution, Rebalance: &plan.Rebalance, Marks: marks},
	} {
		state, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, c)
		if e != nil {
			t.Fatal(e)
		}
	}
	snap, e := account.SnapshotCapped(state, marks, execution)
	if e != nil || len(snap.Distribution.Orders) != 1 {
		t.Fatal(snap, e)
	}
	sized := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: sized.ID, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, At: sized.At, ReduceOnly: sized.ReduceOnly}
	for _, c := range []account.BookCommand{
		{ID: "late-fill-reserve", Kind: "portfolio_reserve", At: execution, Order: &order, Marks: marks},
		{ID: "late-fill-bind", Kind: "bind", At: execution, OrderID: order.ID, VenueOrderID: "late-fill-venue"},
	} {
		state, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, c)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, f := range []account.BookFill{
		{ID: "seen-later-first", OrderID: order.ID, VenueOrderID: "late-fill-venue", Symbol: "SPY", Quantity: "0.01", Price: "100", Fee: "0", Side: "buy", SourceSHA256: strings.Repeat("e", 64), At: execution.Add(20 * time.Second)},
		{ID: "discovered-earlier", OrderID: order.ID, VenueOrderID: "late-fill-venue", Symbol: "SPY", Quantity: "0.01", Price: "100", Fee: "0", Side: "buy", SourceSHA256: strings.Repeat("f", 64), At: execution.Add(10 * time.Second)},
	} {
		model, e := account.ModelIncubationFill(state, f)
		if e != nil {
			t.Fatal(e)
		}
		state, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: f.ID, Kind: "fill", At: execution.Add(30 * time.Second), Fill: &f, ModelFill: model, Marks: marks})
		if e != nil {
			t.Fatal(e)
		}
	}
	retained, e := s.CappedBook(ctx, d.BookID)
	if e != nil || !reflect.DeepEqual(retained, state) {
		t.Fatal("receipt order changed on read", e)
	}
	expiry := execution.Add(10*time.Second + time.Duration(d.MaxHoldingSeconds)*time.Second)
	before, e := s.ForwardEligibility(ctx, d.ID, expiry.Add(-time.Nanosecond))
	if e != nil || before.HoldExpired || before.QualificationCredit {
		t.Fatal(before, e)
	}
	exact, e := s.ForwardEligibility(ctx, d.ID, expiry)
	if e != nil || !exact.HoldExpired || exact.QualificationCredit {
		t.Fatal("late receipt extended deadline", exact, e)
	}
}

func TestMemoryForwardLateReceipt(t *testing.T) { forwardLateReceiptContract(t, NewMemory()) }
func TestPostgresContractsForwardLateReceiptRestart(t *testing.T) {
	p := timerPostgres(t, "forward_late_receipt")
	forwardLateReceiptContract(t, p)
	ctx := context.Background()
	before, e := p.CappedBook(ctx, "forward-book")
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.CappedBook(ctx, "forward-book")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed receipt journal", e)
	}
	r, e := reopened.ForwardDeclaration(ctx, "forward-one")
	if e != nil {
		t.Fatal(e)
	}
	expiry := r.Declaration.WindowStart.Add(time.Second + 10*time.Second + time.Duration(r.Declaration.MaxHoldingSeconds)*time.Second)
	value, e := reopened.ForwardEligibility(ctx, r.Declaration.ID, expiry)
	if e != nil || !value.HoldExpired || value.QualificationCredit {
		t.Fatal("restart lost execution deadline", value, e)
	}
}
