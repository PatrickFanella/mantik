package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

const FixtureCapitalBindingScope = "bind_synthetic_fixture_capital_v1"
const FixtureCapitalReadbackSchema = "mantik-fixture-capital-readback-v1"
const FixtureCapitalReadbackAuthority = "service_retained_synthetic_fixture_capital_only"

var ErrFixtureCapitalUnbound = fmt.Errorf("%w: fixture capital is unbound", fixturecapital.ErrInvalid)
var ErrFixtureCapitalBinding = fmt.Errorf("%w: invalid fixture capital binding command", fixturecapital.ErrInvalid)
var capitalDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type FixtureCapitalBinding = domain.FixtureCapitalBinding

func ValidateFixtureCapitalBinding(c FixtureCapitalBinding) error {
	if !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.AccountID) || !capitalDigest.MatchString(c.AccountSHA256) || c.ScopeCode != FixtureCapitalBindingScope {
		return ErrFixtureCapitalBinding
	}
	return nil
}

// FixtureCapitalReadback distinguishes the transaction bridge from the detached
// reducer snapshot. The in-memory binding has no restart or restore authority.
type FixtureCapitalReadback struct {
	Schema              string                  `json:"schema"`
	Authority           string                  `json:"authority"`
	Capital             fixturecapital.Snapshot `json:"capital"`
	WorkerBound         bool                    `json:"worker_bound"`
	Durable             bool                    `json:"durable"`
	QualificationCredit bool                    `json:"qualification_credit"`
}

func capitalReadback(s fixturecapital.Snapshot, durable bool) FixtureCapitalReadback {
	return FixtureCapitalReadback{Schema: FixtureCapitalReadbackSchema, Authority: FixtureCapitalReadbackAuthority, Capital: s, WorkerBound: true, Durable: durable}
}

func (v FixtureCapitalReadback) Validate(a paperaccount.Account) error {
	if v.Schema != FixtureCapitalReadbackSchema || v.Authority != FixtureCapitalReadbackAuthority || !v.WorkerBound || v.QualificationCredit {
		return fixturecapital.ErrInvalid
	}
	return v.Capital.Validate(a)
}

// BindFixtureCapital opts only a pristine queued-memory fixture into account
// enforcement. The PostgreSQL method supplies the durable equivalent. The
// service exposes the explicit binding contract without adopting active cases.
func (m *Memory) BindFixtureCapital(ctx context.Context, c FixtureCapitalBinding) (FixtureCapitalReadback, error) {
	if err := ValidateFixtureCapitalBinding(c); err != nil {
		return FixtureCapitalReadback{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FixtureCapitalReadback{}, err
	}
	if c.ReceiverID != m.receiverID || !ValidReceiverID(c.ReceiverID) {
		return FixtureCapitalReadback{}, ErrReceiverMismatch
	}
	policies, err := m.allocationPoliciesLocked()
	if err != nil {
		return FixtureCapitalReadback{}, err
	}
	if allocationOwnership(policies, c.AccountID) != nil {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	a, ok := m.paperAccounts[c.AccountID]
	if !ok {
		return FixtureCapitalReadback{}, paperaccount.ErrNotFound
	}
	if a.SHA256 != c.AccountSHA256 {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	if !m.queued {
		return FixtureCapitalReadback{}, fixturecapital.ErrConflict
	}
	if a.Config.ID != c.AccountID || a.Validate() != nil || a.ReceiverID != c.ReceiverID {
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
		return capitalReadback(v, false), nil
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
	return capitalReadback(v, false), err
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
		return FixtureCapitalReadback{}, ErrFixtureCapitalUnbound
	}
	v, err := b.Snapshot()
	return capitalReadback(v, false), err
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
