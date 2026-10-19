package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// ActionStore separates write-ahead delivery intent, adapter effect, and acknowledgment.
// Unknown outcomes must be reconciled before another submission.
type ActionStore interface {
	ClaimActions(context.Context, string, time.Time, time.Duration) ([]domain.ActionClaim, error)
	ReconcileAction(context.Context, domain.ActionClaim, time.Time) (domain.Action, error)
	BeginAction(context.Context, domain.ActionClaim, time.Time) (domain.Action, error)
	SubmitAction(context.Context, domain.ActionClaim, time.Time) (domain.ActionEffect, error)
}

type actionTx struct {
	state   *domain.State
	capital *fixturecapital.Book
	read    func(string) (*domain.ActionEffect, error)
	save    func(domain.ActionEffect) error
}
type actionUpdate func(actionTx) (any, []domain.Event, error)

func (m *Memory) actionMutation(ctx context.Context, update actionUpdate) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := clone(m.state)
	effects := clone(m.effects)
	var capital *fixturecapital.Book
	if m.fixtureCapital != nil {
		v, err := m.fixtureCapital.Snapshot()
		if err != nil {
			return nil, err
		}
		a, ok := m.paperAccounts[v.AccountID]
		if !m.queued || !ok || a.Config.ID != v.AccountID || a.Validate() != nil || a.ReceiverID != m.receiverID || a.SHA256 != v.AccountSHA256 {
			return nil, fixturecapital.ErrConflict
		}
		// Book transitions return immutable copies; never JSON-clone a book.
		b := *m.fixtureCapital
		capital = &b
	}
	tx := actionTx{state: &next, read: func(id string) (*domain.ActionEffect, error) {
		e, ok := effects[id]
		if !ok {
			return nil, nil
		}
		return &e, nil
	}, save: func(e domain.ActionEffect) error { effects[e.Intent.IdempotencyKey] = e; return nil }, capital: capital}
	value, events, err := update(tx)
	if err != nil {
		return nil, err
	}
	m.state, m.effects = next, effects
	m.fixtureCapital = capital
	m.events = append(m.events, events...)
	return value, nil
}

func (p *Postgres) actionMutation(ctx context.Context, update actionUpdate) (any, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return nil, err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	adapter := actionTx{state: &state, read: func(id string) (*domain.ActionEffect, error) {
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT payload FROM mantik_fixture_effects WHERE idempotency_key=$1", id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var e domain.ActionEffect
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return &e, nil
	}, save: func(e domain.ActionEffect) error {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_fixture_effects VALUES($1,$2)", e.Intent.IdempotencyKey, raw)
		return err
	}}
	value, events, err := update(adapter)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,NULL,$2)", e.ID, raw); err != nil {
			return nil, err
		}
	}
	raw, err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return value, nil
}

func claimActions(tx actionTx, worker string, now time.Time, lease time.Duration) (any, []domain.Event, error) {
	c, err := actions.Claim(tx.state, worker, now, lease)
	return c, nil, err
}

func actionFailure(tx actionTx, c domain.ActionClaim, a domain.Action, status, code string, now time.Time) (any, []domain.Event, error) {
	a.Status, a.Code, a.UpdatedAt = status, code, now.UTC()
	tx.state.Actions.Items[c.ID] = a
	tx.state.Actions.LastClock = now.UTC()
	return a, []domain.Event{{ID: fmt.Sprintf("%s-%d-%s", c.ID, c.Generation, status), Kind: "action." + status, ActionID: c.ID, At: now.UTC()}}, nil
}

