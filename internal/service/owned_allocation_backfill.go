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

func ownedAllocationBackfillRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.OwnedAllocationIndexStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/allocation-history/index", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var command storage.OwnedAllocationIndexCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &command, 1024) != nil || command.Validate() != nil {
			ownedAllocationError(w, storage.ErrOwnedAllocationInvalid)
			return
		}
		timeout := raceDeadlineScale * time.Minute
		// This explicit full-prefix maintenance operation can outlive the
		// ordinary server write limit. Keep a bounded response window for it.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 5*time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			ownedAllocationError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		receipt, err := backend.IndexOwnedAllocationHistory(ctx, command, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			ownedAllocationError(w, err)
			return
		}
		write(w, http.StatusOK, receipt)
	})
}
