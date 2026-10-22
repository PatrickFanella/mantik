package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type strategyBackend interface {
	Store
	NewsStore
	PaperStrategyStore
}

func strategyDefinition(id string) paperstrategy.Definition {
	return paperstrategy.Definition{ID: id, Method: paperstrategy.Method, Symbol: "SPY", Feed: "iex", InputEvidenceClass: "synthetic_unqualified", EntryAtOrBelowCents: 10000, Quantity: 10, Safety: domain.Safety{MaxNotionalCents: 100000, StopCents: 9500, TargetCents: 11000, MaxHoldingSeconds: 3600, MaxQuoteAgeSeconds: 60}}
}
func TestMemoryPaperStrategies(t *testing.T) { strategyContracts(t, NewMemory()) }
func TestPostgresContractsPaperStrategies(t *testing.T) {
	strategyContracts(t, timerPostgres(t, "paperstrategies"))
}
func strategyContracts(t *testing.T, s strategyBackend) {
	ctx := context.Background()
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	c := paperstrategy.Command{ReceiverID: news.ReceiverID, Definition: strategyDefinition("baseline")}
	var calls atomic.Int32
	at := time.Now().UTC()
	clock := func() time.Time { calls.Add(1); return at }
	noClock := func() time.Time { panic("retry read clock") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.RegisterPaperStrategy(ctx, c, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate registration reread clock", calls.Load())
	}
	first, err := s.PaperStrategies(ctx)
	if err != nil || first.Validate() != nil || len(first.Records) != 1 {
		t.Fatal(first, err)
	}
	r, err := s.RegisterPaperStrategy(ctx, c, noClock)
	if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Record, first.Records[0]) {
		t.Fatal(r, err)
	}
	r.Record.Command.Definition.Symbol = "MUTATED"
	bad := c
	bad.Definition.Quantity--
	if _, err := s.RegisterPaperStrategy(ctx, bad, noClock); !errors.Is(err, paperstrategy.ErrConflict) {
		t.Fatal(err)
	}
	bad = c
	bad.ReceiverID = "receiver-other_owner_00000001"
	if _, err := s.RegisterPaperStrategy(ctx, bad, noClock); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	for i := 1; i < paperstrategy.MaxDefinitions; i++ {
		v := c
		v.Definition.ID = fmt.Sprintf("strategy-%02d", i)
		if _, err := s.RegisterPaperStrategy(ctx, v, clock); err != nil {
			t.Fatal(i, err)
		}
	}
	bad = c
	bad.Definition.ID = "overflow"
	if _, err := s.RegisterPaperStrategy(ctx, bad, noClock); !errors.Is(err, paperstrategy.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := s.RegisterPaperStrategy(ctx, c, noClock); err != nil {
		t.Fatal("retry at capacity", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.RegisterPaperStrategy(canceled, c, noClock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	all, err := s.PaperStrategies(ctx)
	if err != nil || len(all.Records) != paperstrategy.MaxDefinitions || all.Records[0].Command.Definition.Symbol != "SPY" {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("configuration changed runtime state")
	}
	// Corruption must block discovery and selected retries, even with a valid
	// self hash. The original rows are restored for restart verification.
	original := all.Records[len(all.Records)-1]
	damaged := original
	damaged.AdmissionAvailable = true
	damaged.SHA256 = ""
	damaged.SHA256 = workerplan.Hash(damaged)
	set := func(v paperstrategy.Record) {
		if m, ok := s.(*Memory); ok {
			m.paperStrategies[v.Command.Definition.ID] = v
		} else {
			raw, _ := json.Marshal(v)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_strategies SET payload=$1 WHERE id=$2", raw, v.Command.Definition.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	set(damaged)
	if _, err := s.PaperStrategies(ctx); err == nil {
		t.Fatal("corrupt catalogue accepted")
	}
	if _, err := s.RegisterPaperStrategy(ctx, c, noClock); err == nil {
		t.Fatal("retry hid corruption")
	}
	if _, err := s.RegisterPaperStrategy(ctx, bad, noClock); err == nil {
		t.Fatal("new registration hid corruption")
	}
	set(original)
	if pg, ok := s.(*Postgres); ok {
		cfg := pg.pool.Config().ConnString()
		pg.Close()
		next, err := OpenPostgres(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		got, err := next.PaperStrategies(ctx)
		if err != nil || !reflect.DeepEqual(got, all) {
			t.Fatal("restart changed registry", err)
		}
	}
}
func TestPostgresContractsPaperStrategyUpgradeAndAtomicity(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "strategyupgrade")
	a := policyAccount(t, s, "preserved")
	if _, err := s.RegisterAllocationPolicy(ctx, policyCommand(a, "preserved-policy"), time.Now); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	policies, _ := s.AllocationPolicies(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	cfg := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DELETE FROM mantik_schema WHERE version>=37"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	actual, _ := up.Snapshot(ctx)
	p, _ := up.AllocationPolicies(ctx)
	funding, _ := up.PaperAccounts(ctx)
	if !reflect.DeepEqual(actual, before) || !reflect.DeepEqual(p, policies) || !reflect.DeepEqual(funding, accounts) {
		t.Fatal("upgrade changed prior state")
	}
	n, err := up.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cmd := paperstrategy.Command{ReceiverID: n.ReceiverID, Definition: strategyDefinition("new")}
	if _, err := up.pool.Exec(ctx, `CREATE FUNCTION reject_paper_strategy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test strategy rollback'; END $$; CREATE TRIGGER reject_paper_strategy BEFORE INSERT ON mantik_paper_strategies FOR EACH ROW EXECUTE FUNCTION reject_paper_strategy()`); err != nil {
		t.Fatal(err)
	}
	if _, err := up.RegisterPaperStrategy(ctx, cmd, time.Now); err == nil {
		t.Fatal("failed insert accepted")
	}
	empty, err := up.PaperStrategies(ctx)
	if err != nil || len(empty.Records) != 0 {
		t.Fatal("partial write", err)
	}
	if _, err := up.pool.Exec(ctx, "DROP TRIGGER reject_paper_strategy ON mantik_paper_strategies; DROP FUNCTION reject_paper_strategy()"); err != nil {
		t.Fatal(err)
	}
	if _, err := up.RegisterPaperStrategy(ctx, cmd, time.Now); err != nil {
		t.Fatal(err)
	}
	if _, err := up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=37"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, cfg); err == nil {
		broken.Close()
		t.Fatal("changed migration checksum accepted")
	}
}
