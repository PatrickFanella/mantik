package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type PaperOutcomeMarketRow struct {
	SessionEvidence      *PaperOutcomeSessionEvidence `json:"session_evidence"`
	InputSHA256          string                       `json:"input_sha256"`
	QualityReason        string                       `json:"quality_reason"`
	InputKind            string                       `json:"input_kind"`
	SelectedForwardQuote bool                         `json:"selected_forward_quote"`
	InputID              string                       `json:"input_id"`
	CaptureKey           string                       `json:"capture_key"`
	CaptureSHA256        string                       `json:"capture_sha256"`
	OccurrenceSHA256     string                       `json:"occurrence_sha256"`
	EvidenceClass        string                       `json:"evidence_class"`
	Quality              string                       `json:"quality"`
	OccurredAt           time.Time                    `json:"occurred_at"`
	ReceivedAt           time.Time                    `json:"received_at"`
	PublishedAt          time.Time                    `json:"published_at"`
	AdmittedAt           time.Time                    `json:"admitted_at"`
	AvailableAt          time.Time                    `json:"available_at"`
	TemporalStatus       string                       `json:"temporal_status"`
	DimensionStatus      string                       `json:"dimension_status"`
	Quote                *alpaca.QuoteDimensions      `json:"quote"`
	PublicationFence     feedcollector.Fence          `json:"publication_fence"`
	AdmissionFence       feedcollector.Fence          `json:"admission_fence"`
}
type PaperOutcomeMarketEvidence struct {
	SelectedObservationAvailableAt time.Time               `json:"selected_observation_available_at"`
	Schema                         string                  `json:"schema"`
	ReceiverID                     string                  `json:"receiver_id"`
	OutcomeID                      string                  `json:"outcome_id"`
	OutcomeSHA256                  string                  `json:"outcome_sha256"`
	BasisSHA256                    string                  `json:"basis_sha256"`
	DecisionID                     string                  `json:"decision_id"`
	DecisionSHA256                 string                  `json:"decision_sha256"`
	RegistrationID                 string                  `json:"registration_id"`
	Provider                       string                  `json:"provider"`
	Feed                           string                  `json:"feed"`
	FinalizedAt                    time.Time               `json:"finalized_at"`
	HorizonAt                      time.Time               `json:"horizon_at"`
	ObservationWindowEnd           time.Time               `json:"observation_window_end"`
	EvaluatedAt                    time.Time               `json:"evaluated_at"`
	OutcomeStatus                  string                  `json:"outcome_status"`
	Status                         string                  `json:"status"`
	CoverageStatus                 string                  `json:"coverage_status"`
	Rows                           []PaperOutcomeMarketRow `json:"rows"`
	Authority                      string                  `json:"authority"`
	ContinuousCoverageAvailable    bool                    `json:"continuous_coverage_available"`
	ShareQuantitiesAvailable       bool                    `json:"share_quantities_available"`
	AdmissionAvailable             bool                    `json:"admission_available"`
	ExecutionAvailable             bool                    `json:"execution_available"`
	QualificationCredit            bool                    `json:"qualification_credit"`
	SHA256                         string                  `json:"sha256"`
}

