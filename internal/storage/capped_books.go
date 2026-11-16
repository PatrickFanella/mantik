package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"github.com/jackc/pgx/v5"
)

// CappedBookStore is the durable command boundary used by the paper adapter.
// Commands are validated and serialized before publication; no mutable balance
// or broker buying-power field can be written by a strategy.
type CappedBookStore interface {
	CreateCappedBook(context.Context, account.CappedConfig) (account.CappedState, error)
	CappedBook(context.Context, string) (account.CappedState, error)
	ApplyCappedBook(context.Context, string, int64, account.BookCommand) (account.CappedState, error)
}

func copyCapped(s account.CappedState) (account.CappedState, error) {
	raw, e := json.Marshal(s)
	if e != nil {
		return s, e
	}
	var out account.CappedState
	e = json.Unmarshal(raw, &out)
	return out, e
}

// CreateCappedBook creates one immutable configuration per broker paper account.
func (m *Memory) CreateCappedBook(ctx context.Context, c account.CappedConfig) (account.CappedState, error) {
	if e := ctx.Err(); e != nil {
		return account.CappedState{}, e
	}
	s, e := account.NewCappedState(c)
	c = s.Config
	if e != nil {
		return s, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.cappedBooks[c.ID]; ok {
		if !reflect.DeepEqual(old.Config, c) {
			return s, account.ErrBookConflict
		}
		return copyCapped(old)
	}
	for _, v := range m.cappedBooks {
		if v.Config.BrokerAccountID == c.BrokerAccountID {
			return s, account.ErrBookConflict
		}
	}
	if m.cappedBooks == nil {
		m.cappedBooks = map[string]account.CappedState{}
	}
	m.cappedBooks[c.ID] = s
	return copyCapped(s)
}

// CappedBook reads a detached event stream.
func (m *Memory) CappedBook(ctx context.Context, id string) (account.CappedState, error) {
	if e := ctx.Err(); e != nil {
		return account.CappedState{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.cappedBooks[id]
	if !ok {
		return s, account.ErrBookNotFound
	}
	return copyCapped(s)
}

// ApplyCappedBook atomically validates and appends one event at an expected version.
func (m *Memory) ApplyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	if e := ctx.Err(); e != nil {
		return account.CappedState{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.cappedBooks[id]
	if !ok {
		return s, account.ErrBookNotFound
	}
	next, e := account.ApplyCappedCommand(s, c)
	if e != nil {
		return s, e
	}
	if next.Version != s.Version && version != s.Version {
		return s, account.ErrBookConflict
	}
	if next.Version != s.Version {
		if e = m.checkPortfolioAdmissionsLocked(s, next.Events[len(next.Events)-1]); e != nil {
			return s, e
		}
	}
	m.cappedBooks[id] = next
	return copyCapped(next)
}

// CreateCappedBook creates an immutable, restart-safe capped paper book.
func (p *Postgres) CreateCappedBook(ctx context.Context, c account.CappedConfig) (account.CappedState, error) {
	s, e := account.NewCappedState(c)
	c = s.Config
	if e != nil {
		return s, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return s, e
	}
	_, e = p.pool.Exec(ctx, `INSERT INTO mantik_capped_books(id,broker_account,config) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, c.BrokerAccountID, string(raw))
	if e != nil {
		return s, e
	}
	old, e := p.CappedBook(ctx, c.ID)
	if e != nil {
		return s, e
	}
	if !reflect.DeepEqual(old.Config, c) {
		return s, account.ErrBookConflict
	}
	return old, nil
}

type cappedQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadCapped(ctx context.Context, q cappedQuery, id string) (account.CappedState, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT config FROM mantik_capped_books WHERE id=$1`, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return account.CappedState{}, account.ErrBookNotFound
	}
	if e != nil {
		return account.CappedState{}, e
	}
	var c account.CappedConfig
	if e = json.Unmarshal(raw, &c); e != nil {
		return account.CappedState{}, e
	}
	s, e := account.NewCappedState(c)
	if e != nil {
		return s, e
	}
	rows, e := q.Query(ctx, `SELECT payload FROM mantik_capped_book_events WHERE book_id=$1 ORDER BY sequence`, id)
	if e != nil {
		return s, e
	}
	defer rows.Close()
	for rows.Next() {
		if e = rows.Scan(&raw); e != nil {
			return s, e
		}
		var command account.BookCommand
		if e = json.Unmarshal(raw, &command); e != nil {
			return s, e
		}
		s.Events = append(s.Events, command)
		s.Version++
	}
	return s, rows.Err()
}

// CappedBook reads the retained configuration and ordered journal events.
func (p *Postgres) CappedBook(ctx context.Context, id string) (account.CappedState, error) {
	return loadCapped(ctx, p.pool, id)
}

// ApplyCappedBook locks one book for a command, so independent workers cannot
// reserve the same local capital. The existing adapter still fences the POST.
func (p *Postgres) ApplyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	tx, e := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return account.CappedState{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var locked string
	e = tx.QueryRow(ctx, `SELECT id FROM mantik_capped_books WHERE id=$1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(e, pgx.ErrNoRows) {
		return account.CappedState{}, account.ErrBookNotFound
	}
	if e != nil {
		return account.CappedState{}, e
	}
	s, e := loadCapped(ctx, tx, id)
	if e != nil {
		return s, e
	}
	next, e := account.ApplyCappedCommand(s, c)
	if e != nil {
		return s, e
	}
	if next.Version != s.Version {
		if version != s.Version {
			return s, account.ErrBookConflict
		}
		command := next.Events[len(next.Events)-1]
		if e = checkPortfolioAdmissionsTx(ctx, tx, s, command); e != nil {
			return s, e
		}
		raw, e := json.Marshal(command)
		if e != nil {
			return s, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,$2,$3,$4)`, id, next.Version, command.ID, string(raw)); e != nil {
			return s, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return s, e
	}
	return next, nil
}
