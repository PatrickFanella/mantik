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

type failingDecisionStore struct {
	*storage.Memory
	fail atomic.Bool
}

func (s *failingDecisionStore) PaperStrategies(ctx context.Context) (paperstrategy.Catalog, error) {
	if s.fail.Load() {
		return paperstrategy.Catalog{}, errors.New("manufactured strategy outage")
	}
	return s.Memory.PaperStrategies(ctx)
}

// The worker reports paper_decision health: a failed cycle is not ready, a
// cycle with nothing declared recovers it, and a stop is final.
func TestPaperDecisionWorkerHealthRecoveryAndStop(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	store := &failingDecisionStore{Memory: storage.NewMemory()}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); RunPaperDecisionWorker(ctx, store, "decision-worker", clock) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(snapshot.State, at, true, PaperDecisionWorkerCapability).Ready {
		t.Fatal("failed worker ready")
	}
	store.fail.Store(false)
	clock.ticks <- at.Add(time.Second)
	waitForCycle(t, clock)
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !operational.Readiness(snapshot.State, clock.Now(), true, PaperDecisionWorkerCapability).Ready {
		t.Fatal("recovered worker not ready", snapshot.State.Operations)
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
	health := snapshot.State.Operations.Workers[PaperDecisionWorkerCapability]
	if !health.Stopped || health.LastSuccessAt.IsZero() || health.IntervalMillis != 15000 {
		t.Fatal(health)
	}
}
