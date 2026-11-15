package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

func normalizeTrial(s strategytrial.Start) (strategytrial.Start, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	s.StartedAt = s.StartedAt.UTC().Truncate(time.Microsecond)
	s.Parents = slices.Clone(s.Parents)
	if s.Parents == nil {
		s.Parents = []string{}
	}
	slices.Sort(s.Parents)
	var err error
	if s.Spec, err = trialJSON(s.Spec); err != nil {
		return s, err
	}
	s.Config, err = trialJSON(s.Config)
	return s, err
}
func trialJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func sameTrial(a, b strategytrial.Start) bool {
	var err error
	if a, err = normalizeTrial(a); err != nil {
		return false
	}
	if b, err = normalizeTrial(b); err != nil {
		return false
	}
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}
func copyStrategyTrial(r strategytrial.Record) strategytrial.Record {
	r.Start.Spec = slices.Clone(r.Start.Spec)
	r.Start.Config = slices.Clone(r.Start.Config)
	r.Start.Parents = slices.Clone(r.Start.Parents)
	r.Returns = slices.Clone(r.Returns)
	if r.Outcome != nil {
		o := *r.Outcome
		r.Outcome = &o
	}
	return r
}

// StartStrategyTrial retains the declaration before execution; exact retries
// return false. Reusing an ID for a changed declaration is a conflict.
func (m *Memory) StartStrategyTrial(ctx context.Context, s strategytrial.Start) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s, err := normalizeTrial(s)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.strategyTrials[s.ID]; ok {
		if !sameTrial(r.Start, s) {
			return false, strategytrial.ErrConflict
		}
		return false, nil
	}
	for _, id := range s.Parents {
		p, ok := m.strategyTrials[id]
		if !ok {
			return false, strategytrial.ErrNotFound
		}
		if p.Start.StartedAt.After(s.StartedAt) {
			return false, strategytrial.ErrConflict
		}
	}
	if m.strategyTrials == nil {
		m.strategyTrials = map[string]strategytrial.Record{}
	}
	m.strategyTrials[s.ID] = strategytrial.Record{Start: s, Returns: []strategytrial.DailyReturn{}}
	return true, nil
}

// AppendStrategyTrialReturn saves one session before the next one runs.
func (m *Memory) AppendStrategyTrialReturn(ctx context.Context, id string, d strategytrial.DailyReturn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strategytrial.ValidID(id) {
		return strategytrial.ErrInvalid
	}
	if err := d.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.strategyTrials[id]
	if !ok {
		return strategytrial.ErrNotFound
	}
	for _, old := range r.Returns {
		if old.Date == d.Date {
			if old == d {
				return nil
			}
			return strategytrial.ErrConflict
		}
	}
	if r.Outcome != nil || len(r.Returns) > 0 && d.Date <= r.Returns[len(r.Returns)-1].Date {
		return strategytrial.ErrConflict
	}
	r.Returns = append(r.Returns, d)
	m.strategyTrials[id] = r
	return nil
}

