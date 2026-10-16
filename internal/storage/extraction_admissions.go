package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type ExtractionAdmissionStore interface {
	AdmitExtraction(context.Context, extraction.AdmissionCommand, func() time.Time) (extraction.Admission, error)
	ExtractionAdmission(context.Context, string) (extraction.Admission, error)
}

func prepareExtractionAdmission(s newscase.State, a extraction.Attempt, c extraction.AdmissionCommand, now func() time.Time) (newscase.State, extraction.Admission, error) {
	r, err := extraction.PrepareAdmission(c, a)
	if err != nil {
		return s, extraction.Admission{}, err
	}
	key := newscase.RecordKey(r)
	// Never adopt an untracked record as an approval, even if its bytes match.
	if _, exists := s.Records[key]; exists {
		return s, extraction.Admission{}, extraction.ErrConflict
	}
	at := now().UTC()
	if at.Before(a.CompletedAt) {
		return s, extraction.Admission{}, extraction.ErrConflict
	}
	next, _, err := newscase.Admit(s, r, at)
	if err != nil {
		return s, extraction.Admission{}, err
	}
	approval, err := extraction.MakeAdmission(c, a, next.Records[key])
	return next, approval, err
}
func validateExtractionAdmission(r extraction.Admission, c extraction.AdmissionCommand, a extraction.Attempt, s newscase.State) error {
	if !reflect.DeepEqual(r.Command, c) {
		return extraction.ErrConflict
	}
	record, ok := s.Records[r.NewsReceipt.RecordKey]
	if !ok {
		return extraction.ErrInvalid
	}
	return r.Validate(a, record)
}
func (m *Memory) AdmitExtraction(ctx context.Context, c extraction.AdmissionCommand, now func() time.Time) (extraction.Admission, error) {
	if err := ctx.Err(); err != nil {
		return extraction.Admission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID || !ValidReceiverID(c.ReceiverID) {
		return extraction.Admission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.AttemptID) {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	a, ok := m.extractions[c.AttemptID]
	if !ok {
		return extraction.Admission{}, extraction.ErrNotFound
	}
	if a.ID != c.AttemptID || a.ReceiverID != m.receiverID {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	if r, ok := m.extractionAdmissions[c.AttemptID]; ok {
		if err := validateExtractionAdmission(r, c, a, m.news); err != nil {
			return extraction.Admission{}, err
		}
		return clone(r), nil
	}
	if len(m.extractionAdmissions) >= extraction.MaxAttempts {
		return extraction.Admission{}, extraction.ErrCapacity
	}
	next, r, err := prepareExtractionAdmission(m.news, a, c, now)
	if err != nil {
		return extraction.Admission{}, err
	}
	m.news = next
	m.extractionAdmissions[c.AttemptID] = clone(r)
	return clone(r), nil
}
func (m *Memory) ExtractionAdmission(ctx context.Context, id string) (extraction.Admission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return extraction.Admission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.extractionAdmissions[id]
	if !ok {
		return extraction.Admission{}, extraction.ErrNotFound
	}
	a, ok := m.extractions[id]
	if !ok || a.ID != id || a.ReceiverID != m.receiverID || r.Command.AttemptID != id || r.Command.ReceiverID != m.receiverID {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	if err := validateExtractionAdmission(r, r.Command, a, m.news); err != nil {
		return extraction.Admission{}, err
	}
	return clone(r), nil
}

func loadExtractionAdmission(ctx context.Context, tx pgx.Tx, id string) (extraction.Admission, error) {
	var raw []byte
	var r extraction.Admission
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_news_extraction_admissions WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, extraction.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if workerplan.ReadBoundedShape(raw, &r, 64<<10) != nil || r.Command.AttemptID != id || r.ValidateMetadata() != nil {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	return r, nil
}
func (p *Postgres) AdmitExtraction(ctx context.Context, c extraction.AdmissionCommand, now func() time.Time) (extraction.Admission, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return extraction.Admission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.AttemptID) {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.Admission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	// Lock order is receiver, then news. Existing news/review mutations only
	// read receiver identity without a row lock, so they cannot invert this order.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return extraction.Admission{}, err
	}
	if receiver != c.ReceiverID {
		return extraction.Admission{}, ErrReceiverMismatch
	}
	a, err := readExtraction(ctx, tx, c.AttemptID, receiver)
	if err != nil {
		return extraction.Admission{}, err
	}
	var raw []byte
	var s newscase.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return extraction.Admission{}, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return extraction.Admission{}, err
	}
	r, err := loadExtractionAdmission(ctx, tx, c.AttemptID)
	if err == nil {
		if err = validateExtractionAdmission(r, c, a, s); err != nil {
			return extraction.Admission{}, err
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, extraction.ErrNotFound) {
		return extraction.Admission{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_news_extraction_admissions").Scan(&count); err != nil {
		return extraction.Admission{}, err
	}
	if count >= extraction.MaxAttempts {
		return extraction.Admission{}, extraction.ErrCapacity
	}
	next, r, err := prepareExtractionAdmission(s, a, c, now)
	if err != nil {
		return extraction.Admission{}, err
	}
	raw, _ = json.Marshal(next)
	if _, err = tx.Exec(ctx, "UPDATE mantik_news_cases SET payload=$1 WHERE singleton", raw); err != nil {
		return extraction.Admission{}, err
	}
	raw, _ = json.Marshal(r)
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_news_extraction_admissions(id,payload) VALUES($1,$2)", c.AttemptID, raw); err != nil {
		return extraction.Admission{}, err
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) ExtractionAdmission(ctx context.Context, id string) (extraction.Admission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.Admission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.Admission{}, err
	}
	r, err := loadExtractionAdmission(ctx, tx, id)
	if err != nil {
		return r, err
	}
	a, err := readExtraction(ctx, tx, id, receiver)
	if err != nil {
		return extraction.Admission{}, err
	}
	var raw []byte
	var record newscase.Record
	if err = tx.QueryRow(ctx, "SELECT payload->'records'->$1::text FROM mantik_news_cases WHERE singleton", r.NewsReceipt.RecordKey).Scan(&raw); err != nil {
		return extraction.Admission{}, err
	}
	if json.Unmarshal(raw, &record) != nil || r.Command.ReceiverID != receiver || r.Validate(a, record) != nil {
		return extraction.Admission{}, extraction.ErrInvalid
	}
	return r, tx.Commit(ctx)
}

var _ ExtractionAdmissionStore = (*Memory)(nil)
var _ ExtractionAdmissionStore = (*Postgres)(nil)
