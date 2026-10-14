package service

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestActionWorkerFakeClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := storage.NewQueuedMemory()
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
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("action worker failed to drain")
	}
}

func TestQueuedDeadlineUsesActionWorker(t *testing.T) {
	ctx := context.Background()
	store := storage.NewQueuedMemory()
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
}
