package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperOutcomeWorkerInterval = 30 * time.Second
const PaperOutcomeWorkerCapability = "paper_outcome_collection"
const AutomaticPaperOutcomePrefix = "automatic-forward-"

type PaperOutcomeWorkerStore interface {
	storage.PaperDecisionStore
	storage.PaperOutcomeStore
	storage.PaperStrategyStore
}

// AutomaticPaperOutcomeID binds one sealed observation to one immutable decision
// and recipe. It is not an independent sample ID or an admission receipt.
func AutomaticPaperOutcomeID(d storage.PaperDecisionRecord) string {
	return AutomaticPaperOutcomePrefix + workerplan.Hash(struct{ Recipe, DecisionSHA256 string }{storage.PaperOutcomeRecipe, d.SHA256})
}
func reservedPaperOutcomeID(id string) bool {
	return strings.HasPrefix(id, AutomaticPaperOutcomePrefix)
}

// RunPaperOutcomeCycle only seals matured entry decisions. Waiting until strictly
// after the window end includes publications at its inclusive final instant.
// Each assessment commits atomically; a later error retains the earlier results.
func RunPaperOutcomeCycle(ctx context.Context, store PaperOutcomeWorkerStore, clock Clock) error {
	if clock == nil {
		return storage.ErrPaperOutcomeInvalid
	}
	cycleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	at := clock.Now().UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return storage.ErrPaperOutcomeInvalid
	}
	strategies, err := store.PaperStrategies(cycleCtx)
	if err != nil {
		return err
	}
	decisions, err := store.PaperDecisions(cycleCtx, evaluateOwnedPaperDecision)
	if err != nil {
		return err
	}
	if strategies.ReceiverID != decisions.ReceiverID {
		return storage.ErrPaperOutcomeConflict
	}
	for _, d := range decisions.Records {
		if err := cycleCtx.Err(); err != nil {
			return err
		}
		if d.Evaluation.Action != "entry_candidate" {
			continue
		}
		strategy, err := paperstrategy.Select(strategies, d.Command.StrategyID)
		if err != nil || strategy.SHA256 != d.Command.StrategySHA256 {
			return storage.ErrPaperOutcomeConflict
		}
		safety := strategy.Command.Definition.Safety
		windowEnd := d.FinalizedAt.Add(time.Duration(safety.MaxHoldingSeconds+min(int64(60), safety.MaxQuoteAgeSeconds)) * time.Second)
		if windowEnd.Year() > 9999 {
			return storage.ErrPaperOutcomeInvalid
		}
		if !at.After(windowEnd) {
			continue
		}
		c := storage.PaperOutcomeCommand{ID: AutomaticPaperOutcomeID(d), ReceiverID: decisions.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256}
		assessmentClock := func() time.Time {
			now := clock.Now().UTC()
			// Refuse a clock rollback before creating a canonical automatic record.
			// Otherwise a waiting result could permanently occupy that stable identity.
			if now.Before(at) || !now.After(windowEnd) {
				return time.Time{}
			}
			return now
		}
		receipt, err := store.AssessPaperOutcome(cycleCtx, c, assessmentClock, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err == nil && (receipt.Record.Evaluation.Status != "observed" && receipt.Record.Evaluation.Status != "price_unavailable" || !receipt.Record.EvaluatedAt.After(windowEnd)) {
			// A pre-existing reserved-ID collision must remain visible, not be silently
			// overwritten or mistaken for a completed automatic observation.
			err = storage.ErrPaperOutcomeConflict
		}
		if err != nil {
			return &workerFailure{cause: err, ref: "paper-decision:" + d.Command.ID}
		}
	}
	return nil
}

// RunPaperOutcomeWorker recovers immediately, then observes every 30 seconds.
// Its health is separate from protective monitoring and grants no qualification.
func RunPaperOutcomeWorker(ctx context.Context, store PaperOutcomeWorkerStore, worker string, clock Clock) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, PaperOutcomeWorkerCapability, clock, PaperOutcomeWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := RunPaperOutcomeCycle(ctx, store, clock)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, PaperOutcomeWorkerCapability, clock, PaperOutcomeWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("paper outcome cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, PaperOutcomeWorkerInterval); err != nil {
			return
		}
	}
}
