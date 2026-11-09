package service

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func feedArchiveSourceRoutes(mux *http.ServeMux, store storage.Store) {
	backend, ok := store.(storage.FeedArchiveSourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/feed-archives/source", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(values) != 2 ||
			len(values["kind"]) != 1 || len(values["id"]) != 1 {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		source, err := backend.FeedArchiveSource(ctx, values.Get("kind"), values.Get("id"))
		if err != nil {
			feedArchiveError(w, err)
			return
		}
		write(w, 200, source)
	})
	mux.HandleFunc("GET /v1/feed-archives/source-audit", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			feedArchiveError(w, storage.ErrFeedArchiveInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*30*time.Second)
		defer cancel()
		audit, err := backend.AuditFeedArchiveSources(ctx)
		if err != nil {
			feedArchiveError(w, err)
			return
		}
		write(w, 200, audit)
	})
}
