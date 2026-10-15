package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"github.com/jackc/pgx/v5"
)

var ErrActivityQuery = errors.New("activity requires limit 1..100 and 0 <= after <= through <= current history boundary")

type ActivityQuery struct {
	After int64
	// Nil selects the latest committed boundary, including an empty history.
	Through *int64
	Limit   int
}

type ActivityReader interface {
	Activity(context.Context, ActivityQuery) (domain.ActivityPage, error)
}

func (q ActivityQuery) Validate() error {
	if q.After < 0 || q.Limit < 1 || q.Limit > 100 || q.Through != nil && (*q.Through < q.After || *q.Through < 0) {
		return ErrActivityQuery
	}
	return nil
}

func activityBoundary(q ActivityQuery, latest int64) (domain.ActivityPage, error) {
	p := domain.ActivityPage{Mode: "paper_fixture", Through: latest, NextAfter: q.After, Items: []domain.ActivityEvent{}}
	if q.Through != nil {
		p.Through = *q.Through
	}
	if p.Through > latest || q.After > p.Through {
		return p, ErrActivityQuery
	}
	return p, nil
}

func (m *Memory) Activity(ctx context.Context, q ActivityQuery) (domain.ActivityPage, error) {
	if err := q.Validate(); err != nil {
		return domain.ActivityPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ActivityPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := activityBoundary(q, int64(len(m.events)))
	if err != nil {
		return p, err
	}
	end := min(p.Through, q.After+int64(q.Limit))
	for i := q.After; i < end; i++ {
		p.Items = append(p.Items, domain.ActivityEvent{Sequence: i + 1, Event: clone(m.events[i])})
		p.NextAfter = i + 1
	}
	p.HasMore = end < p.Through
	return p, nil
}

func (p *Postgres) Activity(ctx context.Context, q ActivityQuery) (domain.ActivityPage, error) {
	if err := q.Validate(); err != nil {
		return domain.ActivityPage{}, err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.ActivityPage{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var latest int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(max(sequence),0) FROM mantik_events").Scan(&latest); err != nil {
		return domain.ActivityPage{}, err
	}
	page, err := activityBoundary(q, latest)
	if err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, "SELECT sequence,payload FROM mantik_events WHERE sequence>$1 AND sequence<=$2 ORDER BY sequence LIMIT $3", q.After, page.Through, q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.ActivityEvent
		var raw []byte
		if err := rows.Scan(&item.Sequence, &raw); err != nil {
			return page, err
		}
		if len(page.Items) == q.Limit {
			page.HasMore = true
			break
		}
		if err := json.Unmarshal(raw, &item.Event); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
		page.NextAfter = item.Sequence
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	return page, tx.Commit(ctx)
}
