package service

import (
	"context"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func localCaptureAdmissionRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.LocalCaptureAdmissionStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-local-captures/{id}/admit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID    string `json:"receiver_id"`
			CaptureSHA256 string `json:"capture_sha256"`
			ResultSHA256  string `json:"result_sha256"`
			ApprovalCode  string `json:"approval_code"`
		}
		if !extractionBody(w, r, &body, 4096) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		approval, err := backend.AdmitLocalCapture(ctx, extraction.LocalAdmissionCommand{ReceiverID: body.ReceiverID, CaptureID: r.PathValue("id"), CaptureSHA256: body.CaptureSHA256, ResultSHA256: body.ResultSHA256, ApprovalCode: body.ApprovalCode}, clock.Now)
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, approval)
	})
	mux.HandleFunc("GET /v1/news-local-captures/{id}/admission", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			localCaptureError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		approval, err := backend.LocalCaptureAdmission(ctx, r.PathValue("id"))
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, approval)
	})
}
