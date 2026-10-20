package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type fundedBackend interface {
	actionBackend
	PaperAccountStore
	ResearchReceiverStore
	FixtureCapitalStore
}

func bindCapitalBackend(t *testing.T, s fundedBackend) FixtureCapitalBinding {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "funded", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt })
	if err != nil {
		t.Fatal(err)
	}
	c := FixtureCapitalBinding{ReceiverID: receiver.ID, AccountID: "funded", AccountSHA256: r.Account.SHA256, ScopeCode: FixtureCapitalBindingScope}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := s.BindFixtureCapital(ctx, c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	return c
}

func TestPostgresContractsFundedActionRecovery(t *testing.T) {
	s := timerPostgres(t, "fundedrecovery")
	bindCapitalBackend(t, s)
	config := s.pool.Config().ConnString()
	actionRecovery(t, s)
	// The shared recovery proof reopens its store after adapter commit. Reopen
	// again here to check persisted capital after late acknowledgment.
	next, err := OpenPostgres(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	v, err := next.FixtureCapital(context.Background())
	if err != nil || !v.Durable || v.Capital.PositionCostCents != 100000 || v.Capital.ReservedCashCents != 0 {
		t.Fatal(v, err)
	}
}

func TestPostgresContractsFundedCapitalLifecycle(t *testing.T) {
	s := timerPostgres(t, "fundedlifecycle")
	c := bindCapitalBackend(t, s)
	ctx := context.Background()
	accounts, _ := s.PaperAccounts(ctx)
	for _, q := range []domain.Observation{workflow.Fixtures()[1], workflow.Fixtures()[3]} {
		if _, err := s.Ingest(ctx, q); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, s, q.ReceivedAt)
	}
	v, err := s.FixtureCapital(ctx)
	if err != nil || !v.Durable || !v.WorkerBound || v.QualificationCredit || v.Capital.AvailableCashCents != 94000 || v.Capital.RealizedPnLCents != -6000 || v.Capital.Position != nil || v.Capital.ReservedCashCents != 0 {
		t.Fatal(v, err)
	}
	again, err := s.BindFixtureCapital(ctx, c)
	if err != nil || !reflect.DeepEqual(v, again) {
		t.Fatal("binding retry reset balance", err)
	}
	after, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(accounts, after) {
		t.Fatal("funding changed")
	}
	// Old writers cannot silently discard the account pin.
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_state SET payload=payload-'fixture_capital_binding'"); err == nil {
		t.Fatal("old writer discarded binding")
	}
}

func TestPostgresContractsFundedCapitalPartial(t *testing.T) {
	s := timerPostgres(t, "fundedpartial")
	bindCapitalBackend(t, s)
	adapterStatuses(t, s)
	v, err := s.FixtureCapital(context.Background())
	if err != nil || v.Capital.PositionCostCents != 100000 {
		t.Fatal(v, err)
	}
}

func TestPostgresContractsFundedCapitalRollback(t *testing.T) {
	s := timerPostgres(t, "fundedrollback")
	bindCapitalBackend(t, s)
	ctx := context.Background()
	q := workflow.Fixtures()[1]
	if _, err := s.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimActions(ctx, "worker", q.ReceivedAt, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	c := claims[0]
	beforeBegin, _ := s.FixtureCapital(ctx)
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT fail_capital_begin CHECK (payload->>'kind'<>'action.submitting')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAction(ctx, c, q.ReceivedAt); err == nil {
		t.Fatal("failed write-ahead accepted")
	}
	afterBegin, err := s.FixtureCapital(ctx)
	if err != nil || !reflect.DeepEqual(beforeBegin, afterBegin) {
		t.Fatal("reservation survived failed write-ahead", err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT fail_capital_begin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAction(ctx, c, q.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_fixture_effects ADD CONSTRAINT fail_capital_submit CHECK (payload->>'status'<>'completed')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitAction(ctx, c, q.ReceivedAt); err == nil {
		t.Fatal("failed adapter commit accepted")
	}
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_fixture_effects").Scan(&count); err != nil || count != 0 {
		t.Fatal("adapter effect survived failed commit", err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_fixture_effects DROP CONSTRAINT fail_capital_submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitAction(ctx, c, q.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	capital, err := s.FixtureCapital(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT fail_capital_ack CHECK (payload->>'kind'<>'execution.completed')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileAction(ctx, c, q.ReceivedAt); err == nil {
		t.Fatal("ack failure accepted")
	}
	after, _ := s.Snapshot(ctx)
	again, err := s.FixtureCapital(ctx)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(capital, again) {
		t.Fatal("partial capital ack", err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT fail_capital_ack"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileAction(ctx, c, q.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, "SELECT payload FROM mantik_fixture_capital").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_fixture_capital SET payload=jsonb_set(payload,'{result_sha256}',to_jsonb(repeat('a',64)))"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FixtureCapital(ctx); err == nil {
		t.Fatal("corrupt checkpoint read")
	}
	if _, err := s.ClaimActions(ctx, "other", q.ReceivedAt, time.Minute); err == nil {
		t.Fatal("corrupt checkpoint allowed worker mutation")
	}
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_fixture_capital SET payload=$1", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM mantik_fixture_capital"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FixtureCapital(ctx); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("missing checkpoint became unbound", err)
	}
	if _, err := s.ClaimActions(ctx, "other", q.ReceivedAt, time.Minute); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("missing checkpoint bypassed capital", err)
	}
}

func TestPostgresContractsFixtureCapitalUpgrade(t *testing.T) {
	s := timerPostgres(t, "capitalupgrade")
	ctx := context.Background()
	before, _ := s.Snapshot(ctx)
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DELETE FROM mantik_schema WHERE version>=29"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	next, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after, _ := next.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade rewrote existing fixture")
	}
	if _, err := next.FixtureCapital(ctx); !errors.Is(err, fixturecapital.ErrInvalid) {
		t.Fatal("upgrade activated capital", err)
	}
	if _, err := next.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=29"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
