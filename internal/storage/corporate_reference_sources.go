package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/issuerdoc"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxCorporateReferenceSources = 32
const MaxCorporateReferenceOriginalBytes = 8 << 20
const maxCorporateReferenceRecordBytes = 16 << 20

const CorporateReferencePolygon = "polygon_reference"
const CorporateReferenceIssuer = "issuer_document"

var ErrCorporateReferenceSourceInvalid = errors.New("invalid corporate reference source custody")
var ErrCorporateReferenceSourceConflict = errors.New("corporate reference source identity conflict")
var ErrCorporateReferenceSourceCapacity = errors.New("corporate reference source custody capacity exhausted")
var ErrCorporateReferenceSourceNotFound = errors.New("corporate reference source not retained")

// A reference command carries one configured-journal binding and its exact
// original bytes. Only the owner adapter for that journal constructs one;
// there is no caller-reported reference source.
type CorporateReferenceSourceCommand struct {
	ID         string                  `json:"id"`
	ReceiverID string                  `json:"receiver_id"`
	Kind       string                  `json:"kind"`
	Polygon    *polygonref.Acquisition `json:"polygon,omitempty"`
	Issuer     *issuerdoc.Acquisition  `json:"issuer,omitempty"`
	Originals  [][]byte                `json:"-"`
}

// The record retains the binding and the parse rederived from the originals at
// retention. The originals are held beside it and reverified on inspection.
type CorporateReferenceSourceRecord struct {
	Schema                   string                  `json:"schema"`
	ID                       string                  `json:"id"`
	ReceiverID               string                  `json:"receiver_id"`
	Kind                     string                  `json:"kind"`
	Polygon                  *polygonref.Acquisition `json:"polygon,omitempty"`
	Issuer                   *issuerdoc.Acquisition  `json:"issuer,omitempty"`
	AcquisitionSHA256        string                  `json:"acquisition_sha256"`
	OriginalSHA256           []string                `json:"original_sha256"`
	OriginalBytes            int                     `json:"original_bytes"`
	ParseStatus              string                  `json:"parse_status"`
	ParseError               string                  `json:"parse_error,omitempty"`
	Snapshot                 *polygonref.Snapshot    `json:"snapshot,omitempty"`
	Document                 *issuerdoc.Document     `json:"document,omitempty"`
	Sequence                 int                     `json:"sequence"`
	RetainedAt               time.Time               `json:"retained_at"`
	PreviousSHA256           string                  `json:"previous_sha256"`
	Authority                string                  `json:"authority"`
	ProviderAttested         bool                    `json:"provider_attested"`
	CoverageComplete         bool                    `json:"coverage_complete"`
	EconomicApplicationReady bool                    `json:"economic_application_ready"`
	AdmissionAvailable       bool                    `json:"admission_available"`
	ExecutionAvailable       bool                    `json:"execution_available"`
	QualificationCredit      bool                    `json:"qualification_credit"`
	SHA256                   string                  `json:"sha256"`
}
type CorporateReferenceSourceReceipt struct {
	Record    CorporateReferenceSourceRecord `json:"record"`
	Duplicate bool                           `json:"duplicate"`
}
type CorporateReferenceSourceCatalog struct {
	ReceiverID string                           `json:"receiver_id"`
	Records    []CorporateReferenceSourceRecord `json:"records"`
}
type CorporateReferenceSourceStore interface {
	RetainCorporateReferenceSource(context.Context, CorporateReferenceSourceCommand, func() time.Time) (CorporateReferenceSourceReceipt, error)
	CorporateReferenceSources(context.Context) (CorporateReferenceSourceCatalog, error)
	// CorporateReferenceSource reverifies the retained originals against the
	// binding and the retained parse before returning the record.
	CorporateReferenceSource(context.Context, string) (CorporateReferenceSourceRecord, error)
}

func (c CorporateReferenceSourceCommand) acquisitionSHA256() string {
	switch {
	case c.Kind == CorporateReferencePolygon && c.Polygon != nil && c.Issuer == nil:
		return c.Polygon.SHA256
	case c.Kind == CorporateReferenceIssuer && c.Issuer != nil && c.Polygon == nil:
		return c.Issuer.SHA256
	}
	return ""
}

// Shape validates the identity and binding receipts without reading originals.
func (c CorporateReferenceSourceCommand) shape() error {
	if !paperaccount.ID.MatchString(c.ID) || len(c.ID) > 40 || !ValidReceiverID(c.ReceiverID) || c.acquisitionSHA256() == "" {
		return ErrCorporateReferenceSourceInvalid
	}
	if c.Polygon != nil && c.Polygon.Check() != nil || c.Issuer != nil && c.Issuer.Check() != nil {
		return ErrCorporateReferenceSourceInvalid
	}
	return nil
}

