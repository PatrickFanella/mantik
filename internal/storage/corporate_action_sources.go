package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxCorporateActionSources = 32
const MaxCorporateActionSourceBytes = 2*alpacarecord.MaxBytes + 32768

var ErrCorporateActionSourceInvalid = errors.New("invalid corporate-action source custody")
var ErrCorporateActionSourceConflict = errors.New("corporate-action source identity conflict")
var ErrCorporateActionSourceCapacity = errors.New("corporate-action source custody capacity exhausted")

// Original recordings are caller reports. This operation retains source bytes,
// never an economic interpretation, provider attestation or an admission.
type CorporateActionSourceCommand struct {
	ID         string                 `json:"id"`
	ReceiverID string                 `json:"receiver_id"`
	Recording  alpacarecord.Recording `json:"recording"`
}

func (c CorporateActionSourceCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || c.Recording.Capability != "corporate_actions" || c.Recording.RawRef == "" || len(c.Recording.RawRef) > 2048 || !utf8.ValidString(c.Recording.RawRef) || strings.ContainsFunc(c.Recording.RawRef, unicode.IsControl) || c.Recording.AccessRef == "" || len(c.Recording.AccessRef) > 256 || !utf8.ValidString(c.Recording.AccessRef) || strings.ContainsFunc(c.Recording.AccessRef, unicode.IsControl) {
		return ErrCorporateActionSourceInvalid
	}
	n, err := alpacarecord.NormalizeCorporate(c.Recording)
	if err != nil || n == nil {
		return ErrCorporateActionSourceInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxCorporateActionSourceBytes {
		return ErrCorporateActionSourceInvalid
	}
	return nil
}

type CorporateActionSourceRecord struct {
	Acquisition              *alpacarecord.CorporateActionAcquisition `json:"acquisition,omitempty"`
	Schema                   string                                   `json:"schema"`
	Command                  CorporateActionSourceCommand             `json:"command"`
	Sequence                 int                                      `json:"sequence"`
	RetainedAt               time.Time                                `json:"retained_at"`
	PreviousSHA256           string                                   `json:"previous_sha256"`
	Authority                string                                   `json:"authority"`
	ProviderAttested         bool                                     `json:"provider_attested"`
	CoverageComplete         bool                                     `json:"coverage_complete"`
	EconomicApplicationReady bool                                     `json:"economic_application_ready"`
	AdmissionAvailable       bool                                     `json:"admission_available"`
	ExecutionAvailable       bool                                     `json:"execution_available"`
	QualificationCredit      bool                                     `json:"qualification_credit"`
	SHA256                   string                                   `json:"sha256"`
}
type CorporateActionSourceReceipt struct {
	Record    CorporateActionSourceRecord `json:"record"`
	Duplicate bool                        `json:"duplicate"`
}
type CorporateActionSourceCatalog struct {
	ReceiverID string                        `json:"receiver_id"`
	Records    []CorporateActionSourceRecord `json:"records"`
}
type CorporateActionSourceStore interface {
	RetainCorporateActionSource(context.Context, CorporateActionSourceCommand, func() time.Time) (CorporateActionSourceReceipt, error)
	CorporateActionSources(context.Context) (CorporateActionSourceCatalog, error)
}

// Only an owner adapter reading its configured journal supplies this proof.
// The public caller-reported source command has no acquisition assertion field.
type JournalCorporateActionSourceStore interface {
	CorporateActionSourceStore
	RetainJournalCorporateActionSource(context.Context, CorporateActionSourceCommand, alpacarecord.CorporateActionAcquisition, func() time.Time) (CorporateActionSourceReceipt, error)
}

func newCorporateActionSource(c CorporateActionSourceCommand, seq int, previous string, at time.Time) (CorporateActionSourceRecord, error) {
	if c.Validate() != nil || seq < 1 || !declarationTime(at) || at.Before(c.Recording.ReceivedAt) {
		return CorporateActionSourceRecord{}, ErrCorporateActionSourceInvalid
	}
	r := CorporateActionSourceRecord{Schema: "mantik-owned-corporate-action-source-v1", Command: clone(c), Sequence: seq, RetainedAt: at.UTC(), PreviousSHA256: previous, Authority: "service_custody_of_caller_reported_corporate_action_source_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}

func corporateActionSourceVersion(c CorporateActionSourceCommand, seq int, previous string, at time.Time, acquisition *alpacarecord.CorporateActionAcquisition) (CorporateActionSourceRecord, error) {
	r, err := newCorporateActionSource(c, seq, previous, at)
	if err != nil || acquisition == nil {
		return r, err
	}
	if acquisition.Validate(c.Recording) != nil || at.Before(acquisition.AvailableAt) {
		return CorporateActionSourceRecord{}, ErrCorporateActionSourceInvalid
	}
	a := clone(*acquisition)
	r.Acquisition = &a
	r.Schema = "mantik-owned-corporate-action-source-v2"
	r.Authority = "service_custody_of_configured_journal_acquisition_only"
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxCorporateActionSourceBytes+4096 {
		return CorporateActionSourceRecord{}, ErrCorporateActionSourceCapacity
	}
	return r, nil
}
func corporateActionSourceCatalog(ctx context.Context, owner string, records []CorporateActionSourceRecord) (CorporateActionSourceCatalog, error) {
	if !ValidReceiverID(owner) || len(records) > MaxCorporateActionSources {
		return CorporateActionSourceCatalog{}, ErrCorporateActionSourceInvalid
	}
	out := CorporateActionSourceCatalog{ReceiverID: owner, Records: []CorporateActionSourceRecord{}}
	ids, originals := map[string]bool{}, map[string]bool{}
	var previous string
	var clock time.Time
	for i, r := range records {
		if err := ctx.Err(); err != nil {
			return CorporateActionSourceCatalog{}, err
		}
		want, err := corporateActionSourceVersion(r.Command, i+1, previous, r.RetainedAt, r.Acquisition)
		if err != nil || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) || r.RetainedAt.Before(clock) || ids[r.Command.ID] || originals[r.Command.Recording.RawRef] {
			return CorporateActionSourceCatalog{}, ErrCorporateActionSourceInvalid
		}
		ids[r.Command.ID] = true
		originals[r.Command.Recording.RawRef] = true
		previous = r.SHA256
		clock = r.RetainedAt
		out.Records = append(out.Records, clone(r))
	}
	return out, nil
}
func appendCorporateActionSource(ctx context.Context, owner string, all []CorporateActionSourceRecord, c CorporateActionSourceCommand, now func() time.Time) (CorporateActionSourceReceipt, error) {
	return appendCorporateActionSourceWithAcquisition(ctx, owner, all, c, nil, now)
}
func appendCorporateActionSourceWithAcquisition(ctx context.Context, owner string, all []CorporateActionSourceRecord, c CorporateActionSourceCommand, acquisition *alpacarecord.CorporateActionAcquisition, now func() time.Time) (CorporateActionSourceReceipt, error) {
	cat, err := corporateActionSourceCatalog(ctx, owner, all)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	if c.Validate() != nil || acquisition != nil && acquisition.Validate(c.Recording) != nil {
		return CorporateActionSourceReceipt{}, ErrCorporateActionSourceInvalid
	}
	if c.ReceiverID != owner {
		return CorporateActionSourceReceipt{}, ErrReceiverMismatch
	}
	for _, r := range cat.Records {
		if r.Command.ID == c.ID {
			if !reflect.DeepEqual(r.Command, c) || !reflect.DeepEqual(r.Acquisition, acquisition) {
				return CorporateActionSourceReceipt{}, ErrCorporateActionSourceConflict
			}
			return CorporateActionSourceReceipt{clone(r), true}, nil
		}
		if r.Command.Recording.RawRef == c.Recording.RawRef {
			return CorporateActionSourceReceipt{}, ErrCorporateActionSourceConflict
		}
	}
	if len(all) >= MaxCorporateActionSources {
		return CorporateActionSourceReceipt{}, ErrCorporateActionSourceCapacity
	}
	if now == nil {
		return CorporateActionSourceReceipt{}, ErrCorporateActionSourceInvalid
	}
	at := now()
	previous := ""
	if len(all) > 0 {
		last := all[len(all)-1]
		if at.Before(last.RetainedAt) {
			return CorporateActionSourceReceipt{}, ErrCorporateActionSourceInvalid
		}
		previous = last.SHA256
	}
	r, err := corporateActionSourceVersion(c, len(all)+1, previous, at, acquisition)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	return CorporateActionSourceReceipt{r, false}, nil
}
func (m *Memory) CorporateActionSources(ctx context.Context) (CorporateActionSourceCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	return corporateActionSourceCatalog(ctx, m.receiverID, m.corporateActionSources)
}
func (m *Memory) RetainCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, now func() time.Time) (CorporateActionSourceReceipt, error) {
	return m.retainCorporateActionSource(ctx, c, nil, now)
}
func (m *Memory) RetainJournalCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, a alpacarecord.CorporateActionAcquisition, now func() time.Time) (CorporateActionSourceReceipt, error) {
	return m.retainCorporateActionSource(ctx, c, &a, now)
}
func (m *Memory) retainCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, acquisition *alpacarecord.CorporateActionAcquisition, now func() time.Time) (CorporateActionSourceReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	r, err := appendCorporateActionSourceWithAcquisition(ctx, m.receiverID, m.corporateActionSources, c, acquisition, now)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	if !r.Duplicate {
		if r.Record.RetainedAt.Before(m.collectors.LastClock) {
			return CorporateActionSourceReceipt{}, ErrCorporateActionSourceInvalid
		}
		if err := m.sourceObservationClockLocked(r.Record.RetainedAt); err != nil {
			return CorporateActionSourceReceipt{}, err
		}
		m.corporateActionSources = append(m.corporateActionSources, clone(r.Record))
		m.collectors.LastClock = r.Record.RetainedAt
	}
	return r, nil
}
func readCorporateActionSources(ctx context.Context, tx pgx.Tx, lock bool) (CorporateActionSourceCatalog, error) {
	var owner string
	q := "SELECT id FROM mantik_research_receiver WHERE singleton"
	if lock {
		q += " FOR UPDATE"
	}
	if err := tx.QueryRow(ctx, q).Scan(&owner); err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,sequence,payload FROM mantik_corporate_action_sources ORDER BY sequence LIMIT $1", MaxCorporateActionSources+1)
	if err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	defer rows.Close()
	all := []CorporateActionSourceRecord{}
	for rows.Next() {
		var id string
		var seq int
		var raw []byte
		var r CorporateActionSourceRecord
		if err := rows.Scan(&id, &seq, &raw); err != nil {
			return CorporateActionSourceCatalog{}, err
		}
		if workerplan.ReadBoundedShape(raw, &r, MaxCorporateActionSourceBytes+4096) != nil || r.Command.ID != id || r.Sequence != seq {
			return CorporateActionSourceCatalog{}, ErrCorporateActionSourceInvalid
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	return corporateActionSourceCatalog(ctx, owner, all)
}
func (p *Postgres) CorporateActionSources(ctx context.Context) (CorporateActionSourceCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	cat, err := readCorporateActionSources(ctx, tx, false)
	if err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CorporateActionSourceCatalog{}, err
	}
	return cat, nil
}
func (p *Postgres) RetainCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, now func() time.Time) (CorporateActionSourceReceipt, error) {
	return p.retainCorporateActionSource(ctx, c, nil, now)
}
func (p *Postgres) RetainJournalCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, a alpacarecord.CorporateActionAcquisition, now func() time.Time) (CorporateActionSourceReceipt, error) {
	return p.retainCorporateActionSource(ctx, c, &a, now)
}
func (p *Postgres) retainCorporateActionSource(ctx context.Context, c CorporateActionSourceCommand, acquisition *alpacarecord.CorporateActionAcquisition, now func() time.Time) (CorporateActionSourceReceipt, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Match decision/outcome/assessment issuance lock order: collector first,
	// then receiver. A later source write cannot enter an already retained cut.
	feed, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	cat, err := readCorporateActionSources(ctx, tx, true)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	r, err := appendCorporateActionSourceWithAcquisition(ctx, cat.ReceiverID, cat.Records, c, acquisition, now)
	if err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	if !r.Duplicate {
		if r.Record.RetainedAt.Before(feed.Collectors.LastClock) {
			return CorporateActionSourceReceipt{}, ErrCorporateActionSourceInvalid
		}
		if err := readSourceObservationClock(ctx, tx, cat.ReceiverID, r.Record.RetainedAt); err != nil {
			return CorporateActionSourceReceipt{}, err
		}
		raw, err := json.Marshal(r.Record)
		if err != nil {
			return CorporateActionSourceReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_corporate_action_sources(id,sequence,payload) VALUES($1,$2,$3)", r.Record.Command.ID, r.Record.Sequence, raw); err != nil {
			return CorporateActionSourceReceipt{}, err
		}
		feed.Collectors.LastClock = r.Record.RetainedAt
		encoded, err := json.Marshal(feed.Collectors)
		if err != nil {
			return CorporateActionSourceReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=$1 WHERE singleton", encoded); err != nil {
			return CorporateActionSourceReceipt{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return CorporateActionSourceReceipt{}, err
	}
	return r, nil
}
