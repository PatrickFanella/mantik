package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func generatedCandidateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, generatedcandidate.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, generatedcandidate.ErrConflict):
		write(w, 409, map[string]string{"error": err.Error()})
	default:
		generationIntentError(w, err)
	}
}
func generatedCandidateRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.GeneratedCandidateStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/generated-candidates", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generatedCandidateError(w, generatedcandidate.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, generatedcandidate.MaxCommandBytes)
		defer r.Body.Close()
		c, err := generatedcandidate.ReadCommand(r.Body)
		if err != nil {
			generatedCandidateError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.RegisterGeneratedCandidate(ctx, c, clock.Now)
		if err != nil {
			generatedCandidateError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/generated-candidates", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generatedCandidateError(w, generatedcandidate.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.GeneratedCandidateRegistry(ctx)
		if err != nil {
			generatedCandidateError(w, err)
			return
		}
		write(w, 200, h)
	})
	mux.HandleFunc("GET /v1/generated-candidates/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || !review.ID.MatchString(r.PathValue("id")) {
			generatedCandidateError(w, generatedcandidate.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.GeneratedCandidateEvidence(ctx, r.PathValue("id"))
		if err != nil {
			generatedCandidateError(w, err)
			return
		}
		write(w, 200, a)
	})
}
