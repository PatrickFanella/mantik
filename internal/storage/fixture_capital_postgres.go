package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"github.com/jackc/pgx/v5"
)

type FixtureCapitalStore interface {
	BindFixtureCapital(context.Context, FixtureCapitalBinding) (FixtureCapitalReadback, error)
	FixtureCapital(context.Context) (FixtureCapitalReadback, error)
}

func fixtureEffectReader(ctx context.Context, tx pgx.Tx) fixturecapital.EffectReader {
	return func(key string) (*domain.ActionEffect, error) {
		var raw []byte
		err := tx.QueryRow(ctx, "SELECT payload FROM mantik_fixture_effects WHERE idempotency_key=$1", key).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var e domain.ActionEffect
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		return &e, nil
	}
}
func readFixtureCapital(ctx context.Context, tx pgx.Tx, s domain.State) (*fixturecapital.Book, error) {
	var id string
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT account_id,payload FROM mantik_fixture_capital WHERE id='demo'").Scan(&id, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		if s.FixtureCapitalBinding != nil {
			return nil, fixturecapital.ErrConflict
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := s.FixtureCapitalBinding
	if c == nil || c.AccountID != id || c.ScopeCode != FixtureCapitalBindingScope {
		return nil, fixturecapital.ErrConflict
	}
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton").Scan(&receiver); err != nil {
		return nil, err
	}
	if receiver != c.ReceiverID {
		return nil, ErrReceiverMismatch
	}
	var accountRaw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_paper_accounts WHERE id=$1", id).Scan(&accountRaw); err != nil {
		return nil, err
	}
	a, err := decodePaperAccount(accountRaw, id, receiver)
	if err != nil {
		return nil, err
	}
	if a.SHA256 != c.AccountSHA256 {
		return nil, fixturecapital.ErrConflict
	}
	b, err := fixturecapital.Restore(a, raw, s, fixtureEffectReader(ctx, tx))
	if err != nil {
		return nil, err
	}
	return &b, nil
}
func (p *Postgres) BindFixtureCapital(ctx context.Context, c FixtureCapitalBinding) (FixtureCapitalReadback, error) {
	if err := ValidateFixtureCapitalBinding(c); err != nil {
		return FixtureCapitalReadback{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return FixtureCapitalReadback{}, err
	}
	var s domain.State
	if err = json.Unmarshal(raw, &s); err != nil {
		return FixtureCapitalReadback{}, err
	}
	var receiver string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if receiver != c.ReceiverID || !ValidReceiverID(receiver) {
		return FixtureCapitalReadback{}, ErrReceiverMismatch
	}
	policies, err := readAllocationPolicies(ctx, tx, receiver, s)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if allocationOwnership(policies, c.AccountID) != nil {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	if c.ScopeCode != FixtureCapitalBindingScope {
		return FixtureCapitalReadback{}, fixturecapital.ErrInvalid
	}
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_paper_accounts WHERE id=$1", c.AccountID).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FixtureCapitalReadback{}, paperaccount.ErrNotFound
		}
		return FixtureCapitalReadback{}, err
	}
	a, err := decodePaperAccount(raw, c.AccountID, receiver)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if a.SHA256 != c.AccountSHA256 {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	b, err := readFixtureCapital(ctx, tx, s)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if b != nil {
		if !actions.Equal(s.FixtureCapitalBinding, c) {
			return FixtureCapitalReadback{}, fixturecapital.ErrConflict
		}
		v, err := b.Snapshot()
		if err != nil {
			return FixtureCapitalReadback{}, err
		}
		return capitalReadback(v, true), tx.Commit(ctx)
	}
	var dirty bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_observations) OR EXISTS(SELECT 1 FROM mantik_fixture_effects)").Scan(&dirty); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if dirty || !pristineFixture(s) {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	book, err := fixturecapital.New(a)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	checkpoint, err := book.Checkpoint()
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_fixture_capital(id,account_id,payload) VALUES('demo',$1,$2)", c.AccountID, checkpoint); err != nil {
		return FixtureCapitalReadback{}, err
	}
	s.FixtureCapitalBinding = &c
	raw, err = json.Marshal(s)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return FixtureCapitalReadback{}, err
	}
	v, err := book.Snapshot()
	return capitalReadback(v, true), err
}

func (p *Postgres) FixtureCapital(ctx context.Context) (FixtureCapitalReadback, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return FixtureCapitalReadback{}, err
	}
	var s domain.State
	if err = json.Unmarshal(raw, &s); err != nil {
		return FixtureCapitalReadback{}, err
	}
	b, err := readFixtureCapital(ctx, tx, s)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if b == nil {
		return FixtureCapitalReadback{}, ErrFixtureCapitalUnbound
	}
	v, err := b.Snapshot()
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	return capitalReadback(v, true), tx.Commit(ctx)
}

var _ FixtureCapitalStore = (*Memory)(nil)
var _ FixtureCapitalStore = (*Postgres)(nil)
