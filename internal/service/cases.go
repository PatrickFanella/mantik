package service

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

var caseIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func caseRoutes(mux *http.ServeMux, store storage.Store) {
	reader, ok := store.(storage.CaseReader)
	if !ok {
		return
	}
	handle := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			write(w, 400, map[string]string{"error": "case readback does not accept query parameters"})
			return
		}
		id := r.PathValue("id")
		if id != "" && !caseIDPattern.MatchString(id) {
			write(w, 400, map[string]string{"error": "invalid case identity"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var value any
		var err error
		if id == "" {
			value, err = reader.Cases(ctx)
		} else {
			value, err = reader.InspectCase(ctx, id)
		}
		if errors.Is(err, storage.ErrCaseNotFound) {
			write(w, 404, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			failed(w, err)
			return
		}
		write(w, 200, value)
	}
	mux.HandleFunc("GET /v1/cases", handle)
	mux.HandleFunc("GET /v1/cases/{id}", handle)
}
