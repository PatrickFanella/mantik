package storage

import "context"

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

// Allocation reads retain their complete active outcome-owner check, including
// outcomes outside an individual assessment's selected window.
func (m *Memory) allocationAssessmentOwnersLocked(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperEvaluationAssessmentCatalog, error) {
	if len(m.evaluationAssessments) == 0 {
		return m.paperAssessmentOwnersLocked(ctx, de, oe)
	}
	if oe == nil {
		return PaperEvaluationAssessmentCatalog{}, ErrPaperEvaluationAssessmentInvalid
	}
	strategies, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	if _, err := paperOutcomeCatalog(m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, m.decisionFeedLocked(), de, oe); err != nil {
		return PaperEvaluationAssessmentCatalog{}, err
	}
	return m.paperAssessmentOwnersLocked(ctx, de, oe)
}
