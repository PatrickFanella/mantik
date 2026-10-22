package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func paperStrategyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, paperstrategy.ErrInvalid), errors.Is(err, paperaccount.ErrInvalid):
		write(w, 422, map[string]string{"error": "invalid paper strategy definition"})
	case errors.Is(err, paperstrategy.ErrConflict), errors.Is(err, paperstrategy.ErrCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, paperstrategy.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "paper strategy definition storage unavailable"})
	}
}
func paperStrategyRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperStrategyStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-strategies", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperStrategyError(w, paperstrategy.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, paperstrategy.MaxCommandBytes)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var cmd paperstrategy.Command
		if err != nil || workerplan.ReadBoundedShape(raw, &cmd, paperstrategy.MaxCommandBytes) != nil || cmd.Validate() != nil {
			paperStrategyError(w, paperstrategy.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.RegisterPaperStrategy(ctx, cmd, clock.Now)
		if err != nil {
			paperStrategyError(w, err)
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
			paperStrategyError(w, paperstrategy.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		c, err := backend.PaperStrategies(ctx)
		if err != nil {
			paperStrategyError(w, err)
			return
		}
		if c.Validate() != nil {
			paperStrategyError(w, paperstrategy.ErrInvalid)
			return
		}
		if id == "" {
			write(w, 200, c)
			return
		}
		for _, record := range c.Records {
			if record.Command.Definition.ID == id {
				write(w, 200, record)
				return
			}
		}
		paperStrategyError(w, paperstrategy.ErrNotFound)
	}
	mux.HandleFunc("GET /v1/paper-strategies", read)
	mux.HandleFunc("GET /v1/paper-strategies/{id}", read)
}
