// Package service exposes the same operations to CLI clients and future tools.
package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func Handler(store storage.Store, token string) http.Handler {
	return HandlerWithClock(store, token, WallClock{})
}

// HandlerWithClock uses an explicit operational clock for readiness and recovery.
func HandlerWithClock(store storage.Store, token string, clock Clock) http.Handler {
	return handlerWithPolicy(store, token, clock, nil)
}

func handlerWithPolicy(store storage.Store, token string, clock Clock, policy *FeedReadinessPolicy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/feed-readiness-policy", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, struct {
			Configured                bool                 `json:"configured"`
			Policy                    *FeedReadinessPolicy `json:"policy"`
			CollectorLivenessVerified bool                 `json:"collector_liveness_verified"`
			QualificationCredit       bool                 `json:"qualification_credit"`
		}{Configured: policy != nil, Policy: policy})
	})
	libraryRoutes(mux)
	newsRoutes(mux, store, clock)
	reviewRoutes(mux, store, clock)
	hypothesisRoutes(mux, store, clock)
	trialRoutes(mux, store, clock)
	trialAttemptRoutes(mux, store, clock)
	trialRunRoutes(mux, store, clock)
	trialAssessmentRoutes(mux, store)
	exceptionRoutes(mux, store)
	caseRoutes(mux, store)
	activityRoutes(mux, store)
	routingRoutes(mux, store)
	researchRoutes(mux, store)
	feedCollectorRoutes(mux, store, clock, policy)
	feedEvidenceRoutes(mux, store, clock, policy)
	operationalRoutes(mux, store, clock, policy)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"status": "up", "mode": "paper_fixture"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		if scope == "" {
			scope = "storage"
		}
		readinessResponse(w, r, store, clock, policy, scope, true)
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			failed(w, err)
			return
		}
		write(w, 200, snapshot)
	})
	mux.HandleFunc("POST /v1/observations", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		defer r.Body.Close()
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var observation domain.Observation
		if err := decoder.Decode(&observation); err != nil {
			write(w, 400, map[string]string{"error": "invalid observation JSON"})
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			write(w, 400, map[string]string{"error": "expected one JSON object"})
			return
		}
		if err := workflow.Validate(observation); err != nil {
			write(w, 422, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := store.Ingest(ctx, observation)
		if errors.Is(err, storage.ErrConflict) {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			failed(w, err)
			return
		}
		status := http.StatusCreated
		if receipt.Duplicate {
			status = http.StatusOK
		}
		write(w, status, receipt)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.URL.Path != "/healthz" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			write(w, 401, map[string]string{"error": "authentication required"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func failed(w http.ResponseWriter, err error) {
	slog.Error("storage operation failed", "error", err)
	write(w, 503, map[string]string{"error": "storage operation failed"})
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
