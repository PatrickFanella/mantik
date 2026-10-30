package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// CorporateReferenceReader owns up to one explicitly configured existing
// Polygon journal and one issuer-document journal. Each journal's exclusive
// lock keeps collectors out while the service runs; this reader never
// creates collections, fetches data or changes artifacts.
type CorporateReferenceReader struct {
	mu      sync.Mutex
	closed  bool
	polygon *polygonref.Journal
	issuer  *issuerdoc.Journal
}

// OpenCorporateReferenceReader opens each configured journal and checks its
// identity. An empty path and identity leaves that source kind unconfigured.
func OpenCorporateReferenceReader(polygonPath, polygonID, issuerPath, issuerID string) (*CorporateReferenceReader, error) {
	if (polygonPath == "") != (polygonID == "") || (issuerPath == "") != (issuerID == "") || polygonPath == "" && issuerPath == "" {
		return nil, errors.New("each corporate reference journal requires an existing private path and its expected identity")
	}
	o := &CorporateReferenceReader{}
	if polygonPath != "" {
		j, err := polygonref.OpenExistingJournal(polygonPath)
		if err != nil {
			return nil, err
		}
		o.polygon = j
		if j.ID != polygonID {
			_ = o.Close()
			return nil, errors.New("polygon reference journal identity mismatch")
		}
	}
	if issuerPath != "" {
		j, err := issuerdoc.OpenExistingJournal(issuerPath)
		if err != nil {
			_ = o.Close()
			return nil, err
		}
		o.issuer = j
		if j.ID != issuerID {
			_ = o.Close()
			return nil, errors.New("issuer document journal identity mismatch")
		}
	}
	return o, nil
}
func (o *CorporateReferenceReader) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	var err error
	if o.polygon != nil {
		err = o.polygon.Close()
		o.polygon = nil
	}
	if o.issuer != nil {
		err = errors.Join(err, o.issuer.Close())
		o.issuer = nil
	}
	return err
}

var errReferenceKindUnconfigured = errors.New("corporate reference journal not configured for this kind")

// read builds a custody command only from a retained, verified collection in
// the configured journal for its kind.
func (o *CorporateReferenceReader) read(ctx context.Context, kind, collectionID string) (storage.CorporateReferenceSourceCommand, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.CorporateReferenceSourceCommand{}, err
	}
	if o.closed {
		return storage.CorporateReferenceSourceCommand{}, errors.New("corporate reference reader closed")
	}
	var c storage.CorporateReferenceSourceCommand
	switch {
	case kind == storage.CorporateReferencePolygon && o.polygon != nil:
		_, a, bodies, err := o.polygon.Retained(collectionID)
		if err != nil {
			return c, err
		}
		c = storage.CorporateReferenceSourceCommand{Kind: kind, Polygon: &a, Originals: bodies}
	case kind == storage.CorporateReferenceIssuer && o.issuer != nil:
		_, a, raw, err := o.issuer.Retained(collectionID)
		if err != nil {
			return c, err
		}
		c = storage.CorporateReferenceSourceCommand{Kind: kind, Issuer: &a, Originals: [][]byte{raw}}
	default:
		return c, errReferenceKindUnconfigured
	}
	return c, ctx.Err()
}

func corporateReferenceSourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrCorporateReferenceSourceInvalid):
		write(w, 422, map[string]string{"error": "invalid corporate reference source custody"})
	case errors.Is(err, storage.ErrCorporateReferenceSourceNotFound):
		write(w, 404, map[string]string{"error": "corporate reference source not retained"})
	case errors.Is(err, storage.ErrCorporateReferenceSourceConflict), errors.Is(err, storage.ErrCorporateReferenceSourceCapacity), errors.Is(err, storage.ErrReceiverMismatch), errors.Is(err, feedcollector.ErrClockBackwards):
		write(w, 409, map[string]string{"error": err.Error()})
	default:
		write(w, 503, map[string]string{"error": "corporate reference source storage unavailable"})
	}
}

func corporateReferenceReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReferenceKindUnconfigured):
		write(w, 422, map[string]string{"error": err.Error()})
	case errors.Is(err, polygonref.ErrCollectionNotFound), errors.Is(err, issuerdoc.ErrCollectionNotFound), errors.Is(err, os.ErrNotExist):
		write(w, 404, map[string]string{"error": "corporate reference collection not retained"})
	case errors.Is(err, polygonref.ErrCollectionUnresolved), errors.Is(err, issuerdoc.ErrCollectionUnresolved), errors.Is(err, polygonref.ErrJournalConflict), errors.Is(err, issuerdoc.ErrJournalConflict):
		write(w, 409, map[string]string{"error": "corporate reference collection is interrupted, failed or inconsistent"})
	default:
		write(w, 503, map[string]string{"error": "corporate reference collection unavailable"})
	}
}

