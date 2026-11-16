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
	"git.subcult.tv/subculture-collective/mantik/migrations"
	"github.com/jackc/pgx/v5"
)

func TestPostgresContractsCappedReplayRejectsRestrictedWriterForgery(t *testing.T) {
	p := timerPostgres(t, "replay_forged_head")
	ctx := context.Background()
	gateStorageContracts(t, p)
	g, e := p.StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	marks := map[string]string{"SPY": "100"}
	s, e := p.CreateCappedBook(ctx, account.DefaultCappedConfig("forged", "forged-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	apply := func(c account.BookCommand) {
		t.Helper()
		var e error
		s, e = p.ApplyCappedBook(ctx, s.Config.ID, s.Version, c)
		if e != nil {
			t.Fatal(e)
		}
	}
	req := allocation.RebalanceRequest{ID: "entry", At: at, Sleeves: []allocation.Sleeve{{ID: "model", StrategyID: g.Declaration.StrategyID, GateID: "retained-gate", Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: marks, Fees: map[string]string{"SPY": "0"}}
	apply(account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks})
	snap, e := account.SnapshotCapped(s, marks, at)
	if e != nil {
		t.Fatal(e)
	}
	z := snap.Distribution.Orders[0]
	o := account.BookOrder{ID: z.ID, AllocationID: z.AllocationID, SleeveID: z.SleeveID, StrategyID: z.StrategyID, Symbol: z.Symbol, Quantity: z.Quantity, Limit: z.Limit, Side: z.Side, Fee: z.Fee, At: z.At, ReduceOnly: z.ReduceOnly}
	apply(account.BookCommand{ID: "reserve", Kind: "portfolio_reserve", At: at, Order: &o, Marks: marks})
	apply(account.BookCommand{ID: "owner-kill", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "kill"}})
	for i := range 3 {
		apply(account.BookCommand{ID: fmt.Sprint("observe-", i), Kind: "risk_observe", At: at, Marks: marks})
	}
	if _, e = p.CheckCappedSubmission(ctx, s.Config.ID, o, marks); e == nil || !strings.Contains(e.Error(), "risk_kill_switch") {
		t.Fatal("setup kill missing", e)
	}
	c := account.BookCommand{ID: "next-observe", Kind: "risk_observe", At: at, Marks: marks}
	honest, e := account.ApplyCappedCommand(s, c)
	if e != nil {
		t.Fatal(e)
	}
	forged := clone(honest)
	for _, v := range forged.Events {
		if v.RiskControl != nil {
			v.RiskControl.Action = "unkill"
		}
		if v.RiskCheckpoint != nil {
			v.RiskCheckpoint.State.Kill = false
		}
	}
	if e = account.ValidateCappedState(forged); e != nil {
		t.Fatal("forge invalid", e)
	}
	rec, e := newCappedHistory(s, forged, c)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	head, e := json.Marshal(forged)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.pool.Exec(ctx, `CREATE ROLE capped_replay_append_only NOLOGIN; GRANT SELECT ON mantik_capped_books,mantik_capped_book_events,mantik_capped_book_history,mantik_capped_book_heads TO capped_replay_append_only; GRANT INSERT ON mantik_capped_book_history TO capped_replay_append_only; GRANT UPDATE ON mantik_capped_books,mantik_capped_book_heads TO capped_replay_append_only`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, err := p.pool.Exec(context.Background(), `DROP OWNED BY capped_replay_append_only; DROP ROLE capped_replay_append_only`); err != nil {
			t.Error(err)
		}
	})
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SET LOCAL ROLE capped_replay_append_only`); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO mantik_capped_book_history(book_id,sequence,event_id,payload,previous_sha256,state_sha256) VALUES($1,$2,$3,$4,$5,$6)`, s.Config.ID, forged.Version, c.ID, string(raw), rec.PreviousSHA256, rec.StateSHA256)
	if e != nil {
		t.Fatal("archive forgery rejected", e)
	}
	_, e = tx.Exec(ctx, `UPDATE mantik_capped_book_heads SET version=$2,state=$3,state_sha256=$4 WHERE book_id=$1`, s.Config.ID, forged.Version, string(head), rec.StateSHA256)
	if e != nil {
		t.Fatal("head forgery rejected", e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}

	for _, sql := range []string{`UPDATE mantik_capped_book_history SET event_id=event_id`, `UPDATE mantik_capped_book_events SET event_id=event_id`, `ALTER TABLE mantik_capped_book_heads ADD COLUMN forbidden text`} {
		tx, e := p.pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, `SET LOCAL ROLE capped_replay_append_only`); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, sql); e == nil {
			t.Fatal("restricted fixture unexpectedly owns source/schema", sql)
		}
		if e = tx.Rollback(ctx); e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	for _, reader := range []*Postgres{p, reopened} {
		if got, e := reader.CappedBookBounded(ctx, s.Config.ID, 1<<20); !errors.Is(e, account.ErrBookConflict) || got.Version != 0 {
			t.Fatal("forged head entered bounded owner read", got.Version, e)
		}
		if got, e := reader.CappedBook(ctx, s.Config.ID); !errors.Is(e, account.ErrBookConflict) || got.Version != 0 {
			t.Fatal("forged head entered operational read", got.Version, e)
		}
		if got, e := reader.CheckCappedSubmission(ctx, s.Config.ID, o, marks); !errors.Is(e, account.ErrBookConflict) || got.Version != 0 {
			t.Fatal("forged head entered final fence", got.Version, e)
		}
		if got, e := reader.ApplyCappedBook(ctx, s.Config.ID, forged.Version, account.BookCommand{ID: "future", Kind: "risk_observe", At: at, Marks: marks}); !errors.Is(e, account.ErrBookConflict) || got.Version != 0 {
			t.Fatal("forged head entered command admission", e)
		}
		raw, v, _ := reader.cappedReplay.entry(s.Config.ID).snapshot()
		var trusted account.CappedState
		if e = json.Unmarshal(raw, &trusted); e != nil || v != s.Version {
			t.Fatal("forgery poisoned replay cache", v, e)
		}
		snap, e := account.SnapshotCapped(trusted, marks, at)
		if e != nil || !snap.Risk.Kill {
			t.Fatal("cache lost original control", e)
		}
	}
	originals, e := p.CappedBookHistory(ctx, s.Config.ID, 2, 1)
	if e != nil || originals[0].Command.RiskControl.Action != "kill" {
		t.Fatal("original changed", e)
	}
	t.Log("restricted SQL append/head update accepted; warm and cold operational reads, final fences and writes refused forged transition; immutable kill retained")
}

