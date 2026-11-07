package storage

import (
	"errors"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
)

func drawdownAssessment(base, stress int64, marked bool) PaperEvaluationAssessment {
	v := PaperEvaluationPortfolioVariant{StrategyID: "a", Base: &papereconomics.Result{ObservedMaximumDrawdownCents: base, AllEventMarksAvailable: marked}, Stress: &papereconomics.Result{ObservedMaximumDrawdownCents: stress, AllEventMarksAvailable: true}}
	return PaperEvaluationAssessment{Status: "incomplete", Evidence: PaperEvaluationEvidence{Phase: "outcome_deadline_elapsed"}, Portfolio: &PaperEvaluationPortfolio{Variants: []PaperEvaluationPortfolioVariant{v}}, Checks: []PaperEvaluationCheck{{StrategyID: "a", Name: "maximum_drawdown", Status: "unavailable"}}}
}

func TestDrawdownDecision(t *testing.T) {
	for name, tc := range map[string]struct {
		base, stress   int64
		marked         bool
		phase          string
		status, reason string
	}{
		"within":       {900, 1000, true, "outcome_deadline_elapsed", "passed", "observed_drawdown_within_predeclared_maximum"},
		"stress over":  {900, 1001, true, "outcome_deadline_elapsed", "failed", "observed_drawdown_exceeds_predeclared_maximum"},
		"base over":    {1001, 0, true, "outcome_deadline_elapsed", "failed", "observed_drawdown_exceeds_predeclared_maximum"},
		"unmarked":     {0, 0, false, "outcome_deadline_elapsed", "unavailable", "event_marks_unavailable_for_drawdown"},
		"window open":  {0, 0, true, "awaiting_outcome_deadline", "pending", "predeclared_outcome_window_not_closed"},
		"flat at zero": {0, 0, true, "outcome_deadline_elapsed", "passed", "observed_drawdown_within_predeclared_maximum"},
	} {
		r := drawdownAssessment(tc.base, tc.stress, tc.marked)
		r.Evidence.Phase = tc.phase
		if err := applyDrawdown(&r, 1000); err != nil {
			t.Fatal(name, err)
		}
		if c := r.Checks[0]; c.Status != tc.status || c.ReasonCode != tc.reason || (r.Status == "blocked") != (tc.status == "failed") {
			t.Fatal(name, c, r.Status)
		}
	}
	r := drawdownAssessment(0, 0, true)
	r.Portfolio.Variants[0].Base = nil
	if err := applyDrawdown(&r, 1000); err != nil || r.Checks[0].ReasonCode != "variant_book_unavailable_for_drawdown" {
		t.Fatal("missing book", r.Checks[0], err)
	}
	r = drawdownAssessment(-1, 0, true)
	if err := applyDrawdown(&r, 1000); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("negative drawdown accepted", err)
	}
	r = drawdownAssessment(0, 0, true)
	r.Checks[0].StrategyID = "missing"
	if err := applyDrawdown(&r, 1000); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("check without a book accepted", err)
	}
}
