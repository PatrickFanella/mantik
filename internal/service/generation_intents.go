package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func generationIntentRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CandidateGenerationIntentStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/candidate-generation-intents", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generationIntentError(w, candidategeneration.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, candidategeneration.MaxSubmissionBytes)
		defer r.Body.Close()
		s, err := candidategeneration.ReadSubmission(r.Body)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		i, err := backend.StartCandidateGeneration(ctx, s.Command, s.Dataset, clock.Now)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		write(w, 200, i)
	})
	mux.HandleFunc("GET /v1/candidate-generation-intents", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generationIntentError(w, candidategeneration.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.CandidateGenerationIntentHistory(ctx)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		write(w, 200, h)
	})
	for _, artifact := range []bool{false, true} {
		path := "GET /v1/candidate-generation-intents/{id}"
		if artifact {
			path += "/dataset"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || !review.ID.MatchString(r.PathValue("id")) {
				generationIntentError(w, candidategeneration.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if artifact {
				a, err := backend.CandidateGenerationIntentEvidence(ctx, r.PathValue("id"))
				if err != nil {
					generationIntentError(w, err)
					return
				}
				write(w, 200, a)
			} else {
				i, err := backend.CandidateGenerationIntent(ctx, r.PathValue("id"))
				if err != nil {
					generationIntentError(w, err)
					return
				}
				write(w, 200, i)
			}
		})
	}
}
func generationIntentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, candidategeneration.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, candidategeneration.ErrConflict), errors.Is(err, candidategeneration.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, candidategeneration.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		candidateError(w, err)
	}
}
