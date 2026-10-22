package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type failingOutcomeWorkerStore struct {
	*storage.Memory
	fail atomic.Bool
}

func (s *failingOutcomeWorkerStore) PaperStrategies(ctx context.Context) (paperstrategy.Catalog, error) {
	if s.fail.Load() {
		return paperstrategy.Catalog{}, errors.New("manufactured selection outage")
	}
	return s.Memory.PaperStrategies(ctx)
}
func TestPaperOutcomeWorkerHealthRecoveryAndStop(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	store := &failingOutcomeWorkerStore{Memory: storage.NewMemory()}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); RunPaperOutcomeWorker(ctx, store, "outcome-worker", clock) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(snapshot.State, at, true, PaperOutcomeWorkerCapability).Ready {
		t.Fatal("failed worker ready")
	}
	store.fail.Store(false)
	clock.ticks <- at.Add(time.Second)
	waitForCycle(t, clock)
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !operational.Readiness(snapshot.State, clock.Now(), true, PaperOutcomeWorkerCapability).Ready {
		t.Fatal("recovered worker not ready", snapshot.State.Operations)
	}
	recovered := false
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Capability == PaperOutcomeWorkerCapability && e.Code == "worker_cycle_failed" && e.Status == "resolved" {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("lost failed cycle")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	health := snapshot.State.Operations.Workers[PaperOutcomeWorkerCapability]
	if !health.Stopped || health.LastSuccessAt.IsZero() || health.IntervalMillis != 30000 || operational.Readiness(snapshot.State, clock.Now(), true, PaperOutcomeWorkerCapability).Ready {
		t.Fatal(health)
	}
}
func TestPaperOutcomeCycleEmptyCancellationAndClock(t *testing.T) {
	store := storage.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunPaperOutcomeCycle(ctx, store, WallClock{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := RunPaperOutcomeCycle(context.Background(), store, nil); !errors.Is(err, storage.ErrPaperOutcomeInvalid) {
		t.Fatal(err)
	}
	if err := RunPaperOutcomeCycle(context.Background(), store, newFakeClock(time.Time{})); !errors.Is(err, storage.ErrPaperOutcomeInvalid) {
		t.Fatal(err)
	}
	if err := RunPaperOutcomeCycle(context.Background(), store, WallClock{}); err != nil {
		t.Fatal(err)
	}
}
