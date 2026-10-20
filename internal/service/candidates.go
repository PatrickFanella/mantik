package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func candidateRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CandidateStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/candidates", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			candidateError(w, candidatelineage.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, candidatelineage.MaxSubmissionBytes)
		defer r.Body.Close()
		s, err := candidatelineage.ReadSubmission(r.Body)
		if err != nil {
			candidateError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		registered, err := backend.RegisterCandidate(ctx, s.Command, s.Dataset, clock.Now)
		if err != nil {
			candidateError(w, err)
			return
		}
		write(w, 200, registered)
	})
	mux.HandleFunc("GET /v1/candidates", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			candidateError(w, candidatelineage.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		history, err := backend.CandidateRegistry(ctx)
		if err != nil {
			candidateError(w, err)
			return
		}
		write(w, 200, history)
	})
	for _, dataset := range []bool{false, true} {
		path := "GET /v1/candidates/{id}"
		if dataset {
			path += "/dataset"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
				candidateError(w, candidatelineage.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if dataset {
				artifact, err := backend.CandidateEvidence(ctx, r.PathValue("id"))
				if err != nil {
					candidateError(w, err)
					return
				}
				write(w, 200, artifact)
			} else {
				registered, err := backend.CandidateRegistration(ctx, r.PathValue("id"))
				if err != nil {
					candidateError(w, err)
					return
				}
				write(w, 200, registered)
			}
		})
	}
}

func candidateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, candidatelineage.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, candidatelineage.ErrConflict), errors.Is(err, candidatelineage.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, candidatelineage.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		trialError(w, err)
	}
}
