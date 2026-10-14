package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func routingRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.RoutingStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/triggers", func(w http.ResponseWriter, r *http.Request) {
		var definition domain.TriggerDefinition
		if !routingBody(w, r, &definition) {
			return
		}
		if err := routing.ValidateOperatorDefinition(definition); err != nil {
			write(w, 422, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		stored, err := backend.RegisterTrigger(ctx, definition)
		if err != nil {
			routingError(w, err)
			return
		}
		write(w, 200, stored)
	})
	mux.HandleFunc("POST /v1/routing-inputs", func(w http.ResponseWriter, r *http.Request) {
		var input domain.RoutingInput
		if !routingBody(w, r, &input) {
			return
		}
		if err := routing.ValidateInput(input); err != nil {
			write(w, 422, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.RouteInput(ctx, input)
		if err != nil {
			routingError(w, err)
			return
		}
		write(w, 200, receipt)
	})
	mux.HandleFunc("POST /v1/routing-cases/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Owner string    `json:"owner"`
			At    time.Time `json:"at"`
		}
		if !routingBody(w, r, &request) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := backend.CloseRoutingCase(ctx, r.PathValue("id"), request.Owner, request.At); err != nil {
			routingError(w, err)
			return
		}
		write(w, 200, map[string]string{"status": "closed"})
	})
}

func routingBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		write(w, 400, map[string]string{"error": "invalid routing JSON"})
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		write(w, 400, map[string]string{"error": "expected one routing object"})
		return false
	}
	return true
}

func routingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, routing.ErrConflict):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, routing.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	case errors.Is(err, routing.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	default:
		failed(w, err)
	}
}
