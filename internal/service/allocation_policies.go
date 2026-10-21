package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func allocationPolicyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, allocation.ErrInvalid), errors.Is(err, paperaccount.ErrInvalid):
		write(w, 422, map[string]string{"error": "invalid allocation policy"})
	case errors.Is(err, allocation.ErrPolicyConflict), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, allocation.ErrPolicyNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "allocation policy storage unavailable"})
	}
}
func allocationPolicyRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.AllocationPolicyStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/allocation-policies", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			allocationPolicyError(w, allocation.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, allocation.MaxPolicyRecordBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var cmd allocation.PolicyCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &cmd, allocation.MaxPolicyRecordBytes) != nil || cmd.Validate() != nil {
			allocationPolicyError(w, allocation.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.RegisterAllocationPolicy(ctx, cmd, clock.Now)
		if err != nil {
			allocationPolicyError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.RawQuery != "" || r.URL.ForceQuery || id != "" && !paperaccount.ID.MatchString(id) {
			allocationPolicyError(w, allocation.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		c, err := backend.AllocationPolicies(ctx)
		if err != nil {
			allocationPolicyError(w, err)
			return
		}
		if c.Validate() != nil {
			allocationPolicyError(w, allocation.ErrInvalid)
			return
		}
		if id == "" {
			write(w, 200, c)
			return
		}
		for _, record := range c.Records {
			if record.Command.Policy.ID == id {
				write(w, 200, record)
				return
			}
		}
		allocationPolicyError(w, allocation.ErrPolicyNotFound)
	}
	mux.HandleFunc("GET /v1/allocation-policies", read)
	mux.HandleFunc("GET /v1/allocation-policies/{id}", read)
}
