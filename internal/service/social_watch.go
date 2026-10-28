package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func socialWatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, socialwatch.ErrInvalid), errors.Is(err, routing.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, routing.ErrConflict), errors.Is(err, socialwatch.ErrConflict), errors.Is(err, socialwatch.ErrCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, socialwatch.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "social watch storage unavailable"})
	}
}
func socialWatchRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	if dispatcher, ok := store.(storage.SocialResearchDispatchStore); ok {
		mux.HandleFunc("POST /v1/social-research-dispatches", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			defer r.Body.Close()
			raw, err := io.ReadAll(r.Body)
			var c storage.SocialResearchDispatchCommand
			if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			v, err := dispatcher.DispatchSocialResearch(ctx, c, clock.Now)
			if err != nil {
				socialWatchError(w, err)
				return
			}
			code := 201
			if v.Duplicate {
				code = 200
			}
			write(w, code, v)
		})
		mux.HandleFunc("GET /v1/social-research-dispatches", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			v, err := dispatcher.SocialResearchDispatches(ctx)
			if err != nil {
				socialWatchError(w, err)
				return
			}
			write(w, 200, v)
		})
	}
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
		if policy || r.Pattern == "POST /v1/social-research-requests" {
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
		if r.Pattern == "POST /v1/social-research-requests" {
			var selected socialwatch.ResearchCommand
			if workerplan.ReadBoundedShape(raw, &selected, int(limit)) != nil || selected.Validate() != nil {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			c.Research = &selected
		} else if policy {
			var p socialwatch.PolicyCommand
			if workerplan.ReadBoundedShape(raw, &p, int(limit)) != nil || p.Validate() != nil {
				socialWatchError(w, socialwatch.ErrInvalid)
				return
			}
			if r.Pattern == "POST /v1/copy-watches" && p.Policy.Behavior == "configured_rule" {
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
			if r.Pattern == "POST /v1/copy-recordings" {
				o, _ := p.Decode()
				if o.Schema != socialwatch.CopyRecordingSchema {
					socialWatchError(w, socialwatch.ErrInvalid)
					return
				}
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
	mux.HandleFunc("POST /v1/social-research-requests", func(w http.ResponseWriter, r *http.Request) { post(w, r, false) })
	mux.HandleFunc("POST /v1/social-watches", func(w http.ResponseWriter, r *http.Request) { post(w, r, true) })
	mux.HandleFunc("POST /v1/social-recordings", func(w http.ResponseWriter, r *http.Request) { post(w, r, false) })
	mux.HandleFunc("POST /v1/copy-watches", func(w http.ResponseWriter, r *http.Request) { post(w, r, true) })
	mux.HandleFunc("POST /v1/copy-recordings", func(w http.ResponseWriter, r *http.Request) { post(w, r, false) })
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
	applicability := func(w http.ResponseWriter, r *http.Request) {
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
		result, err := socialwatch.InspectApplicability(socialwatch.Journal{Schema: socialwatch.Schema, ReceiverID: all.ReceiverID, Entries: all.History}, r.PathValue("id"), clock.Now())
		if err != nil {
			socialWatchError(w, err)
			return
		}
		write(w, http.StatusOK, result)
	}
	// A retained recording is immutable source material, not current applicability.
	// SocialHistory replays the complete owner and returns only after COMMIT.
	recording := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			socialWatchError(w, socialwatch.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.SocialHistory(ctx)
		if err != nil {
			// Invalid durable source data is an owner failure, not a bad GET.
			write(w, http.StatusServiceUnavailable, map[string]string{"error": "social watch storage unavailable"})
			return
		}
		copyOnly := r.Pattern == "GET /v1/copy-recordings/{id}"
		for _, entry := range all.History {
			c := entry.Capture
			if c != nil && c.Command.ID == id && (c.Source.Schema == socialwatch.CopyRecordingSchema) == copyOnly {
				write(w, http.StatusOK, c)
				return
			}
		}
		socialWatchError(w, socialwatch.ErrNotFound)
	}
	mux.HandleFunc("GET /v1/social-research-requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			socialWatchError(w, socialwatch.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		history, err := backend.SocialHistory(ctx)
		if err != nil {
			write(w, 503, map[string]string{"error": "social watch storage unavailable"})
			return
		}
		for _, request := range history.ResearchRequests {
			if request.ID == id {
				write(w, 200, request)
				return
			}
		}
		socialWatchError(w, socialwatch.ErrNotFound)
	})
	mux.HandleFunc("GET /v1/social-recordings/{id}", recording)
	mux.HandleFunc("GET /v1/copy-recordings/{id}", recording)
	mux.HandleFunc("GET /v1/social-recordings/{id}/applicability", applicability)
	mux.HandleFunc("GET /v1/copy-recordings/{id}/applicability", applicability)
	mux.HandleFunc("GET /v1/social-history", read)
	mux.HandleFunc("GET /v1/social-watches/{id}", read)
	mux.HandleFunc("GET /v1/copy-watches/{id}", read)
}
