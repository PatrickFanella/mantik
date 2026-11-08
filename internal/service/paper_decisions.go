package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func evaluateOwnedPaperDecision(c storage.PaperDecisionCommand, b storage.PaperDecisionBasis, at time.Time) (storage.PaperDecisionEvaluation, error) {
	d := b.Strategy.Command.Definition
	e, ok := b.Feed.Collectors.Entries[c.RegistrationID]
	if !ok || e.Registration.Symbol != d.Symbol || e.Registration.Feed != d.Feed || e.Registration.EvidenceClass != d.InputEvidenceClass || at.Before(b.Feed.Collectors.LastClock) {
		return storage.PaperDecisionEvaluation{}, storage.ErrPaperDecisionConflict
	}
	report, err := inspectFeedCoverage(b.Feed, c.RegistrationID, at)
	if err != nil {
		return storage.PaperDecisionEvaluation{}, err
	}
	out := storage.PaperDecisionEvaluation{Action: "abstain", ReasonCode: "collector_unavailable", Coverage: report.Evidence}
	if report.LeaseStatus != "lease_current" {
		return out, nil
	}
	if report.Evidence.Session.Status != "open" {
		out.ReasonCode = report.Evidence.Session.Code
		return out, nil
	}
	if !report.OperationalEvidenceSatisfied || !report.Evidence.Quote.Usable || !report.Evidence.Quote.RegularSessionEligible {
		out.ReasonCode = report.Evidence.Quote.Code
		return out, nil
	}
	input, ok := b.Feed.Routing.Inputs[report.Evidence.Quote.InputID]
	if !ok || input.Input.Kind != "quote" {
		return out, storage.ErrPaperDecisionInvalid
	}
	out.PriceCents = input.Input.PriceCents
	if at.Sub(input.Input.OccurredAt) > time.Duration(d.Safety.MaxQuoteAgeSeconds)*time.Second {
		out.ReasonCode = "strategy_quote_stale"
		return out, nil
	}
	out.Signal, err = paperstrategy.Signal(d, out.PriceCents)
	if err != nil {
		return out, err
	}
	if !out.Signal {
		out.ReasonCode = "threshold_not_met"
		return out, nil
	}
	if out.PriceCents <= d.Safety.StopCents {
		out.ReasonCode = "price_at_or_below_stop"
		return out, nil
	}
	out.Action = "entry_candidate"
	out.ReasonCode = "native_threshold_met_admission_required"
	return out, nil
}
func paperDecisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPaperDecisionInvalid), errors.Is(err, paperstrategy.ErrInvalid):
		write(w, 422, map[string]string{"error": "invalid owned paper decision"})
	case errors.Is(err, storage.ErrPaperDecisionConflict), errors.Is(err, storage.ErrPaperDecisionCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrPaperDecisionNotFound), errors.Is(err, paperstrategy.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "owned paper decision storage unavailable"})
	}
}
func paperDecisionRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperDecisionStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-decisions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperDecisionError(w, storage.ErrPaperDecisionInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.PaperDecisionCommand
		// The decision worker's identities are reserved.
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || c.Validate() != nil || reservedPaperDecisionID(c.ID) {
			paperDecisionError(w, storage.ErrPaperDecisionInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := backend.FinalizePaperDecision(ctx, c, clock.Now, evaluateOwnedPaperDecision)
		if err != nil {
			paperDecisionError(w, err)
			return
		}
		code := 201
		if v.Duplicate {
			code = 200
		}
		write(w, code, v)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperDecisionError(w, storage.ErrPaperDecisionInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.PaperDecisions(ctx, evaluateOwnedPaperDecision)
		if err != nil {
			paperDecisionError(w, err)
			return
		}
		id := r.PathValue("id")
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, v := range all.Records {
			if v.Command.ID == id {
				if r.Pattern == "GET /v1/paper-decisions/{id}/explanation" {
					explanation, err := explainPaperDecision(v)
					if err != nil {
						paperDecisionError(w, err)
						return
					}
					write(w, http.StatusOK, explanation)
					return
				}
				write(w, 200, v)
				return
			}
		}
		paperDecisionError(w, storage.ErrPaperDecisionNotFound)
	}
	mux.HandleFunc("GET /v1/paper-decisions", read)
	mux.HandleFunc("GET /v1/paper-decisions/{id}", read)
	mux.HandleFunc("GET /v1/paper-decisions/{id}/explanation", read)
}
