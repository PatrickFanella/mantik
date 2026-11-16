package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

// ErrCappedReadCapacity means a read cannot return a complete bounded prefix.
// Callers must report unavailable, never truncate or synthesize a partial book.
var ErrCappedReadCapacity = errors.New("capped book read exceeds byte capacity")

// BoundedCappedBookStore reads a detached complete current state within a byte budget.
// It is separate from the command interface and never appends or compacts events.
type BoundedCappedBookStore interface {
	CappedBookBounded(context.Context, string, int) (account.CappedState, error)
}

func cappedReadConfig(raw []byte, max int) (account.CappedState, error) {
	if max < 1 || len(raw) == 0 || len(raw) > max {
		return account.CappedState{}, ErrCappedReadCapacity
	}
	var c account.CappedConfig
	if e := json.Unmarshal(raw, &c); e != nil {
		return account.CappedState{}, e
	}
	return account.NewCappedState(c)
}
func appendCappedRead(s *account.CappedState, raw []byte, remaining *int) error {
	// Budget includes per-event JSON delimiters and version field overhead.
	*remaining -= len(raw) + 2
	if len(raw) == 0 || *remaining < 0 {
		return ErrCappedReadCapacity
	}
	var c account.BookCommand
	if e := json.Unmarshal(raw, &c); e != nil {
		return e
	}
	s.Events = append(s.Events, c)
	s.Version += account.CappedCommandCount(c)
	return nil
}

// CappedBookBounded snapshots under the existing memory-store lock. Individual
// event encodings are checked before copying; no whole-book encoding is needed.
func (m *Memory) CappedBookBounded(ctx context.Context, id string, max int) (account.CappedState, error) {
	if e := ctx.Err(); e != nil {
		return account.CappedState{}, e
	}
	if max < 1 {
		return account.CappedState{}, ErrCappedReadCapacity
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	original, ok := m.cappedBooks[id]
	if !ok {
		return account.CappedState{}, account.ErrBookNotFound
	}
	raw, e := json.Marshal(original.Config)
	if e != nil {
		return account.CappedState{}, e
	}
	out, e := cappedReadConfig(raw, max)
	if e != nil {
		return account.CappedState{}, e
	}
	remaining := max - len(raw) - 128
	if remaining < 0 {
		return account.CappedState{}, ErrCappedReadCapacity
	}
	for _, c := range original.Events {
		if e = ctx.Err(); e != nil {
			return account.CappedState{}, e
		}
		raw, e = json.Marshal(c)
		if e != nil {
			return account.CappedState{}, e
		}
		if e = appendCappedRead(&out, raw, &remaining); e != nil {
			return account.CappedState{}, e
		}
	}
	if e = account.ValidateCappedState(out); e != nil {
		return account.CappedState{}, e
	}
	if out.Version != original.Version {
		return account.CappedState{}, account.ErrBookConflict
	}
	return out, nil
}

// CappedBookBounded pins one source-bound version and verifies its original
// command transitions through the same reader as operational admission. SQL
// suppresses oversized config/head bytes before pgx receives them. Immutable
// archive pages preserve that pinned version across concurrent appends.
func (p *Postgres) CappedBookBounded(ctx context.Context, id string, max int) (account.CappedState, error) {
	if max < 1 {
		return account.CappedState{}, ErrCappedReadCapacity
	}
	return p.loadCappedBounded(ctx, p.pool, id, max)
}
