package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func corporateActionSourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrCorporateActionSourceInvalid):
		write(w, 422, map[string]string{"error": "invalid corporate-action source custody"})
	case errors.Is(err, storage.ErrCorporateActionSourceConflict), errors.Is(err, storage.ErrCorporateActionSourceCapacity), errors.Is(err, storage.ErrReceiverMismatch):
		write(w, 409, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "corporate-action source storage unavailable"})
	}
}
func corporateActionSourceRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CorporateActionSourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("POST /v1/corporate-action-sources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			corporateActionSourceError(w, storage.ErrCorporateActionSourceInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, storage.MaxCorporateActionSourceBytes)
		defer func() { _ = r.Body.Close() }()
		raw, err := io.ReadAll(r.Body)
		var c storage.CorporateActionSourceCommand
		if err != nil || workerplan.ReadBoundedShape(raw, &c, storage.MaxCorporateActionSourceBytes) != nil || c.Validate() != nil {
			corporateActionSourceError(w, storage.ErrCorporateActionSourceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		receipt, err := backend.RetainCorporateActionSource(ctx, c, clock.Now)
		if err != nil {
			corporateActionSourceError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	})
	read := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.RawQuery != "" || r.URL.ForceQuery || id != "" && !paperaccount.ID.MatchString(id) {
			corporateActionSourceError(w, storage.ErrCorporateActionSourceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		cat, err := backend.CorporateActionSources(ctx)
		if err != nil {
			corporateActionSourceError(w, err)
			return
		}
		if id != "" {
			for _, v := range cat.Records {
				if v.Command.ID == id {
					write(w, 200, v)
					return
				}
			}
			write(w, 404, map[string]string{"error": "corporate-action source not retained"})
			return
		}
		// Discovery stays bounded without returning every original page at once.
		type ref struct {
			ID           string    `json:"id"`
			SHA256       string    `json:"sha256"`
			SourceSHA256 string    `json:"source_sha256"`
			RetainedAt   time.Time `json:"retained_at"`
			Sequence     int       `json:"sequence"`
		}
		refs := []ref{}
		for _, v := range cat.Records {
			refs = append(refs, ref{v.Command.ID, v.SHA256, v.Command.Recording.SHA256, v.RetainedAt, v.Sequence})
		}
		write(w, 200, struct {
			ReceiverID          string `json:"receiver_id"`
			Records             []ref  `json:"records"`
			Authority           string `json:"authority"`
			QualificationCredit bool   `json:"qualification_credit"`
		}{ReceiverID: cat.ReceiverID, Records: refs, Authority: "service_custody_of_caller_reported_corporate_action_source_only"})
	}
	mux.HandleFunc("GET /v1/corporate-action-sources", read)
	mux.HandleFunc("GET /v1/corporate-action-sources/{id}", read)
}
