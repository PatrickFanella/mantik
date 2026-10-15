package service

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func exceptionRoutes(mux *http.ServeMux, store storage.Store) {
	reader, ok := store.(storage.ExceptionReader)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/exceptions", func(w http.ResponseWriter, r *http.Request) {
		q := storage.ExceptionQuery{Limit: 25}
		v, err := url.ParseQuery(r.URL.RawQuery)
		valid := err == nil
		for name, values := range v {
			if len(values) != 1 || values[0] == "" {
				valid = false
				break
			}
			switch name {
			case "after":
				q.After = values[0]
			case "limit":
				q.Limit, err = strconv.Atoi(values[0])
			default:
				valid = false
			}
			if err != nil {
				valid = false
			}
		}
		if !valid || q.Validate() != nil {
			write(w, 400, map[string]string{"error": "invalid exception query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		page, err := reader.Exceptions(ctx, q)
		if err != nil {
			failed(w, err)
			return
		}
		write(w, 200, page)
	})
}
