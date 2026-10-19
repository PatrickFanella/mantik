package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func paperAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, paperaccount.ErrInvalid):
		write(w, 422, map[string]string{"error": paperaccount.ErrInvalid.Error()})
	case errors.Is(err, paperaccount.ErrConflict), errors.Is(err, paperaccount.ErrCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, paperaccount.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "paper account storage unavailable"})
	}
}
func paperAccountRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperAccountStore)
	if !ok {
		return
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			paperAccountError(w, paperaccount.ErrInvalid)
			return
		}
		id := r.PathValue("id")
		if id != "" && !paperaccount.ID.MatchString(id) {
			paperAccountError(w, paperaccount.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		c, err := backend.PaperAccounts(ctx)
		if err != nil {
			paperAccountError(w, err)
			return
		}
		if c.Validate() != nil {
			paperAccountError(w, paperaccount.ErrInvalid)
			return
		}
		if id == "" {
			write(w, 200, c)
			return
		}
		for _, a := range c.Accounts {
			if a.Config.ID == id {
				write(w, 200, a)
				return
			}
		}
		paperAccountError(w, paperaccount.ErrNotFound)
	}
	mux.HandleFunc("GET /v1/paper-accounts", read)
	mux.HandleFunc("GET /v1/paper-accounts/{id}", read)
	mux.HandleFunc("POST /v1/paper-accounts", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			paperAccountError(w, paperaccount.ErrInvalid)
			return
		}
		var body struct {
			ReceiverID string              `json:"receiver_id"`
			Config     paperaccount.Config `json:"config"`
		}
		if !routingBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.CreatePaperAccount(ctx, body.ReceiverID, body.Config, clock.Now)
		if err != nil {
			paperAccountError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	})
}
