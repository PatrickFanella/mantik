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
			return err
		}
		if actions.Terminal(a.Status) || a.Status == "accepted" || a.Status == "partial" {
			continue
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		a, err = store.BeginAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return err
		}
		if actions.Terminal(a.Status) {
			continue
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		_, err = store.SubmitAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return err
		}
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		_, err = store.ReconcileAction(stepCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// RunActionWorker recovers durable delivery independently of models and requests.
func RunActionWorker(ctx context.Context, store storage.ActionStore, worker string, clock Clock, interval, lease time.Duration) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := RunActionCycle(ctx, store, worker, clock, lease); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("action cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, interval); err != nil {
			return
		}
	}
}
