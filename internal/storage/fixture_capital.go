package storage

import (
	"context"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

const FixtureCapitalBindingScope = "bind_synthetic_fixture_capital_v1"

type FixtureCapitalBinding = domain.FixtureCapitalBinding

// FixtureCapitalReadback distinguishes the transaction bridge from the detached
// reducer snapshot. The in-memory binding has no restart or restore authority.
type FixtureCapitalReadback struct {
	Capital             fixturecapital.Snapshot `json:"capital"`
	WorkerBound         bool                    `json:"worker_bound"`
	Durable             bool                    `json:"durable"`
	QualificationCredit bool                    `json:"qualification_credit"`
}

// BindFixtureCapital opts only a pristine queued-memory fixture into account
// enforcement. The PostgreSQL method supplies the durable equivalent; neither
// internal method is exposed through HTTP or CLI activation commands.
func (m *Memory) BindFixtureCapital(ctx context.Context, c FixtureCapitalBinding) (FixtureCapitalReadback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if c.ReceiverID != m.receiverID || !ValidReceiverID(c.ReceiverID) {
		return FixtureCapitalReadback{}, ErrReceiverMismatch
	}
	a, ok := m.paperAccounts[c.AccountID]
	if !m.queued || c.ScopeCode != FixtureCapitalBindingScope || !ok || a.Config.ID != c.AccountID || a.Validate() != nil || a.SHA256 != c.AccountSHA256 || a.ReceiverID != c.ReceiverID {
		return FixtureCapitalReadback{}, fixturecapital.ErrInvalid
	}
	if m.fixtureCapital != nil {
		b, err := m.fixtureCapitalLocked()
		if err != nil {
			return FixtureCapitalReadback{}, err
		}
		v, _ := b.Snapshot()
		if v.AccountID != c.AccountID || v.AccountSHA256 != c.AccountSHA256 {
			return FixtureCapitalReadback{}, fixturecapital.ErrConflict
		}
		return FixtureCapitalReadback{Capital: v, WorkerBound: true}, nil
	}
	if !pristineFixture(m.state) || len(m.observations) != 0 || len(m.effects) != 0 {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	b, err := fixturecapital.New(a)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	m.fixtureCapital = &b
	m.state.FixtureCapitalBinding = &c
	v, err := b.Snapshot()
	return FixtureCapitalReadback{Capital: v, WorkerBound: true}, err
}

func pristineFixture(s domain.State) bool {
	entry, err := library.ResolveStrategy(s.Strategy)
	return err == nil && entry.EvidenceClass == "synthetic_unqualified" && s.Strategy.Symbol == "DEMO" && s.Case.ID != "" && s.Case.StrategyVersionID == s.Strategy.ID && s.Case.Status == "waiting" && s.Case.Entry == nil && s.Case.Exit == nil && s.LastOccurredAt.IsZero() && s.Actions != nil && len(s.Actions.Items) == 0 && s.FixtureCapitalBinding == nil
}

func (m *Memory) FixtureCapital(ctx context.Context) (FixtureCapitalReadback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FixtureCapitalReadback{}, err
	}
	b, err := m.fixtureCapitalLocked()
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if b == nil {
		return FixtureCapitalReadback{}, fixturecapital.ErrInvalid
	}
	v, err := b.Snapshot()
	return FixtureCapitalReadback{Capital: v, WorkerBound: true}, err
}

func (m *Memory) fixtureCapitalLocked() (*fixturecapital.Book, error) {
	if m.fixtureCapital == nil {
		if m.state.FixtureCapitalBinding != nil {
			return nil, fixturecapital.ErrConflict
		}
		return nil, nil
	}
	v, err := m.fixtureCapital.Snapshot()
	if err != nil {
		return nil, err
	}
	a, ok := m.paperAccounts[v.AccountID]
	want := FixtureCapitalBinding{ReceiverID: m.receiverID, AccountID: v.AccountID, AccountSHA256: v.AccountSHA256, ScopeCode: FixtureCapitalBindingScope}
	if !m.queued || !ok || a.Config.ID != v.AccountID || a.Validate() != nil || a.ReceiverID != m.receiverID || a.SHA256 != v.AccountSHA256 || !actions.Equal(m.state.FixtureCapitalBinding, want) {
		return nil, fixturecapital.ErrConflict
	}
	raw, err := m.fixtureCapital.Checkpoint()
	if err != nil {
		return nil, err
	}
	b, err := fixturecapital.Restore(a, raw, m.state, func(key string) (*domain.ActionEffect, error) {
		e, ok := m.effects[key]
		if !ok {
			return nil, nil
		}
		return &e, nil
	})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// Reconciliation uses the pre-ack action and the effect already read under the
// owning store lock. A pending action may be unreserved before its first Begin;
// unknown/known work must never acquire capital retroactively.
func reconcileCapital(tx actionTx, c domain.ActionClaim, a domain.Action, now time.Time, e *domain.ActionEffect) error {
	if tx.capital == nil {
		return nil
	}
	ok, err := tx.capital.Contains(a.Intent)
	if err != nil {
		return err
	}
	if !ok {
		if e != nil || a.Outcome != nil || (a.Status != "pending" && !actions.Terminal(a.Status)) || a.Status == "completed" {
			return fixturecapital.ErrConflict
		}
		return nil
	}
	b, err := tx.capital.Reconcile(*tx.state, c, now, func(key string) (*domain.ActionEffect, error) {
		if key != a.Intent.IdempotencyKey {
			return nil, errors.New("capital effect key mismatch")
		}
		return e, nil
	})
	if err != nil {
		return err
	}
	*tx.capital = b
	return nil
}
