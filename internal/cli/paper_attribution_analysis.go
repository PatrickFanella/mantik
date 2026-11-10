package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type attributionQuality struct {
	StrategyID             string         `json:"strategy_id"`
	StrategySHA256         string         `json:"strategy_sha256"`
	EvidenceClass          string         `json:"evidence_class"`
	Decisions              int            `json:"decisions"`
	EntryCandidates        int            `json:"entry_candidates"`
	Abstentions            int            `json:"abstentions"`
	Assessments            int            `json:"assessments"`
	ObservedCandidates     int            `json:"observed_candidates"`
	PositiveQuoteDeltas    int            `json:"positive_quote_deltas"`
	FlatQuoteDeltas        int            `json:"flat_quote_deltas"`
	NegativeQuoteDeltas    int            `json:"negative_quote_deltas"`
	LatestOutcomeStatuses  map[string]int `json:"latest_outcome_statuses"`
	EvaluationGateStatuses map[string]int `json:"evaluation_gate_statuses"`
	Status                 string         `json:"status"`
	ProbabilityScoring     string         `json:"probability_scoring"`
	IndependentSamples     *int           `json:"independent_samples"`
}

type attributionQualityTrace struct {
	DecisionID         string                             `json:"decision_id"`
	DecisionSHA256     string                             `json:"decision_sha256"`
	RowSHA256          string                             `json:"row_sha256"`
	StrategyID         string                             `json:"strategy_id"`
	StrategySHA256     string                             `json:"strategy_sha256"`
	Action             string                             `json:"action"`
	ReasonCode         string                             `json:"reason_code"`
	OutcomeID          string                             `json:"outcome_id,omitempty"`
	OutcomeSHA256      string                             `json:"outcome_sha256,omitempty"`
	OutcomeStatus      string                             `json:"outcome_status"`
	OutcomeEvaluatedAt *time.Time                         `json:"outcome_evaluated_at"`
	QuoteDeltaCents    *int64                             `json:"quote_delta_cents"`
	ComparisonID       string                             `json:"comparison_id,omitempty"`
	ComparisonSHA256   string                             `json:"comparison_sha256,omitempty"`
	ComparisonAt       *time.Time                         `json:"comparison_at"`
	ReadinessSHA256    string                             `json:"readiness_sha256,omitempty"`
	EvaluationGate     *storage.OwnedAdmissionGate        `json:"evaluation_gate"`
	EvaluationEvidence []storage.OwnedAssessmentReadiness `json:"evaluation_evidence"`
}

type attributionDelayStats struct {
	MinMS    int64 `json:"min_ms"`
	MedianMS int64 `json:"median_ms"`
	MeanMS   int64 `json:"mean_ms"`
	MaxMS    int64 `json:"max_ms"`
}

type attributionDelays struct {
	StrategyID     string                `json:"strategy_id"`
	StrategySHA256 string                `json:"strategy_sha256"`
	EvidenceClass  string                `json:"evidence_class"`
	Kind           string                `json:"kind"`
	UniqueCaptures int                   `json:"unique_captures"`
	ReviewLinks    int                   `json:"review_links"`
	ClockRelations map[string]int        `json:"clock_relations"`
	Observation    attributionDelayStats `json:"observation"`
	Ingestion      attributionDelayStats `json:"ingestion"`
	Availability   attributionDelayStats `json:"availability"`
}

type attributionDelayTrace struct {
	StrategyID     string                         `json:"strategy_id"`
	StrategySHA256 string                         `json:"strategy_sha256"`
	DecisionID     string                         `json:"decision_id"`
	DecisionSHA256 string                         `json:"decision_sha256"`
	ReviewSHA256   string                         `json:"review_sha256"`
	Kind           string                         `json:"kind"`
	Timing         storage.PaperClaimSourceTiming `json:"timing"`
}

type attributionAnalysis struct {
	Schema                 string                    `json:"schema"`
	ReceiverID             string                    `json:"receiver_id"`
	Scope                  string                    `json:"scope"`
	RowsSHA256             string                    `json:"rows_sha256"`
	OutcomeSelection       string                    `json:"outcome_selection"`
	EvaluationSelection    string                    `json:"evaluation_selection"`
	SourceTimingStatus     string                    `json:"source_timing_status"`
	Quality                []attributionQuality      `json:"quality"`
	DecisionTrace          []attributionQualityTrace `json:"decision_trace"`
	SourceDelays           []attributionDelays       `json:"source_delays"`
	SourceTrace            []attributionDelayTrace   `json:"source_trace"`
	DecisionInputsVerified bool                      `json:"decision_inputs_verified"`
	CopyReturnsVerified    bool                      `json:"copy_returns_verified"`
	QualificationCredit    bool                      `json:"qualification_credit"`
	SHA256                 string                    `json:"sha256"`
}

