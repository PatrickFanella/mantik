package service

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"slices"
	"time"
)

// This is a decision-level forward quote observation, not a fill, PnL, target
// hit or admission. The horizon and observation window are compiled policy:
// strategy max holding, followed by min(60s, strategy max quote age).
func evaluateOwnedPaperOutcome(b storage.PaperOutcomeBasis, at time.Time) (storage.PaperOutcomeEvaluation, error) {
	d := b.Decision
	basis := b.Observation
	def := basis.Strategy.Command.Definition
	out := storage.PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "decision_abstained"}
	if d.Evaluation.Action != "entry_candidate" {
		return out, nil
	}
	if d.Evaluation.PriceCents <= 0 || def.Validate() != nil {
		return out, storage.ErrPaperOutcomeInvalid
	}
	out.EntryPriceCents = d.Evaluation.PriceCents
	out.DueAt = d.FinalizedAt.Add(time.Duration(def.Safety.MaxHoldingSeconds) * time.Second)
	out.WindowEnd = out.DueAt.Add(time.Duration(min(int64(60), def.Safety.MaxQuoteAgeSeconds)) * time.Second)
	if out.WindowEnd.UTC().Year() > 9999 {
		return out, storage.ErrPaperOutcomeInvalid
	}
	if at.Before(out.DueAt) {
		out.Status = "waiting_horizon"
		out.ReasonCode = "forward_horizon_not_due"
		return out, nil
	}
	out.Status = "waiting_price"
	out.ReasonCode = "eligible_forward_quote_unavailable"
	if at.After(out.WindowEnd) {
		out.Status = "price_unavailable"
	}
	// Validate every retained recording, even if it cannot become the outcome.
	// Availability is the server publication timestamp, not a caller capture time.
	times := []time.Time{}
	for key, r := range basis.Feed.Records {
		var recording alpaca.Recording
		if json.Unmarshal(r.Recording, &recording) != nil || key != r.RegistrationID+":"+r.Key || r.Key != recordingKey(recording) || r.Fence.RegistrationID != d.Command.RegistrationID || feedcollector.ValidateFence(r.Fence) != nil || r.RegistrationID != d.Command.RegistrationID || r.PublishedAt.IsZero() || r.PublishedAt.Before(recording.ReceivedAt) {
			return out, storage.ErrPaperOutcomeInvalid
		}
		if err := validateFeedRecording(basis.Feed, d.Command.RegistrationID, recording); err != nil {
			return out, err
		}
		if !r.PublishedAt.Before(out.DueAt) && !r.PublishedAt.After(out.WindowEnd) && !r.PublishedAt.After(at) {
			times = append(times, r.PublishedAt)
		}
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	times = slices.CompactFunc(times, func(a, b time.Time) bool { return a.Equal(b) })
	for _, available := range times {
		prefix := basis.Feed
		prefix.Records = map[string]storage.FeedEvidenceRecord{}
		prefix.Collectors = basis.Feed.Collectors
		prefix.Collectors.Admissions = map[string]feedcollector.Admission{}
		prefix.Routing = &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{}}
		for id, a := range basis.Feed.Collectors.Admissions {
			if !a.At.After(available) {
				prefix.Collectors.Admissions[id] = a
				prefix.Routing.Inputs[id] = basis.Feed.Routing.Inputs[id]
			}
		}
		for key, r := range basis.Feed.Records {
			if !r.PublishedAt.After(available) {
				prefix.Records[key] = r
			}
		}
		report, err := inspectFeedCoverage(prefix, d.Command.RegistrationID, available)
		if err != nil {
			return out, err
		}
		q := report.Evidence.Quote
		if report.Evidence.Session.Status != "open" || !q.Usable || !q.RegularSessionEligible || q.OccurredAt.Before(out.DueAt) || q.OccurredAt.After(out.WindowEnd) || available.Sub(q.OccurredAt) > time.Duration(def.Safety.MaxQuoteAgeSeconds)*time.Second {
			continue
		}
		input, ok := prefix.Routing.Inputs[q.InputID]
		if !ok || input.Input.Kind != "quote" || input.Input.PriceCents <= 0 {
			return out, storage.ErrPaperOutcomeInvalid
		}
		// Pin the actual recording which carried the selected normalized input.
		for _, r := range prefix.Records {
			var recording alpaca.Recording
			if json.Unmarshal(r.Recording, &recording) != nil {
				return out, storage.ErrPaperOutcomeInvalid
			}
			normalized, err := alpaca.Normalize(recording)
			if err != nil {
				return out, err
			}
			for _, i := range normalized.Inputs {
				if i.ID == q.InputID {

					out.Status = "observed"
					out.ReasonCode = "timely_regular_session_forward_quote"
					out.ObservedPriceCents = input.Input.PriceCents
					out.PriceDeltaCents = out.ObservedPriceCents - out.EntryPriceCents
					out.InputID = q.InputID
					out.CaptureSHA256 = recording.SHA256
					out.OccurredAt = q.OccurredAt
					out.PublishedAt = r.PublishedAt
					out.AvailableAt = available
					return out, nil
				}
			}
		}
	}
	return out, nil
}
func paperOutcomeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPaperOutcomeInvalid), errors.Is(err, paperstrategy.ErrInvalid):
		write(w, 422, map[string]string{"error": "invalid owned paper outcome"})
	case errors.Is(err, storage.ErrPaperOutcomeConflict), errors.Is(err, storage.ErrPaperOutcomeCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrPaperOutcomeNotFound), errors.Is(err, paperstrategy.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "owned paper outcome storage unavailable"})
	}
}
func paperOutcomeRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperOutcomeStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperOutcomeError(w, storage.ErrPaperOutcomeInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.PaperOutcomeCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || c.Validate() != nil || reservedPaperOutcomeID(c.ID) {
			paperOutcomeError(w, storage.ErrPaperOutcomeInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := backend.AssessPaperOutcome(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperOutcomeError(w, err)
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
			paperOutcomeError(w, storage.ErrPaperOutcomeInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.PaperOutcomes(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperOutcomeError(w, err)
			return
		}
		id := r.PathValue("id")
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, v := range all.Records {
			if v.Command.ID == id {
				write(w, 200, v)
				return
			}
		}
		paperOutcomeError(w, storage.ErrPaperOutcomeNotFound)
	}
	mux.HandleFunc("GET /v1/paper-outcomes", read)
	mux.HandleFunc("GET /v1/paper-outcomes/{id}", read)
}