// FinishStrategyTrial appends a terminal outcome without rewriting the start
// or returns. Only an exact retry can follow an existing outcome.
func (m *Memory) FinishStrategyTrial(ctx context.Context, id string, o strategytrial.Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strategytrial.ValidID(id) {
		return strategytrial.ErrInvalid
	}
	if err := o.Validate(); err != nil {
		return err
	}
	o.FinishedAt = o.FinishedAt.UTC().Truncate(time.Microsecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.strategyTrials[id]
	if !ok {
		return strategytrial.ErrNotFound
	}
	if o.FinishedAt.Before(r.Start.StartedAt) {
		return strategytrial.ErrConflict
	}
	if r.Outcome != nil {
		if *r.Outcome == o {
			return nil
		}
		return strategytrial.ErrConflict
	}
	r.Outcome = &o
	m.strategyTrials[id] = r
	return nil
}

// StrategyTrial returns an attempt with its ordered returns and outcome.
func (m *Memory) StrategyTrial(ctx context.Context, id string) (strategytrial.Record, error) {
	if err := ctx.Err(); err != nil {
		return strategytrial.Record{}, err
	}
	if !strategytrial.ValidID(id) {
		return strategytrial.Record{}, strategytrial.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.strategyTrials[id]
	if !ok {
		return r, strategytrial.ErrNotFound
	}
	return copyStrategyTrial(r), nil
}

// StrategyTrialSearchTotal counts every start in the family, not just successes.
func (m *Memory) StrategyTrialSearchTotal(ctx context.Context, family string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !strategytrial.ValidFamily(family) {
		return 0, strategytrial.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var total int64
	for _, r := range m.strategyTrials {
		if r.Start.Family == family {
			total++
		}
	}
	return total, nil
}

// StartStrategyTrial retains a declaration atomically with its lineage checks.
func (p *Postgres) StartStrategyTrial(ctx context.Context, s strategytrial.Start) (bool, error) {
	s, err := normalizeTrial(s)
	if err != nil {
		return false, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_strategy_trial:'||$1,0))", s.ID); err != nil {
		return false, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_strategy_trials WHERE id=$1", s.ID).Scan(&raw)
	if err == nil {
		var old strategytrial.Start
		if err = json.Unmarshal(raw, &old); err != nil {
			return false, err
		}
		if !sameTrial(old, s) {
			return false, strategytrial.ErrConflict
		}
		return false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	for _, id := range s.Parents {
		var at time.Time
		err = tx.QueryRow(ctx, "SELECT started_at FROM mantik_strategy_trials WHERE id=$1", id).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, strategytrial.ErrNotFound
		}
		if err != nil {
			return false, err
		}
		if at.After(s.StartedAt) {
			return false, strategytrial.ErrConflict
		}
	}
	raw, err = json.Marshal(s)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_strategy_trials(id,family,started_at,payload) VALUES($1,$2,$3,$4)", s.ID, s.Family, s.StartedAt, raw); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// AppendStrategyTrialReturn appends a daily return, serializing with completion.
func (p *Postgres) AppendStrategyTrialReturn(ctx context.Context, id string, d strategytrial.DailyReturn) error {
	if !strategytrial.ValidID(id) {
		return strategytrial.ErrInvalid
	}
	if err := d.Validate(); err != nil {
		return err
	}
	return p.appendStrategyTrial(ctx, id, d, nil)
}

// FinishStrategyTrial appends the sole terminal outcome.
func (p *Postgres) FinishStrategyTrial(ctx context.Context, id string, o strategytrial.Outcome) error {
	if !strategytrial.ValidID(id) {
		return strategytrial.ErrInvalid
	}
	if err := o.Validate(); err != nil {
		return err
	}
	o.FinishedAt = o.FinishedAt.UTC().Truncate(time.Microsecond)
	return p.appendStrategyTrial(ctx, id, strategytrial.DailyReturn{}, &o)
}

func (p *Postgres) appendStrategyTrial(ctx context.Context, id string, d strategytrial.DailyReturn, o *strategytrial.Outcome) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var started time.Time
	err = tx.QueryRow(ctx, "SELECT started_at FROM mantik_strategy_trials WHERE id=$1 FOR UPDATE", id).Scan(&started)
	if errors.Is(err, pgx.ErrNoRows) {
		return strategytrial.ErrNotFound
	}
	if err != nil {
		return err
	}
	if o != nil && o.FinishedAt.Before(started) {
		return strategytrial.ErrConflict
	}
	if o != nil {
		var old strategytrial.Outcome
		err = tx.QueryRow(ctx, "SELECT status,finished_at FROM mantik_strategy_trial_outcomes WHERE trial_id=$1", id).Scan(&old.Status, &old.FinishedAt)
		if err == nil {
			old.FinishedAt = old.FinishedAt.UTC()
			if old != *o {
				return strategytrial.ErrConflict
			}
			return tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_strategy_trial_outcomes(trial_id,status,finished_at) VALUES($1,$2,$3)", id, o.Status, o.FinishedAt)
	} else {
		var old strategytrial.DailyReturn
		err = tx.QueryRow(ctx, "SELECT to_char(session_date,'YYYY-MM-DD'),daily_return,equity FROM mantik_strategy_trial_returns WHERE trial_id=$1 AND session_date=$2::date", id, d.Date).Scan(&old.Date, &old.Return, &old.Equity)
		if err == nil {
			if old != d {
				return strategytrial.ErrConflict
			}
			return tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var blocked bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mantik_strategy_trial_outcomes WHERE trial_id=$1) OR EXISTS(SELECT 1 FROM mantik_strategy_trial_returns WHERE trial_id=$1 AND session_date >= $2::date)`, id, d.Date).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return strategytrial.ErrConflict
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_strategy_trial_returns(trial_id,session_date,daily_return,equity) VALUES($1,$2::date,$3,$4)", id, d.Date, d.Return, d.Equity)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// StrategyTrial reads a consistent snapshot of the start, returns and outcome.
func (p *Postgres) StrategyTrial(ctx context.Context, id string) (strategytrial.Record, error) {
	if !strategytrial.ValidID(id) {
		return strategytrial.Record{}, strategytrial.ErrInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return strategytrial.Record{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_strategy_trials WHERE id=$1", id).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return strategytrial.Record{}, strategytrial.ErrNotFound
	} else if err != nil {
		return strategytrial.Record{}, err
	}
	var r strategytrial.Record
	if err = json.Unmarshal(raw, &r.Start); err != nil {
		return r, err
	}
	r.Start, err = normalizeTrial(r.Start)
	if err != nil {
		return r, err
	}
	rows, err := tx.Query(ctx, "SELECT to_char(session_date,'YYYY-MM-DD'),daily_return,equity FROM mantik_strategy_trial_returns WHERE trial_id=$1 ORDER BY session_date", id)
	if err != nil {
		return r, err
	}
	r.Returns = []strategytrial.DailyReturn{}
	for rows.Next() {
		var d strategytrial.DailyReturn
		if err = rows.Scan(&d.Date, &d.Return, &d.Equity); err != nil {
			rows.Close()
			return r, err
		}
		r.Returns = append(r.Returns, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return r, err
	}
	var o strategytrial.Outcome
	err = tx.QueryRow(ctx, "SELECT status,finished_at FROM mantik_strategy_trial_outcomes WHERE trial_id=$1", id).Scan(&o.Status, &o.FinishedAt)
	if err == nil {
		o.FinishedAt = o.FinishedAt.UTC()
		r.Outcome = &o
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// StrategyTrialSearchTotal counts starts using the family index.
func (p *Postgres) StrategyTrialSearchTotal(ctx context.Context, family string) (int64, error) {
	if !strategytrial.ValidFamily(family) {
		return 0, strategytrial.ErrInvalid
	}
	var total int64
	err := p.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_strategy_trials WHERE family=$1", family).Scan(&total)
	return total, err
}
