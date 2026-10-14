package service

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func researchRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.ResearchReceiverStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/research-receiver", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		ids := r.URL.Query()["input_id"]
		if len(ids) > 16 {
			write(w, 422, map[string]string{"error": "research readback accepts at most 16 input IDs"})
			return
		}
		pattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
		for _, id := range ids {
			if !pattern.MatchString(id) {
				write(w, 422, map[string]string{"error": "invalid research input ID"})
				return
			}
		}
		snapshot, err := backend.ResearchSnapshot(ctx)
		if err != nil {
			failed(w, err)
			return
		}
		selected := map[string]domain.RetainedRoutingInput{}
		for _, id := range ids {
			if input, ok := snapshot.Inputs[id]; ok {
				selected[id] = input
			}
		}
		snapshot.Inputs = selected
		write(w, 200, snapshot)
	})
	mux.HandleFunc("POST /v1/research-inputs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string              `json:"receiver_id"`
			Input      domain.RoutingInput `json:"input"`
		}
		if !routingBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.SubmitResearch(ctx, body.ReceiverID, body.Input)
		if errors.Is(err, storage.ErrReceiverMismatch) {
			write(w, 409, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			routingError(w, err)
			return
		}
		write(w, 200, receipt)
	})
}