// Storage replays the actual outcome and decision owners before this inspector.
// This is a historical observation sequence, not an execution-ready price path.
// Publication and admission clocks are retained independently of quote occurrence.
func inspectPaperOutcomeMarket(ctx context.Context, s storage.PaperOutcomeSources) (PaperOutcomeMarketEvidence, error) {
	r, b := s.Outcome, s.Basis
	d := b.Decision
	feed := b.Observation.Feed
	if err := ctx.Err(); err != nil {
		return PaperOutcomeMarketEvidence{}, err
	}
	if r.Validate() != nil || d.Validate() != nil || r.BasisSHA256 != workerplan.Hash(b) || r.Command.ReceiverID != feed.ReceiverID || r.Command.DecisionID != d.Command.ID || r.Command.DecisionSHA256 != d.SHA256 || d.Command.ReceiverID != r.Command.ReceiverID || r.EvaluatedAt.Before(d.FinalizedAt) || feed.Routing == nil {
		return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeInvalid
	}
	entry, ok := feed.Collectors.Entries[d.Command.RegistrationID]
	if !ok || entry.Registration.Provider != "alpaca" {
		return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
	}
	if d.Evaluation.Action == "entry_candidate" && (r.Evaluation.DueAt.IsZero() || !r.Evaluation.DueAt.After(d.FinalizedAt) || r.Evaluation.WindowEnd.Before(r.Evaluation.DueAt)) {
		return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeInvalid
	}
	out := PaperOutcomeMarketEvidence{Schema: "mantik-owned-outcome-market-evidence-v3", ReceiverID: r.Command.ReceiverID, OutcomeID: r.Command.ID, OutcomeSHA256: r.SHA256, BasisSHA256: r.BasisSHA256, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, RegistrationID: d.Command.RegistrationID, Provider: entry.Registration.Provider, Feed: entry.Registration.Feed, FinalizedAt: d.FinalizedAt, HorizonAt: r.Evaluation.DueAt, ObservationWindowEnd: r.Evaluation.WindowEnd, EvaluatedAt: r.EvaluatedAt, OutcomeStatus: r.Evaluation.Status, SelectedObservationAvailableAt: r.Evaluation.AvailableAt, Status: "quote_observations_unavailable", CoverageStatus: "retained_observations_only", Rows: []PaperOutcomeMarketRow{}, Authority: "service_owned_retained_quote_observations_only"}
	seen := map[string]bool{}
	selected := 0
	for key, published := range feed.Records {
		if err := ctx.Err(); err != nil {
			return PaperOutcomeMarketEvidence{}, err
		}
		var recording alpaca.Recording
		if json.Unmarshal(published.Recording, &recording) != nil || key != published.RegistrationID+":"+published.Key || published.Key != recordingKey(recording) || published.RegistrationID != d.Command.RegistrationID || published.Fence.RegistrationID != d.Command.RegistrationID || feedcollector.ValidateFence(published.Fence) != nil || published.PublishedAt.IsZero() || published.PublishedAt.Before(recording.ReceivedAt) || published.PublishedAt.After(r.EvaluatedAt) {
			return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
		}
		if err := validateFeedRecording(feed, d.Command.RegistrationID, recording); err != nil {
			return PaperOutcomeMarketEvidence{}, err
		}
		normalized, err := alpaca.Normalize(recording)
		if err != nil {
			return PaperOutcomeMarketEvidence{}, err
		}
		for _, input := range normalized.Inputs {
			admission, ok := feed.Collectors.Admissions[input.ID]
			if !ok || admission.Fence.RegistrationID != d.Command.RegistrationID || feedcollector.ValidateFence(admission.Fence) != nil || admission.InputSHA256 != feedcollector.InputDigest(input) || admission.At.IsZero() || admission.At.Before(input.ReceivedAt) || admission.At.After(r.EvaluatedAt) {
				return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
			}

			if recording.Capability != "latest_quotes" {
				continue
			}
			if seen[input.ID] {
				return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
			}
			seen[input.ID] = true
			quality := "unavailable"
			if input.Provenance != nil {
				quality = input.Provenance.Quality
			}
			row := PaperOutcomeMarketRow{InputSHA256: feedcollector.InputDigest(input), InputKind: input.Kind, InputID: input.ID, CaptureKey: key, CaptureSHA256: recording.SHA256, EvidenceClass: input.EvidenceClass, Quality: quality, OccurredAt: input.OccurredAt, ReceivedAt: input.ReceivedAt, PublishedAt: published.PublishedAt, AdmittedAt: admission.At, AvailableAt: published.PublishedAt, TemporalStatus: "timestamp_unavailable", DimensionStatus: "quote_dimensions_unavailable", PublicationFence: published.Fence, AdmissionFence: admission.Fence}
			if input.Provenance != nil {
				row.QualityReason = input.Provenance.Reason
			}
			if input.Kind != "quote" {
				row.OccurredAt = time.Time{}
			}
			if admission.At.After(row.AvailableAt) {
				row.AvailableAt = admission.At
			}
			if input.Kind == "quote" && !input.OccurredAt.IsZero() {
				row.OccurrenceSHA256 = workerplan.Hash(struct {
					Provider, Feed, Symbol string
					OccurredAt             time.Time
				}{out.Provider, out.Feed, input.Symbol, input.OccurredAt.UTC()})
				switch {
				case d.Evaluation.Action != "entry_candidate":
					row.TemporalStatus = "decision_abstained"
				case input.OccurredAt.Before(d.FinalizedAt):
					row.TemporalStatus = "before_finalization"
				case input.OccurredAt.After(out.HorizonAt):
					row.TemporalStatus = "after_horizon"
				case row.AvailableAt.After(out.HorizonAt):
					row.TemporalStatus = "published_or_admitted_after_horizon"
				default:
					row.TemporalStatus = "within_holding_window"
				}
			}
			if input.Kind == "quote" && quality == "usable" {
				dimensions, err := alpaca.InspectQuoteShareDimensions(recording, input.ID)
				if err != nil {
					return PaperOutcomeMarketEvidence{}, err
				}
				row.Quote = &dimensions
				row.DimensionStatus = "dimensions_retained_share_units_unverified"
				if dimensions.ShareQuantitiesAvailable {
					row.DimensionStatus = "dimensions_retained_documented_share_units"
				}
			}
			if input.ID == r.Evaluation.InputID {
				if r.Evaluation.Status != "observed" || row.Quote == nil || row.CaptureSHA256 != r.Evaluation.CaptureSHA256 || row.Quote.AskCents != r.Evaluation.ObservedPriceCents || !row.OccurredAt.Equal(r.Evaluation.OccurredAt) || !row.PublishedAt.Equal(r.Evaluation.PublishedAt) || row.AvailableAt.After(r.Evaluation.AvailableAt) || r.Evaluation.AvailableAt.After(r.EvaluatedAt) {
					return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
				}
				row.SelectedForwardQuote = true
				selected++
			}
			out.Rows = append(out.Rows, row)
		}
	}
	// Admitted inputs whose original capture was not published are explicit
	// gaps. Their provenance cannot supply verified dimensions or availability.
	for id, admission := range feed.Collectors.Admissions {
		if err := ctx.Err(); err != nil {
			return PaperOutcomeMarketEvidence{}, err
		}
		if admission.Fence.RegistrationID != d.Command.RegistrationID || seen[id] {
			continue
		}
		retained, ok := feed.Routing.Inputs[id]
		if !ok {
			return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
		}
		input := retained.Input
		if input.Kind != "quote" && !(input.Kind == "state" && input.StateKey == "price_coverage") {
			continue
		}
		if feedcollector.ValidateFence(admission.Fence) != nil || admission.InputSHA256 != feedcollector.InputDigest(input) || admission.At.IsZero() || admission.At.Before(input.ReceivedAt) || admission.At.After(r.EvaluatedAt) {
			return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
		}
		row := PaperOutcomeMarketRow{InputSHA256: admission.InputSHA256, InputKind: input.Kind, InputID: id, EvidenceClass: input.EvidenceClass, ReceivedAt: input.ReceivedAt, AdmittedAt: admission.At, TemporalStatus: "raw_capture_unavailable", DimensionStatus: "raw_capture_unavailable", AdmissionFence: admission.Fence, Quality: "unavailable"}
		if input.Provenance != nil {
			row.Quality = input.Provenance.Quality
			row.QualityReason = input.Provenance.Reason
		}
		out.Rows = append(out.Rows, row)
	}
	for i := range out.Rows {
		row := &out.Rows[i]
		if row.Quote == nil {
			continue
		}
		session, err := inspectOutcomeQuoteSession(ctx, feed, d.Command.RegistrationID, *row)
		if err != nil {
			return PaperOutcomeMarketEvidence{}, err
		}
		row.SessionEvidence = &session
	}
	if r.Evaluation.Status == "observed" && selected != 1 {
		return PaperOutcomeMarketEvidence{}, storage.ErrPaperOutcomeConflict
	}
	slices.SortFunc(out.Rows, func(a, b PaperOutcomeMarketRow) int {
		if a.AvailableAt.IsZero() != b.AvailableAt.IsZero() {
			if a.AvailableAt.IsZero() {
				return 1
			}
			return -1
		}
		if a.AvailableAt.IsZero() {
			if c := a.AdmittedAt.Compare(b.AdmittedAt); c != 0 {
				return c
			}
		}

		if c := a.AvailableAt.Compare(b.AvailableAt); c != 0 {
			return c
		}
		if c := a.OccurredAt.Compare(b.OccurredAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.InputID, b.InputID); c != 0 {
			return c
		}
		return strings.Compare(a.CaptureKey, b.CaptureKey)
	})
	out.ShareQuantitiesAvailable = len(out.Rows) > 0
	for _, row := range out.Rows {
		if row.Quote == nil || !row.Quote.ShareQuantitiesAvailable {
			out.ShareQuantitiesAvailable = false
		}
	}
	if len(out.Rows) > 0 {
		out.Status = "retained_quote_observations"
	}
	if d.Evaluation.Action != "entry_candidate" {
		out.Status = "not_applicable_abstention"
	}
	if err := ctx.Err(); err != nil {
		return PaperOutcomeMarketEvidence{}, err
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
func paperOutcomeMarketRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.PaperOutcomeSourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-outcomes/{id}/market-evidence", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid outcome market evidence query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		source, err := backend.PaperOutcomeSources(ctx, id, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrPaperOutcomeNotFound) {
			write(w, 404, map[string]string{"error": "paper outcome not found"})
			return
		}
		if err != nil {
			write(w, 503, map[string]string{"error": "owned outcome market evidence unavailable"})
			return
		}
		out, err := inspectPaperOutcomeMarket(ctx, source)
		if err != nil {
			write(w, 503, map[string]string{"error": "owned outcome market evidence unavailable"})
			return
		}
		write(w, 200, out)
	})
}
