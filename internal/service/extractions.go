package service

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func extractionRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.ExtractionStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-extractions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string       `json:"receiver_id"`
			RequestID  string       `json:"request_id"`
			Article    news.Article `json:"article"`
			EventKey   string       `json:"event_key"`
		}
		if !extractionBody(w, r, &body, (2<<20)+4096) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.StartExtraction(ctx, body.ReceiverID, body.RequestID, body.Article, body.EventKey, clock.Now)
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("POST /v1/news-extractions/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID     string  `json:"receiver_id"`
			ArtifactBase64 *string `json:"artifact_base64"`
		}
		if !extractionBody(w, r, &body, 180<<10) {
			return
		}
		if body.ArtifactBase64 == nil || len(*body.ArtifactBase64) > base64.StdEncoding.EncodedLen(extraction.MaxArtifactBytes) {
			extractionError(w, extraction.ErrInvalid)
			return
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(*body.ArtifactBase64)
		if err != nil || len(raw) > extraction.MaxArtifactBytes || base64.StdEncoding.EncodeToString(raw) != *body.ArtifactBase64 {
			extractionError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.CompleteExtraction(ctx, body.ReceiverID, r.PathValue("id"), raw, clock.Now)
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/news-extractions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			extractionError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a, err := backend.Extraction(ctx, r.PathValue("id"))
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, a)
	})
	mux.HandleFunc("GET /v1/news-extractions", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			extractionError(w, extraction.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h, err := backend.Extractions(ctx)
		if err == nil {
			err = h.Validate()
		}
		if err != nil {
			extractionError(w, err)
			return
		}
		write(w, 200, h)
	})
}
func extractionBody(w http.ResponseWriter, r *http.Request, out any, limit int) bool {
	if len(r.URL.Query()) != 0 {
		extractionError(w, extraction.ErrInvalid)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil || workerplan.ReadBoundedShape(raw, out, limit) != nil {
		write(w, 400, map[string]string{"error": "invalid extraction command JSON"})
		return false
	}
	return true
}
func extractionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, extraction.ErrInvalid):
		write(w, 422, map[string]string{"error": extraction.ErrInvalid.Error()})
	case errors.Is(err, extraction.ErrConflict), errors.Is(err, extraction.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, extraction.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		newsError(w, err)
	}
}
