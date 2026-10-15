package service

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

var newsKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

type newsInspection struct {
	RecordKey string           `json:"record_key"`
	Result    news.Result      `json:"result"`
	Receipt   newscase.Receipt `json:"receipt"`
	Original  string           `json:"original,omitempty"`
}

func newsRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.NewsStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/news-cases", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			write(w, 422, map[string]string{"error": "news catalogue accepts no query parameters"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		catalog, err := backend.NewsCases(ctx)
		if err != nil {
			newsError(w, err)
			return
		}
		write(w, 200, catalog)
	})
	mux.HandleFunc("GET /v1/news-recordings/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		q := r.URL.Query()
		original := q.Get("original")
		if !newsKey.MatchString(key) || len(q) > 1 || len(q["original"]) > 1 || len(q) == 1 && (q["original"] == nil || original != "true" && original != "false") {
			write(w, 422, map[string]string{"error": "invalid news record inspection arguments"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		record, err := backend.NewsRecord(ctx, key)
		if err != nil {
			newsError(w, err)
			return
		}
		view := newsInspection{RecordKey: key, Result: record.Result, Receipt: record.Receipt}
		if original == "true" {
			view.Original = record.Recording.Original
		}
		write(w, 200, view)
	})
	mux.HandleFunc("POST /v1/news-recordings", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) != 1 || len(q["receiver_id"]) != 1 {
			write(w, 422, map[string]string{"error": "news admission requires one explicit receiver_id"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, news.MaxRecordingBytes)
		defer r.Body.Close()
		recording, err := news.Read(r.Body)
		if err != nil {
			write(w, 422, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.AdmitNews(ctx, q.Get("receiver_id"), recording, clock.Now)
		if err != nil {
			newsError(w, err)
			return
		}
		code := http.StatusCreated
		if receipt.Duplicate {
			code = http.StatusOK
		}
		write(w, code, receipt)
	})
	mux.HandleFunc("POST /v1/news-cases/{id}/close", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			write(w, 422, map[string]string{"error": "news closure accepts no query parameters"})
			return
		}
		var body struct {
			ReceiverID string `json:"receiver_id"`
			Version    int    `json:"version"`
		}
		if !routingBody(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		c, err := backend.CloseNewsCase(ctx, body.ReceiverID, r.PathValue("id"), body.Version, clock.Now)
		if err != nil {
			newsError(w, err)
			return
		}
		write(w, 200, c)
	})
}
func newsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrReceiverMismatch), errors.Is(err, newscase.ErrConflict), errors.Is(err, newscase.ErrCapacity):
		write(w, 409, map[string]string{"error": err.Error()})
	case errors.Is(err, newscase.ErrInvalid):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, newscase.ErrNotFound):
		write(w, 404, map[string]string{"error": err.Error()})
	default:
		failed(w, err)
	}
}
