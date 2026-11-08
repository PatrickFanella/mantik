package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type workerFailure struct {
	cause error
	ref   string
}

func (e *workerFailure) Error() string { return e.cause.Error() }
func (e *workerFailure) Unwrap() error { return e.cause }

type pendingFailure struct {
	at   time.Time
	code string
}

func cycleCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, routing.ErrClockBackwards):
		return "clock_backwards"
	case errors.Is(err, actions.ErrLeaseLost), errors.Is(err, routing.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, actions.ErrConflict):
		return "reconciliation_conflict"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation_timeout"
	case errors.Is(err, ErrProtectiveExitUnfilled):
		return "protective_exit_unfilled"
	default:
		return "cycle_failed"
	}
}

func recordWorkerCycle(ctx context.Context, store any, worker, capability string, clock Clock, interval time.Duration, cycleErr error, stopped bool, pending *pendingFailure) {
	backend, ok := store.(storage.OperationalStore)
	if !ok {
		return
	}
	now := clock.Now()
	code := cycleCode(cycleErr)
	if cycleErr != nil && pending.at.IsZero() {
		pending.at, pending.code = now, code
	}
	report := domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: capability, WorkerID: worker, At: now, IntervalMillis: interval.Milliseconds(), ErrorCode: code, Stopped: stopped}, PriorFailureAt: pending.at, PriorErrorCode: pending.code}
	var failure *workerFailure
	if errors.As(cycleErr, &failure) {
		report.EvidenceRef = failure.ref
	}
	reportCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := backend.ReportWorker(reportCtx, report); err != nil {
		if pending.at.IsZero() {
			pending.at, pending.code = now, "storage_unavailable"
		}
		slog.Error("worker health persistence failed", "capability", capability, "error", err)
		return
	}
	pending.at, pending.code = time.Time{}, ""
}
