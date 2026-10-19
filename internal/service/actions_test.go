package service

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestActionWorkerFakeClock(t *testing.T) {
	actionWorkerFakeClock(t, false)
}

func TestFundedActionWorkerFakeClock(t *testing.T) {
	actionWorkerFakeClock(t, true)
}

func bindWorkerCapital(t *testing.T, store *storage.Memory) {
	t.Helper()
	ctx := context.Background()
	receiver, err := store.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "worker-capital", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindFixtureCapital(ctx, storage.FixtureCapitalBinding{ReceiverID: receiver.ID, AccountID: r.Account.Config.ID, AccountSHA256: r.Account.SHA256, ScopeCode: storage.FixtureCapitalBindingScope}); err != nil {
		t.Fatal(err)
	}
}

func actionWorkerFakeClock(t *testing.T, funded bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := storage.NewQueuedMemory()
	if funded {
		bindWorkerCapital(t, store)
	}
	q := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(q.ReceivedAt)
	done := make(chan struct{})
	go func() { defer close(done); RunActionWorker(ctx, store, "worker", clock, time.Second, 30*time.Second) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(ctx)
	if err != nil || snapshot.State.Case.Entry == nil {
		t.Fatal("worker did not enact durable intent", err)
	}
	clock.ticks <- q.ReceivedAt.Add(time.Second)
	waitForCycle(t, clock)
	after, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Events) != len(after.Events) {
		t.Fatal("polling repeated effects")
	}
	if funded {
		v, err := store.FixtureCapital(ctx)
		if err != nil || v.Capital.PositionCostCents != 100000 || v.Capital.ReservedCashCents != 0 || v.Capital.AvailableCashCents != 0 || len(v.Capital.Commitments) != 1 {
			t.Fatal("worker did not settle funded capital once", v, err)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("action worker failed to drain")
	}
}

func TestQueuedDeadlineUsesActionWorker(t *testing.T) {
	queuedDeadlineUsesActionWorker(t, false)
}

func TestFundedDeadlineUsesActionWorker(t *testing.T) {
	queuedDeadlineUsesActionWorker(t, true)
}

func queuedDeadlineUsesActionWorker(t *testing.T, funded bool) {
	ctx := context.Background()
	store := storage.NewQueuedMemory()
	if funded {
		bindWorkerCapital(t, store)
	}
	q := workflow.Fixtures()[1]
	clock := newFakeClock(q.ReceivedAt)
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := RunActionCycle(ctx, store, "entry", clock, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	due := snapshot.State.Case.ExitBy
	q.ID = "retained-price"
	q.PriceCents = 10300
	q.OccurredAt = due.Add(-30 * time.Second)
	q.ReceivedAt = q.OccurredAt
	if _, err := store.Ingest(ctx, q); err != nil {
		t.Fatal(err)
	}
	clock.at = due
	if err := RunTimerCycle(ctx, store, "timer", clock, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Snapshot(ctx)
	if err != nil || pending.State.Case.Exit != nil {
		t.Fatal("timer bypassed durable delivery")
	}
	if err := RunActionCycle(ctx, store, "exit", clock, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot(ctx)
	if err != nil || after.State.Case.Exit == nil || after.State.Case.Exit.PriceCents != 10300 {
		t.Fatal("deadline action not enacted", err)
	}
	if funded {
		v, err := store.FixtureCapital(ctx)
		if err != nil || v.Capital.AvailableCashCents != 103000 || v.Capital.RealizedPnLCents != 3000 || v.Capital.CapitalLimitCents != 100000 || v.Capital.Position != nil || v.Capital.PositionCostCents != 0 || v.Capital.ReservedCashCents != 0 {
			t.Fatal("deadline exit did not reconcile cash or changed ceiling", v, err)
		}
	}
}
