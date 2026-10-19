package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func paperLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, paperledger.ErrInvalid), errors.Is(err, paperaccount.ErrInvalid):
		write(w, 422, map[string]string{"error": "invalid paper ledger rehearsal command or retained record"})
	case errors.Is(err, paperledger.ErrConflict), errors.Is(err, paperledger.ErrLimit), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, paperaccount.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "paper ledger rehearsal storage unavailable"})
	}
}
func paperLedgerRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperLedgerStore)
	if !ok {
		return
	}
	valid := func(w http.ResponseWriter, r *http.Request) bool {
		if !paperaccount.ID.MatchString(r.PathValue("id")) || len(r.URL.Query()) != 0 {
			paperLedgerError(w, paperledger.ErrInvalid)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /v1/paper-accounts/{id}/ledger-rehearsal", func(w http.ResponseWriter, r *http.Request) {
		if !valid(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := backend.PaperLedgerRehearsal(ctx, r.PathValue("id"))
		if err != nil {
			paperLedgerError(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("POST /v1/paper-accounts/{id}/ledger-rehearsal/entries", func(w http.ResponseWriter, r *http.Request) {
		if !valid(w, r) {
			return
		}
		var body paperledger.AppendCommand
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil || workerplan.ReadBoundedShape(raw, &body, 16<<10) != nil {
			write(w, 400, map[string]string{"error": "invalid paper ledger rehearsal JSON"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.AppendPaperLedgerRehearsal(ctx, r.PathValue("id"), body, clock.Now)
		if err != nil {
			paperLedgerError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	})
}
