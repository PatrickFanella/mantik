package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func activityRoutes(mux *http.ServeMux, store storage.Store) {
	mux.HandleFunc("GET /v1/activity", func(w http.ResponseWriter, r *http.Request) {
		q := storage.ActivityQuery{Limit: 25}
		values, err := parseActivityQuery(r, &q)
		if err != nil || !values || q.Validate() != nil {
			write(w, 400, map[string]string{"error": "invalid activity query"})
			return
		}
		reader, ok := store.(storage.ActivityReader)
		if !ok {
			write(w, 503, map[string]string{"error": "activity unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		page, err := reader.Activity(ctx, q)
		if errors.Is(err, storage.ErrActivityQuery) {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			failed(w, err)
			return
		}
		write(w, 200, page)
	})
}

func parseActivityQuery(r *http.Request, q *storage.ActivityQuery) (bool, error) {
	// ParseQuery rejects malformed escapes; Query alone silently discards them.
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false, err
	}
	for name, values := range v {
		if len(values) != 1 || values[0] == "" {
			return false, nil
		}
		switch name {
		case "after":
			q.After, err = strconv.ParseInt(values[0], 10, 64)
		case "through":
			var n int64
			n, err = strconv.ParseInt(values[0], 10, 64)
			q.Through = &n
		case "limit":
			q.Limit, err = strconv.Atoi(values[0])
		default:
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}
