package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func paperEvaluationEvidenceRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationEvidenceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-evaluation-declarations/{id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid paper evaluation evidence query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		out, err := backend.PaperEvaluationEvidence(ctx, id, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound) {
			write(w, 404, map[string]string{"error": "paper evaluation declaration not found"})
			return
		}
		if err != nil {
			write(w, 503, map[string]string{"error": "owned paper evaluation evidence unavailable"})
			return
		}
		write(w, 200, out)
	})
}
