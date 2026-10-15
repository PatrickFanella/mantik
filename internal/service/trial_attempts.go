package service

import (
	"context"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func trialAttemptRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.TrialAttemptStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/trial-attempts", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			trialError(w, trial.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		c, err := trial.ReadAttemptCommand(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.StartTrialAttempt(ctx, c, clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("POST /v1/trial-attempts/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		receiver, err := trial.ReadAttemptCancellation(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.CancelTrialAttempt(ctx, receiver, r.PathValue("id"), clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/trial-attempts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.TrialAttempt(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/hypothesis-trials/{id}/attempts", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.TrialAttempts(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, h)
	})
}
