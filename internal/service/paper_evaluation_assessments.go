package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/http"
	"time"
)

func paperEvaluationAssessmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPaperEvaluationAssessmentInvalid):
		write(w, 422, map[string]string{"error": "invalid owned paper evaluation assessment"})
	case errors.Is(err, storage.ErrPaperEvaluationAssessmentConflict), errors.Is(err, storage.ErrPaperEvaluationAssessmentCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, storage.ErrPaperEvaluationAssessmentNotFound), errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound):
		write(w, 404, map[string]string{"error": "owned paper evaluation assessment or declaration not found"})
	default:
		write(w, 503, map[string]string{"error": "owned paper evaluation assessment unavailable"})
	}
}
func paperEvaluationAssessmentRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationAssessmentStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/paper-evaluation-assessments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		var c storage.PaperEvaluationAssessmentCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || c.Validate() != nil || reservedPaperEvaluationID(c.ID) {
			paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		v, err := backend.AssessPaperEvaluation(ctx, c, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperEvaluationAssessmentError(w, err)
			return
		}
		code := 201
		if v.Duplicate {
			code = 200
		}
		write(w, code, v)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "" && !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		all, err := backend.PaperEvaluationAssessments(ctx, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if err != nil {
			paperEvaluationAssessmentError(w, err)
			return
		}
		if id == "" {
			write(w, 200, all)
			return
		}
		for _, v := range all.Records {
			if v.Command.ID == id {
				write(w, 200, v)
				return
			}
		}
		paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentNotFound)
	}
	mux.HandleFunc("GET /v1/paper-evaluation-assessments", read)
	mux.HandleFunc("GET /v1/paper-evaluation-assessments/{id}", read)
	if selector, ok := store.(storage.PaperEvaluationCorporateSelectionStore); ok {
		mux.HandleFunc("GET /v1/paper-evaluation-assessments/{id}/corporate-selection", func(w http.ResponseWriter, r *http.Request) {
			if !paperaccount.ID.MatchString(r.PathValue("id")) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
				paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			v, err := selector.PaperEvaluationCorporateSelection(ctx, r.PathValue("id"), evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				paperEvaluationAssessmentError(w, err)
				return
			}
			write(w, 200, v)
		})
	}
	if chains, ok := store.(storage.PaperEvaluationCorporateChainStore); ok {
		mux.HandleFunc("GET /v1/paper-evaluation-assessments/{id}/corporate-page-chains/{source_id}", func(w http.ResponseWriter, r *http.Request) {
			if !paperaccount.ID.MatchString(r.PathValue("id")) || !paperaccount.ID.MatchString(r.PathValue("source_id")) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
				paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			v, err := chains.PaperEvaluationCorporatePageChain(ctx, r.PathValue("id"), r.PathValue("source_id"), evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				paperEvaluationAssessmentError(w, err)
				return
			}
			write(w, 200, v)
		})
	}
	if inspector, ok := store.(storage.PaperEvaluationCorporateInspectionStore); ok {
		mux.HandleFunc("GET /v1/paper-evaluation-assessments/{id}/corporate-sources", func(w http.ResponseWriter, r *http.Request) {
			if !paperaccount.ID.MatchString(r.PathValue("id")) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
				paperEvaluationAssessmentError(w, storage.ErrPaperEvaluationAssessmentInvalid)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			v, err := inspector.PaperEvaluationCorporateInspection(ctx, r.PathValue("id"), evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
			if err != nil {
				paperEvaluationAssessmentError(w, err)
				return
			}
			write(w, 200, v)
		})
	}
}
