package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type PaperDecisionMarketEvidence struct {
	SessionEvidence     *PaperOutcomeSessionEvidence `json:"session_evidence,omitempty"`
	Schema              string                       `json:"schema"`
	ReceiverID          string                       `json:"receiver_id"`
	DecisionID          string                       `json:"decision_id"`
	DecisionSHA256      string                       `json:"decision_sha256"`
	BasisSHA256         string                       `json:"basis_sha256"`
	FinalizedAt         time.Time                    `json:"finalized_at"`
	Provider            string                       `json:"provider"`
	Feed                string                       `json:"feed"`
	RegistrationID      string                       `json:"registration_id"`
	Status              string                       `json:"status"`
	Quote               *alpaca.QuoteDimensions      `json:"quote"`
	CaptureKey          string                       `json:"capture_key"`
	PublishedAt         time.Time                    `json:"published_at"`
	AdmittedAt          time.Time                    `json:"admitted_at"`
	PublicationFence    feedcollector.Fence          `json:"publication_fence"`
	AdmissionFence      feedcollector.Fence          `json:"admission_fence"`
	Authority           string                       `json:"authority"`
	AdmissionAvailable  bool                         `json:"admission_available"`
	ExecutionAvailable  bool                         `json:"execution_available"`
	QualificationCredit bool                         `json:"qualification_credit"`
	SHA256              string                       `json:"sha256"`
}

// s was revalidated against the complete current owner catalogue in one storage
// snapshot. This inspector uses its immutable historical clock, never wall time.
func inspectPaperDecisionMarket(ctx context.Context, s storage.PaperDecisionSources) (PaperDecisionMarketEvidence, error) {
	if err := ctx.Err(); err != nil {
		return PaperDecisionMarketEvidence{}, err
	}
	d, b := s.Decision, s.Basis
	if d.Validate() != nil || d.BasisSHA256 != workerplan.Hash(b) || d.Command.ReceiverID != b.Feed.ReceiverID {
		return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionInvalid
	}
	e, ok := b.Feed.Collectors.Entries[d.Command.RegistrationID]
	if !ok || e.Registration.Provider != "alpaca" {
		return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionConflict
	}
	out := PaperDecisionMarketEvidence{Schema: "mantik-owned-decision-market-evidence-v2", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, FinalizedAt: d.FinalizedAt, Provider: e.Registration.Provider, Feed: e.Registration.Feed, RegistrationID: d.Command.RegistrationID, Status: "not_applicable_abstention", Authority: "service_owned_recorded_market_dimensions_only"}
	if d.Evaluation.Action == "entry_candidate" {
		id := d.Evaluation.Coverage.Quote.InputID
		if b.Feed.Routing == nil {
			return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionConflict
		}
		input, ok := b.Feed.Routing.Inputs[id]
		admission, admitted := b.Feed.Collectors.Admissions[id]
		if !ok || !admitted || input.Input.Kind != "quote" || input.Input.PriceCents != d.Evaluation.PriceCents || admission.At.After(d.FinalizedAt) {
			return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionConflict
		}
		matches := 0
		for key, published := range b.Feed.Records {
			if err := ctx.Err(); err != nil {
				return PaperDecisionMarketEvidence{}, err
			}
			var r alpaca.Recording
			if json.Unmarshal(published.Recording, &r) != nil || key != published.RegistrationID+":"+published.Key || published.Key != recordingKey(r) || published.RegistrationID != d.Command.RegistrationID || published.Fence.RegistrationID != d.Command.RegistrationID || feedcollector.ValidateFence(published.Fence) != nil || published.PublishedAt.IsZero() || published.PublishedAt.Before(r.ReceivedAt) || published.PublishedAt.After(d.FinalizedAt) {
				return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionConflict
			}
			if err := validateFeedRecording(b.Feed, d.Command.RegistrationID, r); err != nil {
				return PaperDecisionMarketEvidence{}, err
			}
			normalized, err := alpaca.Normalize(r)
			if err != nil {
				return PaperDecisionMarketEvidence{}, err
			}
			for _, i := range normalized.Inputs {
				if i.ID != id {
					continue
				}
				dimensions, err := alpaca.InspectQuoteDimensions(r, id)
				if err != nil {
					return PaperDecisionMarketEvidence{}, err
				}
				matches++
				out.Quote = &dimensions
				out.CaptureKey, out.PublishedAt, out.AdmittedAt = key, published.PublishedAt, admission.At
				out.PublicationFence, out.AdmissionFence = published.Fence, admission.Fence
			}
		}
		if matches != 1 {
			return PaperDecisionMarketEvidence{}, storage.ErrPaperDecisionConflict
		}
		availableAt := out.PublishedAt
		if out.AdmittedAt.After(availableAt) {
			availableAt = out.AdmittedAt
		}
		session, err := inspectQuoteSessionAt(ctx, b.Feed, d.Command.RegistrationID, PaperOutcomeMarketRow{Quote: out.Quote, InputID: out.Quote.InputID, CaptureKey: out.CaptureKey, AvailableAt: availableAt}, d.FinalizedAt)
		if err != nil {
			return PaperDecisionMarketEvidence{}, err
		}
		session.Method = "owned-decision-quote-session-at-finalization-v1"
		session.Evidence.Scope = "service_owned_decision_quote_session_at_finalization"
		out.SessionEvidence = &session
		out.Status = "dimensions_retained_share_units_unverified"
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

func paperDecisionMarketRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.PaperDecisionSourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-decisions/{id}/market-evidence", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid decision market evidence query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		sources, err := backend.PaperDecisionSources(ctx, id, evaluateOwnedPaperDecision)
		if errors.Is(err, storage.ErrPaperDecisionNotFound) {
			write(w, 404, map[string]string{"error": "paper decision not found"})
			return
		}
		if err != nil {
			write(w, 503, map[string]string{"error": "owned decision market evidence unavailable"})
			return
		}
		out, err := inspectPaperDecisionMarket(ctx, sources)
		if err != nil || ctx.Err() != nil {
			write(w, 503, map[string]string{"error": "owned decision market evidence unavailable"})
			return
		}
		write(w, 200, out)
	})
}
