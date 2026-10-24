package storage

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type paperLedgerBackend interface {
	PaperAccountStore
	PaperLedgerStore
	ResearchReceiverStore
	Store
}

func TestMemoryPaperLedgerRehearsal(t *testing.T) { paperLedgerContract(t, NewMemory()) }
func TestPostgresContractsPaperLedgerRehearsal(t *testing.T) {
	paperLedgerContract(t, timerPostgres(t, "paperledger"))
}
func paperLedgerContract(t *testing.T, s paperLedgerBackend) {
	t.Helper()
	ctx := context.Background()
	namespace, _ := s.ResearchSnapshot(ctx)
	before, _ := s.Snapshot(ctx)
	at := time.Unix(100, 0).UTC()
	now := func() time.Time { return at }
	a, err := s.CreatePaperAccount(ctx, namespace.ID, paperaccount.Config{ID: "one", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 60000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePaperAccount(ctx, namespace.ID, paperaccount.Config{ID: "two", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 60000}, now); err != nil {
		t.Fatal(err)
	}
	accounts, _ := s.PaperAccounts(ctx)
	genesis, err := s.PaperLedgerRehearsal(ctx, "one")
	if err != nil || genesis.Validate(ctx) != nil || len(genesis.Accepted) != 0 {
		t.Fatal(err)
	}
	c := paperledger.AppendCommand{ReceiverID: namespace.ID, AccountSHA256: a.Account.SHA256, ExpectedHeadSHA256: genesis.Result.LedgerHeadSHA256, ScopeCode: paperledger.RehearsalScope, Entry: paperledger.Entry{ID: "reserve-one", Kind: "reserve", ReservationID: "position-one", AmountCents: 40000, OccurredAt: at}}
	var calls atomic.Int32
	clock := func() time.Time { calls.Add(1); return at }
	var wg sync.WaitGroup
	results := make(chan paperledger.AppendReceipt, 10)
	for range 10 {
		wg.Go(func() {
			r, err := s.AppendPaperLedgerRehearsal(ctx, "one", c, clock)
			if err != nil {
				t.Error(err)
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	fresh := 0
	var original paperledger.AppendReceipt
	for r := range results {
		if !r.Duplicate {
			fresh++
			original = r
		}
		if r.Validate("one", c) != nil {
			t.Fatal(r)
		}
	}
	if fresh != 1 || calls.Load() != 1 {
		t.Fatal(fresh, calls.Load())
	}
	current, _ := s.PaperLedgerRehearsal(ctx, "one")
	if current.Result.ReservedCashCents != 40000 || current.Result.AvailableCashCents != 60000 || len(current.Accepted) != 1 {
		t.Fatal(current)
	}
	// Competing distinct commands share a head: exactly one may append.
	var wins, conflicts atomic.Int32
	for _, id := range []string{"candidate-a", "candidate-b"} {
		wg.Go(func() {
			next := c
			next.Entry.ID = id
			next.Entry.ReservationID = id
			next.Entry.AmountCents = 10000
			next.ExpectedHeadSHA256 = current.Result.LedgerHeadSHA256
			_, err := s.AppendPaperLedgerRehearsal(ctx, "one", next, now)
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, paperledger.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 1 {
		t.Fatal(wins.Load(), conflicts.Load())
	}
	retry, err := s.AppendPaperLedgerRehearsal(ctx, "one", c, func() time.Time { t.Fatal("retry clock"); return time.Time{} })
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(original.Accepted, retry.Accepted) {
		t.Fatal(err)
	}
	changed := c
	changed.Entry.AmountCents--
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "one", changed, now); !errors.Is(err, paperledger.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "two", c, now); !errors.Is(err, paperledger.ErrInvalid) {
		t.Fatal("cross account", err)
	}
	wrong := c
	wrong.ReceiverID = "receiver-other_other_other"
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "one", wrong, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	current, _ = s.PaperLedgerRehearsal(ctx, "one")
	head := current.Result.LedgerHeadSHA256
	unknown := c
	unknown.Entry.ID = "unknown-one"
	unknown.Entry.Kind = "unknown"
	unknown.Entry.AmountCents = 0
	unknown.ExpectedHeadSHA256 = head
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "one", unknown, now); err != nil {
		t.Fatal(err)
	}
	held, _ := s.PaperLedgerRehearsal(ctx, "one")
	release := unknown
	release.Entry.ID = "unsafe-release"
	release.Entry.Kind = "release"
	release.Entry.AmountCents = 1
	release.ExpectedHeadSHA256 = held.Result.LedgerHeadSHA256
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "one", release, now); !errors.Is(err, paperledger.ErrConflict) {
		t.Fatal(err)
	}
	again, _ := s.PaperLedgerRehearsal(ctx, "one")
	if !reflect.DeepEqual(held, again) {
		t.Fatal("rejected command changed ledger")
	}
	again.Accepted[0].Command.Entry.AmountCents = 1
	detached, _ := s.PaperLedgerRehearsal(ctx, "one")
	if detached.Accepted[0].Command.Entry.AmountCents != 40000 {
		t.Fatal("alias")
	}
	after, _ := s.Snapshot(ctx)
	afterAccounts, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, afterAccounts) {
		t.Fatal("rehearsal changed account funding or executable state")
	}
	other, _ := s.PaperLedgerRehearsal(ctx, "two")
	if len(other.Accepted) != 0 {
		t.Fatal("cross account write")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PaperLedgerRehearsal(canceled, "one"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if m, ok := s.(*Memory); ok {
		r := m.paperLedgers["one"]
		r.SHA256 = strings.Repeat("0", 64)
		m.paperLedgers["one"] = r
	} else if p, ok := s.(*Postgres); ok {
		if _, err := p.pool.Exec(ctx, `UPDATE mantik_paper_ledger_rehearsals SET payload=jsonb_set(payload,'{sha256}',to_jsonb(repeat('0',64))) WHERE account_id='one'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PaperLedgerRehearsal(ctx, "one"); !errors.Is(err, paperledger.ErrInvalid) {
		t.Fatal("corrupt read", err)
	}
	if _, err := s.AppendPaperLedgerRehearsal(ctx, "one", c, now); !errors.Is(err, paperledger.ErrInvalid) {
		t.Fatal("corrupt retry", err)
	}
}
func TestPostgresContractsPaperLedgerUpgradeRestart(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "paperledgerupgrade")
	receiver, _ := s.ResearchSnapshot(ctx)
	at := time.Unix(100, 0).UTC()
	a, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "upgraded", Currency: "USD", OpeningCashCents: 1000, CapitalLimitCents: 500}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.PaperAccounts(ctx)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DELETE FROM mantik_schema WHERE version>=28"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	up, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	after, _ := up.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed funding")
	}
	r, err := up.PaperLedgerRehearsal(ctx, "upgraded")
	if err != nil || len(r.Accepted) != 0 {
		t.Fatal(err)
	}
	c := paperledger.AppendCommand{ReceiverID: receiver.ID, AccountSHA256: a.Account.SHA256, ExpectedHeadSHA256: r.Result.LedgerHeadSHA256, ScopeCode: paperledger.RehearsalScope, Entry: paperledger.Entry{ID: "reserved", Kind: "reserve", ReservationID: "one", AmountCents: 400, OccurredAt: at}}
	original, err := up.AppendPaperLedgerRehearsal(ctx, "upgraded", c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := up.PaperLedgerRehearsal(ctx, "upgraded")
	up.Close()
	again, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, err := again.PaperLedgerRehearsal(ctx, "upgraded")
	if err != nil || !reflect.DeepEqual(expected, got) {
		t.Fatal("restart", err)
	}
	retry, err := again.AppendPaperLedgerRehearsal(ctx, "upgraded", c, time.Now)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(original.Accepted, retry.Accepted) {
		t.Fatal("retry restart", err)
	}
	if _, err := again.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=28"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("checksum accepted")
	}
}
