package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

func incubationJournalContract(t *testing.T, s CappedBookStore) {
	t.Helper()
	ctx := context.Background()
	gateStorageContracts(t, s.(strategygate.Store))
	gate, e := s.(strategygate.Store).StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	decision := gate.DeclaredAt.Add(time.Minute)
	execution := decision.Add(time.Second)
	state, e := s.CreateCappedBook(ctx, account.DefaultCappedConfig("incubation", "incubation-paper", account.Cash, decision))
	if e != nil {
		t.Fatal(e)
	}
	sid := gate.Declaration.StrategyID
	req := allocation.RebalanceRequest{ID: "inc-cycle", At: execution, Sleeves: []allocation.Sleeve{{ID: "model", StrategyID: sid, GateID: "retained-gate", Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
	plan := account.IncubationPlan{InputSHA256: strings.Repeat("c", 64), DecidedAt: decision, Rebalance: req, Costs: map[string]executioncost.Model{"model": executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: "model", StrategyID: sid, State: []byte("retained")}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", OccurredAt: execution, ReceivedAt: execution, EvidenceClass: "prospective_market_unqualified", SourceSHA256: strings.Repeat("d", 64)}}}
	plan.Health = &workhealth.Report{At: execution, Checks: map[string]workhealth.Check{"data_freshness": {Ready: false, Status: "unavailable", Code: "fixture_stale_bars", At: execution, EvidenceRefs: []string{"input:" + plan.InputSHA256}}}, Work: map[string]workhealth.Check{"entry": {Ready: false, Status: "blocked", Code: "new_risk_not_admitted", At: execution}, "exit": {Ready: true, Status: "ready", Code: "health_independent_safety_work", At: execution}}}
	// Contract-only admission fixture, never real observation/qualification proof.
	command := account.BookCommand{ID: "cycle", Kind: "incubation_plan", At: execution, Incubation: &plan}
	for _, c := range []account.BookCommand{
		{ID: "forged-plan", Kind: "risk_observe", At: execution, Incubation: &plan},
		{ID: "forged-model", Kind: "risk_observe", At: execution, ModelFill: &account.IncubationFill{CostPenalty: "-1000"}},
	} {
		if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, c); e == nil {
			t.Fatal("unrelated command retained incubation payload", c.ID)
		}
	}
	unchanged, e := s.CappedBook(ctx, state.Config.ID)
	if e != nil || unchanged.Version != state.Version {
		t.Fatal("refused payload changed journal", unchanged, e)
	}
	badPlan := plan
	badCost := executioncost.Default()
	badCost.FeeBps = "100"
	badPlan.Costs = map[string]executioncost.Model{"model": badCost}
	badCommand := command
	badCommand.Incubation = &badPlan
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, badCommand); e == nil || !strings.Contains(e.Error(), "cost model differs from gate") {
		t.Fatal("unfrozen cost model accepted", e)
	}
	state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, command)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := s.ApplyCappedBook(ctx, state.Config.ID, 0, command)
	if e != nil || retry.Version != 1 {
		t.Fatal("plan retry", retry, e)
	}
	marks := map[string]string{"SPY": "100"}
	altered := req
	altered.Targets = []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{}}}
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: execution, Rebalance: &altered, Marks: marks}); e == nil {
		t.Fatal("sizing changed retained strategy decision")
	}
	state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: execution, Rebalance: &req, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, marks, execution)
	if e != nil || len(snap.Distribution.Orders) != 1 {
		t.Fatal(snap, e)
	}
	sized := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: sized.ID, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, At: sized.At, ReduceOnly: sized.ReduceOnly}
	for _, c := range []account.BookCommand{{ID: "reserve", Kind: "portfolio_reserve", At: execution, Order: &order, Marks: marks}, {ID: "bind", Kind: "bind", At: execution, OrderID: order.ID, VenueOrderID: "venue"}} {
		state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, c)
		if e != nil {
			t.Fatal(e)
		}
	}
	f := account.BookFill{ID: "fill", OrderID: order.ID, VenueOrderID: "venue", Symbol: "SPY", Quantity: order.Quantity, Price: "100", Fee: "0", Side: "buy", SourceSHA256: strings.Repeat("e", 64), At: execution.Add(time.Second)}
	model, e := account.ModelIncubationFill(state, f)
	if e != nil || model == nil {
		t.Fatal(model, e)
	}
	fill := account.BookCommand{ID: f.ID, Kind: "fill", At: f.At, Fill: &f, ModelFill: model, Marks: marks}
	bad := fill
	bad.ModelFill = nil
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, bad); e == nil {
		t.Fatal("missing modeled result accepted")
	}
	state, e = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, fill)
	if e != nil {
		t.Fatal(e)
	}
	before, e := account.IncubationReturns(state, marks, f.At)
	if e != nil || before.QualificationCredit || before.Fills != 1 || before.CostPenalty == "0.000000000000" {
		t.Fatal(before, e)
	}
	retained, e := s.CappedBook(ctx, state.Config.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(state)
	again, _ := json.Marshal(retained)
	if string(raw) != string(again) {
		t.Fatal("journal bytes changed")
	}
	after, e := account.IncubationReturns(retained, marks, f.At)
	if e != nil || before != after {
		t.Fatal(before, after, e)
	}
	oldVersion := retained.Version
	if retried, e := s.ApplyCappedBook(ctx, state.Config.ID, 0, fill); e != nil || retried.Version != oldVersion {
		t.Fatal("fill retry", retried, e)
	}
	changed := *model
	changed.CostPenalty = "0"
	bad = fill
	bad.ModelFill = &changed
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, oldVersion, bad); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed comparison accepted", e)
	}
	// A newly started trial fences a new plan, as well as sizing/reservations.
	_, e = s.(strategytrial.Store).StartStrategyTrial(ctx, strategytrial.Start{ID: "later-search", Family: gate.Declaration.Family, StrategyID: sid, Spec: gate.Declaration.Spec, Config: json.RawMessage(`{"new":true}`), DataSHA256: gate.Declaration.DataSHA256, EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
	if e != nil {
		t.Fatal(e)
	}
	plan.DecidedAt = f.At
	plan.Rebalance.ID = "inc-next"
	plan.Rebalance.At = f.At.Add(time.Second)
	q := plan.Quotes["SPY"]
	q.OccurredAt = plan.Rebalance.At
	q.ReceivedAt = q.OccurredAt
	plan.Quotes["SPY"] = q
	command = account.BookCommand{ID: "next", Kind: "incubation_plan", At: plan.Rebalance.At, Incubation: &plan}
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, oldVersion, command); e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") {
		t.Fatal("new stale plan accepted", e)
	}
	plan.Rebalance.ID = req.ID
	if _, e = s.ApplyCappedBook(ctx, state.Config.ID, oldVersion, command); e == nil || !strings.Contains(e.Error(), "allocation already") {
		t.Fatal("old venue orders retrospectively acquired a model", e)
	}

}
func TestMemoryIncubationJournal(t *testing.T) { incubationJournalContract(t, NewMemory()) }
func TestPostgresContractsIncubationJournal(t *testing.T) {
	p := timerPostgres(t, "incubation_journal")
	incubationJournalContract(t, p)
	reopened, e := OpenPostgres(context.Background(), p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	state, e := reopened.CappedBook(context.Background(), "incubation")
	if e != nil {
		t.Fatal(e)
	}
	if state.Events[0].Incubation.Health == nil || state.Events[0].Incubation.Health.Checks["data_freshness"].Code != "fixture_stale_bars" || !state.Events[0].Incubation.Health.Work["exit"].Ready || !state.Events[0].Incubation.Health.At.Equal(state.Events[0].At) {
		t.Fatal("SQL restart lost health evidence", state)
	}
	if len(state.Events) != 5 || state.Events[4].ModelFill == nil || state.Events[0].Incubation.Decisions[0].State[0] != 'r' {
		t.Fatal("SQL restart lost plan/model", state)
	}
}
