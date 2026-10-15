package service

import (
	"net/http"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
)

func libraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/library", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			write(w, 400, map[string]string{"error": "library readback does not accept query parameters"})
			return
		}
		catalog, err := experiment.BuiltinCatalog()
		if err != nil {
			write(w, 503, map[string]string{"error": "compiled method catalogue unavailable"})
			return
		}
		write(w, 200, catalog)
	})
}
