package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

type allocationBackend interface {
	Store
	PaperAccountStore
	ResearchReceiverStore
	AllocationPolicyStore
	FixtureCapitalStore
}

func policyCommand(a paperaccount.Account, id string) allocation.PolicyCommand {
	return allocation.PolicyCommand{ReceiverID: a.ReceiverID, AccountID: a.Config.ID, Policy: allocation.Policy{ID: id, AccountSHA256: a.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: "baseline-v1", ArtifactSHA256: strings.Repeat("a", 64), LimitCents: 40000, Priority: 0}}, Symbols: []allocation.Symbol{{ID: "DEMO", LimitCents: 40000, Groups: []string{"group"}}}, Groups: []allocation.Group{{ID: "group", LimitCents: 40000}}}}
}
func policyAccount(t *testing.T, s allocationBackend, id string) paperaccount.Account {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: id, Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 50000}, func() time.Time { return time.Unix(100, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	return r.Account
}
func TestMemoryAllocationPolicies(t *testing.T) { allocationPolicyContract(t, NewQueuedMemory()) }
func TestPostgresContractsAllocationPolicies(t *testing.T) {
	allocationPolicyContract(t, timerPostgres(t, "allocationpolicies"))
}
func allocationPolicyContract(t *testing.T, s allocationBackend) {
	ctx := context.Background()
	a := policyAccount(t, s, "owner")
	cmd := policyCommand(a, "policy-v1")
	before, _ := s.Snapshot(ctx)
	empty, err := s.AllocationPolicies(ctx)
	if err != nil || empty.Validate() != nil || len(empty.Records) != 0 {
		t.Fatal(empty, err)
	}
	bad := cmd
	bad.ReceiverID = "receiver-other_other_other"
	if _, err = s.RegisterAllocationPolicy(ctx, bad, time.Now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	bad = cmd
	bad.AccountID = "missing"
	if _, err = s.RegisterAllocationPolicy(ctx, bad, time.Now); !errors.Is(err, allocation.ErrPolicyNotFound) {
		t.Fatal(err)
	}
	bad = cmd
	bad.Policy = clone(cmd.Policy)
	bad.Policy.Symbols[0].Groups = []string{"missing"}
	if _, err = s.RegisterAllocationPolicy(ctx, bad, func() time.Time { t.Fatal("invalid policy called clock"); return time.Time{} }); !errors.Is(err, allocation.ErrInvalid) {
		t.Fatal(err)
	}
	bad = cmd
	bad.Policy.AccountSHA256 = strings.Repeat("f", 64)
	if _, err = s.RegisterAllocationPolicy(ctx, bad, time.Now); !errors.Is(err, allocation.ErrPolicyConflict) {
		t.Fatal(err)
	}
	var clock atomic.Int32
	now := func() time.Time { clock.Add(1); return time.Unix(200, 0).UTC() }
	results := make(chan allocation.PolicyReceipt, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.RegisterAllocationPolicy(ctx, cmd, now)
			if err != nil {
				failures <- err
			} else {
				results <- r
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var original allocation.PolicyRecord
	fresh := 0
	for r := range results {
		if r.Record.Validate() != nil {
			t.Fatal(r)
		}
		if !r.Duplicate {
			fresh++
		}
		if original.Schema != "" && !reflect.DeepEqual(original, r.Record) {
			t.Fatal("retry changed record")
		}
		original = r.Record
	}
	if fresh != 1 || clock.Load() != 1 {
		t.Fatal(fresh, clock.Load())
	}
	bad = clone(cmd)
	bad.Policy.Strategies[0].Priority++
	if _, err = s.RegisterAllocationPolicy(ctx, bad, now); !errors.Is(err, allocation.ErrPolicyConflict) {
		t.Fatal(err)
	}
	bad = cmd
	bad.Policy.ID = "replacement"
	if _, err = s.RegisterAllocationPolicy(ctx, bad, now); !errors.Is(err, allocation.ErrPolicyConflict) {
		t.Fatal("account acquired second policy", err)
	}
	// Neither registration nor a readback may leak nested slices into the store.
	catalog, err := s.AllocationPolicies(ctx)
	if err != nil || catalog.Validate() != nil {
		t.Fatal(err)
	}
	catalog.Records[0].Command.Policy.Symbols[0].Groups[0] = "changed"
	again, _ := s.AllocationPolicies(ctx)
	if !reflect.DeepEqual(again.Records[0], original) {
		t.Fatal("read aliases policy")
	}
	if _, err = s.BindFixtureCapital(ctx, FixtureCapitalBinding{ReceiverID: a.ReceiverID, AccountID: a.Config.ID, AccountSHA256: a.SHA256, ScopeCode: FixtureCapitalBindingScope}); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("fixture claimed general account", err)
	}
	// The reciprocal claim remains blocked after fixture binding. Other accounts
	// may retain independent policies without changing the executable fixture.
	b := policyAccount(t, s, "fixture")
	if _, err = s.BindFixtureCapital(ctx, FixtureCapitalBinding{ReceiverID: b.ReceiverID, AccountID: b.Config.ID, AccountSHA256: b.SHA256, ScopeCode: FixtureCapitalBindingScope}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterAllocationPolicy(ctx, policyCommand(b, "fixture-policy"), now); !errors.Is(err, allocation.ErrPolicyConflict) {
		t.Fatal("policy claimed fixture account", err)
	}
	for i := 2; i < paperaccount.MaxAccounts; i++ {
		a := policyAccount(t, s, fmt.Sprintf("account-%02d", i))
		if _, err = s.RegisterAllocationPolicy(ctx, policyCommand(a, fmt.Sprintf("policy-%02d", i)), now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.RegisterAllocationPolicy(ctx, cmd, func() time.Time { t.Fatal("retry called clock"); return time.Time{} })
	if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Record, original) {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(ctx)
	before.State.FixtureCapitalBinding = after.State.FixtureCapitalBinding
	if !reflect.DeepEqual(before, after) {
		t.Fatal("policy mutated executable state")
	}
	// Whole-prefix validation refuses corruption even when retrying another row.
	if m, ok := s.(*Memory); ok {
		r := m.allocationPolicies["policy-02"]
		r.SHA256 = "changed"
		m.allocationPolicies["policy-02"] = r
	} else if p, ok := s.(*Postgres); ok {
		if _, err = p.pool.Exec(ctx, `UPDATE mantik_allocation_policies SET payload=jsonb_set(payload,'{sha256}','"changed"') WHERE id='policy-02'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AllocationPolicies(ctx); !errors.Is(err, allocation.ErrInvalid) {
		t.Fatal("corrupt prefix accepted", err)
	}
	if _, err = s.RegisterAllocationPolicy(ctx, cmd, now); !errors.Is(err, allocation.ErrInvalid) {
		t.Fatal("retry hid corruption", err)
	}
}

func TestPostgresContractsAllocationPolicyUpgradeRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "allocationupgrade")
	a := policyAccount(t, s, "owner")
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DELETE FROM mantik_schema WHERE version>=36"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	var maximum int
	if err = up.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 78 {
		t.Fatal(maximum, err)
	}
	after, _ := up.Snapshot(ctx)
	actual, _ := up.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, actual) {
		t.Fatal("upgrade changed existing records")
	}
	cmd := policyCommand(a, "policy")
	if _, err = up.pool.Exec(ctx, `CREATE FUNCTION reject_allocation_policy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test allocation insert rollback'; END $$; CREATE TRIGGER reject_allocation_policy BEFORE INSERT ON mantik_allocation_policies FOR EACH ROW EXECUTE FUNCTION reject_allocation_policy()`); err != nil {
		t.Fatal(err)
	}
	if _, err = up.RegisterAllocationPolicy(ctx, cmd, time.Now); err == nil {
		t.Fatal("insert failure ignored")
	}
	empty, err := up.AllocationPolicies(ctx)
	if err != nil || len(empty.Records) != 0 {
		t.Fatal("partial policy after failed insert", empty, err)
	}
	if _, err = up.pool.Exec(ctx, "DROP TRIGGER reject_allocation_policy ON mantik_allocation_policies; DROP FUNCTION reject_allocation_policy()"); err != nil {
		t.Fatal(err)
	}
	r, err := up.RegisterAllocationPolicy(ctx, cmd, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	up.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	c, err := next.AllocationPolicies(ctx)
	if err != nil || len(c.Records) != 1 || !reflect.DeepEqual(c.Records[0], r.Record) {
		t.Fatal("restart changed policy", err)
	}
	if _, err = next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=36"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, config); err == nil {
		broken.Close()
		t.Fatal("changed checksum accepted")
	}
}

func TestMemoryAllocationOwnershipRace(t *testing.T) { allocationOwnershipRace(t, NewQueuedMemory()) }
func TestPostgresContractsAllocationOwnershipRace(t *testing.T) {
	allocationOwnershipRace(t, timerPostgres(t, "allocationownershiprace"))
}
func allocationOwnershipRace(t *testing.T, s allocationBackend) {
	ctx := context.Background()
	a := policyAccount(t, s, "contended")
	cmd := policyCommand(a, "policy")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { <-start; _, err := s.RegisterAllocationPolicy(ctx, cmd, time.Now); results <- err })
	wg.Go(func() {
		<-start
		_, err := s.BindFixtureCapital(ctx, FixtureCapitalBinding{ReceiverID: a.ReceiverID, AccountID: a.Config.ID, AccountSHA256: a.SHA256, ScopeCode: FixtureCapitalBindingScope})
		results <- err
	})
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, allocation.ErrPolicyConflict) && !errors.Is(err, fixturecapital.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("both owners claimed capital", successes)
	}
	c, err := s.AllocationPolicies(ctx)
	if err != nil || c.Validate() != nil {
		t.Fatal(err)
	}
	capital, err := s.FixtureCapital(ctx)
	if len(c.Records) == 1 {
		if !errors.Is(err, ErrFixtureCapitalUnbound) {
			t.Fatal("fixture also bound", err)
		}
	} else if err != nil || capital.Capital.AccountID != a.Config.ID {
		t.Fatal("ownership disappeared", err)
	}
}
