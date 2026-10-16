package storage

import (
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"github.com/jackc/pgx/v5"
)

type ReviewHistoryStore interface {
	ReviewHistory(context.Context, string) (review.History, error)
}

func (m *Memory) ReviewHistory(ctx context.Context, id string) (review.History, error) {
	if !review.HistoryCaseID.MatchString(id) {
		return review.History{}, review.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return review.History{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.news.Cases[id]
	if !ok {
		return review.History{}, review.ErrNotFound
	}
	all := []review.Attempt{}
	for key, a := range m.reviews {
		if a.Pin.Subject.CaseID == id {
			if a.ID != key {
				return review.History{}, review.ErrInvalid
			}
			all = append(all, a)
		}
	}
	return review.MakeHistory(m.receiverID, c, all)
}

func (p *Postgres) ReviewHistory(ctx context.Context, id string) (review.History, error) {
	if !review.HistoryCaseID.MatchString(id) {
		return review.History{}, review.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return review.History{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receiver string
	var raw []byte
	var c newscase.Case
	if err = tx.QueryRow(ctx, `SELECT r.id,n.payload->'cases'->$1::text FROM mantik_research_receiver r CROSS JOIN mantik_news_cases n WHERE r.singleton AND n.singleton`, id).Scan(&receiver, &raw); err != nil {
		return review.History{}, err
	}
	if len(raw) == 0 {
		return review.History{}, review.ErrNotFound
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return review.History{}, err
	}
	if c.ID != id {
		return review.History{}, review.ErrInvalid
	}
	rows, err := tx.Query(ctx, `SELECT id,payload FROM mantik_news_reviews WHERE payload->'pin'->'subject'->>'case_id'=$1 ORDER BY id LIMIT $2`, id, review.MaxAttempts+1)
	if err != nil {
		return review.History{}, err
	}
	all := []review.Attempt{}
	for rows.Next() {
		var key string
		var a review.Attempt
		if err = rows.Scan(&key, &raw); err == nil {
			err = json.Unmarshal(raw, &a)
		}
		if err == nil && a.ID != key {
			err = review.ErrInvalid
		}
		if err != nil {
			rows.Close()
			return review.History{}, err
		}
		all = append(all, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return review.History{}, err
	}
	h, err := review.MakeHistory(receiver, c, all)
	if err != nil {
		return review.History{}, err
	}
	return h, tx.Commit(ctx)
}

var _ ReviewHistoryStore = (*Memory)(nil)
var _ ReviewHistoryStore = (*Postgres)(nil)
