package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func trialAssessmentRoutes(mux *http.ServeMux, store storage.Store) {
	reader, ok := store.(assessment.HistoryReader)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/hypothesis-trials/{id}/assessments", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := assessment.ReadHistory(ctx, reader, r.PathValue("id"))
		if errors.Is(err, trial.ErrNotFound) {
			trialError(w, err)
			return
		}
		if errors.Is(err, assessment.ErrHistoryChanged) {
			write(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			failed(w, err)
			return
		}
		write(w, http.StatusOK, h)
	})
}
