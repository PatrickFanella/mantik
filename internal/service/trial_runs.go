package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func trialRunRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.TrialRunStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/trial-attempts/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		receiver, err := trial.ReadAttemptCancellation(r.Body)
		if err != nil {
			trialError(w, err)
			return
		}
		// The adapter owns input selection. HTTP never supplies data, code or results.
		dataset, err := replay.FixtureDataset()
		if err != nil {
			failed(w, err)
			return
		}
		input, err := json.Marshal(dataset)
		if err != nil {
			failed(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), trial.RunSeconds*time.Second)
		defer cancel()
		work, err := backend.BeginTrialRun(ctx, receiver, r.PathValue("id"), string(input), rand.Text(), clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		if !work.Acquired {
			write(w, 200, work.Run)
			return
		}
		// Acquisition/input committed before this independent memory replay. No DB
		// transaction, provider adapter or operational service state is involved.
		retained, err := replay.Decode(bytes.NewBufferString(work.Input))
		if err != nil {
			failed(w, err)
			return
		}
		result, err := experiment.Execute(ctx, work.Run.NativeAttemptID, work.Registration.Recipe, retained)
		if err != nil {
			failed(w, err)
			return
		}
		raw, err := json.Marshal(result)
		if err != nil {
			failed(w, err)
			return
		}
		completed, err := backend.FinishTrialRun(ctx, receiver, work.Run.AttemptID, work.Run.InvocationID, string(raw), clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, completed)
	})
	mux.HandleFunc("POST /v1/trial-attempts/{id}/run/resolve", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
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
		run, err := backend.ResolveTrialRun(ctx, receiver, r.PathValue("id"), clock.Now)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, run)
	})
	mux.HandleFunc("GET /v1/trial-attempts/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		run, err := backend.TrialRun(ctx, r.PathValue("id"))
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, run)
	})
	mux.HandleFunc("GET /v1/trial-attempts/{id}/run/{artifact}", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
			return
		}
		kind := r.PathValue("artifact")
		if kind != "input" && kind != "result" {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		artifact, err := backend.TrialRunArtifact(ctx, r.PathValue("id"), kind)
		if err != nil {
			trialError(w, err)
			return
		}
		write(w, 200, artifact)
	})
}
func runArguments(w http.ResponseWriter, r *http.Request) bool {
	if len(r.URL.Query()) != 0 || !review.ID.MatchString(r.PathValue("id")) {
		trialError(w, trial.ErrInvalid)
		return false
	}
	return true
}
