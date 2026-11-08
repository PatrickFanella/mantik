package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Clock relation describes retained timestamps, not decision causality or proof
// of source availability in the decision's market-data basis. Equal clocks do
// not establish an ordering across these independent owners.
type PaperClaimSourceTiming struct {
	ReviewID                 string    `json:"review_id"`
	CaptureID                string    `json:"capture_id"`
	CaptureSHA256            string    `json:"capture_sha256"`
	OccurredAt               time.Time `json:"occurred_at"`
	DeclaredObservedAt       time.Time `json:"declared_observed_at"`
	RetainedAt               time.Time `json:"retained_at"`
	DecisionFinalizedAt      time.Time `json:"decision_finalized_at"`
	ObservationDelaySeconds  int64     `json:"observation_delay_seconds"`
	AvailabilityDelaySeconds int64     `json:"availability_delay_seconds"`
	ClockRelation            string    `json:"clock_relation"`
	DecisionInputVerified    bool      `json:"decision_input_verified"`
}

type PaperClaimReviewHistory struct {
	Records             []DecisionReviewRecord   `json:"records"`
	SourceTimings       []PaperClaimSourceTiming `json:"source_timings"`
	Authority           string                   `json:"authority"`
	AdmissionAvailable  bool                     `json:"admission_available"`
	ExecutionAvailable  bool                     `json:"execution_available"`
	QualificationCredit bool                     `json:"qualification_credit"`
	SHA256              string                   `json:"sha256"`
}

func claimSourceClockRelation(retained, finalized time.Time) string {
	if retained.Before(finalized) {
		return "retained_before_decision_clock"
	}
	if retained.After(finalized) {
		return "retained_after_decision_clock"
	}
	return "retained_at_decision_clock_order_unproven"
}

// Validate checks a portable presentation envelope against its decision. Only
// service owner replay can establish the custody of the underlying source.
func (h PaperClaimReviewHistory) Validate(d PaperDecisionRecord) error {
	sha := h.SHA256
	h.SHA256 = ""
	if !decisionPin.MatchString(sha) || workerplan.Hash(h) != sha || h.Authority != "service_owned_post_decision_claim_reviews_only" || h.AdmissionAvailable || h.ExecutionAvailable || h.QualificationCredit || h.Records == nil || h.SourceTimings == nil || len(h.Records) > MaxDecisionReviews || len(h.SourceTimings) > len(h.Records) {
		return ErrPaperAttributionInvalid
	}
	previous, index := "", 0
	for _, r := range h.Records {
		c := clone(r.Command)
		c.Source = nil
		want, err := newDecisionReview(c, d, r.ReviewedAt)
		if err != nil || r.Command.ID <= previous {
			return ErrPaperAttributionInvalid
		}
		previous = r.Command.ID
		if r.Source != nil {
			b := r.Source
			path := "/v1/social-recordings/"
			if b.Kind == "copy" {
				path = "/v1/copy-recordings/"
			} else if b.Kind != "social" {
				return ErrPaperAttributionInvalid
			}
			if r.Command.Source == nil || r.Command.Validate() != nil || b.CaptureID != r.Command.Source.CaptureID || b.CaptureSHA256 != r.Command.Source.CaptureSHA256 || !decisionPin.MatchString(b.OriginalSHA256) || !decisionPin.MatchString(b.PolicySHA256) || !decisionPin.MatchString(b.ClaimGroupSHA256) || !decisionPin.MatchString(b.JournalHeadSHA256) || !decisionPin.MatchString(b.ApplicabilitySHA256) || b.JournalEntries < 1 || b.JournalEntries > socialwatch.MaxEntries || b.RecordPath != path+b.CaptureID || b.ClaimsExtractionVerified || b.ProviderAccessVerified || index >= len(h.SourceTimings) {
				return ErrPaperAttributionInvalid
			}
			want.Schema, want.Method, want.Command, want.Source = "mantik-finalized-decision-review-v2", "owned-social-copy-decision-claims-v1", clone(r.Command), clone(b)
			t := h.SourceTimings[index]
			index++
			if t.ReviewID != r.Command.ID || t.CaptureID != b.CaptureID || t.CaptureSHA256 != b.CaptureSHA256 || !declarationTime(t.OccurredAt) || !declarationTime(t.DeclaredObservedAt) || !declarationTime(t.RetainedAt) || t.DeclaredObservedAt.Before(t.OccurredAt) || t.RetainedAt.Before(t.DeclaredObservedAt) || t.RetainedAt.After(r.ReviewedAt) || !t.DecisionFinalizedAt.Equal(d.FinalizedAt) || t.RetainedAt.Sub(t.OccurredAt) > 24*time.Hour || t.ObservationDelaySeconds != int64(t.DeclaredObservedAt.Sub(t.OccurredAt)/time.Second) || t.AvailabilityDelaySeconds != int64(t.RetainedAt.Sub(t.OccurredAt)/time.Second) || t.ClockRelation != claimSourceClockRelation(t.RetainedAt, d.FinalizedAt) || t.DecisionInputVerified {
				return ErrPaperAttributionInvalid
			}
		}
		want.SHA256 = ""
		want.SHA256 = workerplan.Hash(want)
		if !reflect.DeepEqual(want, r) {
			return ErrPaperAttributionInvalid
		}
	}
	if index != len(h.SourceTimings) {
		return ErrPaperAttributionInvalid
	}
	return nil
}

