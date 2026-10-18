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

type SwitchyardCaptureAdmissionStore interface {
	AdmitSwitchyardCapture(context.Context, extraction.SwitchyardAdmissionCommand, func() time.Time) (extraction.SwitchyardAdmission, error)
	SwitchyardCaptureAdmission(context.Context, string) (extraction.SwitchyardAdmission, error)
}

func prepareSwitchyardCaptureAdmission(s newscase.State, a extraction.SwitchyardCapture, c extraction.SwitchyardAdmissionCommand, now func() time.Time) (newscase.State, extraction.SwitchyardAdmission, error) {
	r, err := extraction.PrepareSwitchyardAdmission(c, a)
	if err != nil {
		return s, extraction.SwitchyardAdmission{}, err
	}
	key := newscase.RecordKey(r)
	// Never adopt an untracked record as an approval, even if its bytes match.
	if _, exists := s.Records[key]; exists {
		return s, extraction.SwitchyardAdmission{}, extraction.ErrConflict
	}
	at := now().UTC()
	if at.Before(a.ImportedAt) {
		return s, extraction.SwitchyardAdmission{}, extraction.ErrConflict
	}
	next, _, err := newscase.Admit(s, r, at)
	if err != nil {
		return s, extraction.SwitchyardAdmission{}, err
	}
	approval, err := extraction.MakeSwitchyardAdmission(c, a, next.Records[key])
	return next, approval, err
}
func validateSwitchyardCaptureAdmission(r extraction.SwitchyardAdmission, c extraction.SwitchyardAdmissionCommand, a extraction.SwitchyardCapture, s newscase.State) error {
	if !reflect.DeepEqual(r.Command, c) {
		return extraction.ErrConflict
	}
	record, ok := s.Records[r.NewsReceipt.RecordKey]
	if !ok {
		return extraction.ErrInvalid
	}
	return r.Validate(a, record)
}
func (m *Memory) AdmitSwitchyardCapture(ctx context.Context, c extraction.SwitchyardAdmissionCommand, now func() time.Time) (extraction.SwitchyardAdmission, error) {
	if err := ctx.Err(); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ReceiverID != m.receiverID || !ValidReceiverID(c.ReceiverID) {
		return extraction.SwitchyardAdmission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.CaptureID) {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	a, ok := m.switchyardCaptures[c.CaptureID]
	if !ok {
		return extraction.SwitchyardAdmission{}, extraction.ErrNotFound
	}
	if a.ID != c.CaptureID || a.ReceiverID != m.receiverID {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	if r, ok := m.switchyardAdmissions[c.CaptureID]; ok {
		if err := validateSwitchyardCaptureAdmission(r, c, a, m.news); err != nil {
			return extraction.SwitchyardAdmission{}, err
		}
		return clone(r), nil
	}
	if len(m.switchyardAdmissions) >= extraction.MaxAttempts {
		return extraction.SwitchyardAdmission{}, extraction.ErrCapacity
	}
	next, r, err := prepareSwitchyardCaptureAdmission(m.news, a, c, now)
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	if err := ctx.Err(); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	m.news = next
	m.switchyardAdmissions[c.CaptureID] = clone(r)
	return clone(r), nil
}
func (m *Memory) SwitchyardCaptureAdmission(ctx context.Context, id string) (extraction.SwitchyardAdmission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.switchyardAdmissions[id]
	if !ok {
		return extraction.SwitchyardAdmission{}, extraction.ErrNotFound
	}
	a, ok := m.switchyardCaptures[id]
	if !ok || a.ID != id || a.ReceiverID != m.receiverID || r.Command.CaptureID != id || r.Command.ReceiverID != m.receiverID {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	if err := validateSwitchyardCaptureAdmission(r, r.Command, a, m.news); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	return clone(r), nil
}

func loadSwitchyardCaptureAdmission(ctx context.Context, tx pgx.Tx, id string) (extraction.SwitchyardAdmission, error) {
	var raw []byte
	var r extraction.SwitchyardAdmission
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_switchyard_capture_admissions WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, extraction.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if workerplan.ReadBoundedShape(raw, &r, 64<<10) != nil || r.Command.CaptureID != id || r.ValidateMetadata() != nil {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	return r, nil
}
func (p *Postgres) AdmitSwitchyardCapture(ctx context.Context, c extraction.SwitchyardAdmissionCommand, now func() time.Time) (extraction.SwitchyardAdmission, error) {
	if !ValidReceiverID(c.ReceiverID) {
		return extraction.SwitchyardAdmission{}, ErrReceiverMismatch
	}
	if !extraction.ID.MatchString(c.CaptureID) {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	// Lock order is receiver, then news. Existing news/review mutations only
	// read receiver identity without a row lock, so they cannot invert this order.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	if receiver != c.ReceiverID {
		return extraction.SwitchyardAdmission{}, ErrReceiverMismatch
	}
	a, err := readSwitchyardCapture(ctx, tx, c.CaptureID, receiver)
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	var raw []byte
	var s newscase.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_news_cases WHERE singleton FOR UPDATE").Scan(&raw); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	r, err := loadSwitchyardCaptureAdmission(ctx, tx, c.CaptureID)
	if err == nil {
		if err = validateSwitchyardCaptureAdmission(r, c, a, s); err != nil {
			return extraction.SwitchyardAdmission{}, err
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, extraction.ErrNotFound) {
		return extraction.SwitchyardAdmission{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_switchyard_capture_admissions").Scan(&count); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	if count >= extraction.MaxAttempts {
		return extraction.SwitchyardAdmission{}, extraction.ErrCapacity
	}
	next, r, err := prepareSwitchyardCaptureAdmission(s, a, c, now)
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	raw, _ = json.Marshal(next)
	if _, err = tx.Exec(ctx, "UPDATE mantik_news_cases SET payload=$1 WHERE singleton", raw); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	raw, _ = json.Marshal(r)
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_switchyard_capture_admissions(id,payload) VALUES($1,$2)", c.CaptureID, raw); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) SwitchyardCaptureAdmission(ctx context.Context, id string) (extraction.SwitchyardAdmission, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	r, err := loadSwitchyardCaptureAdmission(ctx, tx, id)
	if err != nil {
		return r, err
	}
	a, err := readSwitchyardCapture(ctx, tx, id, receiver)
	if err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	var raw []byte
	var record newscase.Record
	if err = tx.QueryRow(ctx, "SELECT payload->'records'->$1::text FROM mantik_news_cases WHERE singleton", r.NewsReceipt.RecordKey).Scan(&raw); err != nil {
		return extraction.SwitchyardAdmission{}, err
	}
	if json.Unmarshal(raw, &record) != nil || r.Command.ReceiverID != receiver || r.Validate(a, record) != nil {
		return extraction.SwitchyardAdmission{}, extraction.ErrInvalid
	}
	return r, tx.Commit(ctx)
}

var _ SwitchyardCaptureAdmissionStore = (*Memory)(nil)
var _ SwitchyardCaptureAdmissionStore = (*Postgres)(nil)
