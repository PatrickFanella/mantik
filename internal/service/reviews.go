package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func reviewRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.ReviewStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/news-reviews", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string `json:"receiver_id"`
			RequestID  string `json:"request_id"`
			CaseID     string `json:"case_id"`
			Version    int    `json:"version"`
		}
		if !reviewBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		view, err := backend.StartReview(ctx, body.ReceiverID, body.RequestID, body.CaseID, body.Version, clock.Now)
		if err != nil {
			reviewError(w, err)
			return
		}
		write(w, 200, view)
	})
	mux.HandleFunc("GET /v1/news-reviews/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !review.ID.MatchString(r.PathValue("id")) || len(r.URL.Query()) != 0 {
			write(w, 422, map[string]string{"error": "invalid review inspection arguments"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		view, err := backend.Review(ctx, r.PathValue("id"))
		if err != nil {
			reviewError(w, err)
			return
		}
		write(w, 200, view)
	})
	mux.HandleFunc("POST /v1/news-reviews/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReceiverID string            `json:"receiver_id"`
			Steps      []json.RawMessage `json:"steps"`
		}
		if !reviewBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		view, err := backend.CompleteReview(ctx, body.ReceiverID, r.PathValue("id"), body.Steps, clock.Now)
		if err != nil {
			reviewError(w, err)
			return
		}
		write(w, 200, view)
	})
}
func reviewBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if len(r.URL.Query()) != 0 {
		write(w, 422, map[string]string{"error": "review writes accept no query parameters"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 160<<10)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		write(w, 400, map[string]string{"error": "invalid review command JSON"})
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		write(w, 400, map[string]string{"error": "expected one review command"})
		return false
	}
	return true
}
func reviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, review.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, review.ErrConflict), errors.Is(err, review.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, review.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		newsError(w, err)
	}
}
