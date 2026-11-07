package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperEvaluationWorkerInterval = 30 * time.Second
const PaperEvaluationWorkerCapability = "paper_evaluation_assessment"
const AutomaticPaperEvaluationPrefix = "automatic-evaluation-"
const automaticPaperEvaluationRecipe = "native-predeclared-necessary-checks-v5"

type PaperEvaluationWorkerStore interface {
	storage.PaperEvaluationDeclarationStore
	storage.PaperEvaluationAssessmentStore
}

// This identity seals one necessary-check assessment per immutable declaration
// and compiled recipe. It is not a qualified evaluation or admission identity.
func AutomaticPaperEvaluationID(d storage.PaperEvaluationDeclaration) string {
	return automaticEvaluationIDForRecipe(d, automaticPaperEvaluationRecipe)
}

func automaticEvaluationIDForRecipe(d storage.PaperEvaluationDeclaration, recipe string) string {
	return AutomaticPaperEvaluationPrefix + workerplan.Hash(struct{ Recipe, DeclarationSHA256 string }{recipe, d.SHA256})
}
func reservedPaperEvaluationID(id string) bool {
	return strings.HasPrefix(id, AutomaticPaperEvaluationPrefix)
}

// Wait strictly past the inclusive outcome deadline. Each owner write selects
// and replays its live source snapshot atomically; a later failure retains prior
// committed assessments. Scoped write failures are aggregated after independent
// declarations are attempted; global catalogue failures still stop the cycle.
// No source, check result, admission or effect is supplied.
func RunPaperEvaluationCycle(ctx context.Context, store PaperEvaluationWorkerStore, clock Clock) error {
	if clock == nil {
		return storage.ErrPaperEvaluationAssessmentInvalid
	}
	cycleCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	at := clock.Now().UTC()
	if at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return storage.ErrPaperEvaluationAssessmentInvalid
	}
	declarations, err := store.PaperEvaluationDeclarations(cycleCtx)
	if err != nil {
		return err
	}
	recipe := automaticPaperEvaluationRecipe
	if configured, ok := store.(interface{ PaperEvaluationAssessmentRecipe() string }); ok {
		recipe = configured.PaperEvaluationAssessmentRecipe()
		if recipe != automaticPaperEvaluationRecipe && recipe != storage.PaperPortfolioAssessmentRecipe {
			return storage.ErrPaperEvaluationAssessmentInvalid
		}
	}
	var failures []error
	for _, d := range declarations.Records {
		if err := cycleCtx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if d.Command.ReceiverID != declarations.ReceiverID || d.Command.Validate() != nil {
			return storage.ErrPaperEvaluationAssessmentConflict
		}
		deadline := d.Command.Plan.OutcomeDeadline
		if !at.After(deadline) {
			continue
		}
		selectedRecipe := recipe
		if d.Command.Recipe == storage.PaperHoldoutEvaluationRecipe && d.Command.Plan.Holdout != nil {
			selectedRecipe = storage.PaperHoldoutAssessmentRecipe
		}
		if (d.Command.Recipe == storage.PaperBenchmarkEvaluationRecipe || d.Command.Recipe == storage.PaperCausalEvaluationRecipe) && d.Command.Plan.BenchmarkMethod != "" {
			selectedRecipe = storage.PaperBenchmarkAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperCausalEvaluationRecipe && d.Command.Plan.FeatureCausalityMethod == storage.PaperFeatureCausalityMethod {
			selectedRecipe = storage.PaperCausalAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperSampleDesignEvaluationRecipe && d.Command.Plan.SampleDesign != nil {
			selectedRecipe = storage.PaperSampleDesignAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperSearchHistoryEvaluationRecipe && d.Command.Plan.SearchMultiplicity != nil {
			selectedRecipe = storage.PaperSearchHistoryAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperCoverageEvaluationRecipe && d.Command.Plan.CorporateCoverage != nil {
			selectedRecipe = storage.PaperCoverageAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperTotalReturnEvaluationRecipe && d.Command.Plan.TotalReturnMethod != "" {
			selectedRecipe = storage.PaperTotalReturnAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperSensitivityEvaluationRecipe && d.Command.Plan.Sensitivity != nil {
			selectedRecipe = storage.PaperSensitivityAssessmentRecipe
		}
		if d.Command.Recipe == storage.PaperStatisticalEvaluationRecipe && d.Command.Plan.StatisticalTests != nil {
			selectedRecipe = storage.PaperStatisticalAssessmentRecipe
		}
		command := storage.PaperEvaluationAssessmentCommand{ID: automaticEvaluationIDForRecipe(d, selectedRecipe), ReceiverID: declarations.ReceiverID, DeclarationID: d.Command.ID, DeclarationSHA256: d.SHA256}
		assessmentClock := func() time.Time {
			now := clock.Now().UTC()
			if now.Before(at) || !now.After(deadline) {
				return time.Time{}
			}
			return now
		}
		receipt, err := store.AssessPaperEvaluation(cycleCtx, command, assessmentClock, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err == nil {
			r := receipt.Record
			if r.Command != command || r.Recipe != selectedRecipe || !r.AssessedAt.After(deadline) || r.Evidence.Phase != "outcome_deadline_elapsed" || !retainedAssessmentStatus(r) || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
				err = storage.ErrPaperEvaluationAssessmentConflict
			}
		}
		if err != nil {
			ref := "paper-evaluation-declaration:" + d.Command.ID
			failures = append(failures, &workerFailure{cause: fmt.Errorf("%s: %w", ref, err), ref: ref})
			// A valid source-specific failure cannot starve unrelated declarations.
			// The next iteration still enforces the single cycle deadline/context.
		}
	}
	return errors.Join(failures...)
}

// RunPaperEvaluationWorker recovers immediately, then observes every 30 seconds.
// Its health is separate from protective monitoring and grants no qualification.
func RunPaperEvaluationWorker(ctx context.Context, store PaperEvaluationWorkerStore, worker string, clock Clock) {
	pending := pendingFailure{}
	defer func() {
		recordWorkerCycle(context.Background(), store, worker, PaperEvaluationWorkerCapability, clock, PaperEvaluationWorkerInterval, nil, true, &pending)
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := RunPaperEvaluationCycle(ctx, store, clock)
		if ctx.Err() != nil {
			return
		}
		recordWorkerCycle(ctx, store, worker, PaperEvaluationWorkerCapability, clock, PaperEvaluationWorkerInterval, err, false, &pending)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("paper evaluation cycle failed", "error", err)
		}
		if err := clock.Wait(ctx, PaperEvaluationWorkerInterval); err != nil {
			return
		}
	}
}

// Only the statistical recipe can complete an applicable evaluation.
func retainedAssessmentStatus(r storage.PaperEvaluationAssessment) bool {
	if r.Status == "complete" {
		return r.EvaluationComplete && r.Recipe == storage.PaperStatisticalAssessmentRecipe && len(r.CompleteStrategyIDs) > 0
	}
	return (r.Status == "blocked" || r.Status == "incomplete") && !r.EvaluationComplete && r.CompleteStrategyIDs == nil
}
