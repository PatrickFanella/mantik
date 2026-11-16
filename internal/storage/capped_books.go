package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
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
		return account.CappedState{}, e
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
		return account.CappedState{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.cappedBooks[c.ID]; ok {
		if !reflect.DeepEqual(old.Config, c) {
			return account.CappedState{}, account.ErrBookConflict
		}
		return copyCapped(old)
	}
	for _, v := range m.cappedBooks {
		if v.Config.BrokerAccountID == c.BrokerAccountID {
			return account.CappedState{}, account.ErrBookConflict
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
		return account.CappedState{}, account.ErrBookNotFound
	}
	return copyCapped(s)
}

// ApplyCappedBook atomically validates and appends one event at an expected version.
func (m *Memory) ApplyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	return m.applyCappedBook(ctx, id, version, c, false)
}

func (m *Memory) applyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand, collection bool) (account.CappedState, error) {
	if c.Kind == "qualification" || c.Qualification != nil || !collection && (c.Kind == "qualification_plan" || c.Incubation != nil && c.Incubation.Qualification != nil) {
		return account.CappedState{}, forward.ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		return account.CappedState{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.cappedBooks[id]
	if !ok {
		return account.CappedState{}, account.ErrBookNotFound
	}
	if old, ok := m.cappedHistory[id][c.ID]; ok {
		if !sameCappedCommand(old.Command, c) {
			return account.CappedState{}, account.ErrBookConflict
		}
		return copyCapped(s)
	}
	s, copyErr := copyCapped(s)
	if copyErr != nil {
		return account.CappedState{}, copyErr
	}
	if collection {
		if e := m.validateCollectionLocked(ctx, s, &c); e != nil {
			return s, e
		}
	}
	next, e := account.ApplyCappedCommand(s, c)
	if e != nil {
		return account.CappedState{}, e
	}
	if next.Version != s.Version && version != s.Version {
		return account.CappedState{}, account.ErrBookConflict
	}
	if next.Version != s.Version {
		if e = m.checkPortfolioAdmissionsLocked(s, next.Events[len(next.Events)-1]); e != nil {
			return account.CappedState{}, e
		}
	}
	if e := m.retainCappedLocked(s, next, c); e != nil {
		return account.CappedState{}, e
	}
	return copyCapped(next)
}

// CreateCappedBook creates an immutable, restart-safe capped paper book.
func (p *Postgres) CreateCappedBook(ctx context.Context, c account.CappedConfig) (account.CappedState, error) {
	s, e := account.NewCappedState(c)
	c = s.Config
	if e != nil {
		return account.CappedState{}, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return account.CappedState{}, e
	}
	_, e = p.pool.Exec(ctx, `INSERT INTO mantik_capped_books(id,broker_account,config) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, c.BrokerAccountID, string(raw))
	if e != nil {
		return account.CappedState{}, e
	}
	old, e := p.CappedBook(ctx, c.ID)
	if e != nil {
		return account.CappedState{}, e
	}
	if !reflect.DeepEqual(old.Config, c) {
		return account.CappedState{}, account.ErrBookConflict
	}
	return old, nil
}

type cappedQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// CappedBook reads the retained configuration and ordered journal events.
func (p *Postgres) CappedBook(ctx context.Context, id string) (account.CappedState, error) {
	return p.loadCapped(ctx, p.pool, id)
}

// ApplyCappedBook locks one book for a command, so independent workers cannot
// reserve the same local capital. The existing adapter still fences the POST.
func (p *Postgres) ApplyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	return p.applyCappedBook(ctx, id, version, c, false)
}

func (p *Postgres) applyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand, collection bool) (account.CappedState, error) {
	if c.Kind == "qualification" || c.Qualification != nil || !collection && (c.Kind == "qualification_plan" || c.Incubation != nil && c.Incubation.Qualification != nil) {
		return account.CappedState{}, forward.ErrInvalid
	}
	// Warm committed custody before acquiring a connection/row lock.
	if _, err := p.CappedBook(ctx, id); err != nil {
		return account.CappedState{}, err
	}
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
	s, e := p.loadCapped(ctx, tx, id)
	if e != nil {
		return account.CappedState{}, e
	}
	old, found, err := cappedHistoryCommand(ctx, tx, id, c.ID)
	if err != nil {
		return account.CappedState{}, err
	}
	if found {
		if !sameCappedCommand(old, c) {
			return account.CappedState{}, account.ErrBookConflict
		}
		return s, nil
	}
	if collection {
		if e := p.validateCollectionTx(ctx, tx, s, &c); e != nil {
			return s, e
		}
	}
	next, e := account.ApplyCappedCommand(s, c)
	if e != nil {
		return account.CappedState{}, e
	}
	if next.Version != s.Version {
		if version != s.Version {
			return account.CappedState{}, account.ErrBookConflict
		}
		command := next.Events[len(next.Events)-1]
		if e = p.checkPortfolioAdmissionsTx(ctx, tx, s, command); e != nil {
			return account.CappedState{}, e
		}
		if e = retainCappedTx(ctx, tx, s, next, command); e != nil {
			return account.CappedState{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return account.CappedState{}, e
	}
	return next, nil
}
