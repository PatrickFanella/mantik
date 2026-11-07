package storage

const PaperDrawdownEvaluationRecipe = "native-threshold-drawdown-plan-v11"
const PaperDrawdownAssessmentRecipe = "native-predeclared-drawdown-v16"
const PaperDrawdownMethod = "observed-bid-event-path-drawdown-v1"

// applyDrawdown decides maximum_drawdown against the plan's predeclared
// maximum_drawdown_cents. The measure is each variant book's observed
// maximum drawdown: the largest fall in equity marked at the observed bid
// across retained event clocks, from the owner's portfolio accounting. The
// larger of the base and stress values counts. A book with any unmarked event
// stays unavailable, because an unseen low could exceed the limit.
func applyDrawdown(r *PaperEvaluationAssessment, maximum int64) error {
	if r.Portfolio == nil || maximum < 1 {
		return ErrPaperEvaluationAssessmentInvalid
	}
	books := map[string]PaperEvaluationPortfolioVariant{}
	for _, v := range r.Portfolio.Variants {
		books[v.StrategyID] = v
	}
	for i := range r.Checks {
		c := &r.Checks[i]
		if c.Name != "maximum_drawdown" {
			continue
		}
		v, ok := books[c.StrategyID]
		if !ok {
			return ErrPaperEvaluationAssessmentInvalid
		}
		switch {
		case v.Base == nil || v.Stress == nil:
			c.Status, c.ReasonCode = "unavailable", "variant_book_unavailable_for_drawdown"
		case !v.Base.AllEventMarksAvailable || !v.Stress.AllEventMarksAvailable:
			c.Status, c.ReasonCode = "unavailable", "event_marks_unavailable_for_drawdown"
		case v.Base.ObservedMaximumDrawdownCents < 0 || v.Stress.ObservedMaximumDrawdownCents < 0:
			return ErrPaperEvaluationAssessmentInvalid
		case r.Evidence.Phase != "outcome_deadline_elapsed":
			c.Status, c.ReasonCode = "pending", "predeclared_outcome_window_not_closed"
		case max(v.Base.ObservedMaximumDrawdownCents, v.Stress.ObservedMaximumDrawdownCents) > maximum:
			c.Status, c.ReasonCode = "failed", "observed_drawdown_exceeds_predeclared_maximum"
			r.Status = "blocked"
		default:
			c.Status, c.ReasonCode = "passed", "observed_drawdown_within_predeclared_maximum"
		}
	}
	return nil
}
