package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func cappedCheckpointReadContract(t *testing.T, s briefReadStore) {
	t.Helper()
	ctx := context.Background()
	opened := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("checkpoint-read", "checkpoint-paper", account.Cash, opened)
	cfg.FundingMode = account.OwnerReceipts
	state, err := s.CreateCappedBook(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4100 {
		state, err = s.ApplyCappedBook(ctx, cfg.ID, state.Version, account.BookCommand{ID: fmt.Sprint("observe-", i), Kind: "risk_observe", At: opened.Add(time.Duration(i) * time.Second), Marks: map[string]string{"SPY": "100"}})
		if err != nil {
			t.Fatal(i, err)
		}
	}
	// Economic and owner events must join the logical observation count, not
	// the two remaining hot slots. The receipt must not be folded into risk.
	at := opened.AddDate(0, 0, 6)
	due, err := cfg.DueDeposits(at)
	if err != nil {
		t.Fatal(err)
	}
	a := account.OwnerBookAction{ExpectedVersion: state.Version, Action: "deposit", Amount: "500", EffectiveAt: due[0].EffectiveAt.Add(time.Hour), Source: "synthetic-test-receipt-only"}
	a.Confirmation = a.ConfirmationPin(cfg.ID, due[0].ID)
	deposit := account.BookCommand{ID: due[0].ID, Kind: "deposit", At: at, Amount: a.Amount, EffectiveAt: a.EffectiveAt, OwnerAction: &a}
	state, err = s.ApplyCappedBook(ctx, cfg.ID, state.Version, deposit)
	if err != nil {
		t.Fatal("owner receipt after checkpoint", err)
	}
	state, err = s.ApplyCappedBook(ctx, cfg.ID, state.Version, account.BookCommand{ID: "halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	clear := account.OwnerBookAction{ExpectedVersion: state.Version, Action: "owner_clear_halt"}
	clear.Confirmation = clear.ConfirmationPin(cfg.ID, "clear")
	state, err = s.ApplyCappedBook(ctx, cfg.ID, state.Version, account.BookCommand{ID: "clear", Kind: "risk_control", At: at, Marks: map[string]string{}, RiskControl: &risk.Control{Action: clear.Action}, OwnerAction: &clear})
	if err != nil {
		t.Fatal("owner clear after checkpoint", err)
	}
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CappedBookBounded(ctx, cfg.ID, 1<<20)
	if err != nil || !reflect.DeepEqual(got, state) || got.Version != 4103 || len(got.Events) != 5 {
		t.Fatal("incomplete current head", got.Version, len(got.Events), err)
	}
	view, err := account.ReadOwnerBook(got, at)
	if err != nil || view.Halted || view.Cash != "2500.000000000" || len(view.Deposits) != 1 || view.Deposits[0].OwnerAction.Source != a.Source {
		t.Fatal("current economic/control evidence lost", view, err)
	}
	for _, max := range []int{len(before) / 2, 128} {
		if partial, e := s.CappedBookBounded(ctx, cfg.ID, max); !errors.Is(e, ErrCappedReadCapacity) || partial.Version != 0 {
			t.Fatal("over-budget head returned partial state", max, partial.Version, e)
		}
	}
	got.Events[0].RiskCheckpoint.Last.Marks["SPY"] = "1"
	got.Events[0].RiskCheckpoint.State.Halted = true
	got.Events[0].RiskCheckpoint.Last.Marks["SPY"] = "1"
	got.Events[2].OwnerAction.Source = "tampered"
	fresh, err := s.CappedBookBounded(ctx, cfg.ID, 1<<20)
	after, marshalErr := json.Marshal(fresh)
	if err != nil || marshalErr != nil || string(before) != string(after) {
		t.Fatal("bounded read mutated retained projection", err, marshalErr)
	}
	// Every original is still available and replay gives the exact same hash.
	rebuilt, err := ReplayCappedBookHistory(ctx, s.(CappedHistoryStore), cfg, state.Version)
	if err != nil || !reflect.DeepEqual(rebuilt, state) {
		t.Fatal("bounded read changed immutable custody", err)
	}
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		restarted, err := reopened.CappedBookBounded(ctx, cfg.ID, 1<<20)
		if err != nil || !reflect.DeepEqual(restarted, state) {
			t.Fatal("restarted bounded head changed", err)
		}
	}
	t.Logf("4100 observations plus receipt/halt/clear: logical version %d, %d hot events; detached bounded head and exact custody replay", state.Version, len(state.Events))
}

func TestMemoryCappedBookCheckpointRead(t *testing.T) {
	cappedCheckpointReadContract(t, NewMemory())
}

func TestPostgresContractsCappedBookCheckpointRead(t *testing.T) {
	cappedCheckpointReadContract(t, timerPostgres(t, "checkpoint_read"))
}

func TestPostgresContractsCappedBookBoundedLegacyTransition(t *testing.T) {
	p := timerPostgres(t, "bounded_legacy")
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("legacy-read", "legacy-paper", account.Cash, at)
	state, err := p.CreateCappedBook(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cmd := account.BookCommand{ID: "legacy-observe", Kind: "risk_observe", At: at, Marks: map[string]string{"SPY": "100"}}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, `INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,1,$2,$3)`, cfg.ID, cmd.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	state.Events, state.Version = []account.BookCommand{cmd}, 1
	got, err := p.CappedBookBounded(ctx, cfg.ID, 1<<20)
	if err != nil || !reflect.DeepEqual(got, state) || got.Config.FundingMode != "" {
		t.Fatal("legacy bytes/config changed", err)
	}
	if _, err = p.CappedBookBounded(ctx, cfg.ID, 512); !errors.Is(err, ErrCappedReadCapacity) {
		t.Fatal("legacy event budget ignored", err)
	}
	state, err = p.ApplyCappedBook(ctx, cfg.ID, state.Version, account.BookCommand{ID: "new-halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	got, err = p.CappedBookBounded(ctx, cfg.ID, 1<<20)
	if err != nil || !reflect.DeepEqual(got, state) || got.Version != 2 {
		t.Fatal("new head downgraded to legacy rows", got.Version, err)
	}
}
