package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

func normalizeBatch(b strategytrial.Batch) (strategytrial.Batch, error) {
	if !strategytrial.ValidID(b.ID) || !strategytrial.ValidFamily(b.Family) || b.At.IsZero() || len(b.Reply) == 0 || len(b.Reply) > 524288 || !json.Valid(b.Reply) {
		return b, strategytrial.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range b.TrialIDs {
		if !strategytrial.ValidID(id) || seen[id] {
			return b, strategytrial.ErrInvalid
		}
		seen[id] = true
	}
	b.At = b.At.UTC().Truncate(time.Microsecond)
	var e error
	b.Reply, e = trialJSON(b.Reply)
	return clone(b), e
}

// ReserveStrategyTrialBatch retains an immutable intent before reply persistence.
func (m *Memory) ReserveStrategyTrialBatch(ctx context.Context, b strategytrial.Batch) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	b, e := normalizeBatch(b)
	if e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.strategyBatches[b.ID]; ok {
		if !sameGate(old, b) {
			return strategytrial.ErrConflict
		}
		return nil
	}
	if family, ok := m.strategyBatchIntents[b.ID]; ok && family != b.Family {
		return strategytrial.ErrConflict
	}
	if m.strategyBatchIntents == nil {
		m.strategyBatchIntents = map[string]string{}
	}
	m.strategyBatchIntents[b.ID] = b.Family
	if m.strategyBatches == nil {
		m.strategyBatches = map[string]strategytrial.Batch{}
	}
	m.strategyBatches[b.ID] = b
	return nil
}

