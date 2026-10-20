package service

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func generationOutcomeRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CandidateGenerationOutcomeStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/candidate-generation-outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generationIntentError(w, candidategeneration.ErrInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, candidategeneration.MaxReportBytes)
		defer r.Body.Close()
		s, err := candidategeneration.ReadReport(r.Body)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		i, err := backend.ReportCandidateGeneration(ctx, s, clock.Now)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		write(w, 200, i)
	})
	mux.HandleFunc("GET /v1/candidate-generation-outcomes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			generationIntentError(w, candidategeneration.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.CandidateGenerationOutcomeHistory(ctx)
		if err != nil {
			generationIntentError(w, err)
			return
		}
		write(w, 200, h)
	})
	for _, artifact := range []bool{false, true} {
		path := "GET /v1/candidate-generation-outcomes/{id}"
		if artifact {
			path += "/recording"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || !review.ID.MatchString(r.PathValue("id")) {
				generationIntentError(w, candidategeneration.ErrInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if artifact {
				a, err := backend.CandidateGenerationOutcomeEvidence(ctx, r.PathValue("id"))
				if err != nil {
					generationIntentError(w, err)
					return
				}
				write(w, 200, a)
			} else {
				i, err := backend.CandidateGenerationOutcome(ctx, r.PathValue("id"))
				if err != nil {
					generationIntentError(w, err)
					return
				}
				write(w, 200, i)
			}
		})
	}
}