func delayStats(values []time.Duration) attributionDelayStats {
	slices.Sort(values)
	var sum time.Duration
	for _, value := range values {
		sum += value
	}
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + median) / 2
	}
	return attributionDelayStats{values[0].Milliseconds(), median.Milliseconds(), (sum / time.Duration(len(values))).Milliseconds(), values[len(values)-1].Milliseconds()}
}

// The selected native receipts describe these returned rows only. Repeated
// assessments are not samples, and post-decision sources are not causal inputs.
func analyzePaperAttribution(receiver string, rows []storage.PaperAttributionRow) (attributionAnalysis, error) {
	if !storage.ValidReceiverID(receiver) || rows == nil || len(rows) > storage.MaxPaperDecisions {
		return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
	}
	a := attributionAnalysis{Schema: "mantik-paper-attribution-analysis-v1", ReceiverID: receiver, Scope: "returned_rows_only", RowsSHA256: workerplan.Hash(rows), OutcomeSelection: "latest_assessment_per_decision_by_time_then_id", EvaluationSelection: "latest_retained_comparison_per_decision_by_time_then_id", SourceTimingStatus: "source_timing_unavailable", Quality: []attributionQuality{}, DecisionTrace: []attributionQualityTrace{}, SourceDelays: []attributionDelays{}, SourceTrace: []attributionDelayTrace{}}
	quality := map[string]int{}
	type delayGroup struct {
		summary                       attributionDelays
		captures                      map[string]bool
		observed, ingested, available []time.Duration
	}
	delays := map[string]*delayGroup{}
	captures := map[string]storage.PaperClaimSourceTiming{}
	reviews := map[string]bool{}
	outcomes := map[string]bool{}
	comparisons := map[string]string{}
	previous := ""
	for _, r := range rows {
		if validateAttributionRow(r, receiver) != nil || r.Decision.Command.ID <= previous {
			return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
		}
		d := r.Decision
		previous = d.Command.ID
		key := d.Command.StrategyID + ":" + d.Command.StrategySHA256 + ":" + r.EvidenceClass
		index, exists := quality[key]
		if !exists {
			index = len(a.Quality)
			quality[key] = index
			a.Quality = append(a.Quality, attributionQuality{StrategyID: d.Command.StrategyID, StrategySHA256: d.Command.StrategySHA256, EvidenceClass: r.EvidenceClass, ProbabilityScoring: "not_applicable_boolean_predicate", LatestOutcomeStatuses: map[string]int{}, EvaluationGateStatuses: map[string]int{}})
		}
		q := &a.Quality[index]
		q.Decisions++
		q.Assessments += len(r.Observations)
		if d.Evaluation.Action == "abstain" {
			q.Abstentions++
		} else {
			q.EntryCandidates++
		}
		trace := attributionQualityTrace{DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, RowSHA256: r.SHA256, StrategyID: d.Command.StrategyID, StrategySHA256: d.Command.StrategySHA256, Action: d.Evaluation.Action, ReasonCode: d.Evaluation.ReasonCode, OutcomeStatus: "not_assessed", EvaluationEvidence: []storage.OwnedAssessmentReadiness{}}
		var latest *storage.PaperOutcomeRecord
		for i := range r.Observations {
			o := &r.Observations[i]
			if outcomes[o.Command.ID] || o.EvaluatedAt.Before(d.FinalizedAt) {
				return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
			}
			outcomes[o.Command.ID] = true
			if latest == nil || o.EvaluatedAt.After(latest.EvaluatedAt) || o.EvaluatedAt.Equal(latest.EvaluatedAt) && o.Command.ID > latest.Command.ID {
				latest = o
			}
		}
		if latest != nil {
			trace.OutcomeID, trace.OutcomeSHA256, trace.OutcomeStatus = latest.Command.ID, latest.SHA256, latest.Evaluation.Status
			trace.OutcomeEvaluatedAt = &latest.EvaluatedAt
			if trace.OutcomeStatus == "observed" {
				o := latest.Evaluation
				if d.Evaluation.Action != "entry_candidate" || o.EntryPriceCents <= 0 || o.ObservedPriceCents <= 0 || o.PriceDeltaCents != o.ObservedPriceCents-o.EntryPriceCents {
					return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
				}
				q.ObservedCandidates++
				trace.QuoteDeltaCents = &o.PriceDeltaCents
				switch {
				case o.PriceDeltaCents > 0:
					q.PositiveQuoteDeltas++
				case o.PriceDeltaCents < 0:
					q.NegativeQuoteDeltas++
				default:
					q.FlatQuoteDeltas++
				}
			}
		}
		q.LatestOutcomeStatuses[trace.OutcomeStatus]++
		var comparison *storage.PaperAllocationAttribution
		for i := range r.Allocations {
			c := &r.Allocations[i]
			if pin, exists := comparisons[c.ComparisonID]; exists && pin != c.RecordSHA256 {
				return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
			}
			comparisons[c.ComparisonID] = c.RecordSHA256
			if comparison == nil || c.At.After(comparison.At) || c.At.Equal(comparison.At) && c.ComparisonID > comparison.ComparisonID {
				comparison = c
			}
		}
		gateStatus := "comparison_unavailable"
		if comparison != nil {
			trace.ComparisonID, trace.ComparisonSHA256 = comparison.ComparisonID, comparison.RecordSHA256
			trace.ComparisonAt = &comparison.At
			gateStatus = "evaluation_readiness_unavailable"
			if readiness := comparison.AdmissionReadiness; readiness != nil {
				trace.ReadinessSHA256 = readiness.SHA256
				for _, gate := range readiness.Gates {
					if gate.Name == "evaluation_recipe" {
						v := gate
						trace.EvaluationGate, gateStatus = &v, gate.Status
					}
				}
				trace.EvaluationEvidence = append(trace.EvaluationEvidence, readiness.EvaluationEvidence...)
			}
		}
		q.EvaluationGateStatuses[gateStatus]++
		a.DecisionTrace = append(a.DecisionTrace, trace)
		if h := r.ClaimReviewHistory; h != nil {
			byID := map[string]storage.DecisionReviewRecord{}
			for _, review := range h.Records {
				if reviews[review.Command.ID] || len(reviews) >= storage.MaxDecisionReviews {
					return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
				}
				reviews[review.Command.ID] = true
				byID[review.Command.ID] = review
			}
			for _, timing := range h.SourceTimings {
				review := byID[timing.ReviewID]
				captureKey := review.Source.Kind + ":" + timing.CaptureID
				if old, exists := captures[captureKey]; exists && (old.CaptureSHA256 != timing.CaptureSHA256 || !old.OccurredAt.Equal(timing.OccurredAt) || !old.DeclaredObservedAt.Equal(timing.DeclaredObservedAt) || !old.RetainedAt.Equal(timing.RetainedAt)) {
					return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
				}
				captures[captureKey] = timing
				groupKey := key + ":" + review.Source.Kind
				g := delays[groupKey]
				if g == nil {
					g = &delayGroup{summary: attributionDelays{StrategyID: d.Command.StrategyID, StrategySHA256: d.Command.StrategySHA256, EvidenceClass: r.EvidenceClass, Kind: review.Source.Kind, ClockRelations: map[string]int{}}, captures: map[string]bool{}}
					delays[groupKey] = g
				}
				g.summary.ReviewLinks++
				g.summary.ClockRelations[timing.ClockRelation]++
				if !g.captures[captureKey] {
					g.captures[captureKey] = true
					g.observed = append(g.observed, timing.DeclaredObservedAt.Sub(timing.OccurredAt))
					g.ingested = append(g.ingested, timing.RetainedAt.Sub(timing.DeclaredObservedAt))
					g.available = append(g.available, timing.RetainedAt.Sub(timing.OccurredAt))
				}
				a.SourceTrace = append(a.SourceTrace, attributionDelayTrace{d.Command.StrategyID, d.Command.StrategySHA256, d.Command.ID, d.SHA256, review.SHA256, review.Source.Kind, timing})
			}
		}
	}
	for i := range a.Quality {
		q := &a.Quality[i]
		q.Status = "insufficient_observed_outcomes"
		if q.EntryCandidates == 0 {
			q.Status = "no_entry_candidates"
		} else if q.ObservedCandidates > 0 {
			q.Status = "descriptive_observations_available"
		}
	}
	slices.SortFunc(a.Quality, func(a, b attributionQuality) int {
		return strings.Compare(a.StrategyID+":"+a.StrategySHA256+":"+a.EvidenceClass, b.StrategyID+":"+b.StrategySHA256+":"+b.EvidenceClass)
	})
	keys := []string{}
	for key := range delays {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		g := delays[key]
		g.summary.UniqueCaptures = len(g.captures)
		g.summary.Observation, g.summary.Ingestion, g.summary.Availability = delayStats(g.observed), delayStats(g.ingested), delayStats(g.available)
		a.SourceDelays = append(a.SourceDelays, g.summary)
	}
	if len(a.SourceDelays) > 0 {
		a.SourceTimingStatus = "post_decision_source_delays_measured"
	}
	a.SHA256 = workerplan.Hash(a)
	raw, err := json.Marshal(a)
	var detached attributionAnalysis
	if err != nil || workerplan.ReadBoundedShape(raw, &detached, storage.MaxPaperAttributionBytes) != nil {
		return attributionAnalysis{}, storage.ErrPaperAttributionInvalid
	}
	return detached, nil
}

