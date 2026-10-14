package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type fakeClock struct {
	mu      sync.Mutex
	at      time.Time
	waiting chan struct{}
	ticks   chan time.Time
}

func newFakeClock(at time.Time) *fakeClock {
	return &fakeClock{at: at, waiting: make(chan struct{}, 1), ticks: make(chan time.Time)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *fakeClock) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case c.waiting <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case at := <-c.ticks:
		c.mu.Lock()
		c.at = at
		c.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func waitForCycle(t *testing.T, c *fakeClock) {
	t.Helper()
	select {
	case <-c.waiting:
	case <-time.After(time.Second):
		t.Fatal("worker failed to finish cycle")
	}
}

func TestTimerWorkerFakeClockWithoutNewQuote(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := storage.NewMemory()
	if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	due := before.State.Case.ExitBy
	clock := newFakeClock(due.Add(-time.Second))
	done := make(chan struct{})
	go func() { defer close(done); RunTimerWorker(ctx, store, "worker", clock, time.Second, 30*time.Second) }()
	waitForCycle(t, clock)
	after, err := store.Snapshot(ctx)
	if err != nil || len(after.State.Routing.Timers.Exceptions) != 0 {
		t.Fatal("timer fired early")
	}
	clock.ticks <- due
	waitForCycle(t, clock)
	after, err = store.Snapshot(ctx)
	if err != nil || after.ObservationCount != before.ObservationCount || after.State.Case.Exit != nil || len(after.State.Routing.Timers.Exceptions) != 1 {
		t.Fatal("no-quote deadline did not retain exception", err)
	}
	clock.ticks <- due.Add(time.Second)
	waitForCycle(t, clock)
	again, err := store.Snapshot(ctx)
	if err != nil || len(again.Events) != len(after.Events) {
		t.Fatal("worker repeated deadline effect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not drain on cancellation")
	}
}

func TestTimerWorkerOverdueStartupAndClockRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := storage.NewMemory()
	at := workflow.Fixtures()[0].ReceivedAt
	definition := domain.TriggerDefinition{ID: "startup", Owner: "operator", CaseKey: "startup", Behavior: "decide", Condition: domain.Condition{Kind: "elapsed", AfterSeconds: 60}, ValidFrom: at, ValidUntil: at.Add(time.Hour), RepeatPolicy: "once", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	if _, err := store.RegisterTrigger(ctx, definition); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(at.Add(2 * time.Hour))
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunTimerWorker(ctx, store, "startup-worker", clock, time.Second, 30*time.Second)
	}()
	waitForCycle(t, clock)
	after, err := store.Snapshot(ctx)
	if err != nil || len(after.State.Routing.Cases) != 1 {
		t.Fatal("startup did not recover overdue work", err)
	}
	clock.mu.Lock()
	clock.at = at
	clock.mu.Unlock()
	if err := RunTimerCycle(ctx, store, "rewound", clock, time.Second); !errors.Is(err, routing.ErrClockBackwards) {
		t.Fatal("clock rewind did not pause", err)
	}
	clock.mu.Lock()
	clock.at = at.Add(3 * time.Hour)
	clock.mu.Unlock()
	if err := RunTimerCycle(ctx, store, "restored", clock, time.Second); err != nil {
		t.Fatal("clock recovery failed", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}

func TestWallClockCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (WallClock{}).Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("wall-clock wait ignored cancellation")
	}
	if (WallClock{}).Now().Location() != time.UTC {
		t.Fatal("worker clock must use UTC")
	}
	if err := (WallClock{}).Wait(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
}
