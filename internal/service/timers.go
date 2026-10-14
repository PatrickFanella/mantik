package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Clock makes scheduling and cancellation testable without real sleeps.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// WallClock uses UTC for persisted evidence and cancellation-aware waits.
type WallClock struct{}

// Now returns the current UTC instant.
func (WallClock) Now() time.Time { return time.Now().UTC() }

// Wait ends at the poll interval or cancellation, whichever arrives first.
func (WallClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RunTimerCycle claims and completes a bounded batch. Completed results are
// transactionally retained; a failure leaves the claim for lease recovery.
func RunTimerCycle(ctx context.Context, store storage.TimerStore, worker string, clock Clock, lease time.Duration) error {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claims, err := store.ClaimTimers(claimCtx, worker, clock.Now(), lease)
	cancel()
	if err != nil {
		return err
	}
	for _, claim := range claims {
		if err := ctx.Err(); err != nil {
			return &workerFailure{cause: err, ref: "timer:" + claim.DefinitionID}
		}
		completeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := store.CompleteTimer(completeCtx, claim, clock.Now())
		cancel()
		if err != nil {
			return &workerFailure{cause: err, ref: "timer:" + claim.DefinitionID}
		}
	}
	return nil
}

// RunTimerWorker recovers overdue jobs immediately on startup, then polls until
// cancellation. Models, incoming quotes, and HTTP clients are not prerequisites.
func RunTimerWorker(ctx context.Context, store storage.TimerStore, worker string, clock Clock, interval, lease time.Duration) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, "protective_monitoring", clock, interval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := RunTimerCycle(ctx, store, worker, clock, lease)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, "protective_monitoring", clock, interval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("timer cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, interval); err != nil {
			return
		}
	}
}
