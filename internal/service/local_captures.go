package service

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func localCaptureError(w http.ResponseWriter, err error) {
	if errors.Is(err, extraction.ErrLocalEvidence) || errors.Is(err, extraction.ErrLocalVersion) {
		write(w, 422, map[string]string{"error": err.Error()})
		return
	}
	extractionError(w, err)
}
func localCaptureRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.LocalCaptureStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-local-captures", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string                   `json:"receiver_id"`
			RequestID  string                   `json:"request_id"`
			Prepared   extraction.LocalReceipt  `json:"prepared"`
			Finished   *extraction.LocalReceipt `json:"finished,omitempty"`
		}
		if !extractionBody(w, r, &body, extraction.MaxLocalCaptureBytes) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.ImportLocalCapture(ctx, body.ReceiverID, body.RequestID, body.Prepared, body.Finished, clock.Now)
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
	mux.HandleFunc("GET /v1/news-local-captures/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			localCaptureError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.LocalCapture(ctx, r.PathValue("id"))
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
	mux.HandleFunc("GET /v1/news-local-captures", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			localCaptureError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.LocalCaptures(ctx)
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
}
