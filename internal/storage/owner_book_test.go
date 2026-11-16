package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func ownerBookStorageContract(t *testing.T, s CappedBookStore) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("owner-controls", "owner-account", account.Cash, at.AddDate(0, 0, -6))
	cfg.FundingMode = account.OwnerReceipts
	b, err := s.CreateCappedBook(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Funding mode is immutable even before the first receipt, and malformed
	// automatic deposits cannot be persisted in an owner-receipt book.
	for _, mode := range []string{"", account.SyntheticSchedule} {
		changed := cfg
		changed.FundingMode = mode
		if _, e := s.CreateCappedBook(ctx, changed); !errors.Is(e, account.ErrBookConflict) {
			t.Fatal("owner book funding mode changed", mode, e)
		}
	}
	due, _ := cfg.DueDeposits(at)
	if _, e := s.ApplyCappedBook(ctx, cfg.ID, b.Version, due[0]); e == nil {
		t.Fatal("unconfirmed scheduled deposit persisted")
	}
	a := account.OwnerBookAction{Action: "deposit", ExpectedVersion: 0, Amount: "500", Source: "isolated-storage-receipt", EffectiveAt: due[0].EffectiveAt.Add(30 * time.Minute)}
	a.Confirmation = a.ConfirmationPin(cfg.ID, due[0].ID)
	c := account.BookCommand{ID: due[0].ID, Kind: "deposit", At: at, Amount: a.Amount, EffectiveAt: a.EffectiveAt, OwnerAction: &a}
	b, err = s.ApplyCappedBook(ctx, cfg.ID, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.ApplyCappedBook(ctx, cfg.ID, b.Version, account.BookCommand{ID: "halt-after-deposit", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	clear := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: b.Version}
	clear.Confirmation = clear.ConfirmationPin(cfg.ID, "clear")
	cmd := account.BookCommand{ID: "clear", Kind: "risk_control", At: at, Marks: map[string]string{}, RiskControl: &risk.Control{Action: clear.Action}, OwnerAction: &clear}
	b, err = s.ApplyCappedBook(ctx, cfg.ID, b.Version, cmd)
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.ApplyCappedBook(ctx, cfg.ID, b.Version, account.BookCommand{ID: "new-protective-halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ApplyCappedBook(ctx, cfg.ID, clear.ExpectedVersion, cmd)
	if err != nil || retry.Version != b.Version {
		t.Fatal(retry, err)
	}
	v, err := account.ReadOwnerBook(retry, at)
	if err != nil || !v.Halted || v.Deposits[0].OwnerAction.Source != a.Source {
		t.Fatal(v, err)
	}
	changed := cmd
	meta := clear
	meta.ExpectedVersion++
	meta.Confirmation = meta.ConfirmationPin(cfg.ID, "clear")
	changed.OwnerAction = &meta
	if _, err = s.ApplyCappedBook(ctx, cfg.ID, meta.ExpectedVersion, changed); !errors.Is(err, account.ErrBookConflict) {
		t.Fatal("changed retry accepted", err)
	}
	detached, _ := s.CappedBook(ctx, cfg.ID)
	detached.Events[0].OwnerAction.Source = "tamper"
	retained, _ := s.CappedBook(ctx, cfg.ID)
	if retained.Events[0].OwnerAction.Source != a.Source {
		t.Fatal("mutable alias")
	}
	// Existing synthetic cash cannot be relabelled as owner receipt funding.
	legacy := account.DefaultCappedConfig("legacy-funding", "legacy-account", account.Cash, cfg.OpenedAt)
	old, e := s.CreateCappedBook(ctx, legacy)
	if e != nil {
		t.Fatal(e)
	}
	legacyDue, _ := legacy.DueDeposits(at)
	old, e = s.ApplyCappedBook(ctx, legacy.ID, old.Version, legacyDue[0])
	if e != nil {
		t.Fatal(e)
	}
	legacy.FundingMode = account.OwnerReceipts
	if _, e = s.CreateCappedBook(ctx, legacy); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("legacy synthetic cash converted to receipts", e)
	}
	legacyRead, e := s.CappedBook(ctx, legacy.ID)
	if e != nil || legacyRead.Config.FundingMode != "" || legacyRead.Version != old.Version {
		t.Fatal("legacy config/history changed", legacyRead, e)
	}
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		current, err := reopened.CappedBook(ctx, cfg.ID)
		if err != nil || !reflect.DeepEqual(current, retained) {
			t.Fatal("restart changed owner evidence", err)
		}
	}
}

func TestMemoryOwnerBookJournal(t *testing.T) { ownerBookStorageContract(t, NewMemory()) }
func TestPostgresContractsOwnerBookJournal(t *testing.T) {
	ownerBookStorageContract(t, timerPostgres(t, "owner_book"))
}