func writeReplayTransition(t *testing.T, ctx context.Context, tx pgx.Tx, before account.CappedState, c account.BookCommand) account.CappedState {
	t.Helper()
	next, err := account.ApplyCappedCommand(before, c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newCappedHistory(before, next, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mantik_capped_book_history(book_id,sequence,event_id,payload,previous_sha256,state_sha256) VALUES($1,$2,$3,$4,$5,$6)`, before.Config.ID, next.Version, c.ID, string(raw), r.PreviousSHA256, r.StateSHA256); err != nil {
		t.Fatal(err)
	}
	head, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mantik_capped_book_heads(book_id,version,state,state_sha256) VALUES($1,$2,$3,$4) ON CONFLICT(book_id) DO UPDATE SET version=EXCLUDED.version,state=EXCLUDED.state,state_sha256=EXCLUDED.state_sha256`, before.Config.ID, next.Version, string(head), r.StateSHA256); err != nil {
		t.Fatal(err)
	}
	return next
}

type cappedReadAudit struct {
	cappedQuery
	pages int
}

func (q *cappedReadAudit) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.pages++
	return q.cappedQuery.Query(ctx, sql, args...)
}

func TestPostgresContractsCappedReplayTransactionRollbackAndIncrementalRead(t *testing.T) {
	p := timerPostgres(t, "replay_rollback")
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, e := p.CreateCappedBook(ctx, account.DefaultCappedConfig("rollback", "rollback-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	state, e = p.ApplyCappedBook(ctx, state.Config.ID, 0, account.BookCommand{ID: "kill", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "kill"}})
	if e != nil {
		t.Fatal(e)
	}
	// The joined shared writer leaves postcommit cache publication to a read.
	// Warm the committed kill before checking transaction-local isolation.
	if _, e = p.CappedBook(ctx, state.Config.ID); e != nil {
		t.Fatal(e)
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	uncommitted := writeReplayTransition(t, ctx, tx, state, account.BookCommand{ID: "uncommitted-unkill", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "unkill"}})
	read, e := p.loadCapped(ctx, tx, state.Config.ID)
	if e != nil || !reflect.DeepEqual(read, uncommitted) {
		t.Fatal("transaction did not validate its own pinned head", e)
	}
	_, cached, _ := p.cappedReplay.entry(state.Config.ID).snapshot()
	if cached != state.Version {
		t.Fatal("uncommitted transition entered shared cache", cached)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	// Reuse the rolled-back sequence for a different command. The private
	// transaction view cannot poison either subsequent source verification or IDs.
	tx, e = p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	state = writeReplayTransition(t, ctx, tx, state, account.BookCommand{ID: "committed-observe", Kind: "risk_observe", At: at})
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	audit := &cappedReadAudit{cappedQuery: p.pool}
	read, e = p.loadCapped(ctx, audit, state.Config.ID)
	if e != nil || !reflect.DeepEqual(read, state) || audit.pages != 1 {
		t.Fatal("new head did not validate exactly its suffix", audit.pages, e)
	}
	if _, v, _ := p.cappedReplay.entry(state.Config.ID).snapshot(); v != 1 {
		t.Fatal("generic transaction/query view published custody", v)
	}
	read, e = p.CappedBook(ctx, state.Config.ID)
	if e != nil || !reflect.DeepEqual(read, state) {
		t.Fatal(e)
	}
	audit.pages = 0
	read, e = p.loadCapped(ctx, audit, state.Config.ID)
	if e != nil || audit.pages != 0 || !reflect.DeepEqual(read, state) {
		t.Fatal("cache hit reread archive or changed state", audit.pages, e)
	}
	read.Events[0].RiskControl.Action = "unkill"
	read, e = p.CappedBook(ctx, state.Config.ID)
	if e != nil || read.Events[0].RiskControl.Action != "kill" {
		t.Fatal("output poisoned cache", e)
	}
	// A fresh SQL head, rather than the cached older prefix, controls the next read.
	tx, e = p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	state = writeReplayTransition(t, ctx, tx, state, account.BookCommand{ID: "new-unkill", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "unkill"}})
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	audit.pages = 0
	read, e = p.loadCapped(ctx, audit, state.Config.ID)
	if e != nil || audit.pages != 1 || !reflect.DeepEqual(read, state) {
		t.Fatal("stale cache stood in for current head", audit.pages, e)
	}
}

func TestPostgresContractsCappedReplayConcurrentReadersAndWriters(t *testing.T) {
	p := timerPostgres(t, "replay_concurrent")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, e := p.CreateCappedBook(ctx, account.DefaultCappedConfig("concurrent", "concurrent-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	// A separate instance has its own private cache and updates committed SQL.
	other, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			if i == 0 {
				current := state
				for n := range 40 {
					next, e := other.ApplyCappedBook(ctx, state.Config.ID, current.Version, account.BookCommand{ID: fmt.Sprint(n), Kind: "risk_observe", At: at})
					if e != nil {
						errs <- e
						return
					}
					current = next
				}
			} else {
				previous := int64(0)
				for range 40 {
					current, e := p.CappedBook(ctx, state.Config.ID)
					if errors.Is(e, account.ErrBookConflict) {
						continue
					} // A queued pinned read may be older than verified progress.
					if e != nil {
						errs <- e
						return
					}
					if current.Version < previous {
						errs <- fmt.Errorf("reader regressed from %d to %d", previous, current.Version)
						return
					}
					previous = current.Version
					// Mutating one reader's output must not affect another reader/cache.
					for _, event := range current.Events {
						if event.RiskCheckpoint != nil {
							event.RiskCheckpoint.State.Kill = true
						}
					}
				}
			}
			errs <- nil
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, e := p.CappedBook(ctx, state.Config.ID)
	if e != nil || got.Version != 40 {
		t.Fatal("current version missing", got.Version, e)
	}
	snap, e := account.SnapshotCapped(got, nil, at)
	if e != nil || snap.Risk.Kill {
		t.Fatal("concurrent output poisoned cache", e)
	}
}

func TestPostgresContractsCappedReplayFinalFenceSeesCommittedControl(t *testing.T) {
	p := timerPostgres(t, "replay_final_fence")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gateStorageContracts(t, p)
	gate, e := p.StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	marks := map[string]string{"SPY": "100"}
	state, e := p.CreateCappedBook(ctx, account.DefaultCappedConfig("fence", "fence-paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	req := allocation.RebalanceRequest{ID: "entry", At: at, Sleeves: []allocation.Sleeve{{ID: "model", StrategyID: gate.Declaration.StrategyID, GateID: "retained-gate", Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: "model", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: marks, Fees: map[string]string{"SPY": "0"}}
	state, e = p.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(state, marks, at)
	if e != nil {
		t.Fatal(e)
	}
	sized := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: sized.ID, AllocationID: sized.AllocationID, SleeveID: sized.SleeveID, StrategyID: sized.StrategyID, Symbol: sized.Symbol, Quantity: sized.Quantity, Limit: sized.Limit, Side: sized.Side, Fee: sized.Fee, At: sized.At, ReduceOnly: sized.ReduceOnly}
	state, e = p.ApplyCappedBook(ctx, state.Config.ID, state.Version, account.BookCommand{ID: "reserve", Kind: "portfolio_reserve", At: at, Order: &order, Marks: marks})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.CheckCappedSubmission(ctx, state.Config.ID, order, marks); e != nil {
		t.Fatal("positive reservation refused", e)
	}
	// Retain a real owner control in a transaction that has not committed yet.
	// The fence can warm the old prefix, but must wait for the row and recheck it.
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var writerPID int
	if e = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); e != nil {
		t.Fatal(e)
	}
	writeReplayTransition(t, ctx, tx, state, account.BookCommand{ID: "late-owner-kill", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "kill"}})
	done := make(chan error, 1)
	go func() { _, e := p.CheckCappedSubmission(ctx, state.Config.ID, order, marks); done <- e }()
	// Observe the real lock wait, not a timing assumption about the goroutine.
	waiting := false
	for !waiting {
		if e = ctx.Err(); e != nil {
			t.Fatal(e)
		}
		if e = p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if !waiting {
			time.Sleep(time.Millisecond)
		}
	}
	if _, v, _ := p.cappedReplay.entry(state.Config.ID).snapshot(); v != state.Version {
		t.Fatal("uncommitted control cached", v)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil || !strings.Contains(e.Error(), "risk_kill_switch") {
		t.Fatal("old verified cache authorized newer killed head", e)
	}
	t.Log("final fence warmed an old verified prefix, waited on the writer, replayed the committed owner kill and refused submission")
}

// Exercise the joined writer against a completely full predecessor journal
// with an already executed protective fill awaiting local custody.
func TestPostgresContractsCappedReplayFullLegacyProtectiveFill(t *testing.T) {
	p := timerPostgres(t, "replay_legacy_fill")
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("legacy-fill", "legacy-fill-paper", account.Cash, at)
	legacy, e := p.CreateCappedBook(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	entry := account.BookOrder{ID: "entry", Symbol: "SPY", Quantity: "0.1", Limit: "100", Fee: "0", Side: "buy", At: at}
	exit := entry
	exit.ID, exit.Side, exit.ReduceOnly = "protective", "sell", true
	fill := account.BookFill{ID: "entry-fill", OrderID: entry.ID, VenueOrderID: "entry-venue", Symbol: "SPY", Quantity: "0.1", Price: "100", Fee: "0", Side: "buy", At: at, SourceSHA256: strings.Repeat("a", 64)}
	marks := map[string]string{"SPY": "100"}
	for _, c := range []account.BookCommand{
		{ID: "entry-reserve", Kind: "reserve", At: at, Order: &entry, Marks: marks},
		{ID: "entry-bind", Kind: "bind", At: at, OrderID: entry.ID, VenueOrderID: fill.VenueOrderID},
		{ID: fill.ID, Kind: "fill", At: at, Fill: &fill, Marks: marks},
		{ID: "exit-reserve", Kind: "reserve", At: at, Order: &exit, Marks: marks},
		{ID: "exit-bind", Kind: "bind", At: at, OrderID: exit.ID, VenueOrderID: "exit-venue"},
	} {
		legacy, e = account.ApplyCappedCommand(legacy, c)
		if e != nil {
			t.Fatal(e)
		}
	}
	for len(legacy.Events) < 4096 {
		i := len(legacy.Events)
		legacy.Events = append(legacy.Events, account.BookCommand{ID: fmt.Sprintf("legacy-observation-%d", i), Kind: "risk_observe", At: at.Add(time.Duration(i) * time.Second), Marks: marks})
	}
	legacy.Version = 4096
	// Only this disposable fixture bypasses the post-126 legacy insertion seal.
	// Restore the trigger within the same transaction as the predecessor rows.
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `DROP TRIGGER capped_legacy_sealed ON mantik_capped_book_events`); e != nil {
		t.Fatal(e)
	}
	for i, c := range legacy.Events {
		raw, e := json.Marshal(c)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,$2,$3,$4)`, cfg.ID, i+1, c.ID, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(ctx, migrations.CappedBookHistory); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := p.CappedBook(ctx, cfg.ID)
	if e != nil || !reflect.DeepEqual(got, legacy) {
		t.Fatal("predecessor custody changed", e)
	}
	fill.ID, fill.OrderID, fill.VenueOrderID, fill.Side = "protective-fill", exit.ID, "exit-venue", "sell"
	fill.At = at.Add(time.Second)
	now := at.Add(4097 * time.Second)
	command := account.BookCommand{ID: fill.ID, Kind: "fill", At: now, Fill: &fill, Marks: marks}
	next, e := p.ApplyCappedBook(ctx, cfg.ID, legacy.Version, command)
	if e != nil {
		t.Fatal("full legacy fill stranded", e)
	}
	snap, e := account.SnapshotCapped(next, marks, now)
	if e != nil || snap.Positions["SPY"] != "0.000000000" || next.Version != 4097 || len(next.Events) > 8 {
		t.Fatal("protective fill recovery", e)
	}
	cold, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer cold.Close()
	for _, reader := range []*Postgres{p, cold} {
		got, e := reader.CappedBook(ctx, cfg.ID)
		if e != nil || !reflect.DeepEqual(got, next) {
			t.Fatal("warm/cold fill replay", e)
		}
	}
	rebuilt, e := ReplayCappedBookHistory(ctx, p, cfg, next.Version)
	if e != nil || !reflect.DeepEqual(rebuilt, next) {
		t.Fatal("mixed original custody fill recovery", e)
	}
	originals, e := p.CappedBookHistory(ctx, cfg.ID, 4096, 1)
	if e != nil || len(originals) != 1 || !sameCappedCommand(originals[0].Command, command) {
		t.Fatal("original fill lost", e)
	}
	if _, e = p.ApplyCappedBook(ctx, cfg.ID, 0, command); e != nil {
		t.Fatal("protective fill retry", e)
	}
	var retained int
	if e = p.pool.QueryRow(ctx, `SELECT count(*) FROM mantik_capped_book_events WHERE book_id=$1`, cfg.ID).Scan(&retained); e != nil || retained != 4096 {
		t.Fatal("legacy originals lost", retained, e)
	}
	t.Logf("4096 legacy originals retained; protective fill at 4097; %d hot slots; exact warm/cold/immutable recovery", len(next.Events))
}
