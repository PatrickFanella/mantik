package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func feedCollectorRoutes(mux *http.ServeMux, store storage.Store, clock Clock, policy *FeedReadinessPolicy) {
	if submissions, ok := store.(storage.CollectorSubmissionStore); ok {
		mux.HandleFunc("POST /v1/collector-inputs", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				ReceiverID string              `json:"receiver_id"`
				Fence      feedcollector.Fence `json:"fence"`
				Input      domain.RoutingInput `json:"input"`
			}
			if !routingBody(w, r, &body) {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			receipt, err := submissions.SubmitCollectorResearch(ctx, body.ReceiverID, body.Fence, body.Input, clock.Now)
			if err == nil {
				write(w, 200, receipt)
				return
			}
			if errors.Is(err, storage.ErrReceiverMismatch) || errors.Is(err, feedcollector.ErrConflict) || errors.Is(err, feedcollector.ErrLeaseLost) || errors.Is(err, feedcollector.ErrClockBackwards) || errors.Is(err, feedcollector.ErrCapacity) {
				write(w, 409, map[string]string{"error": err.Error()})
				return
			}
			if errors.Is(err, feedcollector.ErrInvalid) {
				write(w, 422, map[string]string{"error": err.Error()})
				return
			}
			if errors.Is(err, feedcollector.ErrNotFound) {
				write(w, 404, map[string]string{"error": err.Error()})
				return
			}
			routingError(w, err)
		})
	}
	backend, ok := store.(storage.FeedCollectorStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/feed-collectors", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		snapshot, err := backend.FeedCollectors(ctx)
		if err != nil {
			failed(w, err)
			return
		}
		at := clock.Now().UTC()
		statuses := map[string]string{}
		for id, entry := range snapshot.State.Entries {
			statuses[id] = feedcollector.LeaseStatus(entry, snapshot.State.LastClock, at)
		}
		write(w, 200, struct {
			storage.FeedCollectorSnapshot
			CheckedAt                 time.Time         `json:"checked_at"`
			LeaseStatus               map[string]string `json:"lease_status"`
			ProcessLivenessVerified   bool              `json:"process_liveness_verified"`
			GlobalReadinessConfigured bool              `json:"global_readiness_configured"`
			QualificationCredit       bool              `json:"qualification_credit"`
		}{FeedCollectorSnapshot: snapshot, CheckedAt: at, LeaseStatus: statuses, GlobalReadinessConfigured: policy != nil})
	})
	mux.HandleFunc("POST /v1/feed-collectors", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string                `json:"receiver_id"`
			Command    feedcollector.Command `json:"command"`
		}
		if !routingBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.FeedCollectorCommand(ctx, body.ReceiverID, body.Command, clock.Now)
		switch {
		case err == nil:
			write(w, 200, receipt)
		case errors.Is(err, storage.ErrReceiverMismatch), errors.Is(err, feedcollector.ErrConflict), errors.Is(err, feedcollector.ErrLeaseLost), errors.Is(err, feedcollector.ErrClockBackwards), errors.Is(err, feedcollector.ErrCapacity):
			write(w, 409, map[string]string{"error": err.Error()})
		case errors.Is(err, feedcollector.ErrInvalid):
			write(w, 422, map[string]string{"error": err.Error()})
		case errors.Is(err, feedcollector.ErrNotFound):
			write(w, 404, map[string]string{"error": err.Error()})
		default:
			failed(w, err)
		}
	})
}
