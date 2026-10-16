package service

import (
	"context"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func extractionAdmissionRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.ExtractionAdmissionStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-extractions/{id}/admit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID    string `json:"receiver_id"`
			AttemptSHA256 string `json:"attempt_sha256"`
			ResultSHA256  string `json:"result_sha256"`
			ApprovalCode  string `json:"approval_code"`
		}
		if !extractionBody(w, r, &body, 4096) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		approval, err := backend.AdmitExtraction(ctx, extraction.AdmissionCommand{ReceiverID: body.ReceiverID, AttemptID: r.PathValue("id"), AttemptSHA256: body.AttemptSHA256, ResultSHA256: body.ResultSHA256, ApprovalCode: body.ApprovalCode}, clock.Now)
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, approval)
	})
	mux.HandleFunc("GET /v1/news-extractions/{id}/admission", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			extractionError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		approval, err := backend.ExtractionAdmission(ctx, r.PathValue("id"))
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, approval)
	})
}