// derive rederives the parse fields from exact originals. Bytes that do not
// match the binding are invalid; intact bytes that do not parse are retained
// as unparseable rather than partially used.
func derive(r *CorporateReferenceSourceRecord, originals [][]byte) error {
	total := 0
	r.OriginalSHA256 = []string{}
	for _, o := range originals {
		total += len(o)
		r.OriginalSHA256 = append(r.OriginalSHA256, workerplan.Digest(o))
	}
	if total > MaxCorporateReferenceOriginalBytes {
		return ErrCorporateReferenceSourceCapacity
	}
	r.OriginalBytes = total
	var parseErr error
	switch r.Kind {
	case CorporateReferencePolygon:
		snap, err := r.Polygon.Verify(originals)
		if errors.Is(err, polygonref.ErrJournalConflict) {
			return ErrCorporateReferenceSourceInvalid
		}
		if err == nil {
			r.Snapshot = &snap
		}
		parseErr = err
	case CorporateReferenceIssuer:
		if len(originals) != 1 {
			return ErrCorporateReferenceSourceInvalid
		}
		doc, err := r.Issuer.Verify(originals[0])
		if errors.Is(err, issuerdoc.ErrJournalConflict) {
			return ErrCorporateReferenceSourceInvalid
		}
		if err == nil {
			r.Document = &doc
		}
		parseErr = err
	default:
		return ErrCorporateReferenceSourceInvalid
	}
	r.ParseStatus = "parsed"
	if parseErr != nil {
		if !errors.Is(parseErr, polygonref.ErrUnparseable) && !errors.Is(parseErr, issuerdoc.ErrUnparseable) {
			return ErrCorporateReferenceSourceInvalid
		}
		r.ParseStatus, r.ParseError = "unparseable", parseErr.Error()
	}
	return nil
}

func (c CorporateReferenceSourceCommand) completedAt() time.Time {
	if c.Polygon != nil {
		return c.Polygon.CompletedAt
	}
	return c.Issuer.CompletedAt
}

func newCorporateReferenceSource(c CorporateReferenceSourceCommand, seq int, previous string, at time.Time) (CorporateReferenceSourceRecord, error) {
	if c.shape() != nil || seq < 1 || seq > MaxCorporateReferenceSources || !declarationTime(at) || at.Before(c.completedAt()) {
		return CorporateReferenceSourceRecord{}, ErrCorporateReferenceSourceInvalid
	}
	r := CorporateReferenceSourceRecord{Schema: "mantik-owned-corporate-reference-source-v1", ID: c.ID, ReceiverID: c.ReceiverID, Kind: c.Kind, Polygon: c.Polygon, Issuer: c.Issuer, AcquisitionSHA256: c.acquisitionSHA256(), Sequence: seq, RetainedAt: at.UTC(), PreviousSHA256: previous, Authority: "service_custody_of_configured_journal_reference_acquisition_only"}
	if err := derive(&r, c.Originals); err != nil {
		return CorporateReferenceSourceRecord{}, err
	}
	r = clone(r)
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > maxCorporateReferenceRecordBytes {
		return CorporateReferenceSourceRecord{}, ErrCorporateReferenceSourceCapacity
	}
	return r, nil
}

func (r CorporateReferenceSourceRecord) command() CorporateReferenceSourceCommand {
	return CorporateReferenceSourceCommand{ID: r.ID, ReceiverID: r.ReceiverID, Kind: r.Kind, Polygon: r.Polygon, Issuer: r.Issuer}
}

// recordShape checks a retained record's chain position, seal and binding
// receipts. Original bytes are checked separately by reverify.
func recordShape(r CorporateReferenceSourceRecord, owner string, seq int, previous string) error {
	c := r.command()
	if r.Schema != "mantik-owned-corporate-reference-source-v1" || c.shape() != nil || r.ReceiverID != owner || r.Sequence != seq || r.PreviousSHA256 != previous || r.AcquisitionSHA256 != c.acquisitionSHA256() || !declarationTime(r.RetainedAt) || r.RetainedAt.Before(c.completedAt()) || r.Authority != "service_custody_of_configured_journal_reference_acquisition_only" || r.ProviderAttested || r.CoverageComplete || r.EconomicApplicationReady || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		return ErrCorporateReferenceSourceInvalid
	}
	if r.ParseStatus != "parsed" && r.ParseStatus != "unparseable" || (r.Snapshot != nil) != (r.Kind == CorporateReferencePolygon && r.ParseStatus == "parsed") || (r.Document != nil) != (r.Kind == CorporateReferenceIssuer && r.ParseStatus == "parsed") {
		return ErrCorporateReferenceSourceInvalid
	}
	sealed := r
	sealed.SHA256 = ""
	if workerplan.Hash(sealed) != r.SHA256 {
		return ErrCorporateReferenceSourceInvalid
	}
	return nil
}

