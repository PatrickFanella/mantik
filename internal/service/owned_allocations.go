package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func ownedAllocationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrOwnedAllocationInvalid):
		write(w, 422, map[string]string{"error": "invalid owned allocation comparison"})
	case errors.Is(err, storage.ErrOwnedAllocationConflict), errors.Is(err, storage.ErrOwnedAllocationCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "owned allocation storage unavailable"})
	}
}
func ownedAllocationRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.OwnedAllocationStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/allocation-comparisons", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.OwnedAllocationCommand
		// The decision worker's comparison identities are reserved.
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 8192) != nil || c.Validate() != nil || reservedPaperComparisonID(c.ID) {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		result, err := backend.CompareOwnedAllocation(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAllocationError(w, err)
			return
		}
		code := 201
		if result.Duplicate {
			code = 200
		}
		write(w, code, result)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "" && !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		result, err := backend.OwnedAllocations(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAllocationError(w, err)
			return
		}
		if id != "" {
			for _, record := range result.Records {
				if record.Command.ID == id {
					write(w, 200, record)
					return
				}
			}
			write(w, 404, map[string]string{"error": "owned allocation comparison not found"})
			return
		}
		write(w, 200, result)
	}
	mux.HandleFunc("GET /v1/allocation-comparisons", read)
	mux.HandleFunc("GET /v1/allocation-comparisons/{id}", read)
	// The audit re-executes the whole history, including sealed records that
	// routine replay trusts through the journal checkpoint.
	if auditor, ok := store.(storage.OwnedAllocationAuditStore); ok {
		mux.HandleFunc("GET /v1/allocation-comparisons/audit", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
				ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			result, err := auditor.AuditOwnedAllocations(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if errors.Is(err, storage.ErrOwnedAllocationAudit) {
				write(w, 409, map[string]string{"error": err.Error()})
				return
			}
			if err != nil {
				ownedAllocationError(w, err)
				return
			}
			write(w, 200, result)
		})
	}
}
