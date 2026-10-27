package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

// CorporateAcquisitionReader owns one explicitly configured existing journal.
// The journal's exclusive lock prevents concurrent collectors or other owners;
// this reader never creates collections, fetches data or changes artifacts.
type CorporateAcquisitionReader struct {
	mu      sync.Mutex
	journal *alpaca.Journal
}

func OpenCorporateAcquisitionReader(path, expectedID string) (*CorporateAcquisitionReader, error) {
	if path == "" || expectedID == "" {
		return nil, errors.New("corporate acquisition reader requires an existing private journal and its expected identity")
	}
	j, err := alpaca.OpenExistingJournal(path)
	if err != nil {
		return nil, err
	}
	if j.ID != expectedID {
		_ = j.Close()
		return nil, errors.New("corporate acquisition journal identity mismatch")
	}
	return &CorporateAcquisitionReader{journal: j}, nil
}
func (o *CorporateAcquisitionReader) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.journal == nil {
		return nil
	}
	err := o.journal.Close()
	o.journal = nil
	return err
}
func (o *CorporateAcquisitionReader) read(ctx context.Context, id string) (alpaca.CorporateActionAcquiredSource, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return alpaca.CorporateActionAcquiredSource{}, err
	}
	if o.journal == nil {
		return alpaca.CorporateActionAcquiredSource{}, errors.New("corporate acquisition reader closed")
	}
	v, err := o.journal.CorporateActionAcquiredSource(id)
	if err != nil {
		return alpaca.CorporateActionAcquiredSource{}, err
	}
	if err := ctx.Err(); err != nil {
		return alpaca.CorporateActionAcquiredSource{}, err
	}
	return v, nil
}
func corporateAcquisitionRoutes(mux *http.ServeMux, owner *CorporateAcquisitionReader) {
	if owner == nil {
		return
	}
	mux.HandleFunc("GET /v1/corporate-action-acquisitions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if len(r.PathValue("id")) > 40 || !paperaccount.ID.MatchString(r.PathValue("id")) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid corporate acquisition inspection"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := owner.read(ctx, r.PathValue("id"))
		switch {
		case err == nil:
			write(w, 200, v)
		case errors.Is(err, os.ErrNotExist):
			write(w, 404, map[string]string{"error": "corporate acquisition not retained"})
		case errors.Is(err, alpaca.ErrCollectionIncomplete), errors.Is(err, alpaca.ErrCollectionFailed), errors.Is(err, alpaca.ErrJournalConflict):
			write(w, 409, map[string]string{"error": "corporate acquisition is incomplete, failed or inconsistent"})
		default:
			write(w, 503, map[string]string{"error": "corporate acquisition unavailable"})
		}
	})
}
