package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func trialRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.TrialStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/hypothesis-trials", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			trialError(w, trial.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, trial.MaxBytes)
		defer r.Body.Close()
		command, err := trial.Read(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		registered, err := backend.RegisterTrial(ctx, command, clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, registered)
	})
	mux.HandleFunc("GET /v1/hypothesis-trials/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		registered, err := backend.Trial(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, registered)
	})
	mux.HandleFunc("GET /v1/hypotheses/{id}/trials", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !hypothesis.QuestionID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		history, err := backend.HypothesisTrials(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, history)
	})
}
func trialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, trial.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, trial.ErrConflict), errors.Is(err, trial.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, trial.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		hypothesisError(w, err)
	}
}
