package fixturecapital_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
)

func TestCheckpointRequiresOwningActionEffectAndAcknowledgment(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	raw, err := h.book.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var effect *domain.ActionEffect
	read := func(string) (*domain.ActionEffect, error) { return effect, nil }
	restore := func(raw []byte, state domain.State) fixturecapital.Book {
		t.Helper()
		b, err := fixturecapital.Restore(h.account, raw, state, read)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if snapshot(t, restore(raw, h.state)).SHA256 != snapshot(t, h.book).SHA256 {
		t.Fatal("reservation lost")
	}
	e := h.submit(t)
	effect = &e
	// Independently committed effect survives, but has not yet been credited.
	before := snapshot(t, restore(raw, h.state))
	if before.ReservedCashCents != 100000 || before.PositionCostCents != 0 {
		t.Fatal(before)
	}
	b, err := h.book.Reconcile(h.state, h.claim, h.now, read)
	if err != nil {
		t.Fatal(err)
	}
	ackRaw, err := b.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixturecapital.Restore(h.account, ackRaw, h.state, read); err == nil {
		t.Fatal("capital credit without source acknowledgment")
	}
	if _, err := h.store.ReconcileAction(context.Background(), h.claim, h.now); err != nil {
		t.Fatal(err)
	}
	s, _ := h.store.Snapshot(context.Background())
	h.state = s.State
	if !reflect.DeepEqual(snapshot(t, b), snapshot(t, restore(ackRaw, h.state))) {
		t.Fatal("funded restore changed result")
	}
	if _, err := fixturecapital.Restore(h.account, raw, h.state, read); err == nil {
		t.Fatal("rolled-back checkpoint concealed acknowledged position")
	}
	effect = nil
	if _, err := fixturecapital.Restore(h.account, ackRaw, h.state, read); err == nil {
		t.Fatal("missing actual effect accepted")
	}
	effect = &e
	bad := h.state
	bad.Actions = detachActions(h.state.Actions)
	a := bad.Actions.Items[h.claim.ID]
	o := *a.Outcome
	o.PriceCents++
	a.Outcome = &o
	bad.Actions.Items[h.claim.ID] = a
	if _, err := fixturecapital.Restore(h.account, ackRaw, bad, read); err == nil {
		t.Fatal("changed source acknowledgment accepted")
	}
	var shape map[string]any
	if err := json.Unmarshal(ackRaw, &shape); err != nil {
		t.Fatal(err)
	}
	shape["result_sha256"] = "forged"
	forged, _ := json.Marshal(shape)
	if _, err := fixturecapital.Restore(h.account, forged, h.state, read); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	snapRaw, _ := json.Marshal(snapshot(t, b))
	if _, err := fixturecapital.Restore(h.account, snapRaw, h.state, read); err == nil {
		t.Fatal("readback imported as authority")
	}
}

func TestCheckpointRetryHistoryIsBoundedAndDetached(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	before, _ := h.book.Checkpoint()
	for range 10 {
		b, err := h.book.Reserve(h.state, h.claim, h.now)
		if err != nil {
			t.Fatal(err)
		}
		h.book = b
	}
	after, _ := h.book.Checkpoint()
	if string(before) != string(after) {
		t.Fatal("retry grew replay history")
	}
	before[0] = '!'
	again, _ := h.book.Checkpoint()
	if string(again) != string(after) {
		t.Fatal("checkpoint buffer aliased book")
	}
	if _, err := fixturecapital.Restore(h.account, make([]byte, fixturecapital.MaxCheckpointBytes+1), h.state, func(string) (*domain.ActionEffect, error) { return nil, nil }); err == nil {
		t.Fatal("oversize checkpoint accepted")
	}
}

func TestCheckpointCapacityIsNotInsufficientCapital(t *testing.T) {
	h := setup(t, 100000, 100000)
	before := snapshot(t, h.book)
	a := h.state.Actions.Items[h.claim.ID]
	a.Code = strings.Repeat("x", fixturecapital.MaxCheckpointBytes)
	h.state.Actions.Items[h.claim.ID] = a
	if _, err := h.book.Reserve(h.state, h.claim, h.now); !errors.Is(err, fixturecapital.ErrCapacity) || errors.Is(err, fixturecapital.ErrLimit) {
		t.Fatal("storage capacity mislabeled as a capital rejection", err)
	}
	if snapshot(t, h.book).SHA256 != before.SHA256 {
		t.Fatal("capacity failure changed capital")
	}
}
