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

type LocalCaptureAdmissionStore interface {
	AdmitLocalCapture(context.Context, extraction.LocalAdmissionCommand, func() time.Time) (extraction.LocalAdmission, error)
	LocalCaptureAdmission(context.Context, string) (extraction.LocalAdmission, error)
}

func prepareLocalCaptureAdmission(s newscase.State, a extraction.LocalCapture, c extraction.LocalAdmissionCommand, now func() time.Time) (newscase.State, extraction.LocalAdmission, error) {
	r, err := extraction.PrepareLocalAdmission(c, a)
	if err != nil {
		return s, extraction.LocalAdmission{}, err
	}
	key := newscase.RecordKey(r)
	// Never adopt an untracked record as an approval, even if its bytes match.
	if _, exists := s.Records[key]; exists {
		return s, extraction.LocalAdmission{}, extraction.ErrConflict
	}
	at := now().UTC()
	if at.Before(a.ImportedAt) {
		return s, extraction.LocalAdmission{}, extraction.ErrConflict
	}
	next, _, err := newscase.Admit(s, r, at)
	if err != nil {
		return s, extraction.LocalAdmission{}, err
	}
	approval, err := extraction.MakeLocalAdmission(c, a, next.Records[key])
	return next, approval, err
}
func validateLocalCaptureAdmission(r extraction.LocalAdmission, c extraction.LocalAdmissionCommand, a extraction.LocalCapture, s newscase.State) error {
	if !reflect.DeepEqual(r.Command, c) {
		return extraction.ErrConflict
	}
	record, ok := s.Records[r.NewsReceipt.RecordKey]
	if !ok {
		return extraction.ErrInvalid
	}
	return r.Validate(a, record)
}
func (m *Memory) AdmitLocalCapture(ctx context.Context, c extraction.LocalAdmissionCommand, now func() time.Time) (extraction.LocalAdmission, error) {
	if err := ctx.Err(); err != nil {
		return extraction.LocalAdmission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID || !ValidReceiverID(c.ReceiverID) {
		return extraction.LocalAdmission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.CaptureID) {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	a, ok := m.localCaptures[c.CaptureID]
	if !ok {
		return extraction.LocalAdmission{}, extraction.ErrNotFound
	}
	if a.ID != c.CaptureID || a.ReceiverID != m.receiverID {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	if r, ok := m.localCaptureAdmissions[c.CaptureID]; ok {
		if err := validateLocalCaptureAdmission(r, c, a, m.news); err != nil {
			return extraction.LocalAdmission{}, err
		}
		return clone(r), nil
	}
	if len(m.localCaptureAdmissions) >= extraction.MaxAttempts {
		return extraction.LocalAdmission{}, extraction.ErrCapacity
	}
	next, r, err := prepareLocalCaptureAdmission(m.news, a, c, now)
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	if err := ctx.Err(); err != nil {
		return extraction.LocalAdmission{}, err
	}
	m.news = next
	m.localCaptureAdmissions[c.CaptureID] = clone(r)
	return clone(r), nil
}
func (m *Memory) LocalCaptureAdmission(ctx context.Context, id string) (extraction.LocalAdmission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return extraction.LocalAdmission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.localCaptureAdmissions[id]
	if !ok {
		return extraction.LocalAdmission{}, extraction.ErrNotFound
	}
	a, ok := m.localCaptures[id]
	if !ok || a.ID != id || a.ReceiverID != m.receiverID || r.Command.CaptureID != id || r.Command.ReceiverID != m.receiverID {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	if err := validateLocalCaptureAdmission(r, r.Command, a, m.news); err != nil {
		return extraction.LocalAdmission{}, err
	}
	return clone(r), nil
}

func loadLocalCaptureAdmission(ctx context.Context, tx pgx.Tx, id string) (extraction.LocalAdmission, error) {
	var raw []byte
	var r extraction.LocalAdmission
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_local_capture_admissions WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, extraction.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if workerplan.ReadBoundedShape(raw, &r, 64<<10) != nil || r.Command.CaptureID != id || r.ValidateMetadata() != nil {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	return r, nil
}
func (p *Postgres) AdmitLocalCapture(ctx context.Context, c extraction.LocalAdmissionCommand, now func() time.Time) (extraction.LocalAdmission, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return extraction.LocalAdmission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.CaptureID) {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	// Lock order is receiver, then news. Existing news/review mutations only
	// read receiver identity without a row lock, so they cannot invert this order.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return extraction.LocalAdmission{}, err
	}
	if receiver != c.ReceiverID {
		return extraction.LocalAdmission{}, ErrReceiverMismatch
	}
	a, err := readLocalCapture(ctx, tx, c.CaptureID, receiver)
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	var raw []byte
	var s newscase.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return extraction.LocalAdmission{}, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return extraction.LocalAdmission{}, err
	}
	r, err := loadLocalCaptureAdmission(ctx, tx, c.CaptureID)
	if err == nil {
		if err = validateLocalCaptureAdmission(r, c, a, s); err != nil {
			return extraction.LocalAdmission{}, err
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, extraction.ErrNotFound) {
		return extraction.LocalAdmission{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_local_capture_admissions").Scan(&count); err != nil {
		return extraction.LocalAdmission{}, err
	}
	if count >= extraction.MaxAttempts {
		return extraction.LocalAdmission{}, extraction.ErrCapacity
	}
	next, r, err := prepareLocalCaptureAdmission(s, a, c, now)
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	raw, _ = json.Marshal(next)
	if _, err = tx.Exec(ctx, "UPDATE mantik_news_cases SET payload=$1 WHERE singleton", raw); err != nil {
		return extraction.LocalAdmission{}, err
	}
	raw, _ = json.Marshal(r)
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_local_capture_admissions(id,payload) VALUES($1,$2)", c.CaptureID, raw); err != nil {
		return extraction.LocalAdmission{}, err
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) LocalCaptureAdmission(ctx context.Context, id string) (extraction.LocalAdmission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.LocalAdmission{}, err
	}
	r, err := loadLocalCaptureAdmission(ctx, tx, id)
	if err != nil {
		return r, err
	}
	a, err := readLocalCapture(ctx, tx, id, receiver)
	if err != nil {
		return extraction.LocalAdmission{}, err
	}
	var raw []byte
	var record newscase.Record
	if err = tx.QueryRow(ctx, "SELECT payload->'records'->$1::text FROM mantik_news_cases WHERE singleton", r.NewsReceipt.RecordKey).Scan(&raw); err != nil {
		return extraction.LocalAdmission{}, err
	}
	if json.Unmarshal(raw, &record) != nil || r.Command.ReceiverID != receiver || r.Validate(a, record) != nil {
		return extraction.LocalAdmission{}, extraction.ErrInvalid
	}
	return r, tx.Commit(ctx)
}

var _ LocalCaptureAdmissionStore = (*Memory)(nil)
var _ LocalCaptureAdmissionStore = (*Postgres)(nil)
