package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategyidentity"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

func forwardSource(d forward.Declaration, g strategygate.Record, b account.CappedState) (string, error) {
	var spec strategyidentity.Spec
	if json.Unmarshal(g.Declaration.Spec, &spec) != nil {
		return "", forward.ErrConflict
	}
	sid, e := spec.ID()
	if e != nil || sid != d.StrategyID || g.Declaration.Family != d.Family || g.Decision == nil || !g.Decision.AdmitsIncubation() || g.Decision.StrategyID != sid || g.Declaration.Account != string(b.Config.Type) {
		return "", forward.ErrConflict
	}
	return strategygate.Hash(struct {
		Gate strategygate.Record
		Book account.CappedConfig
	}{g, b.Config})
}

func newForward(d forward.Declaration, at time.Time, starts []strategytrial.Start, g strategygate.Record, b account.CappedState) (forward.Record, error) {
	if d.Validate() != nil || !declarationTime(at) || !d.WindowStart.After(at) || d.WindowStart.Sub(at) > 365*24*time.Hour || at.Before(g.DeclaredAt) || g.Decision == nil || at.Before(g.Decision.At) || at.Before(b.Config.OpenedAt) {
		return forward.Record{}, forward.ErrInvalid
	}
	source, e := forwardSource(d, g, b)
	if e != nil {
		return forward.Record{}, e
	}
	if int64(len(starts)) > d.MaxSearchTrials {
		return forward.Record{}, forward.ErrConflict
	}
	for _, s := range starts {
		if s.Validate() != nil || s.Family != d.Family {
			return forward.Record{}, forward.ErrInvalid
		}
	}
	slices.SortFunc(starts, func(a, b strategytrial.Start) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for i, s := range starts {
		if i > 0 && starts[i-1].ID == s.ID {
			return forward.Record{}, forward.ErrInvalid
		}
	}
	r := forward.Record{Schema: forward.Schema, Declaration: d, DeclaredAt: at.UTC(), SearchStarts: starts, SearchTotal: int64(len(starts)), SourceSHA256: source}
	r.SHA256 = workerplan.Hash(r)
	raw, e := json.Marshal(r)
	if e != nil || len(raw) > 2<<20 {
		return forward.Record{}, forward.ErrInvalid
	}
	return clone(r), nil
}

func validateForward(r forward.Record, g strategygate.Record, b account.CappedState) error {
	want, e := newForward(r.Declaration, r.DeclaredAt, slices.Clone(r.SearchStarts), g, b)
	if e != nil || !sameGate(want, r) {
		return forward.ErrInvalid
	}
	return nil
}

// DeclareForward captures all starts under the same mutex as trial registration.
func (m *Memory) DeclareForward(ctx context.Context, d forward.Declaration, now func() time.Time) (forward.Record, error) {
	if e := ctx.Err(); e != nil {
		return forward.Record{}, e
	}
	if d.Validate() != nil {
		return forward.Record{}, forward.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.forwardDeclarations[d.ID]; ok {
		if e := m.validateForwardLocked(old); e != nil {
			return forward.Record{}, e
		}
		if !sameGate(old.Declaration, d) {
			return forward.Record{}, forward.ErrConflict
		}
		return clone(old), nil
	}
	b, ok := m.cappedBooks[d.BookID]
	if !ok {
		return forward.Record{}, account.ErrBookNotFound
	}
	g, ok := m.strategyGates[d.GateID]
	if !ok {
		return forward.Record{}, strategygate.ErrNotFound
	}
	if e := m.strategyFamilyReadyLocked(d.Family); e != nil {
		return forward.Record{}, e
	}
	starts := []strategytrial.Start{}
	for _, r := range m.strategyTrials {
		if r.Start.Family == d.Family {
			starts = append(starts, copyStrategyTrial(r).Start)
			if int64(len(starts)) > d.MaxSearchTrials {
				return forward.Record{}, forward.ErrConflict
			}
		}
	}
	at := time.Now().UTC()
	if now != nil {
		at = now()
	}
	r, e := newForward(d, at, starts, g, b)
	if e != nil {
		return r, e
	}
	if m.forwardDeclarations == nil {
		m.forwardDeclarations = map[string]forward.Record{}
	}
	m.forwardDeclarations[d.ID] = r
	return clone(r), nil
}
func (m *Memory) validateForwardLocked(r forward.Record) error {
	return validateForward(r, m.strategyGates[r.Declaration.GateID], m.cappedBooks[r.Declaration.BookID])
}

// ForwardDeclaration reads and validates a detached immutable record.
func (m *Memory) ForwardDeclaration(ctx context.Context, id string) (forward.Record, error) {
	if e := ctx.Err(); e != nil {
		return forward.Record{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.forwardDeclarations[id]
	if !ok {
		return r, forward.ErrNotFound
	}
	return clone(r), m.validateForwardLocked(r)
}

// ForwardEligibility derives clocks and the current N from one memory snapshot.
func (m *Memory) ForwardEligibility(ctx context.Context, id string, at time.Time) (forward.Eligibility, error) {
	if e := ctx.Err(); e != nil {
		return forward.Eligibility{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.forwardDeclarations[id]
	if !ok {
		return forward.Eligibility{}, forward.ErrNotFound
	}
	if e := m.validateForwardLocked(r); e != nil {
		return forward.Eligibility{}, e
	}
	n, e := m.strategySearchTotalLocked(r.Declaration.Family)
	if e != nil {
		return forward.Eligibility{}, e
	}
	opened, e := account.ForwardHoldingOpened(m.cappedBooks[r.Declaration.BookID], r.Declaration.SleeveID, at)
	if e != nil {
		return forward.Eligibility{}, e
	}
	return r.Clocks(at, n, opened), nil
}

func (p *Postgres) loadForward(ctx context.Context, q cappedQuery, id string) (forward.Record, error) {
	var raw []byte
	e := q.QueryRow(ctx, "SELECT payload FROM mantik_forward_declarations WHERE id=$1", id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return forward.Record{}, forward.ErrNotFound
	}
	if e != nil {
		return forward.Record{}, e
	}
	var r forward.Record
	if e = json.Unmarshal(raw, &r); e != nil {
		return r, e
	}
	g, e := loadGate(ctx, q, r.Declaration.GateID)
	if e != nil {
		return r, e
	}
	b, e := p.loadCapped(ctx, q, r.Declaration.BookID)
	if e != nil {
		return r, e
	}
	return r, validateForward(r, g, b)
}

// DeclareForward serializes the snapshot against all starts in this family.
// Exact retries retain the original clock and bytes even after window expiry.
func (p *Postgres) DeclareForward(ctx context.Context, d forward.Declaration, now func() time.Time) (forward.Record, error) {
	if d.Validate() != nil {
		return forward.Record{}, forward.ErrInvalid
	}
	if _, err := p.CappedBook(ctx, d.BookID); err != nil {
		return forward.Record{}, err
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return forward.Record{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = lockStrategyFamily(ctx, tx, d.Family); e != nil {
		return forward.Record{}, e
	}
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_forward:'||$1,0))", d.ID); e != nil {
		return forward.Record{}, e
	}
	old, e := p.loadForward(ctx, tx, d.ID)
	if e == nil {
		if !sameGate(old.Declaration, d) {
			return forward.Record{}, forward.ErrConflict
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(e, forward.ErrNotFound) {
		return forward.Record{}, e
	}
	g, e := loadGate(ctx, tx, d.GateID)
	if e != nil {
		return forward.Record{}, e
	}
	b, e := p.loadCapped(ctx, tx, d.BookID)
	if e != nil {
		return forward.Record{}, e
	}
	if e := strategyFamilyReady(ctx, tx, d.Family); e != nil {
		return forward.Record{}, e
	}
	rows, e := tx.Query(ctx, "SELECT payload FROM mantik_strategy_trials WHERE family=$1 ORDER BY id LIMIT $2", d.Family, d.MaxSearchTrials+1)
	if e != nil {
		return forward.Record{}, e
	}
	starts := []strategytrial.Start{}
	for rows.Next() {
		var raw []byte
		var s strategytrial.Start
		if e = rows.Scan(&raw); e == nil {
			e = json.Unmarshal(raw, &s)
		}
		if e != nil {
			rows.Close()
			return forward.Record{}, e
		}
		starts = append(starts, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return forward.Record{}, e
	}
	var at time.Time
	if now != nil {
		at = now()
	} else if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); e != nil {
		return forward.Record{}, e
	}
	r, e := newForward(d, at, starts, g, b)
	if e != nil {
		return r, e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mantik_forward_declarations(id,family,payload) VALUES($1,$2,$3)", d.ID, d.Family, string(raw)); e != nil {
		return forward.Record{}, e
	}
	return r, tx.Commit(ctx)
}

// ForwardDeclaration validates retained source and hashes without rewriting bytes.
func (p *Postgres) ForwardDeclaration(ctx context.Context, id string) (forward.Record, error) {
	return p.loadForward(ctx, p.pool, id)
}

// ForwardEligibility fences the live registry count against concurrent starts.
func (p *Postgres) ForwardEligibility(ctx context.Context, id string, at time.Time) (forward.Eligibility, error) {
	if _, err := p.ForwardDeclaration(ctx, id); err != nil {
		return forward.Eligibility{}, err
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return forward.Eligibility{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	r, e := p.loadForward(ctx, tx, id)
	if e != nil {
		return forward.Eligibility{}, e
	}
	if e = lockStrategyFamily(ctx, tx, r.Declaration.Family); e != nil {
		return forward.Eligibility{}, e
	}
	b, e := p.loadCapped(ctx, tx, r.Declaration.BookID)
	if e != nil {
		return forward.Eligibility{}, e
	}
	n, e := strategySearchTotal(ctx, tx, r.Declaration.Family)
	if e != nil {
		return forward.Eligibility{}, e
	}
	opened, e := account.ForwardHoldingOpened(b, r.Declaration.SleeveID, at)
	if e != nil {
		return forward.Eligibility{}, e
	}
	return r.Clocks(at, n, opened), tx.Commit(ctx)
}
