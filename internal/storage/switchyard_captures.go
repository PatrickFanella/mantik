package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
	"time"
)

type SwitchyardCaptureStore interface {
	ImportSwitchyardCapture(context.Context, string, string, extraction.SwitchyardReceipt, *extraction.SwitchyardReceipt, func() time.Time) (extraction.SwitchyardCapture, error)
	SwitchyardCapture(context.Context, string) (extraction.SwitchyardCapture, error)
	SwitchyardCaptures(context.Context) (extraction.SwitchyardCaptureHistory, error)
}

func (m *Memory) ImportSwitchyardCapture(ctx context.Context, receiver, id string, p extraction.SwitchyardReceipt, f *extraction.SwitchyardReceipt, now func() time.Time) (extraction.SwitchyardCapture, error) {
	// Replay and detach caller evidence before acquiring the mutation lock.
	candidate, err := extraction.RetainSwitchyard(ctx, receiver, id, p, f, time.Unix(1, 0))
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	if receiver != m.receiverID {
		return extraction.SwitchyardCapture{}, ErrReceiverMismatch
	}
	if r, ok := m.switchyardCaptures[id]; ok {
		if r.ID != id || r.ReceiverID != receiver || r.ValidateContext(ctx) != nil {
			return extraction.SwitchyardCapture{}, extraction.ErrInvalid
		}
		if !extraction.SameSwitchyardCapture(r, candidate.Prepared, candidate.Finished) {
			return extraction.SwitchyardCapture{}, extraction.ErrConflict
		}
		return clone(r), nil
	}
	if len(m.switchyardCaptures) >= extraction.MaxAttempts {
		return extraction.SwitchyardCapture{}, extraction.ErrCapacity
	}
	r, err := extraction.RetainSwitchyard(ctx, receiver, id, candidate.Prepared, candidate.Finished, now())
	if err != nil {
		return r, err
	}
	m.switchyardCaptures[id] = clone(r)
	return clone(r), nil
}
func (m *Memory) SwitchyardCapture(ctx context.Context, id string) (extraction.SwitchyardCapture, error) {
	if err := ctx.Err(); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	if !extraction.ID.MatchString(id) {
		return extraction.SwitchyardCapture{}, extraction.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.switchyardCaptures[id]
	if !ok {
		return r, extraction.ErrNotFound
	}
	if r.ID != id || r.ReceiverID != m.receiverID {
		return extraction.SwitchyardCapture{}, extraction.ErrInvalid
	}
	if err := r.ValidateContext(ctx); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	return clone(r), nil
}
func (m *Memory) SwitchyardCaptures(ctx context.Context) (extraction.SwitchyardCaptureHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := []extraction.SwitchyardCapture{}
	for id, r := range m.switchyardCaptures {
		if r.ID != id {
			return extraction.SwitchyardCaptureHistory{}, extraction.ErrInvalid
		}
		all = append(all, r)
	}
	if err := ctx.Err(); err != nil {
		return extraction.SwitchyardCaptureHistory{}, err
	}
	return extraction.MakeSwitchyardCaptureHistory(ctx, m.receiverID, all)
}
func decodeSwitchyardCapture(ctx context.Context, raw []byte, id, receiver string) (extraction.SwitchyardCapture, error) {
	var r extraction.SwitchyardCapture
	if workerplan.ReadBoundedShape(raw, &r, extraction.MaxSwitchyardCaptureBytes) != nil || r.ID != id || r.ReceiverID != receiver {
		return r, extraction.ErrInvalid
	}
	if err := r.ValidateContext(ctx); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	return r, nil
}
func (p *Postgres) SwitchyardCapture(ctx context.Context, id string) (extraction.SwitchyardCapture, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.SwitchyardCapture{}, extraction.ErrInvalid
	}
	var raw []byte
	var receiver string
	err := p.pool.QueryRow(ctx, `SELECT c.payload,r.id FROM mantik_switchyard_extraction_captures c CROSS JOIN mantik_research_receiver r WHERE c.id=$1 AND r.singleton`, id).Scan(&raw, &receiver)
	if errors.Is(err, pgx.ErrNoRows) {
		return extraction.SwitchyardCapture{}, extraction.ErrNotFound
	}
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	return decodeSwitchyardCapture(ctx, raw, id, receiver)
}
func (p *Postgres) ImportSwitchyardCapture(ctx context.Context, receiver, id string, prepared extraction.SwitchyardReceipt, finished *extraction.SwitchyardReceipt, now func() time.Time) (extraction.SwitchyardCapture, error) {
	candidate, err := extraction.RetainSwitchyard(ctx, receiver, id, prepared, finished, time.Unix(1, 0))
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	// The receiver lock serializes capacity and deduplication, without touching research or operations.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&expected); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	if expected != receiver {
		return extraction.SwitchyardCapture{}, ErrReceiverMismatch
	}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_switchyard_extraction_captures WHERE id=$1", id).Scan(&raw)
	if err == nil {
		r, e := decodeSwitchyardCapture(ctx, raw, id, receiver)
		if e != nil {
			return extraction.SwitchyardCapture{}, e
		}
		if !extraction.SameSwitchyardCapture(r, candidate.Prepared, candidate.Finished) {
			return extraction.SwitchyardCapture{}, extraction.ErrConflict
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return extraction.SwitchyardCapture{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_switchyard_extraction_captures").Scan(&count); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	if count >= extraction.MaxAttempts {
		return extraction.SwitchyardCapture{}, extraction.ErrCapacity
	}
	r, err := extraction.RetainSwitchyard(ctx, receiver, id, candidate.Prepared, candidate.Finished, now())
	if err != nil {
		return r, err
	}
	raw, err = json.Marshal(r)
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_switchyard_extraction_captures(id,payload) VALUES($1,$2)", id, raw); err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) SwitchyardCaptures(ctx context.Context) (extraction.SwitchyardCaptureHistory, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.SwitchyardCaptureHistory{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.SwitchyardCaptureHistory{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_switchyard_extraction_captures ORDER BY id LIMIT $1", extraction.MaxAttempts+1)
	if err != nil {
		return extraction.SwitchyardCaptureHistory{}, err
	}
	all := []extraction.SwitchyardCapture{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return extraction.SwitchyardCaptureHistory{}, err
		}
		r, e := decodeSwitchyardCapture(ctx, raw, id, receiver)
		if e != nil {
			rows.Close()
			return extraction.SwitchyardCaptureHistory{}, e
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return extraction.SwitchyardCaptureHistory{}, err
	}
	h, err := extraction.MakeSwitchyardCaptureHistory(ctx, receiver, all)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

var _ SwitchyardCaptureStore = (*Memory)(nil)
var _ SwitchyardCaptureStore = (*Postgres)(nil)

func readSwitchyardCapture(ctx context.Context, tx pgx.Tx, id, receiver string) (extraction.SwitchyardCapture, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_switchyard_extraction_captures WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return extraction.SwitchyardCapture{}, extraction.ErrNotFound
	}
	if err != nil {
		return extraction.SwitchyardCapture{}, err
	}
	return decodeSwitchyardCapture(ctx, raw, id, receiver)
}