// StrategyTrialBatch reads exact intent bytes for recovery without a model call.
func (m *Memory) StrategyTrialBatch(ctx context.Context, id string) (strategytrial.Batch, error) {
	if e := ctx.Err(); e != nil {
		return strategytrial.Batch{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.strategyBatches[id]
	if !ok {
		return b, strategytrial.ErrNotFound
	}
	return clone(b), nil
}

// ResolveStrategyTrialBatch releases only a fully registered batch fence.
func (m *Memory) ResolveStrategyTrialBatch(ctx context.Context, id string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.strategyBatches[id]
	if !ok {
		return strategytrial.ErrNotFound
	}
	for _, id := range b.TrialIDs {
		r, ok := m.strategyTrials[id]
		if !ok || r.Start.Family != b.Family {
			return strategytrial.ErrFamilyPending
		}
	}
	if m.resolvedStrategyBatches == nil {
		m.resolvedStrategyBatches = map[string]bool{}
	}
	m.resolvedStrategyBatches[id] = true
	return nil
}

func (m *Memory) strategyFamilyReadyLocked(family string) error {
	for id, f := range m.strategyBatchIntents {
		if f == family && !m.resolvedStrategyBatches[id] {
			return strategytrial.ErrFamilyPending
		}
	}
	return nil
}
func (m *Memory) strategySearchTotalLocked(family string) (int64, error) {
	if e := m.strategyFamilyReadyLocked(family); e != nil {
		return 0, e
	}
	var n int64
	for _, r := range m.strategyTrials {
		if r.Start.Family == family {
			n++
		}
	}
	return n, nil
}

// ReserveStrategyTrialBatch uses the same family lock as starts and admission.
func (p *Postgres) ReserveStrategyTrialBatch(ctx context.Context, b strategytrial.Batch) error {
	b, e := normalizeBatch(b)
	if e != nil {
		return e
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = lockStrategyFamily(ctx, tx, b.Family); e != nil {
		return e
	}
	if e = beginStrategyBatchTx(ctx, tx, b.ID, b.Family); e != nil {
		return e
	}
	raw, e := json.Marshal(b)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mantik_strategy_trial_batches(id,family,payload) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", b.ID, b.Family, string(raw)); e != nil {
		return e
	}
	old, e := loadStrategyBatch(ctx, tx, b.ID)
	if e != nil {
		return e
	}
	if !sameGate(old, b) {
		return strategytrial.ErrConflict
	}
	return tx.Commit(ctx)
}
func loadStrategyBatch(ctx context.Context, q cappedQuery, id string) (strategytrial.Batch, error) {
	var b strategytrial.Batch
	var raw []byte
	e := q.QueryRow(ctx, "SELECT payload FROM mantik_strategy_trial_batches WHERE id=$1", id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return b, strategytrial.ErrNotFound
	}
	if e != nil {
		return b, e
	}
	e = json.Unmarshal(raw, &b)
	return b, e
}

// StrategyTrialBatch returns the durable reply intent even if journal append failed.
func (p *Postgres) StrategyTrialBatch(ctx context.Context, id string) (strategytrial.Batch, error) {
	return loadStrategyBatch(ctx, p.pool, id)
}

// ResolveStrategyTrialBatch retains an append-only receipt after all starts exist.
func (p *Postgres) ResolveStrategyTrialBatch(ctx context.Context, id string) error {
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	b, e := loadStrategyBatch(ctx, tx, id)
	if e != nil {
		return e
	}
	if e = lockStrategyFamily(ctx, tx, b.Family); e != nil {
		return e
	}
	var n int64
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_strategy_trials WHERE family=$1 AND id=ANY($2)", b.Family, b.TrialIDs).Scan(&n); e != nil {
		return e
	}
	if n != int64(len(b.TrialIDs)) {
		return strategytrial.ErrFamilyPending
	}
	if _, e = tx.Exec(ctx, "INSERT INTO mantik_strategy_trial_batch_resolutions(id) VALUES($1) ON CONFLICT DO NOTHING", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Legacy pending replies also fence the family, before their first recovery on
// schema125. A retained resolution supersedes that fence before result append.
const pendingStrategyFamilySQL = `SELECT EXISTS (
 SELECT 1 FROM mantik_strategy_trial_batch_intents b LEFT JOIN mantik_strategy_trial_batch_resolutions r USING(id) WHERE b.family=$1 AND r.id IS NULL
) OR EXISTS (
 SELECT 1 FROM mantik_research_desk_events e JOIN mantik_research_desk_events s ON s.session_id=e.session_id AND s.sequence=0
 WHERE e.kind IN ('reply','call') AND s.body->>'family'=$1
 AND NOT EXISTS(SELECT 1 FROM mantik_research_desk_events later WHERE later.session_id=e.session_id AND later.sequence>e.sequence AND later.kind IN ('result','done'))
 AND NOT EXISTS(SELECT 1 FROM mantik_strategy_trial_batch_resolutions r WHERE r.id=e.session_id||'-trial-'||(SELECT count(*) FROM mantik_research_desk_events c WHERE c.session_id=e.session_id AND c.kind='call' AND c.sequence<=e.sequence)::text)
)`

func strategyFamilyReady(ctx context.Context, q cappedQuery, family string) error {
	var pending bool
	if e := q.QueryRow(ctx, pendingStrategyFamilySQL, family).Scan(&pending); e != nil {
		return e
	}
	if pending {
		return strategytrial.ErrFamilyPending
	}
	return nil
}
func strategySearchTotal(ctx context.Context, q cappedQuery, family string) (int64, error) {
	// One SQL snapshot couples the pending fence and count for pool readers too.
	var n int64
	var pending bool
	e := q.QueryRow(ctx, "SELECT ("+pendingStrategyFamilySQL+"), (SELECT count(*) FROM mantik_strategy_trials WHERE family=$1)", family).Scan(&pending, &n)
	if e != nil {
		return 0, e
	}
	if pending {
		return 0, strategytrial.ErrFamilyPending
	}
	return n, nil
}

// BeginStrategyTrialBatch fences the family before the model call can retain
// generated output. An uncertain call stays unavailable until reconciled.
func (m *Memory) BeginStrategyTrialBatch(ctx context.Context, id, family string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !strategytrial.ValidID(id) || !strategytrial.ValidFamily(family) {
		return strategytrial.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.strategyBatchIntents[id]; ok {
		if f != family {
			return strategytrial.ErrConflict
		}
		return nil
	}
	if m.strategyBatchIntents == nil {
		m.strategyBatchIntents = map[string]string{}
	}
	m.strategyBatchIntents[id] = family
	return nil
}
func beginStrategyBatchTx(ctx context.Context, tx pgx.Tx, id, family string) error {
	if _, e := tx.Exec(ctx, "INSERT INTO mantik_strategy_trial_batch_intents(id,family) VALUES($1,$2) ON CONFLICT DO NOTHING", id, family); e != nil {
		return e
	}
	var f string
	if e := tx.QueryRow(ctx, "SELECT family FROM mantik_strategy_trial_batch_intents WHERE id=$1", id).Scan(&f); e != nil {
		return e
	}
	if f != family {
		return strategytrial.ErrConflict
	}
	return nil
}

// BeginStrategyTrialBatch shares the admission/start family transaction lock.
func (p *Postgres) BeginStrategyTrialBatch(ctx context.Context, id, family string) error {
	if !strategytrial.ValidID(id) || !strategytrial.ValidFamily(family) {
		return strategytrial.ErrInvalid
	}
	tx, e := p.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = lockStrategyFamily(ctx, tx, family); e != nil {
		return e
	}
	if e = beginStrategyBatchTx(ctx, tx, id, family); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
