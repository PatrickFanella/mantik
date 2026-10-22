package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func decisionReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrDecisionReviewInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrDecisionReviewConflict), errors.Is(err, storage.ErrDecisionReviewCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrDecisionReviewNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "decision review storage unavailable"})
	}
}
func decisionReviewRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.DecisionReviewStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-decision-reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			decisionReviewError(w, storage.ErrDecisionReviewInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, storage.MaxDecisionReviewCommandBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.DecisionReviewCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, storage.MaxDecisionReviewCommandBytes) != nil || c.Validate() != nil {
			decisionReviewError(w, storage.ErrDecisionReviewInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := backend.ReviewPaperDecision(ctx, c, clock.Now, evaluateOwnedPaperDecision)
		if err != nil {
			decisionReviewError(w, err)
			return
		}
		status := 201
		if result.Duplicate {
			status = 200
		}
		write(w, status, result)
	})
	read := func(w http.ResponseWriter, r *http.Request, cases bool) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			decisionReviewError(w, storage.ErrDecisionReviewInvalid)
			return
		}
		id := r.PathValue("id")
		if id != "" && !paperaccount.ID.MatchString(id) {
			decisionReviewError(w, storage.ErrDecisionReviewInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.DecisionReviews(ctx, evaluateOwnedPaperDecision)
		if err != nil {
			decisionReviewError(w, err)
			return
		}
		if id == "" {
			if cases {
				write(w, 200, struct {
					ReceiverID string                       `json:"receiver_id"`
					Cases      []storage.DecisionReviewCase `json:"cases"`
				}{all.ReceiverID, all.Cases})
			} else {
				write(w, 200, all)
			}
			return
		}
		if cases {
			for _, c := range all.Cases {
				if c.ID == id {
					write(w, 200, c)
					return
				}
			}
		} else {
			for _, v := range all.Records {
				if v.Command.ID == id {
					write(w, 200, v)
					return
				}
			}
		}
		decisionReviewError(w, storage.ErrDecisionReviewNotFound)
	}
	mux.HandleFunc("GET /v1/paper-decision-reviews", func(w http.ResponseWriter, r *http.Request) { read(w, r, false) })
	mux.HandleFunc("GET /v1/paper-decision-reviews/{id}", func(w http.ResponseWriter, r *http.Request) { read(w, r, false) })
	mux.HandleFunc("GET /v1/decision-review-cases", func(w http.ResponseWriter, r *http.Request) { read(w, r, true) })
	mux.HandleFunc("GET /v1/decision-review-cases/{id}", func(w http.ResponseWriter, r *http.Request) { read(w, r, true) })
}
