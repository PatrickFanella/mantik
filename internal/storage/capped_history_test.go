package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

// Synthetic command-capacity proof, not elapsed-window or market qualification.
func cappedCapacityContract(t *testing.T, s CappedBookStore) account.CappedState {
	t.Helper()
	ctx := context.Background()
	gateStorageContracts(t, s.(strategygate.Store))
	gate, err := s.(strategygate.Store).StrategyGate(ctx, "retained-gate")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, err := s.CreateCappedBook(ctx, account.DefaultCappedConfig("capacity", "capacity-paper", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	apply := func(c account.BookCommand) {
		t.Helper()
		next, e := s.ApplyCappedBook(ctx, state.Config.ID, state.Version, c)
		if e != nil {
			t.Fatalf("command %s at version %d: %v", c.ID, state.Version, e)
		}
		state = next
	}
	marks := map[string]string{"SPY": "100"}
	req := allocation.RebalanceRequest{ID: "opening", At: at, Sleeves: []allocation.Sleeve{{ID: "model", StrategyID: gate.Declaration.StrategyID, GateID: "retained-gate", Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0"}}
	apply(account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks})
	snap, err := account.SnapshotCapped(state, marks, at)
	if err != nil || snap.Distribution == nil || len(snap.Distribution.Orders) != 1 {
		t.Fatal(snap, err)
	}
	sized := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: sized.ID, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, At: sized.At, ReduceOnly: sized.ReduceOnly}
	apply(account.BookCommand{ID: "reserve-opening", Kind: "portfolio_reserve", At: at, Order: &order, Marks: marks})
	apply(account.BookCommand{ID: "bind-opening", Kind: "bind", At: at, OrderID: order.ID, VenueOrderID: "venue-opening"})
	f := account.BookFill{ID: "opening-fill", OrderID: order.ID, VenueOrderID: "venue-opening", Symbol: "SPY", Quantity: order.Quantity, Price: "100", Fee: "0", Side: "buy", At: at, SourceSHA256: strings.Repeat("e", 64)}
	apply(account.BookCommand{ID: f.ID, Kind: "fill", At: at, Fill: &f, Marks: marks})
	for _, control := range []risk.Control{{Action: "kill"}, {Action: "pause_strategy", StrategyID: order.StrategyID}, {Action: "retire_strategy", StrategyID: "retired-fixture"}} {
		apply(account.BookCommand{ID: control.Action, Kind: "risk_control", At: at, RiskControl: &control})
	}
	var expected risk.State
	// The rebalance initialized risk at equity 2000, without any external flow.
	expected, err = risk.Observe(expected, at, "2000", "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []risk.Control{{Action: "kill"}, {Action: "pause_strategy", StrategyID: order.StrategyID}, {Action: "retire_strategy", StrategyID: "retired-fixture"}} {
		expected, err = risk.ApplyControl(expected, control)
		if err != nil {
			t.Fatal(err)
		}
	}
	var first account.BookCommand
	for i := 0; i < 4200; i++ {
		price := fmt.Sprintf("100.%03d", i%997)
		if i == 17 {
			price = "10000"
		}
		marks = map[string]string{"SPY": price}
		clock := at.Add(time.Duration(i+1) * time.Second)
		cmd := account.BookCommand{ID: fmt.Sprintf("observe-%d", i), Kind: "risk_observe", At: clock, Marks: marks}
		if i == 0 {
			first = clone(cmd)
		}
		apply(cmd)
		snap, err = account.SnapshotCapped(state, marks, clock)
		if err != nil {
			t.Fatal(err)
		}
		expected, err = risk.Observe(expected, clock, snap.Equity, "0")
		if err != nil || !reflect.DeepEqual(expected, snap.Risk) {
			t.Fatalf("observation %d differs from uncompressed Observe: %#v %#v %v", i, expected, snap.Risk, err)
		}
		if len(state.Events) > 12 {
			t.Fatal("hot observations grew", len(state.Events))
		}
	}
	if !snap.Risk.Halted || !snap.Risk.Kill || !snap.Risk.Breakers[order.StrategyID] || !snap.Risk.Retired["retired-fixture"] {
		t.Fatal("brakes lost", snap.Risk)
	}
	if retry, e := s.ApplyCappedBook(ctx, state.Config.ID, 0, first); e != nil || retry.Version != state.Version {
		t.Fatal("archived exact retry", retry.Version, e)
	}
	changed := clone(first)
	changed.Marks["SPY"] = "101"
	if _, e := s.ApplyCappedBook(ctx, state.Config.ID, state.Version, changed); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed archived retry accepted", e)
	}
	// Checkpoints cannot be presented as ordinary commands, even at a fresh ID.
	for _, event := range state.Events {
		if event.RiskCheckpoint != nil {
			forged := clone(event)
			forged.ID = "forged"
			forged.RiskCheckpoint.State.Halted = false
			if _, e := s.ApplyCappedBook(ctx, state.Config.ID, state.Version, forged); e == nil {
				t.Fatal("caller forged checkpoint")
			}
			forged.Kind = "risk_observe"
			if _, e := s.ApplyCappedBook(ctx, state.Config.ID, state.Version, forged); e == nil {
				t.Fatal("caller smuggled checkpoint")
			}
		}
	}
	beforeSnapshot, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	detachedSnapshot, err := account.SnapshotCapped(state, marks, at.Add(4200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	detachedSnapshot.Risk.Breakers["snapshot-intruder"] = true
	afterSnapshot, err := json.Marshal(state)
	if err != nil || string(beforeSnapshot) != string(afterSnapshot) {
		t.Fatal("snapshot aliased a checkpoint", err)
	}
	// A later day and scheduled external flow must keep the observed peak/brakes.
	through := at.AddDate(0, 0, 5)
	due, e := state.Config.DueDeposits(through)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range due {
		apply(c)
	}
	apply(account.BookCommand{ID: "next-day", Kind: "risk_observe", At: through, Marks: marks})
	snap, e = account.SnapshotCapped(state, marks, through)
	if e != nil {
		t.Fatal(e)
	}
	expected, e = risk.Observe(expected, through, snap.Equity, fmt.Sprint(500*len(due)))
	if e != nil || !reflect.DeepEqual(expected, snap.Risk) {
		t.Fatal("flow/day baseline changed", expected, snap.Risk, e)
	}
	// One expected version admits one writer after thousands of archived events.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"concurrent-a", "concurrent-b"} {
		wg.Go(func() {
			_, e := s.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: id, Kind: "risk_observe", At: through, Marks: marks})
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	successes := 0
	for e := range errs {
		if e == nil {
			successes++
		} else if !errors.Is(e, account.ErrBookConflict) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatal("version fence", successes)
	}
	state, e = s.CappedBook(ctx, state.Config.ID)
	if e != nil {
		t.Fatal(e)
	}
	// Retained ownership permits a reduction while every opening brake is set.
	exit := req
	exit.ID = "protective"
	exit.At = through
	exit.Targets = []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{}}}
	apply(account.BookCommand{ID: exit.ID, Kind: "rebalance", At: through, Rebalance: &exit, Marks: marks})
	snap, e = account.SnapshotCapped(state, marks, through)
	if e != nil || snap.Distribution == nil || len(snap.Distribution.Orders) != 1 {
		t.Fatal("protective distribution", snap, e)
	}
	sized = snap.Distribution.Orders[0]
	if !sized.ReduceOnly || sized.Side != "sell" {
		t.Fatal("exit is not reducing", sized)
	}
	order = account.BookOrder{ID: sized.ID, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, At: sized.At, ReduceOnly: sized.ReduceOnly}
	apply(account.BookCommand{ID: "reserve-exit", Kind: "portfolio_reserve", At: through, Order: &order, Marks: marks})
	// The final submission fence still consults persistent owner controls and
	// current same-store family/ownership checks after compaction.
	fence := s.(interface {
		CheckCappedSubmission(context.Context, string, account.BookOrder, map[string]string) (account.CappedState, error)
	})
	if _, e = fence.CheckCappedSubmission(ctx, state.Config.ID, order, marks); e != nil {
		t.Fatal("protective final fence", e)
	}
	apply(account.BookCommand{ID: "bind-exit", Kind: "bind", At: through, OrderID: order.ID, VenueOrderID: "venue-exit"})
	f = account.BookFill{ID: "exit-fill", OrderID: order.ID, VenueOrderID: "venue-exit", Symbol: "SPY", Quantity: order.Quantity, Price: "100", Fee: "0", Side: "sell", At: through, SourceSHA256: strings.Repeat("f", 64)}
	apply(account.BookCommand{ID: f.ID, Kind: "fill", At: through, Fill: &f, Marks: marks})
	final, e := account.SnapshotCapped(state, marks, through)
	if e != nil || final.Positions["SPY"] != "0.000000000" || !final.Risk.Halted || final.Risk.HighNAV != expected.HighNAV {
		t.Fatal("exit or peak lost", final, e)
	}
	// Every immutable original is available and deterministically rebuilds the
	// projection, including economic source hashes and logical prefix versions.
	recovered, e := account.NewCappedState(state.Config)
	if e != nil {
		t.Fatal(e)
	}
	history := s.(CappedHistoryStore)
	count := 0
	for {
		page, e := history.CappedBookHistory(ctx, state.Config.ID, recovered.Version, MaxCappedHistoryPage)
		if e != nil {
			t.Fatal(e)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			before, _ := json.Marshal(recovered)
			if r.Sequence != recovered.Version+1 || r.PreviousSHA256 != cappedDigest(before) {
				t.Fatal("archive chain", r.Sequence)
			}
			raw, _ := json.Marshal(r.Command)
			if cappedDigest(raw) != r.CommandSHA256 {
				t.Fatal("source digest changed")
			}
			recovered, e = account.ApplyCappedCommand(recovered, r.Command)
			if e != nil {
				t.Fatal(e)
			}
			after, _ := json.Marshal(recovered)
			if cappedDigest(after) != r.StateSHA256 {
				t.Fatal("recovery digest", r.Sequence)
			}
			count++
		}
	}
	if !reflect.DeepEqual(state, recovered) || int64(count) != state.Version {
		t.Fatal("archive recovery differs", count, state.Version)
	}
	rebuilt, err := ReplayCappedBookHistory(ctx, history, state.Config, state.Version)
	if err != nil || !reflect.DeepEqual(rebuilt, state) {
		t.Fatal("recovery API changed state", err)
	}
	// Reads, retries and checkpoint snapshots must not expose mutable custody.
	detached, e := s.CappedBook(ctx, state.Config.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range detached.Events {
		if c.RiskCheckpoint != nil {
			c.RiskCheckpoint.State.Breakers["intruder"] = true
			c.RiskCheckpoint.Last.Marks["SPY"] = "1"
		}
	}
	page, e := history.CappedBookHistory(ctx, state.Config.ID, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	page[0].Command.Rebalance.Sleeves[0].StrategyID = "changed"
	fresh, e := s.CappedBook(ctx, state.Config.ID)
	if e != nil || !reflect.DeepEqual(state, fresh) {
		t.Fatal("read mutated custody", e)
	}
	t.Logf("capacity proof: %d original commands, %d hot slots; 4200 changing-price observations; peak %s; halt retained; protective exit filled; immutable recovery exact", state.Version, len(state.Events), final.Risk.HighNAV)
	return state
}

func TestMemoryCappedHistoryCapacity(t *testing.T) { cappedCapacityContract(t, NewMemory()) }

func TestPostgresContractsCappedHistoryCapacity(t *testing.T) {
	p := timerPostgres(t, "capped_history_capacity")
	state := cappedCapacityContract(t, p)
	ctx := context.Background()
	for _, migration := range []string{migrations.CappedBooks, migrations.CappedBookHistory, migrations.CappedBookHistory} {
		if _, e := p.pool.Exec(ctx, migration); e != nil {
			t.Fatal("migration replay", e)
		}
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	retained, e := reopened.CappedBook(ctx, state.Config.ID)
	if e != nil || !reflect.DeepEqual(state, retained) {
		t.Fatal("restart changed projection", e)
	}
	for _, table := range []string{"mantik_capped_book_history", "mantik_capped_book_heads"} {
		for _, sql := range []string{"DELETE FROM " + table, "TRUNCATE " + table + " CASCADE", "UPDATE " + table + " SET book_id=book_id"} {
			if _, e := p.pool.Exec(ctx, sql); e == nil {
				t.Fatal("mutable history/head", sql)
			}
		}
	}
	var cold, hot int
	if e = p.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mantik_capped_book_history WHERE book_id=$1),json_array_length(state->'Events') FROM mantik_capped_book_heads WHERE book_id=$1`, state.Config.ID).Scan(&cold, &hot); e != nil || cold <= 4096 || hot >= 40 {
		t.Fatal(cold, hot, e)
	}
}

func TestPostgresContractsCappedHistoryUpgradeFrom125(t *testing.T) {
	p := timerPostgres(t, "capped_history_upgrade")
	ctx := context.Background()
	// Exact predecessor construction is confined to this owned disposable DB.
	for _, sql := range []string{
		"DROP TRIGGER capped_legacy_sealed ON mantik_capped_book_events",
		"DROP TRIGGER capped_legacy_no_mutation ON mantik_capped_book_events",
		"DROP TABLE mantik_capped_book_heads",
		"DROP TABLE mantik_capped_book_history",
		"DROP FUNCTION mantik_capped_history_guard()",
		"DELETE FROM mantik_schema WHERE version>=126",
	} {
		if _, e := p.pool.Exec(ctx, sql); e != nil {
			t.Fatal(e)
		}
	}
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("legacy", "legacy-paper", account.Cash, at)
	raw, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.pool.Exec(ctx, `INSERT INTO mantik_capped_books(id,broker_account,config) VALUES($1,$2,$3)`, cfg.ID, cfg.BrokerAccountID, string(raw)); e != nil {
		t.Fatal(e)
	}
	legacy, e := account.NewCappedState(cfg)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := range 4096 {
		c := account.BookCommand{ID: fmt.Sprintf("legacy-%d", i), Kind: "risk_observe", At: at.Add(time.Duration(i) * time.Second), Marks: map[string]string{"SPY": fmt.Sprint(100 + i)}}
		legacy.Events = append(legacy.Events, c)
		legacy.Version++
		raw, e = json.Marshal(c)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,$2,$3,$4)`, cfg.ID, i+1, c.ID, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	// The old SQL cap is still present, and never increased by migration 126.
	last := account.BookCommand{ID: "after-upgrade", Kind: "risk_observe", At: at.Add(4096 * time.Second)}
	raw, e = json.Marshal(last)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.pool.Exec(ctx, `INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,4097,$2,$3)`, cfg.ID, last.ID, string(raw)); e == nil {
		t.Fatal("old SQL cap disappeared")
	}
	before, e := json.Marshal(legacy)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = p.migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
	got, e := p.CappedBook(ctx, cfg.ID)
	if e != nil {
		t.Fatal(e)
	}
	after, e := json.Marshal(got)
	if e != nil || string(before) != string(after) {
		t.Fatal("upgrade changed legacy journal/hash", e)
	}
	got, e = p.ApplyCappedBook(ctx, cfg.ID, legacy.Version, last)
	if e != nil || got.Version != 4097 || len(got.Events) != 2 {
		t.Fatal("full legacy tail cannot advance", got.Version, len(got.Events), e)
	}
	if _, e = p.ApplyCappedBook(ctx, cfg.ID, 0, legacy.Events[0]); e != nil {
		t.Fatal("legacy archived retry", e)
	}
	changed := clone(legacy.Events[0])
	changed.Marks["SPY"] = "1"
	if _, e = p.ApplyCappedBook(ctx, cfg.ID, got.Version, changed); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed legacy retry", e)
	}
	recovered, e := ReplayCappedBookHistory(ctx, p, cfg, got.Version)
	if e != nil || !reflect.DeepEqual(recovered, got) {
		t.Fatal("mixed legacy/archive recovery", e)
	}
	t.Logf("legacy upgrade proof: 4096 original rows unchanged; version %d; %d hot slots; mixed custody recovery exact", got.Version, len(got.Events))
	var retained int
	if e = p.pool.QueryRow(ctx, `SELECT count(*) FROM mantik_capped_book_events WHERE book_id=$1`, cfg.ID).Scan(&retained); e != nil || retained != 4096 {
		t.Fatal("legacy originals lost", retained, e)
	}
}
