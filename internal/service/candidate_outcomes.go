package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func candidateOutcomeRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CandidateWorkerOutcomeStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/candidate-worker-outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			candidateIntentError(w, candidateattempt.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, candidateevidence.MaxReportBytes)
		defer r.Body.Close()
		report, err := candidateevidence.ReadReport(r.Body)
		if err != nil {
			candidateIntentError(w, candidateattempt.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := backend.ReportCandidateWorkerOutcome(ctx, report, clock.Now)
		if err != nil {
			candidateOutcomeError(w, err)
			return
		}
		write(w, 200, result)
	})
	for _, artifact := range []bool{false, true} {
		path := "GET /v1/candidate-worker-outcomes/{id}"
		if artifact {
			path += "/capture"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || !review.ID.MatchString(r.PathValue("id")) {
				candidateIntentError(w, candidateattempt.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if artifact {
				a, err := backend.CandidateWorkerOutcomeEvidence(ctx, r.PathValue("id"))
				if err != nil {
					candidateOutcomeError(w, err)
					return
				}
				write(w, 200, a)
			} else {
				result, err := backend.CandidateWorkerOutcome(ctx, r.PathValue("id"))
				if err != nil {
					candidateOutcomeError(w, err)
					return
				}
				write(w, 200, result)
			}
		})
	}
}
func candidateOutcomeError(w http.ResponseWriter, err error) {
	if errors.Is(err, candidateevidence.ErrConflict) {
		write(w, 409, map[string]string{"error": err.Error()})
		return
	}
	candidateIntentError(w, err)
}
