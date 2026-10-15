package storage

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

var ErrExceptionQuery = errors.New("exceptions require limit 1..100 and an optional exception identity cursor")
var exceptionCursor = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type ExceptionQuery struct {
	After string
	Limit int
}

func (q ExceptionQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 100 || q.After != "" && !exceptionCursor.MatchString(q.After) {
		return ErrExceptionQuery
	}
	return nil
}

type ExceptionReader interface {
	Exceptions(context.Context, ExceptionQuery) (domain.ExceptionPage, error)
}

func exceptionPage(q ExceptionQuery) domain.ExceptionPage {
	return domain.ExceptionPage{Mode: "paper_fixture", Scope: "retained_exceptions", NextAfter: q.After, Items: []domain.OperationalException{}}
}

func (m *Memory) Exceptions(ctx context.Context, q ExceptionQuery) (domain.ExceptionPage, error) {
	if err := q.Validate(); err != nil {
		return domain.ExceptionPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ExceptionPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := exceptionPage(q)
	ids := []string{}
	for id := range m.state.Routing.Timers.Exceptions {
		if id > q.After {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if len(p.Items) == q.Limit {
			p.HasMore = true
			break
		}
		p.Items = append(p.Items, clone(m.state.Routing.Timers.Exceptions[id]))
		p.NextAfter = id
	}
	return p, nil
}

func (p *Postgres) Exceptions(ctx context.Context, q ExceptionQuery) (domain.ExceptionPage, error) {
	if err := q.Validate(); err != nil {
		return domain.ExceptionPage{}, err
	}
	page := exceptionPage(q)
	// Exception keys still live in the existing aggregate. PostgreSQL may
	// enumerate/sort its keys, but returns at most limit+1 selected records and
	// never queries audit events or returns unrelated aggregate fields.
	rows, err := p.pool.Query(ctx, `SELECT e.key,e.value FROM mantik_state
 CROSS JOIN LATERAL jsonb_each(payload#>'{routing,timers,exceptions}') e
 WHERE mantik_state.id='demo' AND e.key COLLATE "C">$1 COLLATE "C"
 ORDER BY e.key COLLATE "C" LIMIT $2`, q.After, q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return page, err
		}
		if len(page.Items) == q.Limit {
			page.HasMore = true
			break
		}
		var e domain.OperationalException
		if err := json.Unmarshal(raw, &e); err != nil {
			return page, err
		}
		page.Items = append(page.Items, e)
		page.NextAfter = id
	}
	return page, rows.Err()
}