func corporateReferenceRoutes(mux *http.ServeMux, owner *CorporateReferenceReader, store storage.Store, clock Clock) {
	backend, ok := store.(storage.CorporateReferenceSourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/corporate-reference-sources", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			corporateReferenceSourceError(w, storage.ErrCorporateReferenceSourceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		cat, err := backend.CorporateReferenceSources(ctx)
		if err != nil {
			corporateReferenceSourceError(w, err)
			return
		}
		// Discovery omits parsed rows; inspect one source for its full record.
		type ref struct {
			ID                string    `json:"id"`
			Kind              string    `json:"kind"`
			AcquisitionSHA256 string    `json:"acquisition_sha256"`
			ParseStatus       string    `json:"parse_status"`
			OriginalBytes     int       `json:"original_bytes"`
			RetainedAt        time.Time `json:"retained_at"`
			Sequence          int       `json:"sequence"`
			SHA256            string    `json:"sha256"`
		}
		refs := []ref{}
		for _, v := range cat.Records {
			refs = append(refs, ref{v.ID, v.Kind, v.AcquisitionSHA256, v.ParseStatus, v.OriginalBytes, v.RetainedAt, v.Sequence, v.SHA256})
		}
		write(w, 200, struct {
			ReceiverID          string `json:"receiver_id"`
			Records             []ref  `json:"records"`
			Authority           string `json:"authority"`
			CoverageComplete    bool   `json:"coverage_complete"`
			QualificationCredit bool   `json:"qualification_credit"`
		}{ReceiverID: cat.ReceiverID, Records: refs, Authority: "service_custody_of_configured_journal_reference_acquisition_only"})
	})
	mux.HandleFunc("GET /v1/corporate-reference-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.RawQuery != "" || r.URL.ForceQuery || len(id) > 40 || !paperaccount.ID.MatchString(id) {
			corporateReferenceSourceError(w, storage.ErrCorporateReferenceSourceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		v, err := backend.CorporateReferenceSource(ctx, id)
		if err != nil {
			corporateReferenceSourceError(w, err)
			return
		}
		// Originals stay in custody; the record carries their hashes, which
		// were reverified against the retained bytes for this response.
		write(w, 200, struct {
			Record            storage.CorporateReferenceSourceRecord `json:"record"`
			OriginalsVerified bool                                   `json:"originals_verified"`
		}{v, true})
	})
	if owner == nil {
		return
	}
	mux.HandleFunc("POST /v1/corporate-reference-imports", func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			ID                string `json:"id"`
			ReceiverID        string `json:"receiver_id"`
			Kind              string `json:"kind"`
			CollectionID      string `json:"collection_id"`
			AcquisitionSHA256 string `json:"acquisition_sha256"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil || workerplan.ReadBoundedShape(raw, &c, 4096) != nil || !paperaccount.ID.MatchString(c.ID) || len(c.ID) > 40 || !storage.ValidReceiverID(c.ReceiverID) || c.Kind != storage.CorporateReferencePolygon && c.Kind != storage.CorporateReferenceIssuer || !paperaccount.ID.MatchString(c.CollectionID) || len(c.CollectionID) > 64 || !alpacarecord.ValidSHA256(c.AcquisitionSHA256) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			corporateReferenceSourceError(w, storage.ErrCorporateReferenceSourceInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cat, err := backend.CorporateReferenceSources(ctx)
		if err != nil {
			corporateReferenceSourceError(w, err)
			return
		}
		if cat.ReceiverID != c.ReceiverID {
			corporateReferenceSourceError(w, storage.ErrReceiverMismatch)
			return
		}
		// An exact retry returns the retained record without rereading the journal.
		for _, old := range cat.Records {
			if old.ID != c.ID {
				continue
			}
			if old.Kind != c.Kind || old.AcquisitionSHA256 != c.AcquisitionSHA256 || collectionOf(old) != c.CollectionID {
				corporateReferenceSourceError(w, storage.ErrCorporateReferenceSourceConflict)
				return
			}
			write(w, 200, storage.CorporateReferenceSourceReceipt{Record: old, Duplicate: true})
			return
		}
		command, err := owner.read(ctx, c.Kind, c.CollectionID)
		if err != nil {
			corporateReferenceReadError(w, err)
			return
		}
		command.ID, command.ReceiverID = c.ID, c.ReceiverID
		if collectionOf(storage.CorporateReferenceSourceRecord{Kind: command.Kind, Polygon: command.Polygon, Issuer: command.Issuer}) != c.CollectionID || bindingSHA256(command) != c.AcquisitionSHA256 {
			corporateReferenceSourceError(w, storage.ErrCorporateReferenceSourceConflict)
			return
		}
		receipt, err := backend.RetainCorporateReferenceSource(ctx, command, clock.Now)
		if err != nil {
			corporateReferenceSourceError(w, err)
			return
		}
		code := 201
		if receipt.Duplicate {
			code = 200
		}
		write(w, code, receipt)
	})
}

func collectionOf(r storage.CorporateReferenceSourceRecord) string {
	switch {
	case r.Polygon != nil:
		return r.Polygon.Spec.ID
	case r.Issuer != nil:
		return r.Issuer.Spec.ID
	}
	return ""
}
func bindingSHA256(c storage.CorporateReferenceSourceCommand) string {
	switch {
	case c.Polygon != nil:
		return c.Polygon.SHA256
	case c.Issuer != nil:
		return c.Issuer.SHA256
	}
	return ""
}
