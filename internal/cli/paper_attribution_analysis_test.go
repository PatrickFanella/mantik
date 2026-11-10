package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func analysisRow(id, action string, outcomes ...storage.PaperOutcomeRecord) storage.PaperAttributionRow {
	d := decisionViewFixture(id)
	d.Evaluation.Action = action
	d.SHA256 = ""
	d.SHA256 = workerplan.Hash(d)
	r := attributionViewFixture(d)
	r.Observations = append([]storage.PaperOutcomeRecord{}, outcomes...)
	if len(outcomes) > 0 {
		r.ObservationStatus = "assessments_retained"
	}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	return r
}

func analysisOutcome(d storage.PaperDecisionRecord, id, status string, at time.Time, delta int64) storage.PaperOutcomeRecord {
	id = d.Command.ID + "-" + id
	r := storage.PaperOutcomeRecord{Schema: "mantik-owned-paper-outcome-v1", Command: storage.PaperOutcomeCommand{ID: id, ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256}, EvaluatedAt: at, Recipe: storage.PaperOutcomeRecipe, BasisSHA256: strings.Repeat("e", 64), Authority: "service_owned_forward_quote_outcome_only", Evaluation: storage.PaperOutcomeEvaluation{Status: status, ReasonCode: "presentation_fixture_only"}}
	if status == "observed" {
		r.Evaluation.EntryPriceCents, r.Evaluation.ObservedPriceCents, r.Evaluation.PriceDeltaCents = 100, 100+delta, delta
	}
	r.SHA256 = workerplan.Hash(r)
	return r
}

func TestAttributionAnalysisLatestOutcomesAndVersions(t *testing.T) {
	one, two, three := analysisRow("one", "entry_candidate"), analysisRow("two", "entry_candidate"), analysisRow("three", "abstain")
	at := one.Decision.FinalizedAt.Add(time.Second)
	one = analysisRow("one", "entry_candidate", analysisOutcome(one.Decision, "a-up", "observed", at, 5), analysisOutcome(one.Decision, "b-down", "observed", at, -3), analysisOutcome(one.Decision, "c-waiting", "waiting_price", at.Add(time.Second), 0))
	two = analysisRow("two", "entry_candidate", analysisOutcome(two.Decision, "a-up", "observed", at, 5), analysisOutcome(two.Decision, "b-down", "observed", at, -3))
	three = analysisRow("three", "abstain", analysisOutcome(three.Decision, "not-applicable", "not_applicable", at, 0))
	version := analysisRow("version", "abstain")
	version.Decision.Command.StrategySHA256 = strings.Repeat("f", 64)
	version.Decision.SHA256 = ""
	version.Decision.SHA256 = workerplan.Hash(version.Decision)
	version.SHA256 = ""
	version.SHA256 = workerplan.Hash(version)
	a, err := analyzePaperAttribution(one.Decision.Command.ReceiverID, []storage.PaperAttributionRow{one, three, two, version})
	if err != nil || len(a.Quality) != 2 || a.Scope != "returned_rows_only" || a.SourceTimingStatus != "source_timing_unavailable" || a.QualificationCredit || a.DecisionInputsVerified || a.CopyReturnsVerified {
		t.Fatal(a, err)
	}
	q := a.Quality[0]
	if q.Decisions != 3 || q.EntryCandidates != 2 || q.Abstentions != 1 || q.Assessments != 6 || q.ObservedCandidates != 1 || q.PositiveQuoteDeltas != 0 || q.NegativeQuoteDeltas != 1 || q.FlatQuoteDeltas != 0 || q.IndependentSamples != nil || q.LatestOutcomeStatuses["waiting_price"] != 1 || q.LatestOutcomeStatuses["not_applicable"] != 1 || q.EvaluationGateStatuses["comparison_unavailable"] != 3 || q.ProbabilityScoring != "not_applicable_boolean_predicate" {
		t.Fatal("repeated or favorable outcomes inflated quality", q)
	}
	if a.DecisionTrace[0].OutcomeID != "one-c-waiting" || a.DecisionTrace[0].QuoteDeltaCents != nil || a.DecisionTrace[2].OutcomeID != "two-b-down" || *a.DecisionTrace[2].QuoteDeltaCents != -3 || a.Quality[1].Status != "no_entry_candidates" {
		t.Fatal("latest assessment selection changed", a.DecisionTrace)
	}
	before := a.SHA256
	a.SHA256 = ""
	if workerplan.Hash(a) != before {
		t.Fatal("analysis root does not bind the report")
	}
	for _, rows := range [][]storage.PaperAttributionRow{nil, {one, one}, {two, one}} {
		if got, err := analyzePaperAttribution(one.Decision.Command.ReceiverID, rows); err == nil || !reflect.DeepEqual(got, attributionAnalysis{}) {
			t.Fatal("invalid or repeated rows produced analysis", err)
		}
	}
}