// reverify rederives the record from its originals and requires equality.
func reverify(r CorporateReferenceSourceRecord, originals [][]byte) error {
	c := r.command()
	c.Originals = originals
	want, err := newCorporateReferenceSource(c, r.Sequence, r.PreviousSHA256, r.RetainedAt)
	if err != nil || !reflect.DeepEqual(want, r) {
		return ErrCorporateReferenceSourceInvalid
	}
	return nil
}

func corporateReferenceSourceCatalog(ctx context.Context, owner string, records []CorporateReferenceSourceRecord) (CorporateReferenceSourceCatalog, error) {
	if !ValidReceiverID(owner) || len(records) > MaxCorporateReferenceSources {
		return CorporateReferenceSourceCatalog{}, ErrCorporateReferenceSourceInvalid
	}
	out := CorporateReferenceSourceCatalog{ReceiverID: owner, Records: []CorporateReferenceSourceRecord{}}
	ids, acquisitions := map[string]bool{}, map[string]bool{}
	previous := ""
	var clock time.Time
	for i, r := range records {
		if err := ctx.Err(); err != nil {
			return CorporateReferenceSourceCatalog{}, err
		}
		if recordShape(r, owner, i+1, previous) != nil || r.RetainedAt.Before(clock) || ids[r.ID] || acquisitions[r.AcquisitionSHA256] {
			return CorporateReferenceSourceCatalog{}, ErrCorporateReferenceSourceInvalid
		}
		ids[r.ID], acquisitions[r.AcquisitionSHA256] = true, true
		previous, clock = r.SHA256, r.RetainedAt
		out.Records = append(out.Records, clone(r))
	}
	return out, nil
}

// appendCorporateReferenceSource returns an exact retry as a duplicate and
// refuses a changed binding under the same identity or a binding reused under
// another identity.
func appendCorporateReferenceSource(ctx context.Context, cat CorporateReferenceSourceCatalog, c CorporateReferenceSourceCommand, now func() time.Time) (CorporateReferenceSourceReceipt, error) {
	if c.shape() != nil {
		return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceInvalid
	}
	if c.ReceiverID != cat.ReceiverID {
		return CorporateReferenceSourceReceipt{}, ErrReceiverMismatch
	}
	for _, r := range cat.Records {
		if r.ID == c.ID {
			if r.Kind != c.Kind || r.AcquisitionSHA256 != c.acquisitionSHA256() {
				return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceConflict
			}
			return CorporateReferenceSourceReceipt{clone(r), true}, nil
		}
		if r.AcquisitionSHA256 == c.acquisitionSHA256() {
			return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceConflict
		}
	}
	if len(cat.Records) >= MaxCorporateReferenceSources {
		return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceCapacity
	}
	if now == nil {
		return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceInvalid
	}
	at, previous := now(), ""
	if n := len(cat.Records); n > 0 {
		if at.Before(cat.Records[n-1].RetainedAt) {
			return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceInvalid
		}
		previous = cat.Records[n-1].SHA256
	}
	r, err := newCorporateReferenceSource(c, len(cat.Records)+1, previous, at)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	return CorporateReferenceSourceReceipt{r, false}, nil
}

func copyOriginals(originals [][]byte) [][]byte {
	out := make([][]byte, len(originals))
	for i, o := range originals {
		out[i] = append([]byte{}, o...)
	}
	return out
}

func (m *Memory) CorporateReferenceSources(ctx context.Context) (CorporateReferenceSourceCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	return corporateReferenceSourceCatalog(ctx, m.receiverID, m.corporateReferenceSources)
}
func (m *Memory) CorporateReferenceSource(ctx context.Context, id string) (CorporateReferenceSourceRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cat, err := corporateReferenceSourceCatalog(ctx, m.receiverID, m.corporateReferenceSources)
	if err != nil {
		return CorporateReferenceSourceRecord{}, err
	}
	for _, r := range cat.Records {
		if r.ID == id {
			if err := reverify(r, m.corporateReferenceOriginals[id]); err != nil {
				return CorporateReferenceSourceRecord{}, err
			}
			return r, nil
		}
	}
	return CorporateReferenceSourceRecord{}, ErrCorporateReferenceSourceNotFound
}
func (m *Memory) RetainCorporateReferenceSource(ctx context.Context, c CorporateReferenceSourceCommand, now func() time.Time) (CorporateReferenceSourceReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cat, err := corporateReferenceSourceCatalog(ctx, m.receiverID, m.corporateReferenceSources)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	r, err := appendCorporateReferenceSource(ctx, cat, c, now)
	if err != nil || r.Duplicate {
		return r, err
	}
	if r.Record.RetainedAt.Before(m.collectors.LastClock) {
		return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceInvalid
	}
	if err := m.sourceObservationClockLocked(r.Record.RetainedAt); err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	if m.corporateReferenceOriginals == nil {
		m.corporateReferenceOriginals = map[string][][]byte{}
	}
	m.corporateReferenceSources = append(m.corporateReferenceSources, clone(r.Record))
	m.corporateReferenceOriginals[r.Record.ID] = copyOriginals(c.Originals)
	m.collectors.LastClock = r.Record.RetainedAt
	return r, nil
}

