package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// RunActionCycle reconciles every claimed key before attempting submission.
func RunActionCycle(ctx context.Context, store storage.ActionStore, worker string, clock Clock, lease time.Duration) error {
	return runActionCycle(ctx, store, worker, clock, lease, nil)
}

func runActionCycle(ctx context.Context, store storage.ActionStore, worker string, clock Clock, lease time.Duration, gate *FixtureActionGate) error {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claims, err := store.ClaimActions(claimCtx, worker, clock.Now(), lease)
	cancel()
	if err != nil {
		return err
	}
	for _, claim := range claims {
		stepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		a, err := store.ReconcileAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
		if actions.Terminal(a.Status) || a.Status == "accepted" || a.Status == "partial" {
			continue
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		a, err = store.BeginAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
		if actions.Terminal(a.Status) {
			continue
		}
		if err := gate.pause(ctx, "before_submit", a.Intent); err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		_, err = store.SubmitAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
		if err := gate.pause(ctx, "after_submit", a.Intent); err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		_, err = store.ReconcileAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return &workerFailure{cause: err, ref: "action:" + claim.ID}
		}
	}
	return nil
}

// RunActionWorker recovers durable delivery independently of models and requests.
func RunActionWorker(ctx context.Context, store storage.ActionStore, worker string, clock Clock, interval, lease time.Duration) {
	RunActionWorkerWithFixtureGate(ctx, store, worker, clock, interval, lease, nil)
}

// RunActionWorkerWithFixtureGate is for deliberately interrupted synthetic qualification stacks.
func RunActionWorkerWithFixtureGate(ctx context.Context, store storage.ActionStore, worker string, clock Clock, interval, lease time.Duration, gate *FixtureActionGate) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, "action_delivery", clock, interval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := runActionCycle(ctx, store, worker, clock, lease, gate)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, "action_delivery", clock, interval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("action cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, interval); err != nil {
			return
		}
	}
}
