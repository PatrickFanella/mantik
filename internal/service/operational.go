package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func readinessResponse(w http.ResponseWriter, r *http.Request, store storage.Store, clock Clock, policy *FeedReadinessPolicy, scope string, enforce bool) {
	if !slices.Contains(operational.Scopes, scope) {
		write(w, 400, map[string]string{"error": "unknown readiness scope"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := store.Snapshot(ctx)
	at := clock.Now()
	report := operational.Readiness(snapshot.State, at, err == nil, scope)
	applyFeedReadiness(ctx, store, at, policy, err == nil, &report)
	status := 200
	if enforce && !report.Ready {
		status = 503
	}
	write(w, status, report)
}

func operationalRoutes(mux *http.ServeMux, store storage.Store, clock Clock, policy *FeedReadinessPolicy) {
	mux.HandleFunc("GET /v1/readiness/{scope}", func(w http.ResponseWriter, r *http.Request) {
		readinessResponse(w, r, store, clock, policy, r.PathValue("scope"), false)
	})
	mux.HandleFunc("GET /v1/operations", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			failed(w, err)
			return
		}
		at := clock.Now()
		report := operational.Inspect(snapshot.State, at)
		applyFeedReadiness(ctx, store, at, policy, true, &report.Readiness)
		write(w, 200, report)
	})
	backend, ok := store.(storage.OperationalStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/operations/exceptions/{id}/recover", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID string `json:"request_id"`
			Operation string `json:"operation"`
		}
		if !routingBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.RecoverException(ctx, r.PathValue("id"), body.RequestID, body.Operation, clock.Now())
		if err != nil {
			switch {
			case errors.Is(err, storage.ErrExceptionNotFound):
				write(w, 404, map[string]string{"error": err.Error()})
			case errors.Is(err, storage.ErrRecoveryConflict), errors.Is(err, storage.ErrRecoveryLimit):
				write(w, 409, map[string]string{"error": err.Error()})
			case errors.Is(err, storage.ErrRecoveryInvalid):
				write(w, 422, map[string]string{"error": err.Error()})
			default:
				failed(w, err)
			}
			return
		}
		write(w, 200, receipt)
	})
}
