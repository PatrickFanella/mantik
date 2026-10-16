package service

import (
	"context"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func externalJobRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	externalAttemptRoutes(mux, store, clock)
	backend, ok := store.(storage.ExternalJobStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/external-trial-jobs", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			trialError(w, trial.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		c, err := workertrial.Read(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		registered, err := backend.DeclareExternalJob(ctx, c, clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, registered)
	})
	mux.HandleFunc("GET /v1/external-trial-jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		registered, err := backend.ExternalJob(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, registered)
	})
	mux.HandleFunc("GET /v1/external-trial-jobs/{id}/inputs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		name := r.PathValue("name")
		if name != "dataset.json" && name != "recipe.json" && name != "native.json" {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		input, err := backend.ExternalJobInput(ctx, r.PathValue("id"), name)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, input)
	})
}
func externalJobArguments(w http.ResponseWriter, r *http.Request) bool {
	if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
		trialError(w, trial.ErrInvalid)
		return false
	}
	return true
}
