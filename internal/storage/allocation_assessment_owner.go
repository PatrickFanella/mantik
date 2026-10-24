package storage

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

// Evaluators are compiled service callbacks, never public command fields. Old
// internal callers may omit the outcome callback only while no assessment owner
// records exist. An explicit nil or multiple callbacks is always invalid.
func allocationOutcomeEvaluator(evals []PaperOutcomeEvaluator) (PaperOutcomeEvaluator, error) {
	if len(evals) > 1 || len(evals) == 1 && evals[0] == nil {
		return nil, ErrPaperEvaluationAssessmentInvalid
	}
	if len(evals) == 0 {
		return nil, nil
	}
	return evals[0], nil
}

// Reuse the already replayed owner catalogues and the caller's aggregate lock or
// database transaction. No assessment is accepted just because its digest or
// self-declared check flags look valid. This validates the entire owner before a
// comparison catalogue, new receipt or historical exact retry can escape.
func resolveAllocationAssessmentOwner(ctx context.Context, owner string, all map[string]PaperEvaluationAssessment, declarations PaperEvaluationDeclarationCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome, feed FeedEvidenceSnapshot, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	if len(all) == 0 {
		return PaperEvaluationAssessmentCatalog{ReceiverID: owner, Records: []PaperEvaluationAssessment{}}, nil
	}
	if oe == nil {
		return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentInvalid
	}
	actual, err := paperOutcomeCatalog(owner, outcomes, retained, strategies, feed, de, oe)
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	return paperEvaluationAssessmentCatalog(ctx, owner, all, paperEvaluationSources{declarations, decisions, actual, retained, feed.Collectors.LastClock})
}
