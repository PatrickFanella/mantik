package service

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// A cycle that fails on several identities reports all of them, once each,
// through joined and wrapped errors, and bounds what it stores.
func TestFailureRefsListsEveryFailure(t *testing.T) {
	cause := errors.New("cause")
	a, b := &workerFailure{cause: cause, ref: "paper-decision:a"}, &workerFailure{cause: cause, ref: "paper-comparison:b"}
	joined := errors.Join(a, fmt.Errorf("wrapped: %w", b), errors.New("no identity"), a)
	if refs, count := failureRefs(joined); !slices.Equal(refs, []string{"paper-decision:a", "paper-comparison:b"}) || count != 2 {
		t.Fatal(refs, count)
	}
	if refs, count := failureRefs(cause); refs != nil || count != 0 {
		t.Fatal(refs, count)
	}
	if refs, count := failureRefs(nil); refs != nil || count != 0 {
		t.Fatal(refs, count)
	}
	many := []error{}
	for i := range operational.MaxWorkerFailureRefs + 4 {
		many = append(many, &workerFailure{cause: cause, ref: fmt.Sprint("paper-decision:", i)})
	}
	if refs, count := failureRefs(errors.Join(many...)); len(refs) != operational.MaxWorkerFailureRefs || count != operational.MaxWorkerFailureRefs+4 {
		t.Fatal(len(refs), count)
	}
}

// Worker health and its open exception name every identity a cycle failed on.
func TestWorkerHealthKeepsEveryFailedRef(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	store := storage.NewMemory()
	err := errors.Join(&workerFailure{cause: errors.New("first"), ref: "paper-decision:a"}, &workerFailure{cause: errors.New("second"), ref: "paper-comparison:b"})
	recordWorkerCycle(t.Context(), store, "worker", PaperDecisionWorkerCapability, newFakeClock(at), PaperDecisionWorkerInterval, err, false, &pendingFailure{})
	snapshot, snapshotErr := store.Snapshot(t.Context())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	health := snapshot.State.Operations.Workers[PaperDecisionWorkerCapability]
	if health.EvidenceRef != "paper-decision:a" || !slices.Equal(health.EvidenceRefs, []string{"paper-decision:a", "paper-comparison:b"}) || health.FailureCount != 2 {
		t.Fatal(health)
	}
	found := false
	for _, e := range snapshot.State.Routing.Timers.Exceptions {
		if e.Capability == PaperDecisionWorkerCapability {
			found = slices.Contains(e.EvidenceRefs, "paper-decision:a") && slices.Contains(e.EvidenceRefs, "paper-comparison:b")
		}
	}
	if !found {
		t.Fatal("exception lost a failed identity", snapshot.State.Routing.Timers.Exceptions)
	}
}
