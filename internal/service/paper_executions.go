package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// POST /v1/paper-executions advances every open entry book to the service
// clock. The caller names only the step identity; quotes, books, fills and
// releases all come from owner records. A step with nothing to commit returns
// status no_effects and retains nothing.
func paperExecutionRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.OwnedExecutionStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-executions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.OwnedExecutionCommand
		// The execution worker's identities are reserved, so a caller cannot
		// occupy the identity it retries.
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || reservedPaperExecutionID(c.ID) {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		result, err := backend.ExecuteOwnedAllocations(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrOwnedExecutionUnavailable) {
			write(w, 503, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			ownedAllocationError(w, err)
			return
		}
		code := 200
		if result.Record != nil && !result.Duplicate {
			code = 201
		}
		write(w, code, result)
	})
}
