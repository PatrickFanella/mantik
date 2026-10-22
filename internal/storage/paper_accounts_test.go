package storage

import (
	"context"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type paperAccountBackend interface {
	PaperAccountStore
	ResearchReceiverStore
	Store
}

func TestMemoryPaperAccounts(t *testing.T) { paperAccountContract(t, NewMemory()) }
func TestPostgresContractsPaperAccounts(t *testing.T) {
	paperAccountContract(t, timerPostgres(t, "paperaccounts"))
}
func paperAccountContract(t *testing.T, s paperAccountBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	c := paperaccount.Config{ID: "pilot", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 50000}
	var clockCalls atomic.Int32
	at := time.Unix(100, 0).UTC()
	now := func() time.Time { clockCalls.Add(1); return at }
	empty, err := s.PaperAccounts(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Accounts) != 0 {
		t.Fatal(empty, err)
	}
	if _, err = s.CreatePaperAccount(ctx, "receiver-other_other_other", c, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	bad := c
	bad.OpeningCashCents = -1
	if _, err = s.CreatePaperAccount(ctx, receiver.ID, bad, now); !errors.Is(err, paperaccount.ErrInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CreatePaperAccount(canceled, receiver.ID, c, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan paperaccount.Receipt, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			r, err := s.CreatePaperAccount(ctx, receiver.ID, c, now)
			if err != nil {
				failures <- err
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	fresh := 0
	var original paperaccount.Account
	for r := range results {
		if !r.Duplicate {
			fresh++
		}
		if r.Account.Validate() != nil {
			t.Fatal(r)
		}
		if original.Schema != "" && !reflect.DeepEqual(original, r.Account) {
			t.Fatal("duplicate changed receipt")
		}
		original = r.Account
	}
	if fresh != 1 || clockCalls.Load() != 1 {
		t.Fatal("duplicate opening funding", fresh, clockCalls.Load())
	}
	bad = c
	bad.CapitalLimitCents--
	if _, err = s.CreatePaperAccount(ctx, receiver.ID, bad, now); !errors.Is(err, paperaccount.ErrConflict) {
		t.Fatal(err)
	}
	catalog, err := s.PaperAccounts(ctx)
	if err != nil || len(catalog.Accounts) != 1 {
		t.Fatal(err)
	}
	catalog.Accounts[0].Config.CapitalLimitCents = 1
	again, _ := s.PaperAccounts(ctx)
	if again.Accounts[0].Config != c {
		t.Fatal("readback aliases stored state")
	}
	for i := 1; i < paperaccount.MaxAccounts; i++ {
		next := c
		next.ID = fmt.Sprintf("account-%02d", i)
		if _, err = s.CreatePaperAccount(ctx, receiver.ID, next, now); err != nil {
			t.Fatal(err)
		}
	}
	next := c
	next.ID = "overflow"
	if _, err = s.CreatePaperAccount(ctx, receiver.ID, next, now); !errors.Is(err, paperaccount.ErrCapacity) {
		t.Fatal(err)
	}
	retry, err := s.CreatePaperAccount(ctx, receiver.ID, c, func() time.Time { t.Fatal("retry called clock"); return time.Time{} })
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(original, retry.Account) {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("account creation changed executable fixture")
	}
	if m, ok := s.(*Memory); ok {
		a := m.paperAccounts[c.ID]
		a.SHA256 = "bad"
		m.paperAccounts[c.ID] = a
	} else if p, ok := s.(*Postgres); ok {
		if _, err = p.pool.Exec(ctx, `UPDATE mantik_paper_accounts SET payload=jsonb_set(payload,'{sha256}',to_jsonb(repeat('0',64))) WHERE id=$1`, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.PaperAccounts(ctx); !errors.Is(err, paperaccount.ErrInvalid) {
		t.Fatal("corrupt catalogue accepted", err)
	}
	if _, err = s.CreatePaperAccount(ctx, receiver.ID, c, now); !errors.Is(err, paperaccount.ErrInvalid) {
		t.Fatal("corrupt retry accepted", err)
	}
}
func TestPostgresContractsPaperAccountUpgradeAndRestart(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "paperaccountupgrade")
	before, _ := s.Snapshot(ctx)
	receiver, _ := s.ResearchSnapshot(ctx)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DELETE FROM mantik_schema WHERE version>=27"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	up, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	after, _ := up.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed fixture")
	}
	catalog, err := up.PaperAccounts(ctx)
	if err != nil || catalog.ReceiverID != receiver.ID || len(catalog.Accounts) != 0 {
		t.Fatal(catalog, err)
	}
	c := paperaccount.Config{ID: "restored", Currency: "USD", OpeningCashCents: 100, CapitalLimitCents: 50}
	original, err := up.CreatePaperAccount(ctx, receiver.ID, c, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	up.Close()
	again, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	retry, err := again.CreatePaperAccount(ctx, receiver.ID, c, time.Now)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(original.Account, retry.Account) {
		t.Fatal("restart changed funding", err)
	}
	if _, err = again.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=27"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("migration checksum mismatch accepted")
	}
}

func TestMemoryPaperAccountCapacityRace(t *testing.T) { paperAccountCapacityRace(t, NewMemory()) }
func TestPostgresContractsPaperAccountCapacityRace(t *testing.T) {
	paperAccountCapacityRace(t, timerPostgres(t, "paperaccountcapacity"))
}
func paperAccountCapacityRace(t *testing.T, s paperAccountBackend) {
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	c := paperaccount.Config{Currency: "USD", OpeningCashCents: 100, CapitalLimitCents: 50}
	for i := 0; i < paperaccount.MaxAccounts-2; i++ {
		c.ID = fmt.Sprintf("seed-%d", i)
		if _, err := s.CreatePaperAccount(ctx, receiver.ID, c, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var accepted, full atomic.Int32
	for i := range 8 {
		wg.Go(func() {
			candidate := c
			candidate.ID = fmt.Sprintf("racer-%d", i)
			_, err := s.CreatePaperAccount(ctx, receiver.ID, candidate, time.Now)
			if err == nil {
				accepted.Add(1)
			} else if errors.Is(err, paperaccount.ErrCapacity) {
				full.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	catalog, err := s.PaperAccounts(ctx)
	if err != nil || catalog.Validate() != nil || len(catalog.Accounts) != paperaccount.MaxAccounts || accepted.Load() != 2 || full.Load() != 6 {
		t.Fatal(err, len(catalog.Accounts), accepted.Load(), full.Load())
	}
}
