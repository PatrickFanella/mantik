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

type LocalCaptureStore interface {
	ImportLocalCapture(context.Context, string, string, extraction.LocalReceipt, *extraction.LocalReceipt, func() time.Time) (extraction.LocalCapture, error)
	LocalCapture(context.Context, string) (extraction.LocalCapture, error)
	LocalCaptures(context.Context) (extraction.LocalCaptureHistory, error)
}

func (m *Memory) ImportLocalCapture(ctx context.Context, receiver, id string, p extraction.LocalReceipt, f *extraction.LocalReceipt, now func() time.Time) (extraction.LocalCapture, error) {
	// Replay and detach caller evidence before acquiring the mutation lock.
	candidate, err := extraction.RetainLocal(ctx, receiver, id, p, f, time.Unix(1, 0))
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return extraction.LocalCapture{}, err
	}
	if receiver != m.receiverID {
		return extraction.LocalCapture{}, ErrReceiverMismatch
	}
	if r, ok := m.localCaptures[id]; ok {
		if r.ID != id || r.ReceiverID != receiver || r.ValidateContext(ctx) != nil {
			return extraction.LocalCapture{}, extraction.ErrInvalid
		}
		if !extraction.SameLocalCapture(r, candidate.Prepared, candidate.Finished) {
			return extraction.LocalCapture{}, extraction.ErrConflict
		}
		return clone(r), nil
	}
	if len(m.localCaptures) >= extraction.MaxAttempts {
		return extraction.LocalCapture{}, extraction.ErrCapacity
	}
	r, err := extraction.RetainLocal(ctx, receiver, id, candidate.Prepared, candidate.Finished, now())
	if err != nil {
		return r, err
	}
	m.localCaptures[id] = clone(r)
	return clone(r), nil
}
func (m *Memory) LocalCapture(ctx context.Context, id string) (extraction.LocalCapture, error) {
	if err := ctx.Err(); err != nil {
		return extraction.LocalCapture{}, err
	}
	if !extraction.ID.MatchString(id) {
		return extraction.LocalCapture{}, extraction.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.localCaptures[id]
	if !ok {
		return r, extraction.ErrNotFound
	}
	if r.ID != id || r.ReceiverID != m.receiverID {
		return extraction.LocalCapture{}, extraction.ErrInvalid
	}
	if err := r.ValidateContext(ctx); err != nil {
		return extraction.LocalCapture{}, err
	}
	return clone(r), nil
}
func (m *Memory) LocalCaptures(ctx context.Context) (extraction.LocalCaptureHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := []extraction.LocalCapture{}
	for id, r := range m.localCaptures {
		if r.ID != id {
			return extraction.LocalCaptureHistory{}, extraction.ErrInvalid
		}
		all = append(all, r)
	}
	if err := ctx.Err(); err != nil {
		return extraction.LocalCaptureHistory{}, err
	}
	return extraction.MakeLocalCaptureHistory(ctx, m.receiverID, all)
}
func decodeLocalCapture(ctx context.Context, raw []byte, id, receiver string) (extraction.LocalCapture, error) {
	var r extraction.LocalCapture
	if workerplan.ReadBoundedShape(raw, &r, extraction.MaxLocalCaptureBytes) != nil || r.ID != id || r.ReceiverID != receiver {
		return r, extraction.ErrInvalid
	}
	if err := r.ValidateContext(ctx); err != nil {
		return extraction.LocalCapture{}, err
	}
	return r, nil
}
func (p *Postgres) LocalCapture(ctx context.Context, id string) (extraction.LocalCapture, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.LocalCapture{}, extraction.ErrInvalid
	}
	var raw []byte
	var receiver string
	err := p.pool.QueryRow(ctx, `SELECT c.payload,r.id FROM mantik_local_extraction_captures c CROSS JOIN mantik_research_receiver r WHERE c.id=$1 AND r.singleton`, id).Scan(&raw, &receiver)
	if errors.Is(err, pgx.ErrNoRows) {
		return extraction.LocalCapture{}, extraction.ErrNotFound
	}
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	return decodeLocalCapture(ctx, raw, id, receiver)
}
func (p *Postgres) ImportLocalCapture(ctx context.Context, receiver, id string, prepared extraction.LocalReceipt, finished *extraction.LocalReceipt, now func() time.Time) (extraction.LocalCapture, error) {
	candidate, err := extraction.RetainLocal(ctx, receiver, id, prepared, finished, time.Unix(1, 0))
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	// The receiver lock serializes capacity and deduplication, without touching research or operations.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&expected); err != nil {
		return extraction.LocalCapture{}, err
	}
	if expected != receiver {
		return extraction.LocalCapture{}, ErrReceiverMismatch
	}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_local_extraction_captures WHERE id=$1", id).Scan(&raw)
	if err == nil {
		r, e := decodeLocalCapture(ctx, raw, id, receiver)
		if e != nil {
			return extraction.LocalCapture{}, e
		}
		if !extraction.SameLocalCapture(r, candidate.Prepared, candidate.Finished) {
			return extraction.LocalCapture{}, extraction.ErrConflict
		}
		return r, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return extraction.LocalCapture{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_local_extraction_captures").Scan(&count); err != nil {
		return extraction.LocalCapture{}, err
	}
	if count >= extraction.MaxAttempts {
		return extraction.LocalCapture{}, extraction.ErrCapacity
	}
	r, err := extraction.RetainLocal(ctx, receiver, id, candidate.Prepared, candidate.Finished, now())
	if err != nil {
		return r, err
	}
	raw, err = json.Marshal(r)
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_local_extraction_captures(id,payload) VALUES($1,$2)", id, raw); err != nil {
		return extraction.LocalCapture{}, err
	}
	return r, tx.Commit(ctx)
}
func (p *Postgres) LocalCaptures(ctx context.Context) (extraction.LocalCaptureHistory, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.LocalCaptureHistory{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.LocalCaptureHistory{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_local_extraction_captures ORDER BY id LIMIT $1", extraction.MaxAttempts+1)
	if err != nil {
		return extraction.LocalCaptureHistory{}, err
	}
	all := []extraction.LocalCapture{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return extraction.LocalCaptureHistory{}, err
		}
		r, e := decodeLocalCapture(ctx, raw, id, receiver)
		if e != nil {
			rows.Close()
			return extraction.LocalCaptureHistory{}, e
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return extraction.LocalCaptureHistory{}, err
	}
	h, err := extraction.MakeLocalCaptureHistory(ctx, receiver, all)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

var _ LocalCaptureStore = (*Memory)(nil)
var _ LocalCaptureStore = (*Postgres)(nil)

func readLocalCapture(ctx context.Context, tx pgx.Tx, id, receiver string) (extraction.LocalCapture, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT payload FROM mantik_local_extraction_captures WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return extraction.LocalCapture{}, extraction.ErrNotFound
	}
	if err != nil {
		return extraction.LocalCapture{}, err
	}
	return decodeLocalCapture(ctx, raw, id, receiver)
}
