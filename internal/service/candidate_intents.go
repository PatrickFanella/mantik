package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func candidateIntentRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CandidateWorkerIntentStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/candidate-worker-intents", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			candidateIntentError(w, candidateattempt.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, candidateattempt.MaxBytes)
		defer r.Body.Close()
		c, err := candidateattempt.Read(r.Body)
		if err != nil {
			candidateIntentError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		i, err := backend.StartCandidateWorker(ctx, c, clock.Now)
		if err != nil {
			candidateIntentError(w, err)
			return
		}
		write(w, 200, i)
	})
	mux.HandleFunc("GET /v1/candidate-worker-intents", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			candidateIntentError(w, candidateattempt.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.CandidateWorkerIntentHistory(ctx)
		if err != nil {
			candidateIntentError(w, err)
			return
		}
		write(w, 200, h)
	})
	mux.HandleFunc("GET /v1/candidate-worker-intents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
			candidateIntentError(w, candidateattempt.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		i, err := backend.CandidateWorkerIntent(ctx, r.PathValue("id"))
		if err != nil {
			candidateIntentError(w, err)
			return
		}
		write(w, 200, i)
	})
}

func candidateIntentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, candidateattempt.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, candidateattempt.ErrConflict), errors.Is(err, candidateattempt.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, candidateattempt.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		candidateError(w, err)
	}
}
