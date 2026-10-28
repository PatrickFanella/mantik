package storage

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPurgedHoldoutUsesLabelAvailabilityAndStrictTrainingBoundary(t *testing.T) {
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	stamp := func(seconds int) time.Time { return at.Add(time.Duration(seconds) * time.Second) }
	sha := strings.Repeat("a", 64)
	e := PaperEvaluationEvidence{SHA256: sha, Phase: "outcome_deadline_elapsed", Declaration: PaperEvaluationDeclaration{SHA256: sha, Command: PaperEvaluationDeclarationCommand{Recipe: PaperHoldoutEvaluationRecipe, Plan: PaperEvaluationPlan{WindowStart: at, WindowEnd: stamp(60), Variants: []PaperEvaluationVariant{{ID: "strategy", SHA256: sha}}, Holdout: &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: stamp(20), EmbargoSeconds: 2, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}}}}}
	var pins []PaperEvaluationHoldoutRow
	add := func(id string, decision, horizon, label int, status string) {
		r := PaperEvaluationEvidenceRow{Status: "selected_research_evidence", Decision: PaperDecisionRecord{Command: PaperDecisionCommand{ID: id, StrategyID: "strategy", StrategySHA256: sha}, FinalizedAt: stamp(decision), SHA256: sha}, Outcomes: []PaperOutcomeRecord{{SHA256: sha, EvaluatedAt: stamp(label), Evaluation: PaperOutcomeEvaluation{Status: status, AvailableAt: stamp(label - 1)}}}}
		e.Rows = append(e.Rows, r)
		p := PaperEvaluationHoldoutRow{FeatureInputSHA256: sha, FeatureOccurredAt: stamp(decision), FeatureAvailableAt: stamp(decision), DecisionID: id, DecisionSHA256: sha, StrategySHA256: sha, HorizonAt: stamp(horizon), OutcomeSHA256: []string{sha}}
		if status == "observed" {
			p.LabelAvailableAt = stamp(label)
		}
		pins = append(pins, p)
	}
	add("training-one", 1, 3, 4, "observed")
	add("training-two", 5, 7, 8, "observed")
	add("horizon-equals-cut", 10, 18, 15, "observed")
	add("label-equals-cut", 11, 14, 18, "observed")
	add("label-created-after-split", 12, 14, 23, "observed")
	add("embargo", 21, 24, 25, "observed")
	add("holdout-boundary", 22, 25, 26, "observed")
	add("holdout-two", 27, 30, 31, "observed")
	add("waiting-is-not-a-label", 2, 4, 5, "waiting_price")
	add("training-feature-carried-into-holdout", 34, 36, 37, "observed")
	pins[len(pins)-1].FeatureOccurredAt = stamp(17)
	pins[len(pins)-1].FeatureAvailableAt = stamp(22)
	duplicate := e.Rows[0]
	duplicate.Status = "duplicate_strategy_occurrence"
	e.Rows = append(e.Rows, duplicate)
	r, err := holdoutFromRows(e, pins)
	if err != nil {
		t.Fatal(err)
	}
	v := r.Variants[0]
	if v.TrainingOccurrenceUpperBound != 2 || v.HoldoutOccurrenceUpperBound != 2 || v.Purged != 6 || v.Status != "unavailable" || r.IndependenceEstablished || r.OutOfSamplePerformanceAvailable || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
	for i, want := range []string{"training", "training", "purged_split_or_embargo_overlap", "purged_split_or_embargo_overlap", "purged_split_or_embargo_overlap", "purged_split_or_embargo_overlap", "holdout", "holdout", "outcome_unavailable", "purged_split_or_embargo_overlap"} {
		if r.Rows[i].Partition != want {
			t.Fatal(i, r.Rows[i])
		}
	}
	e.Phase = "collecting_observations"
	pending, err := holdoutFromRows(e, pins)
	if err != nil || pending.Variants[0].Status != "pending" {
		t.Fatal(pending, err)
	}
	e.Phase = "outcome_deadline_elapsed"
	e.Declaration.Command.Plan.Holdout.MinimumHoldoutOccurrences = 3
	failed, err := holdoutFromRows(e, pins)
	if err != nil || failed.Variants[0].Status != "failed" {
		t.Fatal(failed, err)
	}
	pins[0].LabelAvailableAt = stamp(3)
	if _, err := holdoutFromRows(e, pins); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("caller label clock accepted", err)
	}
}

func TestHoldoutDeclarationChoicesAreProspectiveAndVersioned(t *testing.T) {
	m := NewMemory()
	c, at := declarationFixture(t, m)
	if c.Validate() != nil {
		t.Fatal("legacy fixture invalid")
	}
	c.Plan.Holdout = &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: c.Plan.WindowStart.Add(30 * time.Minute), EmbargoSeconds: 60, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	if c.Validate() == nil {
		t.Fatal("legacy recipe accepted new method choices")
	}
	c.Recipe = PaperHoldoutEvaluationRecipe
	if c.Validate() != nil {
		t.Fatal("new prospective method rejected")
	}
	r, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil || r.Record.Schema != "mantik-owned-paper-evaluation-declaration-v4" {
		t.Fatal(r, err)
	}
	clock := func() time.Time { panic("retry or conflict sampled clock") }
	if retry, err := m.DeclarePaperEvaluation(t.Context(), c, clock); err != nil || !retry.Duplicate || retry.Record.SHA256 != r.Record.SHA256 {
		t.Fatal(retry, err)
	}
	c.Plan.Holdout.EmbargoSeconds++
	if _, err := m.DeclarePaperEvaluation(t.Context(), c, clock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal(err)
	}
	c.Plan.Holdout.SplitAt = c.Plan.WindowStart
	if c.Validate() == nil {
		t.Fatal("empty training window accepted")
	}
}
