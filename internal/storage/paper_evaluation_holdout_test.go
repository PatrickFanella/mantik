package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
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

func TestHoldoutPublicationMustResolveExactOriginal(t *testing.T) {
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	recording := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "publication-reference", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: at, EvidenceClass: "prospective_market_unqualified", AccessRef: "manufactured-only", RawRef: "journal:journal-publication:capture-publication-reference-r1.json", MaxQuoteAgeSeconds: 60, Original: `{"quotes":{"SPY":{"t":"2026-10-21T14:00:00Z","ap":100,"bp":99,"as":10,"bs":10}}}`}
	recording.SHA256 = alpaca.Digest(recording.Original)
	normalized, err := alpaca.Normalize(recording)
	if err != nil {
		t.Fatal(err)
	}
	var input domain.RoutingInput
	for _, candidate := range normalized.Inputs {
		if candidate.Kind == "quote" {
			input = candidate
		}
	}
	if input.ID == "" {
		t.Fatal("manufactured quote missing")
	}
	raw, err := json.Marshal(recording)
	if err != nil {
		t.Fatal(err)
	}
	publication := FeedEvidenceRecord{RegistrationID: "reg-one", Key: "publication-reference-r1", PublishedAt: at.Add(2 * time.Second), Recording: raw}
	feed := FeedEvidenceSnapshot{Records: map[string]FeedEvidenceRecord{"reg-one:publication-reference-r1": publication}}
	decision := PaperDecisionRecord{Command: PaperDecisionCommand{RegistrationID: "reg-one"}, FinalizedAt: at.Add(3 * time.Second)}
	got, err := holdoutFeaturePublishedAt(t.Context(), feed, decision, input)
	if err != nil || !got.Equal(publication.PublishedAt) {
		t.Fatal("publication clock omitted", got, err)
	}
	mutated := input
	changedProvenance := *input.Provenance
	changedProvenance.RecordKey += "-other"
	mutated.Provenance = &changedProvenance
	if _, err := holdoutFeaturePublishedAt(t.Context(), feed, decision, mutated); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("mismatched capture identity accepted", err)
	}
	if _, err := holdoutFeaturePublishedAt(t.Context(), FeedEvidenceSnapshot{Records: map[string]FeedEvidenceRecord{}}, decision, input); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("missing publication manufactured fallback", err)
	}
	feed.Records["alias"] = publication
	if _, err := holdoutFeaturePublishedAt(t.Context(), feed, decision, input); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("ambiguous publication accepted", err)
	}
	delete(feed.Records, "alias")
	publication.PublishedAt = decision.FinalizedAt.Add(time.Second)
	feed.Records["reg-one:publication-reference-r1"] = publication
	if _, err := holdoutFeaturePublishedAt(t.Context(), feed, decision, input); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("post-decision publication accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := holdoutFeaturePublishedAt(ctx, feed, decision, input); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