// Portable UI fixtures prove arithmetic and duplicate handling. Actual native
// source custody and clocks are exercised in sourceDecisionReviewHTTP for both
// Memory and disposable PostgreSQL, rather than attested by these envelopes.
func analysisTimingRow(id, reviewID, captureID string, observed, retained time.Duration) storage.PaperAttributionRow {
	r := analysisRow(id, "abstain")
	d := r.Decision
	occurred := d.FinalizedAt.Add(-time.Second)
	value := "abstain"
	pin := workerplan.Hash(captureID)
	command := storage.DecisionReviewCommand{ID: reviewID, ReceiverID: d.Command.ReceiverID, DecisionID: id, DecisionSHA256: d.SHA256, Source: &storage.DecisionReviewSourceCommand{CaptureID: captureID, CaptureSHA256: pin}, Claims: []storage.DecisionReviewClaim{{Field: "action", Value: value}}}
	review := storage.DecisionReviewRecord{Schema: "mantik-finalized-decision-review-v2", Method: "owned-social-copy-decision-claims-v1", Command: command, ReviewedAt: occurred.Add(retained + time.Second), BasisSHA256: d.BasisSHA256, Findings: []storage.DecisionReviewFinding{{Claim: command.Claims[0], Status: "supported", Expected: &value}}, Status: "claims_consistent", RecordPath: "/v1/paper-decisions/" + id, Authority: "finalized_decision_claim_review_only", Source: &storage.DecisionReviewSourceBinding{Kind: "copy", CaptureID: captureID, CaptureSHA256: pin, OriginalSHA256: pin, PolicySHA256: pin, ClaimGroupSHA256: pin, JournalEntries: 1, JournalHeadSHA256: pin, ApplicabilitySHA256: pin, RecordPath: "/v1/copy-recordings/" + captureID}}
	review.SHA256 = workerplan.Hash(review)
	relation := "retained_after_decision_clock"
	if retained < time.Second {
		relation = "retained_before_decision_clock"
	} else if retained == time.Second {
		relation = "retained_at_decision_clock_order_unproven"
	}
	timing := storage.PaperClaimSourceTiming{ReviewID: reviewID, CaptureID: captureID, CaptureSHA256: pin, OccurredAt: occurred, DeclaredObservedAt: occurred.Add(observed), RetainedAt: occurred.Add(retained), DecisionFinalizedAt: d.FinalizedAt, ObservationDelaySeconds: int64(observed / time.Second), AvailabilityDelaySeconds: int64(retained / time.Second), ClockRelation: relation}
	history := storage.PaperClaimReviewHistory{Records: []storage.DecisionReviewRecord{review}, SourceTimings: []storage.PaperClaimSourceTiming{timing}, Authority: "service_owned_post_decision_claim_reviews_only"}
	history.SHA256 = workerplan.Hash(history)
	r.ClaimReviewHistory = &history
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	return r
}

func TestAttributionAnalysisUniqueCaptureDelays(t *testing.T) {
	rows := []storage.PaperAttributionRow{analysisTimingRow("one", "review-one", "capture-one", 1250*time.Millisecond, 3*time.Second), analysisTimingRow("two", "review-two", "capture-one", 1250*time.Millisecond, 3*time.Second), analysisTimingRow("z-last", "review-last", "capture-two", 250*time.Millisecond, 750*time.Millisecond)}
	a, err := analyzePaperAttribution(rows[0].Decision.Command.ReceiverID, rows)
	if err != nil || len(a.SourceDelays) != 1 || len(a.SourceTrace) != 3 || a.SourceTimingStatus != "post_decision_source_delays_measured" {
		t.Fatal(a, err)
	}
	d := a.SourceDelays[0]
	if d.UniqueCaptures != 2 || d.ReviewLinks != 3 || d.Observation != (attributionDelayStats{250, 750, 750, 1250}) || d.Ingestion != (attributionDelayStats{500, 1125, 1125, 1750}) || d.Availability != (attributionDelayStats{750, 1875, 1875, 3000}) || d.ClockRelations["retained_after_decision_clock"] != 2 || d.ClockRelations["retained_before_decision_clock"] != 1 {
		t.Fatal("duplicate links weighted latency or components were rounded early", d)
	}
	var out bytes.Buffer
	if err := renderAttributionAnalysis(&out, a); err != nil || !strings.Contains(out.String(), "unique_captures=2 review_links=3") || !strings.Contains(out.String(), "capture-one") || !strings.Contains(out.String(), "probability_scoring=not_applicable") {
		t.Fatal(out.String(), err)
	}
	rows[1] = analysisTimingRow("two", "review-two", "capture-one", 2*time.Second, 3*time.Second)
	if got, err := analyzePaperAttribution(rows[0].Decision.Command.ReceiverID, rows); err == nil || !reflect.DeepEqual(got, attributionAnalysis{}) {
		t.Fatal("one capture acquired conflicting original clocks", err)
	}
}

func TestDecisionAttributionAnalysisNavigation(t *testing.T) {
	d := decisionViewFixture("one")
	r := attributionViewFixture(d)
	var out bytes.Buffer
	requests := 0
	if err := superviseDecisions(context.Background(), io.NopCloser(strings.NewReader("aa ../operations\naa one extra\naa one\naaj one\nq\n")), &out, 16, func(context.Context) (storage.PaperDecisionCatalog, error) {
		return storage.PaperDecisionCatalog{ReceiverID: d.Command.ReceiverID, Records: []storage.PaperDecisionRecord{d}}, nil
	}, func(_ context.Context, got storage.PaperDecisionRecord, kind string) (any, error) {
		requests++
		if !reflect.DeepEqual(got, d) || kind != "a" {
			t.Fatal("analysis dispatched another inspection", got, kind)
		}
		return r, nil
	}); err != nil || requests != 2 || !strings.Contains(out.String(), "QUALITY strategy=") || !strings.Contains(out.String(), "independent_samples=unavailable") {
		t.Fatal(out.String(), requests, err)
	}
	start := strings.Index(out.String(), "{\n")
	if start < 0 {
		t.Fatal("analysis JSON missing")
	}
	var a attributionAnalysis
	if err := json.NewDecoder(strings.NewReader(out.String()[start:])).Decode(&a); err != nil || len(a.DecisionTrace) != 1 || a.DecisionTrace[0].DecisionSHA256 != d.SHA256 {
		t.Fatal(a, err)
	}
}
