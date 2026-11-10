package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func paperAttributionRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.PaperAttributionStore)
	if !ok {
		return
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.RawQuery != "" || r.URL.ForceQuery || id != "" && !paperaccount.ID.MatchString(id) {
			write(w, 422, map[string]string{"error": "invalid paper attribution query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		if target, ok := store.(storage.PaperAttributionDecisionStore); ok && id != "" {
			row, err := target.PaperAttributionDecision(ctx, id, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			// Legacy history without its segment index falls through to the
			// complete catalogue below.
			if !errors.Is(err, storage.ErrPaperAttributionIndexUnavailable) {
				switch {
				case errors.Is(err, storage.ErrPaperAttributionNotFound):
					write(w, 404, map[string]string{"error": "paper attribution decision not found"})
				case err != nil:
					write(w, 503, map[string]string{"error": "paper attribution storage unavailable"})
				default:
					write(w, 200, row)
				}
				return
			}
		}
		all, err := backend.PaperAttribution(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			write(w, 503, map[string]string{"error": "paper attribution storage unavailable"})
			return
		}
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, row := range all.Rows {
			if row.Decision.Command.ID == id {
				write(w, 200, row)
				return
			}
		}
		write(w, 404, map[string]string{"error": "paper attribution decision not found"})
	}
	mux.HandleFunc("GET /v1/paper-attribution", read)
	mux.HandleFunc("GET /v1/paper-attribution/{id}", read)
}