func renderAttributionAnalysis(out io.Writer, a attributionAnalysis) error {
	var b strings.Builder
	fmt.Fprintf(&b, "ANALYSIS scope=returned_rows_only rows=%d qualification=false\n", len(a.DecisionTrace))
	for _, q := range a.Quality {
		fmt.Fprintf(&b, "  QUALITY strategy=%s version=%s evidence=%s status=%s decisions=%d entry_candidates=%d abstentions=%d assessments=%d observed_candidates=%d quote_direction_up=%d flat=%d down=%d probability_scoring=not_applicable independent_samples=unavailable\n", strconv.Quote(q.StrategyID), q.StrategySHA256, strconv.Quote(q.EvidenceClass), q.Status, q.Decisions, q.EntryCandidates, q.Abstentions, q.Assessments, q.ObservedCandidates, q.PositiveQuoteDeltas, q.FlatQuoteDeltas, q.NegativeQuoteDeltas)
	}
	for _, d := range a.SourceDelays {
		fmt.Fprintf(&b, "  SOURCE_DELAY strategy=%s version=%s evidence=%s kind=%s unique_captures=%d review_links=%d observed_ms[min/median/mean/max]=%d/%d/%d/%d ingestion_ms=%d/%d/%d/%d available_ms=%d/%d/%d/%d\n", strconv.Quote(d.StrategyID), d.StrategySHA256, strconv.Quote(d.EvidenceClass), d.Kind, d.UniqueCaptures, d.ReviewLinks, d.Observation.MinMS, d.Observation.MedianMS, d.Observation.MeanMS, d.Observation.MaxMS, d.Ingestion.MinMS, d.Ingestion.MedianMS, d.Ingestion.MeanMS, d.Ingestion.MaxMS, d.Availability.MinMS, d.Availability.MedianMS, d.Availability.MeanMS, d.Availability.MaxMS)
	}
	for _, trace := range a.DecisionTrace {
		gate, reason := "unavailable", "retained_applicable_evaluation_unavailable"
		if trace.EvaluationGate != nil {
			gate, reason = trace.EvaluationGate.Status, trace.EvaluationGate.ReasonCode
		}
		fmt.Fprintf(&b, "  QUALITY_SOURCE decision=%s sha256=%s latest_outcome=%s outcome_sha256=%s status=%s comparison=%s evaluation=%s reason=%s\n", strconv.Quote(trace.DecisionID), trace.DecisionSHA256, strconv.Quote(trace.OutcomeID), trace.OutcomeSHA256, trace.OutcomeStatus, strconv.Quote(trace.ComparisonID), strconv.Quote(gate), strconv.Quote(reason))
		for _, evidence := range trace.EvaluationEvidence {
			fmt.Fprintf(&b, "    APPLICABLE_EVALUATION assessment=%s sha256=%s declaration=%s evidence_sha256=%s sample_status=%s decision_evidence=%s\n", strconv.Quote(evidence.AssessmentID), evidence.AssessmentSHA256, strconv.Quote(evidence.DeclarationID), evidence.EvidenceSHA256, strconv.Quote(evidence.SampleStatus), strconv.Quote(evidence.DecisionEvidenceStatus))
			for _, check := range evidence.FailedChecks {
				fmt.Fprintf(&b, "      FAILED_CHECK name=%s status=%s reason=%s\n", strconv.Quote(check.Name), strconv.Quote(check.Status), strconv.Quote(check.ReasonCode))
			}
		}
	}
	for _, trace := range a.SourceTrace {
		fmt.Fprintf(&b, "  DELAY_SOURCE decision=%s review=%s review_sha256=%s capture=%s capture_sha256=%s kind=%s relation=%s\n", strconv.Quote(trace.DecisionID), strconv.Quote(trace.Timing.ReviewID), trace.ReviewSHA256, strconv.Quote(trace.Timing.CaptureID), trace.Timing.CaptureSHA256, trace.Kind, trace.Timing.ClockRelation)
	}
	fmt.Fprintf(&b, "  SOURCE_TIMING_STATUS %s\n  ANALYSIS SHA256 %s\n", a.SourceTimingStatus, a.SHA256)
	fmt.Fprintln(&b, "Quote directions are descriptive outcomes, not forecast accuracy or trading returns. Capture counts measure retained latency evidence; causal decision inputs and copy returns remain unverified.")
	_, err := io.WriteString(out, b.String())
	return err
}

func writeAttributionAnalysis(out io.Writer, receiver string, rows []storage.PaperAttributionRow, format string) error {
	a, err := analyzePaperAttribution(receiver, rows)
	if err != nil {
		return err
	}
	if format == "json" {
		return emit(out, a)
	}
	return renderAttributionAnalysis(out, a)
}
