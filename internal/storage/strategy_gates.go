package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"github.com/jackc/pgx/v5"
)

func copyGate(r strategygate.Record) (strategygate.Record, error) {
	raw, e := json.Marshal(r)
	if e != nil {
		return strategygate.Record{}, e
	}
	var out strategygate.Record
	e = json.Unmarshal(raw, &out)
	return out, e
}
func sameGate(a, b any) bool {
	ah, ae := strategygate.Hash(a)
	bh, be := strategygate.Hash(b)
	return ae == nil && be == nil && ah == bh
}
func validateGateDecision(r strategygate.Record, d strategygate.Decision) error {
	if d.Validate() != nil {
		return strategygate.ErrInvalid
	}
	h, e := strategygate.Hash(r.Declaration)
	if e != nil || d.DeclarationSHA256 != h || d.StrategyID != r.Declaration.StrategyID || d.At.Before(r.DeclaredAt) {
		return strategygate.ErrInvalid
	}
	var spec struct {
		ModelDriven bool `json:"model_driven"`
	}
	if json.Unmarshal(r.Declaration.Spec, &spec) != nil {
		return strategygate.ErrInvalid
	}
	if spec.ModelDriven && d.Status == "passed" || !spec.ModelDriven && d.Status == "forward_only" {
		return strategygate.ErrInvalid
	}
	return nil
}

// DeclareStrategyGate freezes an experiment before its registry attempts start.
func (m *Memory) DeclareStrategyGate(ctx context.Context, d strategygate.Declaration) (strategygate.Record, error) {
	if e := ctx.Err(); e != nil {
		return strategygate.Record{}, e
	}
	if e := d.Validate(); e != nil {
		return strategygate.Record{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.strategyGates[d.ID]; ok {
		if !sameGate(old.Declaration, d) {
			detached, e := copyGate(old)
			return detached, errors.Join(strategygate.ErrConflict, e)
		}
		return copyGate(old)
	}
	r, e := copyGate(strategygate.Record{Declaration: d, DeclaredAt: time.Now().UTC()})
	if e != nil {
		return r, e
	}
	if m.strategyGates == nil {
		m.strategyGates = map[string]strategygate.Record{}
	}
	m.strategyGates[d.ID] = r
	return copyGate(r)
}

// StrategyGate returns a detached declaration and final decision, if any.
func (m *Memory) StrategyGate(ctx context.Context, id string) (strategygate.Record, error) {
	if e := ctx.Err(); e != nil {
		return strategygate.Record{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.strategyGates[id]
	if !ok {
		return r, strategygate.ErrNotFound
	}
	return copyGate(r)
}

// RecordStrategyGate appends one final decision; changed retries conflict.
func (m *Memory) RecordStrategyGate(ctx context.Context, d strategygate.Decision) (strategygate.Record, error) {
	if e := ctx.Err(); e != nil {
		return strategygate.Record{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.strategyGates[d.ID]
	if !ok {
		return r, strategygate.ErrNotFound
	}
	if e := validateGateDecision(r, d); e != nil {
		detached, copyErr := copyGate(r)
		return detached, errors.Join(e, copyErr)
	}
	if r.Decision != nil {
		if !sameGate(*r.Decision, d) {
			detached, e := copyGate(r)
			return detached, errors.Join(strategygate.ErrConflict, e)
		}
		return copyGate(r)
	}
	r.Decision = &d
	detached, e := copyGate(r)
	if e != nil {
		return strategygate.Record{}, e
	}
	m.strategyGates[d.ID] = detached
	return copyGate(detached)
}

// DeclareStrategyGate stores a retry-safe, append-only declaration and DB clock.
func (p *Postgres) DeclareStrategyGate(ctx context.Context, d strategygate.Declaration) (strategygate.Record, error) {
	if e := d.Validate(); e != nil {
		return strategygate.Record{}, e
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return strategygate.Record{}, e
	}
	_, e = p.pool.Exec(ctx, `INSERT INTO mantik_strategy_gates(id,declaration) VALUES($1,$2) ON CONFLICT DO NOTHING`, d.ID, string(raw))
	if e != nil {
		return strategygate.Record{}, e
	}
	r, e := p.StrategyGate(ctx, d.ID)
	if e == nil && !sameGate(r.Declaration, d) {
		e = strategygate.ErrConflict
	}
	return r, e
}
func loadGate(ctx context.Context, q cappedQuery, id string) (strategygate.Record, error) {
	var raw, decision []byte
	r := strategygate.Record{}
	e := q.QueryRow(ctx, `SELECT g.declaration,g.declared_at,d.decision FROM mantik_strategy_gates g LEFT JOIN mantik_strategy_gate_decisions d USING(id) WHERE g.id=$1`, id).Scan(&raw, &r.DeclaredAt, &decision)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, strategygate.ErrNotFound
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(raw, &r.Declaration); e != nil {
		return r, e
	}
	if len(decision) > 0 {
		r.Decision = new(strategygate.Decision)
		e = json.Unmarshal(decision, r.Decision)
	}
	return r, e
}

// StrategyGate reads the immutable declaration and optional decision.
func (p *Postgres) StrategyGate(ctx context.Context, id string) (strategygate.Record, error) {
	return loadGate(ctx, p.pool, id)
}

// RecordStrategyGate serializes a single final decision against its declaration.
func (p *Postgres) RecordStrategyGate(ctx context.Context, d strategygate.Decision) (strategygate.Record, error) {
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return strategygate.Record{}, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var id string
	e = tx.QueryRow(ctx, `SELECT id FROM mantik_strategy_gates WHERE id=$1 FOR UPDATE`, d.ID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return strategygate.Record{}, strategygate.ErrNotFound
	}
	if e != nil {
		return strategygate.Record{}, e
	}
	r, e := loadGate(ctx, tx, d.ID)
	if e != nil {
		return r, e
	}
	if e = validateGateDecision(r, d); e != nil {
		return r, e
	}
	if r.Decision != nil {
		if !sameGate(*r.Decision, d) {
			return r, strategygate.ErrConflict
		}
	} else {
		raw, e := json.Marshal(d)
		if e != nil {
			return r, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO mantik_strategy_gate_decisions(id,decision) VALUES($1,$2)`, d.ID, string(raw)); e != nil {
			return r, e
		}
		r.Decision = &d
	}
	if e = tx.Commit(ctx); e != nil {
		return r, e
	}
	return r, nil
}
