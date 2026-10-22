package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func socialWatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, socialwatch.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, socialwatch.ErrConflict), errors.Is(err, socialwatch.ErrCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, socialwatch.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "social watch storage unavailable"})
	}
}
func socialWatchRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.SocialWatchStore)
	if !ok {
		return
	}
	post := func(w http.ResponseWriter, r *http.Request, policy bool) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			socialWatchError(w, socialwatch.ErrInvalid)
			return
		}
		limit := int64(64 << 10)
		if policy {
			limit = 4096
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			socialWatchError(w, socialwatch.ErrInvalid)
			return
		}
		var c socialwatch.Command
		if policy {
			var p socialwatch.PolicyCommand
			if workerplan.ReadBoundedShape(raw, &p, int(limit)) != nil || p.Validate() != nil {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			c.Policy = &p
		} else {
			var p socialwatch.CaptureCommand
			if workerplan.ReadBoundedShape(raw, &p, int(limit)) != nil {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			if _, err := p.Decode(); err != nil {
				socialWatchError(w, err)
				return
			}
			c.Capture = &p
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.ApplySocialCommand(ctx, c, clock.Now)
		if err != nil {
			socialWatchError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	}
	mux.HandleFunc("POST /v1/social-watches", func(w http.ResponseWriter, r *http.Request) { post(w, r, true) })
	mux.HandleFunc("POST /v1/social-recordings", func(w http.ResponseWriter, r *http.Request) { post(w, r, false) })
	read := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			socialWatchError(w, socialwatch.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.SocialHistory(ctx)
		if err != nil {
			socialWatchError(w, err)
			return
		}
		if id := r.PathValue("id"); id != "" {
			for _, v := range all.Watches {
				if v.Policy.Command.Policy.ID == id {
					write(w, 200, v)
					return
				}
			}
			socialWatchError(w, socialwatch.ErrNotFound)
			return
		}
		write(w, 200, all)
	}
	mux.HandleFunc("GET /v1/social-history", read)
	mux.HandleFunc("GET /v1/social-watches/{id}", read)
}
