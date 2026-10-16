package service

import (
	"context"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func externalAttemptRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	b, ok := store.(storage.ExternalAttemptStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/external-trial-attempts", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			trialError(w, trial.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		c, err := workertrial.ReadAttempt(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		i, err := b.StartExternalAttempt(ctx, c, clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, i)
	})
	mux.HandleFunc("POST /v1/external-trial-attempts/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, workertrial.MaxReportBytes)
		defer r.Body.Close()
		c, err := workertrial.ReadReport(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		if c.AttemptID != r.PathValue("id") {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		o, err := b.ReportExternalAttempt(ctx, c, clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, o)
	})
	mux.HandleFunc("GET /v1/external-trial-attempts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := b.ExternalAttempt(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/external-trial-jobs/{id}/attempts", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := b.ExternalAttempts(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, all)
	})
	mux.HandleFunc("GET /v1/external-trial-attempts/{id}/outputs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !externalJobArguments(w, r) {
			return
		}
		name := r.PathValue("name")
		if name != "stdout" && name != "stderr" {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := b.ExternalAttemptOutput(ctx, r.PathValue("id"), name)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, a)
	})
}
