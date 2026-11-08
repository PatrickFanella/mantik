package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
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

// failureRefs lists the distinct identities a cycle failed on, in the order
// the cycle reported them and up to the report bound, with their full count.
func failureRefs(err error) ([]string, int) {
	refs := []string{}
	seen := map[string]bool{}
	var walk func(error)
	walk = func(err error) {
		switch e := err.(type) {
		case *workerFailure:
			if !seen[e.ref] {
				seen[e.ref] = true
				if len(refs) < operational.MaxWorkerFailureRefs {
					refs = append(refs, e.ref)
				}
			}
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				walk(child)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)
	if len(refs) == 0 {
		return nil, 0
	}
	return refs, len(seen)
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
	report.EvidenceRefs, report.FailureCount = failureRefs(cycleErr)
	if len(report.EvidenceRefs) > 0 {
		report.EvidenceRef = report.EvidenceRefs[0]
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
