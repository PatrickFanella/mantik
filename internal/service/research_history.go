package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/researchhistory"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func researchHistoryRoutes(mux *http.ServeMux, store storage.Store) {
	reader, ok := store.(researchhistory.Reader)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/hypothesis-trials/{id}/research-history", func(w http.ResponseWriter, r *http.Request) {
		if !runArguments(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := researchhistory.ReadTrial(ctx, reader, r.PathValue("id"))
		if researchHistoryError(w, err) {
			return
		}
		write(w, 200, h)
	})
	mux.HandleFunc("GET /v1/hypotheses/{id}/research-history", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 || !hypothesis.QuestionID.MatchString(r.PathValue("id")) {
			trialError(w, trial.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := researchhistory.ReadQuestion(ctx, reader, r.PathValue("id"))
		if researchHistoryError(w, err) {
			return
		}
		write(w, 200, h)
	})
}
func researchHistoryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, assessment.ErrHistoryChanged):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, trial.ErrNotFound), errors.Is(err, hypothesis.ErrNotFound):
		trialError(w, trial.ErrNotFound)
	default:
		failed(w, err)
	}
	return true
}
