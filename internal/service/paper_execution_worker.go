package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperExecutionWorkerInterval = 15 * time.Second
const PaperExecutionWorkerCapability = "paper_execution"
const AutomaticPaperExecutionPrefix = "automatic-execution-"

// ErrProtectiveExitUnfilled reports a paper position whose exit ended
// without closing it. Nothing closes the rest, so the worker reports it on
// every cycle until the position is resolved.
var ErrProtectiveExitUnfilled = errors.New("paper position left open after its exit ended")

type PaperExecutionWorkerStore interface {
	storage.OwnedExecutionStore
	storage.PaperStrategyStore
}

func reservedPaperExecutionID(id string) bool {
	return strings.HasPrefix(id, AutomaticPaperExecutionPrefix)
}

// PaperExecutionStepper advances every open book to the service clock with
// execution steps, so fills, protective exits and releases do not wait for a
// caller. It keeps a step identity until a response settles it: after an
// error, including one that arrives after the step committed, the next cycle
// retries the same identity and receives the retained step instead of a
// second one. A step that finds nothing to commit retains nothing, so its
// identity is dropped.
type PaperExecutionStepper struct {
	Worker  string
	pending string
}

// Cycle runs one step. A step that lists an exit_unfilled position still
// succeeded, but Cycle returns ErrProtectiveExitUnfilled for it, so the
// worker's health shows the open position. Effects come only from owner
// records, so a fresh identity after a restart cannot duplicate a committed
// effect either: the books it advances already hold it.
func (s *PaperExecutionStepper) Cycle(ctx context.Context, store PaperExecutionWorkerStore, clock Clock) (storage.OwnedExecutionReceipt, error) {
	if clock == nil || s.Worker == "" {
		return storage.OwnedExecutionReceipt{}, storage.ErrOwnedAllocationInvalid
	}
	at := clock.Now().UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return storage.OwnedExecutionReceipt{}, storage.ErrOwnedAllocationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	strategies, err := store.PaperStrategies(ctx)
	if err != nil {
		return storage.OwnedExecutionReceipt{}, err
	}
	if s.pending == "" {
		s.pending = AutomaticPaperExecutionPrefix + workerplan.Hash(struct {
			Worker string
			At     time.Time
		}{s.Worker, at})
	}
	c := storage.OwnedExecutionCommand{ID: s.pending, ReceiverID: strategies.ReceiverID}
	receipt, err := store.ExecuteOwnedAllocations(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		return receipt, &workerFailure{cause: err, ref: "paper-execution:" + c.ID}
	}
	s.pending = ""
	for _, p := range receipt.Positions {
		if p.Status == "exit_unfilled" {
			return receipt, &workerFailure{cause: ErrProtectiveExitUnfilled, ref: "paper-position:" + p.PolicyID + "/" + p.OrderID}
		}
	}
	return receipt, nil
}

// RunPaperExecutionWorker steps immediately, then every 15 seconds. Its health
// is reported under the paper_execution capability and grants no
// qualification.
func RunPaperExecutionWorker(ctx context.Context, store PaperExecutionWorkerStore, worker string, clock Clock) {
	stepper := &PaperExecutionStepper{Worker: worker}
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, PaperExecutionWorkerCapability, clock, PaperExecutionWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		_, err := stepper.Cycle(ctx, store, clock)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, PaperExecutionWorkerCapability, clock, PaperExecutionWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("paper execution cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, PaperExecutionWorkerInterval); err != nil {
			return
		}
	}
}
