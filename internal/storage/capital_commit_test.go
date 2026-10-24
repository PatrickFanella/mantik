package storage

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresContractsCapitalConfigurationCommit(t *testing.T) {
	p := timerPostgres(t, "capitalconfigurationcommit")
	ctx := context.Background()
	owner, err := p.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operational, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("capital configuration retry read clock") }
	config := paperaccount.Config{ID: "capital-commit", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 50000}
	var account paperaccount.Account
	type operation struct {
		name string
		call func(context.Context, *Postgres) (any, error)
	}
	accountWrite := operation{"account", func(c context.Context, s *Postgres) (any, error) {
		return s.CreatePaperAccount(c, owner.ID, config, now)
	}}
	// Account creation already suppresses a failed new-write COMMIT. Keep it as
	// a control alongside policy creation and the separate exact-retry branches.
	for _, table := range []string{"mantik_paper_accounts", "mantik_allocation_policies"} {
		t.Run(table+"_deferred_commit", func(t *testing.T) {
			accountsBefore, e := p.PaperAccounts(ctx)
			if e != nil {
				t.Fatal(e)
			}
			policiesBefore, e := p.AllocationPolicies(ctx)
			if e != nil {
				t.Fatal(e)
			}
			op := accountWrite
			if table == "mantik_allocation_policies" {
				command := policyCommand(account, "capital-policy")
				op = operation{"policy", func(c context.Context, s *Postgres) (any, error) {
					return s.RegisterAllocationPolicy(c, command, now)
				}}
			}
			if _, e = p.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_capital_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'capital configuration deferred rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_capital_commit AFTER INSERT ON %s DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_capital_commit()`, table)); e != nil {
				t.Fatal(e)
			}
			got, e := op.call(ctx, p)
			if _, drop := p.pool.Exec(ctx, "DROP TRIGGER reject_capital_commit ON "+table+"; DROP FUNCTION reject_capital_commit()"); drop != nil {
				t.Fatal(drop)
			}
			if e == nil || !strings.Contains(e.Error(), "capital configuration deferred rollback") || !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed deferred COMMIT acknowledged capital configuration: %#v, %v", got, e)
			}
			accountsAfter, e := p.PaperAccounts(ctx)
			if e != nil || !reflect.DeepEqual(accountsBefore, accountsAfter) {
				t.Fatal("rollback changed accounts", e)
			}
			policiesAfter, e := p.AllocationPolicies(ctx)
			if e != nil || !reflect.DeepEqual(policiesBefore, policiesAfter) {
				t.Fatal("rollback changed policies", e)
			}
			if got, e = op.call(ctx, p); e != nil || reflect.ValueOf(got).IsZero() {
				t.Fatal("identity unavailable after confirmed rollback", got, e)
			}
			if table == "mantik_paper_accounts" {
				account = got.(paperaccount.Receipt).Account
			}
		})
	}
	policy := policyCommand(account, "capital-policy")
	spareConfig := config
	spareConfig.ID = "capital-spare"
	spare, err := p.CreatePaperAccount(ctx, owner.ID, spareConfig, now)
	if err != nil {
		t.Fatal(err)
	}
	sparePolicy := policyCommand(spare.Account, "spare-policy")
	freshConfig := config
	freshConfig.ID = "cancelled-new-account"
	accounts, err := p.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := p.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operations := []operation{
		{"account_read", func(c context.Context, s *Postgres) (any, error) { return s.PaperAccounts(c) }},
		{"policy_read", func(c context.Context, s *Postgres) (any, error) { return s.AllocationPolicies(c) }},
		{"account_retry", func(c context.Context, s *Postgres) (any, error) {
			return s.CreatePaperAccount(c, owner.ID, config, noClock)
		}},
		{"policy_retry", func(c context.Context, s *Postgres) (any, error) {
			return s.RegisterAllocationPolicy(c, policy, noClock)
		}},
		{"account_new", func(c context.Context, s *Postgres) (any, error) {
			return s.CreatePaperAccount(c, owner.ID, freshConfig, now)
		}},
		{"policy_new", func(c context.Context, s *Postgres) (any, error) {
			return s.RegisterAllocationPolicy(c, sparePolicy, now)
		}},
	}
	for _, op := range operations {
		t.Run(op.name+"_commit_cancel", func(t *testing.T) {
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			tracer := &cancelCollectorCommit{cancel: cancel}
			poolConfig := p.pool.Config()
			poolConfig.ConnConfig.Tracer = tracer
			pool, e := pgxpool.NewWithConfig(ctx, poolConfig)
			if e != nil {
				t.Fatal(e)
			}
			got, e := op.call(request, &Postgres{pool: pool})
			pool.Close()
			if !tracer.fired.Load() || e == nil || !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed cancelled COMMIT acknowledged capital configuration: %#v, %v; fired=%v", got, e, tracer.fired.Load())
			}
			actualAccounts, e := p.PaperAccounts(ctx)
			if e != nil || !reflect.DeepEqual(accounts, actualAccounts) {
				t.Fatal("failed transaction changed accounts", e)
			}
			actualPolicies, e := p.AllocationPolicies(ctx)
			if e != nil || !reflect.DeepEqual(policies, actualPolicies) {
				t.Fatal("failed transaction changed policies", e)
			}
		})
	}
	retry, err := p.CreatePaperAccount(ctx, owner.ID, config, noClock)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Account, account) {
		t.Fatal("account retry changed committed owner", retry, err)
	}
	policyRetry, err := p.RegisterAllocationPolicy(ctx, policy, noClock)
	if err != nil || !policyRetry.Duplicate || !reflect.DeepEqual(policyRetry.Record, policies.Records[0]) {
		t.Fatal("policy retry changed committed owner", policyRetry, err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actualAccounts, err := reopened.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accounts, actualAccounts) {
		t.Fatal("reopen changed accounts", err)
	}
	actualPolicies, err := reopened.AllocationPolicies(ctx)
	if err != nil || !reflect.DeepEqual(policies, actualPolicies) {
		t.Fatal("reopen changed policies", err)
	}
	after, err := p.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(operational, after) {
		t.Fatal("capital configuration changed executable operations", err)
	}
}
