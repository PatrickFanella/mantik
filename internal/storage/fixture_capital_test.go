package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func fundedMemory(t *testing.T, cash, limit int64) (*Memory, FixtureCapitalBinding) {
	t.Helper()
	m := NewQueuedMemory()
	c := createCapitalAccount(t, m, "funded", cash, limit)
	if _, err := m.BindFixtureCapital(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return m, c
}
func createCapitalAccount(t *testing.T, m *Memory, id string, cash, limit int64) FixtureCapitalBinding {
	t.Helper()
	r, err := m.CreatePaperAccount(context.Background(), m.receiverID, paperaccount.Config{ID: id, Currency: "USD", OpeningCashCents: cash, CapitalLimitCents: limit}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt.Add(-time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	return FixtureCapitalBinding{ReceiverID: m.receiverID, AccountID: id, AccountSHA256: r.Account.SHA256, ScopeCode: FixtureCapitalBindingScope}
}
func capitalRead(t *testing.T, m *Memory) FixtureCapitalReadback {
	t.Helper()
	v, err := m.FixtureCapital(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := v.Capital
	if !v.WorkerBound || v.Durable || v.QualificationCredit || s.QualificationCredit || s.ServiceIntegrated || s.AvailableCashCents+s.ReservedCashCents+s.PositionCostCents != s.OpeningCashCents+s.RealizedPnLCents {
		t.Fatal("incorrect authority/conservation", v)
	}
	return v
}
func capitalClaim(t *testing.T, m *Memory) (domain.ActionClaim, time.Time) {
	t.Helper()
	q := workflow.Fixtures()[1]
	if _, err := m.Ingest(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	claims, err := m.ClaimActions(context.Background(), "funded-worker", q.ReceivedAt, time.Second*30)
	if err != nil || len(claims) != 1 {
		t.Fatal("claim", claims, err)
	}
	return claims[0], q.ReceivedAt
}
func TestFixtureCapitalBindingIsExplicitAndPristine(t *testing.T) {
	ctx := context.Background()
	m := NewQueuedMemory()
	if _, err := m.FixtureCapital(ctx); err == nil {
		t.Fatal("account enforcement enabled by default")
	}
	c := createCapitalAccount(t, m, "first", 100000, 100000)
	for _, mutate := range []func(*FixtureCapitalBinding){
		func(v *FixtureCapitalBinding) { v.ReceiverID += "other" },
		func(v *FixtureCapitalBinding) { v.AccountID = "missing" },
		func(v *FixtureCapitalBinding) { v.AccountSHA256 = "changed" },
		func(v *FixtureCapitalBinding) { v.ScopeCode = "rehearsal" },
	} {
		bad := c
		mutate(&bad)
		if _, err := m.BindFixtureCapital(ctx, bad); err == nil || m.fixtureCapital != nil {
			t.Fatal("invalid binding changed authority", err)
		}
	}
	before, err := m.BindFixtureCapital(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	other := createCapitalAccount(t, m, "other", 100000, 100000)
	if _, err := m.BindFixtureCapital(ctx, other); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("rebound account", err)
	}
	capitalClaim(t, m)
	again, err := m.BindFixtureCapital(ctx, c)
	if err != nil || !reflect.DeepEqual(before, again) {
		t.Fatal("exact retry changed binding", err)
	}
	for _, legacy := range []*Memory{NewMemory(), NewQueuedMemory()} {
		c := createCapitalAccount(t, legacy, "late", 100000, 100000)
		if _, err := legacy.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.BindFixtureCapital(ctx, c); err == nil {
			t.Fatal("bound synchronous or already-active fixture")
		}
	}
	synchronous := NewMemory()
	c = createCapitalAccount(t, synchronous, "early", 100000, 100000)
	if _, err := synchronous.BindFixtureCapital(ctx, c); err == nil {
		t.Fatal("bound synchronous ingestion path")
	}
}

func TestFundedMemoryActionRecovery(t *testing.T) {
	m, _ := fundedMemory(t, 100000, 100000)
	// Existing delivery proof includes concurrent claims/submits/acks, a lost
	// pre-submit worker, replacement generation fencing, and late known effects.
	actionRecovery(t, m)
	s := capitalRead(t, m).Capital
	if s.PositionCostCents != 100000 || s.AvailableCashCents != 0 || s.ReservedCashCents != 0 || len(s.Commitments) != 1 || s.Commitments[0].Status != "completed" {
		t.Fatal(s)
	}
}

func TestFundedMemoryEntryExitAndExactRetries(t *testing.T) {
	ctx := context.Background()
	m, binding := fundedMemory(t, 100000, 100000)
	accounts, _ := m.PaperAccounts(ctx)
	for _, q := range []domain.Observation{workflow.Fixtures()[1], workflow.Fixtures()[3]} {
		if _, err := m.Ingest(ctx, q); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, m, q.ReceivedAt)
	}
	s := capitalRead(t, m).Capital
	if s.AvailableCashCents != 94000 || s.RealizedPnLCents != -6000 || s.PositionCostCents != 0 || s.ReservedCashCents != 0 || s.Position != nil || len(s.Commitments) != 2 {
		t.Fatal(s)
	}
	after, _ := m.PaperAccounts(ctx)
	if !reflect.DeepEqual(accounts, after) || len(m.paperLedgers) != 0 {
		t.Fatal("capital altered immutable funding or rehearsal records")
	}
	if _, err := m.BindFixtureCapital(ctx, binding); err != nil {
		t.Fatal("binding retry reset capital", err)
	}
	if capitalRead(t, m).Capital.SHA256 != s.SHA256 {
		t.Fatal("binding retry rewound cash")
	}
	// Every terminal retry is read-only, including Begin's fast path.
	before, _ := m.Snapshot(ctx)
	for id, a := range before.State.Actions.Items {
		c := domain.ActionClaim{ID: id, Worker: a.Worker, Generation: a.Generation}
		if _, err := m.BeginAction(ctx, c, workflow.Fixtures()[3].ReceivedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReconcileAction(ctx, c, workflow.Fixtures()[3].ReceivedAt); err != nil {
			t.Fatal(err)
		}
	}
	again, _ := m.Snapshot(ctx)
	if !reflect.DeepEqual(before, again) || capitalRead(t, m).Capital.SHA256 != s.SHA256 {
		t.Fatal("terminal retry changed aggregate")
	}
}

func TestFundedMemoryCapitalRejectionIsRetained(t *testing.T) {
	for _, test := range []struct{ cash, limit int64 }{{99999, 99999}, {200000, 99999}} {
		m, _ := fundedMemory(t, test.cash, test.limit)
		c, now := capitalClaim(t, m)
		a, err := m.BeginAction(context.Background(), c, now)
		if err != nil || a.Status != "rejected" || a.Code != "fixture_capital_limit" || a.Attempts != 0 {
			t.Fatal("capital limit lacked a terminal reason", a, err)
		}
		s := capitalRead(t, m).Capital
		if s.AvailableCashCents != test.cash || s.ReservedCashCents != 0 || len(s.Commitments) != 0 || len(m.effects) != 0 || m.state.Case.Entry != nil {
			t.Fatal("rejected action changed capital/effect", s)
		}
		if _, err := m.SubmitAction(context.Background(), c, now); err == nil {
			t.Fatal("rejected action submitted")
		}
		again, err := m.ReconcileAction(context.Background(), c, now)
		if err != nil || !reflect.DeepEqual(a, again) {
			t.Fatal("rejection retry", err)
		}
		found := false
		for _, e := range m.events {
			found = found || e.Kind == "action.rejected" && e.ActionID == c.ID
		}
		if !found {
			t.Fatal("rejection lacked audit event")
		}
	}
}

func TestFundedMemoryAbsenceReleaseAndChangedAuthority(t *testing.T) {
	for _, changed := range []bool{false, true} {
		m, _ := fundedMemory(t, 100000, 100000)
		c, now := capitalClaim(t, m)
		ctx := context.Background()
		if _, err := m.BeginAction(ctx, c, now); err != nil {
			t.Fatal(err)
		}
		if s := capitalRead(t, m).Capital; s.ReservedCashCents != 100000 || s.AvailableCashCents != 0 {
			t.Fatal("write-ahead not funded", s)
		}
		if _, err := m.ReconcileAction(ctx, c, now); err != nil {
			t.Fatal(err)
		}
		if s := capitalRead(t, m).Capital; s.ReservedCashCents != 100000 || s.Commitments[0].Status != "held" {
			t.Fatal("valid absent retry released cash", s)
		}
		later := now.Add(30 * time.Second)
		if changed {
			later = now.Add(time.Second)
			q := workflow.Fixtures()[2]
			q.ReceivedAt, q.OccurredAt = later, later
			if _, err := m.Ingest(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		claims, err := m.ClaimActions(ctx, "replacement", later, time.Second*5)
		if err != nil {
			t.Fatal(err)
		}
		// Same worker still owns its lease in the changed-authority case.
		if len(claims) > 0 {
			if _, err := m.SubmitAction(ctx, c, later); !errors.Is(err, actions.ErrLeaseLost) {
				t.Fatal("old lease submitted", err)
			}
			c = claims[0]
		}
		// Begin must also release an earlier reservation if authority changed
		// after a proven-absent reconciliation returned it to pending.
		a, err := m.BeginAction(ctx, c, later)
		if err != nil || !actions.Terminal(a.Status) {
			t.Fatal("absent invalid work not terminated", a, err)
		}
		s := capitalRead(t, m).Capital
		if s.AvailableCashCents != 100000 || s.ReservedCashCents != 0 || s.Commitments[0].Status != "released" || len(m.effects) != 0 {
			t.Fatal("absence did not release exactly once", s)
		}
	}
}

func TestFundedMemoryPartialAndUnknown(t *testing.T) {
	m, _ := fundedMemory(t, 100000, 100000)
	adapterStatuses(t, m)
	if s := capitalRead(t, m).Capital; s.PositionCostCents != 100000 || s.ReservedCashCents != 0 {
		t.Fatal("known completion not funded", s)
	}
	// A partial effect remains held even after decision expiry. Losing the
	// retained effect fails reconciliation without restoring spendable cash.
	m, _ = fundedMemory(t, 100000, 100000)
	c, now := capitalClaim(t, m)
	a, err := m.BeginAction(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	installEffect(t, m, domain.ActionEffect{Intent: a.Intent, Status: "partial"})
	later := now.Add(time.Minute)
	claims, err := m.ClaimActions(context.Background(), "replacement", later, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	c = claims[0]
	if _, err := m.ReconcileAction(context.Background(), c, later); err != nil {
		t.Fatal(err)
	}
	before := capitalRead(t, m)
	if before.Capital.ReservedCashCents != 100000 || before.Capital.Position != nil {
		t.Fatal(before)
	}
	delete(m.effects, a.Intent.IdempotencyKey)
	if _, err := m.ReconcileAction(context.Background(), c, later); err == nil {
		t.Fatal("disappeared partial effect accepted")
	}
	if _, err := m.FixtureCapital(context.Background()); err == nil {
		t.Fatal("readback concealed missing known effect")
	}
	unchanged, _ := m.fixtureCapital.Snapshot()
	if !reflect.DeepEqual(before.Capital, unchanged) {
		t.Fatal("missing effect released held cash")
	}
}

func TestFundedMemoryTransactionRollback(t *testing.T) {
	ctx := context.Background()
	m, _ := fundedMemory(t, 100000, 100000)
	c, now := capitalClaim(t, m)
	failure := errors.New("forced transaction failure after update")
	for _, update := range []actionUpdate{
		func(tx actionTx) (any, []domain.Event, error) { return beginAction(tx, c, now) },
		func(tx actionTx) (any, []domain.Event, error) { return submitAction(tx, c, now) },
		func(tx actionTx) (any, []domain.Event, error) { return reconcileAction(tx, c, now) },
	} {
		before, _ := m.Snapshot(ctx)
		capital := capitalRead(t, m)
		effects := clone(m.effects)
		_, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) {
			if _, _, err := update(tx); err != nil {
				return nil, nil, err
			}
			return nil, nil, failure
		})
		if !errors.Is(err, failure) {
			t.Fatal("injection failed", err)
		}
		after, _ := m.Snapshot(ctx)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(capital, capitalRead(t, m)) || !reflect.DeepEqual(effects, m.effects) {
			t.Fatal("partial transaction commit")
		}
		if _, err := m.actionMutation(ctx, update); err != nil {
			t.Fatal(err)
		}
	}
	if s := capitalRead(t, m).Capital; s.PositionCostCents != 100000 || len(m.effects) != 1 {
		t.Fatal(s)
	}
}

func TestFundedMemoryLookupFailureAndMissingReservation(t *testing.T) {
	ctx := context.Background()
	m, _ := fundedMemory(t, 100000, 100000)
	c, now := capitalClaim(t, m)
	if _, err := m.BeginAction(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Snapshot(ctx)
	capital := capitalRead(t, m)
	_, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) {
		tx.read = func(string) (*domain.ActionEffect, error) { return nil, errors.New("fixture ledger unavailable") }
		return reconcileAction(tx, c, now)
	})
	after, _ := m.Snapshot(ctx)
	if err == nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(capital, capitalRead(t, m)) {
		t.Fatal("lookup failure changed capital/aggregate", err)
	}
	// Simulate a missing reservation rather than adopting an in-flight action.
	a := m.paperAccounts[capital.Capital.AccountID]
	b, _ := fixturecapital.New(a)
	m.fixtureCapital = &b
	if _, err := m.SubmitAction(ctx, c, now); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("submitted without reservation", err)
	}
	if _, err := m.ReconcileAction(ctx, c, now); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("retroactively funded unknown action", err)
	}
	if len(m.effects) != 0 {
		t.Fatal("missing reservation enacted effect")
	}
}

func TestFundedMemoryConcurrentBeginAndDetachedReadback(t *testing.T) {
	ctx := context.Background()
	m, _ := fundedMemory(t, 100000, 100000)
	c, now := capitalClaim(t, m)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = m.BeginAction(ctx, c, now) })
	}
	wg.Wait()
	v := capitalRead(t, m)
	if v.Capital.ReservedCashCents != 100000 || len(v.Capital.Commitments) != 1 || m.state.Actions.Items[c.ID].Attempts != 1 {
		t.Fatal("concurrent begin multiplied reservation", v)
	}
	v.Capital.Commitments[0].AmountCents++
	if capitalRead(t, m).Capital.Commitments[0].AmountCents != 100000 {
		t.Fatal("readback aliases private book")
	}
}
