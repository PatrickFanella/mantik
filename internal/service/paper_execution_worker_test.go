package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// scriptedExecutionStore answers each step from a script and records the
// identities it was asked for.
type scriptedExecutionStore struct {
	*storage.Memory
	mu      sync.Mutex
	ids     []string
	answers []func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error)
}

func (s *scriptedExecutionStore) ExecuteOwnedAllocations(_ context.Context, c storage.OwnedExecutionCommand, _ func() time.Time, _ storage.PaperDecisionEvaluator, _ ...storage.PaperOutcomeEvaluator) (storage.OwnedExecutionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, c.ID)
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer(c)
}

// A step whose response is lost is retried with the same identity, so the
// retry receives the retained step instead of committing a second one. A
// settled step, retained or not, releases its identity.
func TestPaperExecutionStepperKeepsIdentityUntilSettled(t *testing.T) {
	retained := &storage.OwnedExecutionRecord{SHA256: "step"}
	lost := errors.New("response lost after commit")
	store := &scriptedExecutionStore{Memory: storage.NewMemory(), answers: []func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error){
		func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error) {
			return storage.OwnedExecutionReceipt{}, lost
		},
		func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error) {
			return storage.OwnedExecutionReceipt{Record: retained, Duplicate: true}, nil
		},
		func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error) {
			return storage.OwnedExecutionReceipt{Status: "no_effects"}, nil
		},
		func(storage.OwnedExecutionCommand) (storage.OwnedExecutionReceipt, error) {
			return storage.OwnedExecutionReceipt{Status: "no_effects"}, nil
		},
	}}
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	stepper := &PaperExecutionStepper{Worker: "worker"}
	var failure *workerFailure
	if _, err := stepper.Cycle(t.Context(), store, clock); !errors.Is(err, lost) || !errors.As(err, &failure) || failure.ref != "paper-execution:"+store.ids[0] {
		t.Fatal("lost response", err)
	}
	for i := range 3 {
		clock.at = at.Add(time.Duration(i+1) * PaperExecutionWorkerInterval)
		if _, err := stepper.Cycle(t.Context(), store, clock); err != nil {
			t.Fatal(i, err)
		}
	}
	ids := store.ids
	if ids[1] != ids[0] || ids[2] == ids[1] || ids[3] == ids[2] {
		t.Fatal("step identities", ids)
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, AutomaticPaperExecutionPrefix) || !reservedPaperExecutionID(id) {
			t.Fatal("unreserved identity", id)
		}
	}
}

type failingExecutionStore struct {
	*storage.Memory
	fail atomic.Bool
}

func (s *failingExecutionStore) ExecuteOwnedAllocations(ctx context.Context, c storage.OwnedExecutionCommand, now func() time.Time, d storage.PaperDecisionEvaluator, o ...storage.PaperOutcomeEvaluator) (storage.OwnedExecutionReceipt, error) {
	if s.fail.Load() {
		return storage.OwnedExecutionReceipt{}, errors.New("manufactured step outage")
	}
	return s.Memory.ExecuteOwnedAllocations(ctx, c, now, d, o...)
}

// The worker reports paper_execution health: a failed step is not ready, a
// later step that finds nothing to commit recovers it, and a stop is final.
func TestPaperExecutionWorkerHealthRecoveryAndStop(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	clock := newFakeClock(at)
	store := &failingExecutionStore{Memory: storage.NewMemory()}
	if err := store.EnablePaperExecution(SelectPaperExecutionQuotes); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); RunPaperExecutionWorker(ctx, store, "execution-worker", clock) }()
	waitForCycle(t, clock)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operational.Readiness(snapshot.State, at, true, PaperExecutionWorkerCapability).Ready {
		t.Fatal("failed worker ready")
	}
	store.fail.Store(false)
	clock.ticks <- at.Add(time.Second)
	waitForCycle(t, clock)
	snapshot, err = store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !operational.Readiness(snapshot.State, clock.Now(), true, PaperExecutionWorkerCapability).Ready {
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
	health := snapshot.State.Operations.Workers[PaperExecutionWorkerCapability]
	if !health.Stopped || health.LastSuccessAt.IsZero() || health.IntervalMillis != 15000 || operational.Readiness(snapshot.State, clock.Now(), true, PaperExecutionWorkerCapability).Ready {
		t.Fatal(health)
	}
}
