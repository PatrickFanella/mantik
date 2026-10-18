package service

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"time"
)

func switchyardCaptureRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.SwitchyardCaptureStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-switchyard-captures", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string                        `json:"receiver_id"`
			RequestID  string                        `json:"request_id"`
			Prepared   extraction.SwitchyardReceipt  `json:"prepared"`
			Finished   *extraction.SwitchyardReceipt `json:"finished,omitempty"`
		}
		if !extractionBody(w, r, &body, extraction.MaxSwitchyardCaptureBytes) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.ImportSwitchyardCapture(ctx, body.ReceiverID, body.RequestID, body.Prepared, body.Finished, clock.Now)
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
	mux.HandleFunc("GET /v1/news-switchyard-captures/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			localCaptureError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.SwitchyardCapture(ctx, r.PathValue("id"))
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
	mux.HandleFunc("GET /v1/news-switchyard-captures", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			localCaptureError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := backend.SwitchyardCaptures(ctx)
		if err != nil {
			localCaptureError(w, err)
			return
		}
		write(w, 200, result)
	})
}
