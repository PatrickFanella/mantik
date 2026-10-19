package fixturecapital_test

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"reflect"
	"testing"
	"time"
)

type harness struct {
	account paperaccount.Account
	store   *storage.Memory
	book    fixturecapital.Book
	now     time.Time
	claim   domain.ActionClaim
	state   domain.State
}

func setup(t *testing.T, cash, limit int64) *harness {
	t.Helper()
	s := storage.NewQueuedMemory()
	q := workflow.Fixtures()[1]
	if _, err := s.Ingest(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimActions(context.Background(), "capital-worker", q.ReceivedAt, 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(context.Background())
	a, err := paperaccount.Create("receiver-fixture_capital_01", paperaccount.Config{ID: "paper", Currency: "USD", OpeningCashCents: cash, CapitalLimitCents: limit}, q.ReceivedAt.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	b, err := fixturecapital.New(a)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{store: s, book: b, account: a, now: q.ReceivedAt, claim: claims[0], state: snap.State}
}
func snapshot(t *testing.T, b fixturecapital.Book) fixturecapital.Snapshot {
	t.Helper()
	s, err := b.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if s.AvailableCashCents+s.ReservedCashCents+s.PositionCostCents != s.OpeningCashCents+s.RealizedPnLCents || s.ReservedCashCents+s.PositionCostCents > s.CapitalLimitCents {
		t.Fatal("conservation", s)
	}
	return s
}
func (h *harness) reserve(t *testing.T) {
	t.Helper()
	b, err := h.book.Reserve(h.state, h.claim, h.now)
	if err != nil {
		t.Fatal(err)
	}
	h.book = b
}
func (h *harness) submit(t *testing.T) domain.ActionEffect {
	t.Helper()
	ctx := context.Background()
	if _, err := h.store.BeginAction(ctx, h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	e, err := h.store.SubmitAction(ctx, h.claim, h.now)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.store.Snapshot(ctx)
	h.state = s.State
	return e
}
func TestCapitalUsesActualFixtureActionsAndEffects(t *testing.T) {
	h := setup(t, 100000, 100000)
	sourceBefore := snapshot(t, h.book)
	h.reserve(t)
	reserved := snapshot(t, h.book)
	if reserved.AvailableCashCents != 0 || reserved.ReservedCashCents != 100000 {
		t.Fatal(reserved)
	}
	retry, err := h.book.Reserve(h.state, h.claim, h.now)
	if err != nil || snapshot(t, retry).SHA256 != reserved.SHA256 {
		t.Fatal("double reservation", err)
	}
	e := h.submit(t)
	preAck := h.state
	b, err := h.book.Reconcile(h.state, h.claim, h.now, func(id string) (*domain.ActionEffect, error) {
		if id != e.Intent.IdempotencyKey {
			t.Fatal(id)
		}
		return &e, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.book = b
	if !reflect.DeepEqual(preAck, h.state) {
		t.Fatal("capital calculation mutated source action/case")
	}
	funded := snapshot(t, h.book)
	if funded.PositionCostCents != 100000 || funded.ReservedCashCents != 0 || funded.AvailableCashCents != 0 || funded.Position == nil || funded.ServiceIntegrated || funded.QualificationCredit {
		t.Fatal(funded)
	}
	if _, err := h.store.ReconcileAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	// A zero-free-cash protective exit still gets a zero-cash commitment.
	q := workflow.Fixtures()[3]
	if _, err := h.store.Ingest(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	claims, err := h.store.ClaimActions(context.Background(), "exit-worker", q.ReceivedAt, 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	h.claim, h.now = claims[0], q.ReceivedAt
	s, _ := h.store.Snapshot(context.Background())
	h.state = s.State
	h.reserve(t)
	if snapshot(t, h.book).ReservedCashCents != 0 {
		t.Fatal("exit consumed new cash")
	}
	e = h.submit(t)
	b, err = h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &e, nil })
	if err != nil {
		t.Fatal(err)
	}
	h.book = b
	closed := snapshot(t, h.book)
	if closed.AvailableCashCents != 94000 || closed.RealizedPnLCents != -6000 || closed.Position != nil || closed.PositionCostCents != 0 {
		t.Fatal(closed)
	}
	if _, err := h.store.ReconcileAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	s, _ = h.store.Snapshot(context.Background())
	again, err := h.book.Reconcile(s.State, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &e, nil })
	if err != nil || snapshot(t, again).SHA256 != closed.SHA256 {
		t.Fatal("duplicate exit credit", err)
	}
	if sourceBefore.AvailableCashCents != 100000 {
		t.Fatal("prior book mutated")
	}
}
func TestUnknownAndPartialKeepCashHeld(t *testing.T) {
	h := setup(t, 200000, 100000)
	h.reserve(t)
	if _, err := h.store.BeginAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	s, _ := h.store.Snapshot(context.Background())
	h.state = s.State
	absent := func(string) (*domain.ActionEffect, error) { return nil, nil }
	b, err := h.book.Reconcile(h.state, h.claim, h.now, absent)
	if err != nil {
		t.Fatal(err)
	}
	h.book = b
	held := snapshot(t, h.book)
	if held.ReservedCashCents != 100000 || held.Position != nil {
		t.Fatal(held)
	}
	if _, err := h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return nil, errors.New("lookup unavailable") }); err == nil {
		t.Fatal("read error accepted")
	}
	if snapshot(t, h.book).SHA256 != held.SHA256 {
		t.Fatal("lookup failure released cash")
	}
	intent := h.state.Actions.Items[h.claim.ID].Intent
	for _, status := range []string{"accepted", "partial"} {
		e := domain.ActionEffect{Intent: intent, Status: status}
		b, err = h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &e, nil })
		if err != nil {
			t.Fatal(err)
		}
		h.book = b
		if s := snapshot(t, b); s.ReservedCashCents != 100000 || s.Position != nil {
			t.Fatal(s)
		}
	}
	if _, err := h.book.Reconcile(h.state, h.claim, h.now, absent); !errors.Is(err, fixturecapital.ErrConflict) {
		t.Fatal("known effect disappeared", err)
	}
}
func TestDefinitiveAbsenceExpiryAndFencing(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	if _, err := h.store.BeginAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	later := h.now.Add(time.Minute)
	claims, err := h.store.ClaimActions(context.Background(), "replacement", later, 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	s, _ := h.store.Snapshot(context.Background())
	before := snapshot(t, h.book)
	if _, err := h.book.Reconcile(s.State, h.claim, later, func(string) (*domain.ActionEffect, error) { return nil, nil }); !errors.Is(err, actions.ErrLeaseLost) {
		t.Fatal(err)
	}
	released, err := h.book.Reconcile(s.State, claims[0], later, func(string) (*domain.ActionEffect, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	out := snapshot(t, released)
	if out.ReservedCashCents != 0 || out.AvailableCashCents != 100000 || out.Commitments[0].Status != "released" {
		t.Fatal(out)
	}
	if snapshot(t, h.book).SHA256 != before.SHA256 {
		t.Fatal("mutated original")
	}
	// A known effect completed under old authority must settle after expiry,
	// rather than being erased by the later acknowledgment clock.
	h = setup(t, 100000, 100000)
	h.reserve(t)
	e := h.submit(t)
	later = h.now.Add(time.Minute)
	claims, err = h.store.ClaimActions(context.Background(), "replacement", later, 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal(err)
	}
	s, _ = h.store.Snapshot(context.Background())
	funded, err := h.book.Reconcile(s.State, claims[0], later, func(string) (*domain.ActionEffect, error) { return &e, nil })
	if err != nil || snapshot(t, funded).PositionCostCents != 100000 {
		t.Fatal("late known effect erased", err)
	}
}
func TestCapitalRejectsLimitsAndChangedAuthority(t *testing.T) {
	h := setup(t, 99999, 99999)
	if _, err := h.book.Reserve(h.state, h.claim, h.now); !errors.Is(err, fixturecapital.ErrLimit) {
		t.Fatal(err)
	}
	h = setup(t, 200000, 99999)
	if _, err := h.book.Reserve(h.state, h.claim, h.now); !errors.Is(err, fixturecapital.ErrLimit) {
		t.Fatal(err)
	}
	h = setup(t, 200000, 100000)
	bad := h.state
	a := bad.Actions.Items[h.claim.ID]
	a.Intent.Decision.Evaluations = nil
	bad.Actions = detachActions(h.state.Actions)
	bad.Actions.Items[h.claim.ID] = a
	if _, err := h.book.Reserve(bad, h.claim, h.now); err == nil {
		t.Fatal("missing required checks accepted")
	}
	bad = h.state
	bad.Actions = detachActions(h.state.Actions)
	a = bad.Actions.Items[h.claim.ID]
	a.Intent.Mode = "unsupported"
	bad.Actions.Items[h.claim.ID] = a
	if _, err := h.book.Reserve(bad, h.claim, h.now); err == nil {
		t.Fatal("unsupported action mode accepted")
	}
	h.reserve(t)
	e := h.submit(t)
	badEffect := e
	badEffect.Intent.Decision.ID = "changed"
	if _, err := h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &badEffect, nil }); err == nil {
		t.Fatal("changed effect accepted")
	}
	badEffect = e
	out := *e.Outcome
	out.PriceCents++
	badEffect.Outcome = &out
	if _, err := h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &badEffect, nil }); err == nil {
		t.Fatal("invented fill accepted")
	}
	before := snapshot(t, h.book)
	e.Outcome.PriceCents++ // External data cannot alias the capital book.
	if snapshot(t, h.book).SHA256 != before.SHA256 {
		t.Fatal("source effect mutated capital")
	}
}
func detachActions(a *domain.ActionState) *domain.ActionState {
	copy := *a
	copy.Items = make(map[string]domain.Action)
	for k, v := range a.Items {
		copy.Items[k] = v
	}
	return &copy
}

func TestSnapshotsCannotRestoreSpendingAuthority(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	snapshot := snapshot(t, h.book)
	raw, _ := json.Marshal(snapshot)
	var forged fixturecapital.Book
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	if _, err := forged.Snapshot(); err == nil {
		t.Fatal("readback restored a spendable book")
	}
}
func TestKnownRejectedEffectReleasesOnlyReservation(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	if _, err := h.store.BeginAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	s, _ := h.store.Snapshot(context.Background())
	h.state = s.State
	e := domain.ActionEffect{Intent: h.state.Actions.Items[h.claim.ID].Intent, Status: "rejected", Code: "fixture_rejection"}
	released, err := h.book.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &e, nil })
	if err != nil {
		t.Fatal(err)
	}
	out := snapshot(t, released)
	if out.AvailableCashCents != 100000 || out.ReservedCashCents != 0 || out.Position != nil {
		t.Fatal(out)
	}
	again, err := released.Reconcile(h.state, h.claim, h.now, func(string) (*domain.ActionEffect, error) { return &e, nil })
	if err != nil || snapshot(t, again).SHA256 != out.SHA256 {
		t.Fatal("duplicate release", err)
	}
}
