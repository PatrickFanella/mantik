package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func hypothesisRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	if catalogue, ok := store.(storage.HypothesisCatalogueStore); ok {
		mux.HandleFunc("GET /v1/hypotheses", func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) != 0 {
				hypothesisError(w, hypothesis.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			c, err := catalogue.HypothesisCatalogue(ctx)
			if err == nil {
				err = c.Validate()
			}
			if err != nil {
				hypothesisError(w, err)
				return
			}
			write(w, 200, c)
		})
	}
	backend, ok := store.(storage.HypothesisStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/hypothesis-publications", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			hypothesisError(w, hypothesis.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, hypothesis.MaxBytes+4096)
		defer r.Body.Close()
		body, err := hypothesis.ReadPublicationCommand(r.Body)
		if err != nil {
			hypothesisError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		p, err := backend.PublishHypothesis(ctx, body.ReceiverID, body.RequestID, body.ReviewID, body.Declaration, clock.Now)
		if err != nil {
			hypothesisError(w, err)
			return
		}
		write(w, 200, p)
	})
	mux.HandleFunc("GET /v1/hypotheses/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !hypothesis.QuestionID.MatchString(r.PathValue("id")) {
			hypothesisError(w, hypothesis.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		q, err := backend.Hypothesis(ctx, r.PathValue("id"))
		if err != nil {
			hypothesisError(w, err)
			return
		}
		write(w, 200, q)
	})
}
func hypothesisError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hypothesis.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, hypothesis.ErrConflict), errors.Is(err, hypothesis.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, hypothesis.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		reviewError(w, err)
	}
}