func readCorporateReferenceSources(ctx context.Context, tx pgx.Tx, lock bool) (CorporateReferenceSourceCatalog, error) {
	var owner string
	q := "SELECT id FROM mantik_research_receiver WHERE singleton"
	if lock {
		q += " FOR UPDATE"
	}
	if err := tx.QueryRow(ctx, q).Scan(&owner); err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,sequence,acquisition_sha256,payload FROM mantik_corporate_reference_sources ORDER BY sequence LIMIT $1", MaxCorporateReferenceSources+1)
	if err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	defer rows.Close()
	all := []CorporateReferenceSourceRecord{}
	for rows.Next() {
		var id, acquisition string
		var seq int
		var raw []byte
		var r CorporateReferenceSourceRecord
		if err := rows.Scan(&id, &seq, &acquisition, &raw); err != nil {
			return CorporateReferenceSourceCatalog{}, err
		}
		if workerplan.ReadBoundedShape(raw, &r, maxCorporateReferenceRecordBytes) != nil || r.ID != id || r.Sequence != seq || r.AcquisitionSHA256 != acquisition {
			return CorporateReferenceSourceCatalog{}, ErrCorporateReferenceSourceInvalid
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	return corporateReferenceSourceCatalog(ctx, owner, all)
}
func (p *Postgres) CorporateReferenceSources(ctx context.Context) (CorporateReferenceSourceCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	cat, err := readCorporateReferenceSources(ctx, tx, false)
	if err != nil {
		return CorporateReferenceSourceCatalog{}, err
	}
	return cat, tx.Commit(ctx)
}
func (p *Postgres) CorporateReferenceSource(ctx context.Context, id string) (CorporateReferenceSourceRecord, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CorporateReferenceSourceRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	cat, err := readCorporateReferenceSources(ctx, tx, false)
	if err != nil {
		return CorporateReferenceSourceRecord{}, err
	}
	for _, r := range cat.Records {
		if r.ID != id {
			continue
		}
		var originals [][]byte
		if err := tx.QueryRow(ctx, "SELECT originals FROM mantik_corporate_reference_sources WHERE id=$1", id).Scan(&originals); err != nil {
			return CorporateReferenceSourceRecord{}, err
		}
		if err := reverify(r, originals); err != nil {
			return CorporateReferenceSourceRecord{}, err
		}
		return r, tx.Commit(ctx)
	}
	return CorporateReferenceSourceRecord{}, ErrCorporateReferenceSourceNotFound
}
func (p *Postgres) RetainCorporateReferenceSource(ctx context.Context, c CorporateReferenceSourceCommand, now func() time.Time) (CorporateReferenceSourceReceipt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Same lock order as corporate-action sources and evaluation issuance:
	// collector first, then receiver.
	feed, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	cat, err := readCorporateReferenceSources(ctx, tx, true)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	r, err := appendCorporateReferenceSource(ctx, cat, c, now)
	if err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	if !r.Duplicate {
		if r.Record.RetainedAt.Before(feed.Collectors.LastClock) {
			return CorporateReferenceSourceReceipt{}, ErrCorporateReferenceSourceInvalid
		}
		if err := readSourceObservationClock(ctx, tx, cat.ReceiverID, r.Record.RetainedAt); err != nil {
			return CorporateReferenceSourceReceipt{}, err
		}
		raw, err := json.Marshal(r.Record)
		if err != nil {
			return CorporateReferenceSourceReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_corporate_reference_sources(id,sequence,acquisition_sha256,payload,originals) VALUES($1,$2,$3,$4,$5)", r.Record.ID, r.Record.Sequence, r.Record.AcquisitionSHA256, raw, c.Originals); err != nil {
			return CorporateReferenceSourceReceipt{}, err
		}
		feed.Collectors.LastClock = r.Record.RetainedAt
		encoded, err := json.Marshal(feed.Collectors)
		if err != nil {
			return CorporateReferenceSourceReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", encoded); err != nil {
			return CorporateReferenceSourceReceipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return CorporateReferenceSourceReceipt{}, err
	}
	return r, nil
}