// Replays the complete claim-review owner against decisions and social/copy
// history from this same Memory lock or PostgreSQL snapshot. Readback neither
// changes historical reviews nor creates a new review or financial effect.
func withClaimReviewAttribution(ctx context.Context, out PaperAttributionCatalog, decisions PaperDecisionCatalog, all map[string]DecisionReviewRecord, sources []socialwatch.Journal) (PaperAttributionCatalog, error) {
	reviews, err := decisionReviewCatalog(decisions.ReceiverID, all, decisions, sources...)
	if err != nil {
		return PaperAttributionCatalog{}, err
	}
	out.SourceSHA256 = workerplan.Hash(struct {
		Previous string
		Reviews  DecisionReviewCatalog
	}{out.SourceSHA256, reviews})
	for i := range out.Rows {
		if err := ctx.Err(); err != nil {
			return PaperAttributionCatalog{}, err
		}
		row := &out.Rows[i]
		h := PaperClaimReviewHistory{Records: []DecisionReviewRecord{}, SourceTimings: []PaperClaimSourceTiming{}, Authority: "service_owned_post_decision_claim_reviews_only"}
		for _, r := range reviews.Records {
			if r.Command.DecisionID != row.Decision.Command.ID {
				continue
			}
			h.Records = append(h.Records, clone(r))
			if r.Source == nil {
				continue
			}
			if len(sources) != 1 {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
			found := false
			// Only the source prefix bound by the actual reviewed record applies.
			for index, e := range sources[0].Entries {
				if index >= r.Source.JournalEntries {
					break
				}
				c := e.Capture
				if c == nil || c.Command.ID != r.Source.CaptureID {
					continue
				}
				if c.SHA256 != r.Source.CaptureSHA256 {
					return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
				}
				relation := claimSourceClockRelation(c.RetainedAt, row.Decision.FinalizedAt)
				h.SourceTimings = append(h.SourceTimings, PaperClaimSourceTiming{ReviewID: r.Command.ID, CaptureID: c.Command.ID, CaptureSHA256: c.SHA256, OccurredAt: c.Source.OccurredAt, DeclaredObservedAt: c.Source.ObservedAt, RetainedAt: c.RetainedAt, DecisionFinalizedAt: row.Decision.FinalizedAt, ObservationDelaySeconds: c.ObservationDelaySeconds, AvailabilityDelaySeconds: c.AvailabilityDelaySeconds, ClockRelation: relation})
				found = true
				break
			}
			if !found {
				return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
			}
		}
		h.SHA256 = workerplan.Hash(h)
		if h.Validate(row.Decision) != nil {
			return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
		}
		row.ClaimReviewHistory = &h
		row.SHA256 = ""
		row.SHA256 = workerplan.Hash(*row)
	}
	out.SHA256 = ""
	out.SHA256 = workerplan.Hash(out)
	// The final capacity check includes all newly joined source records.
	if raw, err := json.Marshal(out); err != nil || len(raw) > MaxPaperAttributionBytes {
		return PaperAttributionCatalog{}, ErrPaperAttributionInvalid
	}
	return clone(out), nil
}