func reconcileAction(tx actionTx, c domain.ActionClaim, now time.Time) (any, []domain.Event, error) {
	a, err := actions.CheckClaim(*tx.state, c, now)
	if err != nil {
		return nil, nil, err
	}
	if actions.Terminal(a.Status) && tx.capital == nil {
		return a, nil, nil
	}
	effect, err := tx.read(a.Intent.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	if err := reconcileCapital(tx, c, a, now, effect); err != nil {
		return nil, nil, err
	}
	if actions.Terminal(a.Status) {
		return a, nil, nil
	}
	if effect != nil {
		events, err := actions.Settle(tx.state, c, *effect, now)
		if err != nil {
			return nil, nil, err
		}
		workflow.ResolveProtection(tx.state, now)
		return tx.state.Actions.Items[c.ID], events, nil
	}
	if a.Status == "accepted" || a.Status == "partial" {
		return nil, nil, errors.New("known adapter effect disappeared")
	}
	// The local fixture ledger provides linearizable absence under the same row
	// lock as submission. A future provider must establish equivalent absence.
	if _, status, err := actions.Evaluate(*tx.state, a, now); err != nil {
		return actionFailure(tx, c, a, status, "current_authority_unavailable", now)
	}
	if a.Status == "unknown" {
		a.Status, a.Code, a.UpdatedAt = "pending", "reconciled_absent", now.UTC()
		tx.state.Actions.Items[c.ID] = a
	}
	return a, nil, nil
}

func beginAction(tx actionTx, c domain.ActionClaim, now time.Time) (any, []domain.Event, error) {
	a, err := actions.CheckClaim(*tx.state, c, now)
	if err != nil {
		return nil, nil, err
	}
	if actions.Terminal(a.Status) {
		if tx.capital != nil {
			return reconcileAction(tx, c, now)
		}
		return a, nil, nil
	}
	if a.Status != "pending" {
		return nil, nil, errors.New("action requires reconciliation before submission")
	}
	effect, err := tx.read(a.Intent.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	if effect != nil {
		return nil, nil, errors.New("existing effect requires reconciliation")
	}
	if _, status, err := actions.Evaluate(*tx.state, a, now); err != nil {
		if err := reconcileCapital(tx, c, a, now, nil); err != nil {
			return nil, nil, err
		}
		return actionFailure(tx, c, a, status, "current_authority_unavailable", now)
	}
	if tx.capital != nil {
		b, err := tx.capital.Reserve(*tx.state, c, now)
		if errors.Is(err, fixturecapital.ErrLimit) {
			return actionFailure(tx, c, a, "rejected", "fixture_capital_limit", now)
		}
		if err != nil {
			return nil, nil, err
		}
		*tx.capital = b
	}
	a.Status, a.Code, a.UpdatedAt = "unknown", "submission_unacknowledged", now.UTC()
	a.Attempts++
	tx.state.Actions.Items[c.ID] = a
	tx.state.Actions.LastClock = now.UTC()
	return a, []domain.Event{{ID: fmt.Sprintf("%s-%d-%d-submitting", c.ID, c.Generation, a.Attempts), Kind: "action.submitting", ActionID: c.ID, At: now.UTC()}}, nil
}

func submitAction(tx actionTx, c domain.ActionClaim, now time.Time) (any, []domain.Event, error) {
	a, err := actions.CheckClaim(*tx.state, c, now)
	if err != nil {
		return nil, nil, err
	}
	if actions.Terminal(a.Status) || a.Status != "unknown" {
		return nil, nil, errors.New("action has no write-ahead submission intent")
	}
	if tx.capital != nil {
		if err := tx.capital.CheckSubmission(*tx.state, c, now); err != nil {
			return nil, nil, err
		}
	}
	effect, err := tx.read(a.Intent.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	if effect != nil {
		if !actions.Equal(effect.Intent, a.Intent) {
			return nil, nil, actions.ErrConflict
		}
		return *effect, nil, nil
	}
	out, status, err := actions.Evaluate(*tx.state, a, now)
	e := domain.ActionEffect{Intent: a.Intent, Status: "completed", Outcome: &out}
	if err != nil {
		e.Status = status
		e.Outcome = nil
		e.Code = status + "_before_effect"
	}
	if err := tx.save(e); err != nil {
		return nil, nil, err
	}
	// No economic aggregate update occurs here. A crash after this commit leaves
	// a durable effect that acknowledgment/restart must reconcile by its key.
	return e, nil, nil
}

// ClaimActions applies the fenced delivery contract transactionally.
func (m *Memory) ClaimActions(ctx context.Context, worker string, now time.Time, lease time.Duration) ([]domain.ActionClaim, error) {
	value, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return claimActions(tx, worker, now, lease) })
	if err != nil {
		return nil, err
	}
	return clone(value.([]domain.ActionClaim)), nil
}

// ReconcileAction applies the fenced delivery contract transactionally.
func (m *Memory) ReconcileAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.Action, error) {
	value, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return reconcileAction(tx, claim, now) })
	if err != nil {
		return domain.Action{}, err
	}
	return clone(value.(domain.Action)), nil
}

// BeginAction applies the fenced delivery contract transactionally.
func (m *Memory) BeginAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.Action, error) {
	value, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return beginAction(tx, claim, now) })
	if err != nil {
		return domain.Action{}, err
	}
	return clone(value.(domain.Action)), nil
}

// SubmitAction applies the fenced delivery contract transactionally.
func (m *Memory) SubmitAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.ActionEffect, error) {
	value, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return submitAction(tx, claim, now) })
	if err != nil {
		return domain.ActionEffect{}, err
	}
	return clone(value.(domain.ActionEffect)), nil
}

// ClaimActions applies the fenced delivery contract transactionally.
func (p *Postgres) ClaimActions(ctx context.Context, worker string, now time.Time, lease time.Duration) ([]domain.ActionClaim, error) {
	value, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return claimActions(tx, worker, now, lease) })
	if err != nil {
		return nil, err
	}
	return clone(value.([]domain.ActionClaim)), nil
}

// ReconcileAction applies the fenced delivery contract transactionally.
func (p *Postgres) ReconcileAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.Action, error) {
	value, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return reconcileAction(tx, claim, now) })
	if err != nil {
		return domain.Action{}, err
	}
	return clone(value.(domain.Action)), nil
}

// BeginAction applies the fenced delivery contract transactionally.
func (p *Postgres) BeginAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.Action, error) {
	value, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return beginAction(tx, claim, now) })
	if err != nil {
		return domain.Action{}, err
	}
	return clone(value.(domain.Action)), nil
}

// SubmitAction applies the fenced delivery contract transactionally.
func (p *Postgres) SubmitAction(ctx context.Context, claim domain.ActionClaim, now time.Time) (domain.ActionEffect, error) {
	value, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return submitAction(tx, claim, now) })
	if err != nil {
		return domain.ActionEffect{}, err
	}
	return clone(value.(domain.ActionEffect)), nil
}

var _ ActionStore = (*Memory)(nil)
var _ ActionStore = (*Postgres)(nil)
