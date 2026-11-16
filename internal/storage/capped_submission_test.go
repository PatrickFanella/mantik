package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

type cappedSubmissionStore interface {
	CappedBookStore
	strategygate.Store
	CheckCappedSubmission(context.Context, string, account.BookOrder, map[string]string) (account.CappedState, error)
	BeginStrategyTrialBatch(context.Context, string, string) error
}

func cappedSubmissionContracts(t *testing.T, store cappedSubmissionStore) {
	t.Helper()
	ctx := context.Background()
	gateStorageContracts(t, store)
	gate, err := store.StrategyGate(ctx, "retained-gate")
	if err != nil {
		t.Fatal(err)
	}
	at := gate.DeclaredAt.Add(time.Second)
	state, err := store.CreateCappedBook(ctx, account.DefaultCappedConfig("submission-book", "submission-account", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]string{"SPY": "100"}
	req := allocation.RebalanceRequest{ID: "submission-entry", At: at, Sleeves: []allocation.Sleeve{{ID: "model", StrategyID: gate.Declaration.StrategyID, GateID: gate.Declaration.ID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: marks, Fees: map[string]string{"SPY": "0"}}
	apply := func(cmd account.BookCommand) {
		t.Helper()
		var e error
		state, e = store.ApplyCappedBook(ctx, state.Config.ID, state.Version, cmd)
		if e != nil {
			t.Fatal(cmd.Kind, e)
		}
	}
	apply(account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks})
	snap, err := account.SnapshotCapped(state, marks, at)
	if err != nil {
		t.Fatal(err)
	}
	orderOf := func(o allocation.SizedOrder) account.BookOrder {
		return account.BookOrder{ID: o.ID, AllocationID: o.AllocationID, SleeveID: o.SleeveID, StrategyID: o.StrategyID, Symbol: o.Symbol, Quantity: o.Quantity, Limit: o.Limit, Side: o.Side, Fee: o.Fee, At: o.At, ReduceOnly: o.ReduceOnly}
	}
	entry := orderOf(snap.Distribution.Orders[0])
	if _, err = store.CheckCappedSubmission(ctx, state.Config.ID, entry, marks); err == nil {
		t.Fatal("unreserved order admitted")
	}
	apply(account.BookCommand{ID: "reserve-entry", Kind: "portfolio_reserve", At: at, Order: &entry, Marks: marks})
	checked, err := store.CheckCappedSubmission(ctx, state.Config.ID, entry, marks)
	if err != nil || checked.Version != state.Version {
		t.Fatal(checked, err)
	}
	checked.Events[0].Rebalance.Sleeves[0].Risk = "0.5"
	retained, err := store.CappedBook(ctx, state.Config.ID)
	if err != nil || retained.Events[0].Rebalance.Sleeves[0].Risk != "1" {
		t.Fatal("submission read escaped immutable custody", err)
	}
	changed := entry
	changed.ReduceOnly = true
	if _, err = store.CheckCappedSubmission(ctx, state.Config.ID, changed, marks); !errors.Is(err, account.ErrBookConflict) {
		t.Fatal("changed order bypassed final admission", err)
	}
	if err = store.BeginStrategyTrialBatch(ctx, "submission-pending-family", gate.Declaration.Family); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CheckCappedSubmission(ctx, state.Config.ID, entry, marks); !errors.Is(err, strategytrial.ErrFamilyPending) {
		t.Fatal("pending family admitted at final fence", err)
	}
	apply(account.BookCommand{ID: "owner-retirement", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: entry.StrategyID}})
	if _, err = store.CheckCappedSubmission(ctx, state.Config.ID, entry, marks); err == nil || !strings.Contains(err.Error(), "risk_strategy_retired") {
		t.Fatal("retirement missed at final fence", err)
	}
	apply(account.BookCommand{ID: "bind-entry", Kind: "bind", At: at, OrderID: entry.ID, VenueOrderID: "venue-entry"})
	apply(account.BookCommand{ID: "fill-entry", Kind: "fill", At: at, Fill: &account.BookFill{ID: "fill-entry", OrderID: entry.ID, VenueOrderID: "venue-entry", Symbol: "SPY", Quantity: entry.Quantity, Price: "100", Fee: "0", Side: "buy", SourceSHA256: strings.Repeat("d", 64), At: at}, Marks: marks})
	req.ID, req.At = "submission-exit", at.Add(time.Second)
	req.Targets[0].Decision = allocation.TargetDecision{}
	apply(account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: marks})
	snap, err = account.SnapshotCapped(state, marks, req.At)
	if err != nil {
		t.Fatal(err)
	}
	exit := orderOf(snap.Distribution.Orders[0])
	if !exit.ReduceOnly {
		t.Fatal("exit opened exposure")
	}
	apply(account.BookCommand{ID: "reserve-exit", Kind: "portfolio_reserve", At: req.At, Order: &exit, Marks: marks})
	if _, err = store.CheckCappedSubmission(ctx, state.Config.ID, exit, marks); err != nil {
		t.Fatal("retirement or pending family stranded reduction", err)
	}
}

func TestMemoryCappedSubmission(t *testing.T) { cappedSubmissionContracts(t, NewMemory()) }
func TestPostgresContractsCappedSubmission(t *testing.T) {
	cappedSubmissionContracts(t, timerPostgres(t, "capped_submission"))
}
