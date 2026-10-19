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

type FixtureCapitalBinding struct {
	ReceiverID    string `json:"receiver_id"`
	AccountID     string `json:"account_id"`
	AccountSHA256 string `json:"account_sha256"`
	ScopeCode     string `json:"scope_code"`
}

// FixtureCapitalReadback distinguishes the transaction bridge from the detached
// reducer snapshot. The in-memory binding has no restart or restore authority.
type FixtureCapitalReadback struct {
	Capital             fixturecapital.Snapshot `json:"capital"`
	WorkerBound         bool                    `json:"worker_bound"`
	Durable             bool                    `json:"durable"`
	QualificationCredit bool                    `json:"qualification_credit"`
}

// BindFixtureCapital opts only a pristine queued-memory fixture into account
// enforcement. There is deliberately no HTTP/CLI or PostgreSQL activation path
// until durable checkpoint validation and recovery have been implemented.
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
		v, err := m.fixtureCapital.Snapshot()
		if err != nil {
			return FixtureCapitalReadback{}, err
		}
		if v.AccountID != c.AccountID || v.AccountSHA256 != c.AccountSHA256 {
			return FixtureCapitalReadback{}, fixturecapital.ErrConflict
		}
		return FixtureCapitalReadback{Capital: v, WorkerBound: true}, nil
	}
	s := m.state
	entry, err := library.ResolveStrategy(s.Strategy)
	if err != nil || entry.EvidenceClass != "synthetic_unqualified" || s.Strategy.Symbol != "DEMO" || s.Case.ID == "" || s.Case.StrategyVersionID != s.Strategy.ID || s.Case.Status != "waiting" || s.Case.Entry != nil || s.Case.Exit != nil || !s.LastOccurredAt.IsZero() || len(m.observations) != 0 || len(m.effects) != 0 || s.Actions == nil || len(s.Actions.Items) != 0 {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	b, err := fixturecapital.New(a)
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	m.fixtureCapital = &b
	v, err := b.Snapshot()
	return FixtureCapitalReadback{Capital: v, WorkerBound: true}, err
}

func (m *Memory) FixtureCapital(ctx context.Context) (FixtureCapitalReadback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if m.fixtureCapital == nil {
		return FixtureCapitalReadback{}, fixturecapital.ErrInvalid
	}
	v, err := m.fixtureCapital.Snapshot()
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	a, ok := m.paperAccounts[v.AccountID]
	if !ok || a.Config.ID != v.AccountID || a.Validate() != nil || a.ReceiverID != m.receiverID || a.SHA256 != v.AccountSHA256 {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	return FixtureCapitalReadback{Capital: v, WorkerBound: true}, nil
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
