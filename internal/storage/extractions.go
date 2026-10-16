package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

type ExtractionStore interface {
	StartExtraction(context.Context, string, string, news.Article, string, func() time.Time) (extraction.Attempt, error)
	CompleteExtraction(context.Context, string, string, []byte, func() time.Time) (extraction.Attempt, error)
	Extraction(context.Context, string) (extraction.Attempt, error)
	Extractions(context.Context) (extraction.History, error)
}

func (m *Memory) StartExtraction(ctx context.Context, receiver, id string, article news.Article, key string, now func() time.Time) (extraction.Attempt, error) {
	if err := ctx.Err(); err != nil {
		return extraction.Attempt{}, err
	}
	if !extraction.ID.MatchString(id) {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	if !ValidReceiverID(receiver) {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if receiver != m.receiverID {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	if a, ok := m.extractions[id]; ok {
		if a.ID != id || a.Validate() != nil {
			return extraction.Attempt{}, extraction.ErrInvalid
		}
		if !extraction.SameStart(a, receiver, article, key) {
			return extraction.Attempt{}, extraction.ErrConflict
		}
		return clone(a), nil
	}
	if len(m.extractions) >= extraction.MaxAttempts {
		return extraction.Attempt{}, extraction.ErrCapacity
	}
	a, err := extraction.Start(receiver, id, article, key, now())
	if err != nil {
		return a, err
	}
	m.extractions[id] = clone(a)
	return clone(a), nil
}
func (m *Memory) Extraction(ctx context.Context, id string) (extraction.Attempt, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return extraction.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.extractions[id]
	if !ok {
		return a, extraction.ErrNotFound
	}
	if a.ID != id || a.ReceiverID != m.receiverID || a.Validate() != nil {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	return clone(a), nil
}
func (m *Memory) CompleteExtraction(ctx context.Context, receiver, id string, raw []byte, now func() time.Time) (extraction.Attempt, error) {
	if len(raw) > extraction.MaxArtifactBytes {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	raw = append([]byte(nil), raw...)
	a, err := m.Extraction(ctx, id)
	if err != nil {
		return a, err
	}
	if receiver != a.ReceiverID || !ValidReceiverID(receiver) {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	var result extraction.Result
	if a.Status == "pending" {
		result, err = extraction.EvaluateRecorded(ctx, a, raw)
		if err != nil {
			return extraction.Attempt{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return extraction.Attempt{}, err
	}
	if receiver != m.receiverID {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	a = m.extractions[id]
	a, err = extraction.Commit(a, raw, result, now)
	if err != nil {
		return extraction.Attempt{}, err
	}
	m.extractions[id] = clone(a)
	return clone(a), nil
}
func (m *Memory) Extractions(ctx context.Context) (extraction.History, error) {
	if err := ctx.Err(); err != nil {
		return extraction.History{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all := []extraction.Attempt{}
	for key, a := range m.extractions {
		if key != a.ID {
			return extraction.History{}, extraction.ErrInvalid
		}
		all = append(all, a)
	}
	return extraction.MakeHistory(m.receiverID, all)
}

func readExtraction(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id, receiver string) (extraction.Attempt, error) {
	var raw []byte
	var a extraction.Attempt
	err := q.QueryRow(ctx, "SELECT payload FROM mantik_news_extractions WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, extraction.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if workerplan.ReadBoundedShape(raw, &a, 4<<20) != nil || a.ID != id || a.ReceiverID != receiver || a.Validate() != nil {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	return a, nil
}
func (p *Postgres) Extraction(ctx context.Context, id string) (extraction.Attempt, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	// One statement binds the retained payload to its actual receiver identity.
	var raw []byte
	var receiver string
	var a extraction.Attempt
	err := p.pool.QueryRow(ctx, `SELECT a.payload,r.id FROM mantik_news_extractions a CROSS JOIN mantik_research_receiver r WHERE a.id=$1 AND r.singleton`, id).Scan(&raw, &receiver)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, extraction.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if workerplan.ReadBoundedShape(raw, &a, 4<<20) != nil || a.ID != id || a.ReceiverID != receiver || a.Validate() != nil {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	return a, nil
}
func (p *Postgres) extractionMutation(ctx context.Context, receiver string, apply func(pgx.Tx) (extraction.Attempt, error)) (extraction.Attempt, error) {
	if !ValidReceiverID(receiver) {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return extraction.Attempt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expected string
	// This lock serializes capacity and immutable attempt mutations. It does not
	// modify news cases, publications, fixture state or operational authority.
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&expected); err != nil {
		return extraction.Attempt{}, err
	}
	if expected != receiver {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	a, err := apply(tx)
	if err != nil {
		return extraction.Attempt{}, err
	}
	return a, tx.Commit(ctx)
}
func (p *Postgres) StartExtraction(ctx context.Context, receiver, id string, article news.Article, key string, now func() time.Time) (extraction.Attempt, error) {
	if !extraction.ID.MatchString(id) {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	return p.extractionMutation(ctx, receiver, func(tx pgx.Tx) (extraction.Attempt, error) {
		a, err := readExtraction(ctx, tx, id, receiver)
		if err == nil {
			if !extraction.SameStart(a, receiver, article, key) {
				return extraction.Attempt{}, extraction.ErrConflict
			}
			return a, nil
		}
		if !errors.Is(err, extraction.ErrNotFound) {
			return a, err
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_news_extractions").Scan(&count); err != nil {
			return a, err
		}
		if count >= extraction.MaxAttempts {
			return a, extraction.ErrCapacity
		}
		a, err = extraction.Start(receiver, id, article, key, now())
		if err != nil {
			return a, err
		}
		raw, _ := json.Marshal(a)
		_, err = tx.Exec(ctx, "INSERT INTO mantik_news_extractions(id,payload) VALUES($1,$2)", id, raw)
		return a, err
	})
}
func (p *Postgres) CompleteExtraction(ctx context.Context, receiver, id string, raw []byte, now func() time.Time) (extraction.Attempt, error) {
	if len(raw) > extraction.MaxArtifactBytes {
		return extraction.Attempt{}, extraction.ErrInvalid
	}
	raw = append([]byte(nil), raw...)
	a, err := p.Extraction(ctx, id)
	if err != nil {
		return a, err
	}
	if receiver != a.ReceiverID || !ValidReceiverID(receiver) {
		return extraction.Attempt{}, ErrReceiverMismatch
	}
	var result extraction.Result
	if a.Status == "pending" {
		result, err = extraction.EvaluateRecorded(ctx, a, raw)
		if err != nil {
			return extraction.Attempt{}, err
		}
	}
	return p.extractionMutation(ctx, receiver, func(tx pgx.Tx) (extraction.Attempt, error) {
		current, err := readExtraction(ctx, tx, id, receiver)
		if err != nil {
			return current, err
		}
		a, err = extraction.Commit(current, raw, result, now)
		if err != nil {
			return a, err
		}
		if current.Status != "pending" {
			return a, nil
		}
		body, _ := json.Marshal(a)
		_, err = tx.Exec(ctx, "UPDATE mantik_news_extractions SET payload=$2 WHERE id=$1", id, body)
		return a, err
	})
}
func (p *Postgres) Extractions(ctx context.Context) (extraction.History, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return extraction.History{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return extraction.History{}, err
	}
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_news_extractions ORDER BY id LIMIT $1", extraction.MaxAttempts+1)
	if err != nil {
		return extraction.History{}, err
	}
	all := []extraction.Attempt{}
	for rows.Next() {
		var key string
		var raw []byte
		var a extraction.Attempt
		if err = rows.Scan(&key, &raw); err == nil {
			err = workerplan.ReadBoundedShape(raw, &a, 4<<20)
		}
		if err == nil && a.ID != key {
			err = extraction.ErrInvalid
		}
		if err != nil {
			rows.Close()
			return extraction.History{}, err
		}
		all = append(all, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return extraction.History{}, err
	}
	h, err := extraction.MakeHistory(receiver, all)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

var _ ExtractionStore = (*Memory)(nil)
var _ ExtractionStore = (*Postgres)(nil)
