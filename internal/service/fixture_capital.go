package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

const FixtureCapitalUnboundCode = "fixture_capital_unbound"

func fixtureCapitalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrFixtureCapitalUnbound):
		write(w, 404, map[string]string{"error": "fixture capital is unbound", "code": FixtureCapitalUnboundCode})
	case errors.Is(err, paperaccount.ErrNotFound):
		write(w, 404, map[string]string{"error": "fixture capital binding or paper account not found"})
	case errors.Is(err, storage.ErrFixtureCapitalBinding):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrReceiverMismatch), errors.Is(err, fixturecapital.ErrConflict):
		write(w, 409, map[string]string{"error": "fixture capital binding conflicts with the account or retained state"})
	default:
		write(w, 503, map[string]string{"error": "fixture capital storage unavailable or inconsistent"})
	}
}
func fixtureCapitalRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.FixtureCapitalStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/fixture-capital", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			fixtureCapitalError(w, storage.ErrFixtureCapitalBinding)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := backend.FixtureCapital(ctx)
		if err != nil {
			if errors.Is(err, storage.ErrFixtureCapitalUnbound) {
				fixtureCapitalError(w, err)
			} else {
				write(w, 503, map[string]string{"error": "fixture capital storage unavailable or inconsistent"})
			}
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("POST /v1/fixture-capital/binding", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			fixtureCapitalError(w, storage.ErrFixtureCapitalBinding)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.FixtureCapitalBinding
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 16<<10) != nil {
			write(w, 400, map[string]string{"error": "invalid fixture capital binding JSON"})
			return
		}
		if err := storage.ValidateFixtureCapitalBinding(c); err != nil {
			fixtureCapitalError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := backend.BindFixtureCapital(ctx, c)
		if err != nil {
			fixtureCapitalError(w, err)
			return
		}
		write(w, 200, v)
	})
}
