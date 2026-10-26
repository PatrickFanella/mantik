package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type failingEvaluationWorkerStore struct {
	*storage.Memory
	fail atomic.Bool
}

func (s *failingEvaluationWorkerStore) PaperEvaluationDeclarations(ctx context.Context) (storage.PaperEvaluationDeclarationCatalog, error) {
	if s.fail.Load() {
		return storage.PaperEvaluationDeclarationCatalog{}, errors.New("manufactured selection outage")
	}
	return s.Memory.PaperEvaluationDeclarations(ctx)
}
func TestPaperEvaluationWorkerHealthRecoveryAndStop(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	store := &failingEvaluationWorkerStore{Memory: storage.NewMemory()}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); RunPaperEvaluationWorker(ctx, store, "evaluation-worker", clock) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(snapshot.State, at, true, PaperEvaluationWorkerCapability).Ready {
		t.Fatal("failed worker ready")
	}
	store.fail.Store(false)
	clock.ticks <- at.Add(time.Second)
	waitForCycle(t, clock)
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !operational.Readiness(snapshot.State, clock.Now(), true, PaperEvaluationWorkerCapability).Ready {
		t.Fatal("recovered worker not ready", snapshot.State.Operations)
	}
	recovered := false
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Capability == PaperEvaluationWorkerCapability && e.Code == "worker_cycle_failed" && e.Status == "resolved" {
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
	health := snapshot.State.Operations.Workers[PaperEvaluationWorkerCapability]
	if !health.Stopped || health.LastSuccessAt.IsZero() || health.IntervalMillis != 30000 || operational.Readiness(snapshot.State, clock.Now(), true, PaperEvaluationWorkerCapability).Ready {
		t.Fatal(health)
	}
}
func TestPaperEvaluationCycleEmptyCancellationAndClock(t *testing.T) {
	store := storage.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunPaperEvaluationCycle(ctx, store, WallClock{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := RunPaperEvaluationCycle(context.Background(), store, nil); !errors.Is(err, storage.ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal(err)
	}
	if err := RunPaperEvaluationCycle(context.Background(), store, newFakeClock(time.Time{})); !errors.Is(err, storage.ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal(err)
	}
	if err := RunPaperEvaluationCycle(context.Background(), store, WallClock{}); err != nil {
		t.Fatal(err)
	}
}
