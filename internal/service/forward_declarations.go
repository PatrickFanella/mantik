package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func forwardDeclarationRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	s, ok := store.(forward.Store)
	if !ok {
		return
	}
	fail := func(w http.ResponseWriter, e error) {
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(e, forward.ErrInvalid):
			status = 422
		case errors.Is(e, forward.ErrNotFound):
			status = 404
		case errors.Is(e, forward.ErrConflict):
			status = 409
		}
		write(w, status, map[string]string{"error": http.StatusText(status)})
	}
	mux.HandleFunc("POST /v1/forward-declarations", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(w, forward.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		defer r.Body.Close()
		raw, e := io.ReadAll(r.Body)
		var d forward.Declaration
		if e != nil || workerplan.ReadBoundedShape(raw, &d, 128<<10) != nil || d.Validate() != nil {
			fail(w, forward.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		record, e := s.DeclareForward(ctx, d, clock.Now)
		if e != nil {
			fail(w, e)
			return
		}
		write(w, 200, record)
	})
	for _, path := range []string{"GET /v1/forward-declarations/{id}", "GET /v1/forward-declarations/{id}/eligibility"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				fail(w, forward.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
			defer cancel()
			if r.Pattern == "GET /v1/forward-declarations/{id}/eligibility" {
				value, e := s.ForwardEligibility(ctx, r.PathValue("id"), clock.Now())
				if e != nil {
					fail(w, e)
					return
				}
				write(w, 200, value)
				return
			}
			value, e := s.ForwardDeclaration(ctx, r.PathValue("id"))
			if e != nil {
				fail(w, e)
				return
			}
			write(w, 200, value)
		})
	}
}
