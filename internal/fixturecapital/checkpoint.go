package fixturecapital

import (
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxTransitions = 1024
const MaxCheckpointBytes = 4 << 20
const CheckpointSchema = "mantik-fixture-capital-checkpoint-v1"

type transition struct {
	Kind         string               `json:"kind"`
	State        domain.State         `json:"state"`
	Claim        domain.ActionClaim   `json:"claim"`
	At           time.Time            `json:"at"`
	Effect       *domain.ActionEffect `json:"effect,omitempty"`
	ResultSHA256 string               `json:"result_sha256"`
}
type checkpoint struct {
	Schema        string       `json:"schema"`
	AccountSHA256 string       `json:"account_sha256"`
	Transitions   []transition `json:"transitions"`
	ResultSHA256  string       `json:"result_sha256"`
	SHA256        string       `json:"sha256"`
}

func (b Book) finish(n Book, kind string, s domain.State, c domain.ActionClaim, at time.Time, e *domain.ActionEffect) (Book, error) {
	before, err := b.Snapshot()
	if err != nil {
		return Book{}, err
	}
	after, err := n.Snapshot()
	if err != nil {
		return Book{}, err
	}
	if before.SHA256 == after.SHA256 {
		return n, nil
	}
	if len(n.transitions) >= MaxTransitions {
		return Book{}, ErrCapacity
	}
	if len(n.transitions) > 0 && at.Before(n.transitions[len(n.transitions)-1].At) {
		return Book{}, ErrConflict
	}
	n.transitions = append(n.transitions, detach(transition{Kind: kind, State: validationState(s, s.Actions.Items[c.ID]), Claim: c, At: at.UTC(), Effect: e, ResultSHA256: after.SHA256}))
	if _, err := n.Checkpoint(); err != nil {
		return Book{}, err
	}
	return n, nil
}

// Checkpoint is private store material, not an import command or readback. Every
// changing transition includes its bounded pre-ack source and result digest.
// Unchanged polling/retry calls do not grow the history.
func (b Book) Checkpoint() ([]byte, error) {
	s, err := b.Snapshot()
	if err != nil {
		return nil, ErrInvalid
	}
	if len(b.transitions) > MaxTransitions {
		return nil, ErrCapacity
	}
	steps := b.transitions
	if steps == nil {
		steps = []transition{}
	}
	c := checkpoint{Schema: CheckpointSchema, AccountSHA256: b.account.SHA256, Transitions: steps, ResultSHA256: s.SHA256}
	c.SHA256 = digest(c)
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxCheckpointBytes {
		return nil, ErrCapacity
	}
	return raw, nil
}

// Restore replays only a store-owned checkpoint, then checks it against that
// store's independently read current aggregate and fixture effects. The caller
// must hold one consistent transaction/lock for account, state and effects.
// A snapshot, rehearsal or self-reported balance cannot restore authority.
func Restore(a paperaccount.Account, raw []byte, actual domain.State, read EffectReader) (Book, error) {
	var c checkpoint
	if workerplan.ReadBoundedShape(raw, &c, MaxCheckpointBytes) != nil || c.Schema != CheckpointSchema || c.AccountSHA256 != a.SHA256 || c.Transitions == nil || len(c.Transitions) > MaxTransitions || read == nil {
		return Book{}, ErrInvalid
	}
	want := c.SHA256
	c.SHA256 = ""
	if digest(c) != want {
		return Book{}, ErrInvalid
	}
	c.SHA256 = want
	b, err := New(a)
	if err != nil {
		return Book{}, err
	}
	for _, step := range c.Transitions {
		if step.State.Actions == nil || actual.Actions == nil || !actions.Equal(step.State.Strategy, actual.Strategy) || step.State.Case.ID != actual.Case.ID || step.State.Case.StrategyVersionID != actual.Case.StrategyVersionID {
			return Book{}, ErrConflict
		}
		source, ok := actual.Actions.Items[step.Claim.ID]
		old, oldOK := step.State.Actions.Items[step.Claim.ID]
		if !ok || !oldOK || !actions.Equal(source.Intent, old.Intent) {
			return Book{}, ErrConflict
		}
		switch step.Kind {
		case "reserve":
			if step.Effect != nil {
				return Book{}, ErrInvalid
			}
			b, err = b.Reserve(step.State, step.Claim, step.At)
		case "reconcile":
			b, err = b.Reconcile(step.State, step.Claim, step.At, func(key string) (*domain.ActionEffect, error) {
				if key != source.Intent.IdempotencyKey {
					return nil, ErrConflict
				}
				return step.Effect, nil
			})
		default:
			return Book{}, ErrInvalid
		}
		if err != nil {
			return Book{}, err
		}
		s, err := b.Snapshot()
		if err != nil || s.SHA256 != step.ResultSHA256 {
			return Book{}, ErrInvalid
		}
	}
	// This also rejects redundant/ignored fields, duplicate transitions and an
	// internally consistent but fabricated final balance/result digest.
	rebuilt, err := b.Checkpoint()
	if err != nil {
		return Book{}, err
	}
	var canonical checkpoint
	if json.Unmarshal(rebuilt, &canonical) != nil || !actions.Equal(c, canonical) {
		return Book{}, ErrInvalid
	}
	if err = b.CheckSource(actual, read); err != nil {
		return Book{}, err
	}
	return b, nil
}

// CheckSource permits an independently committed, not-yet-acknowledged effect,
// but never credits it. It also works after a lease expires: shape validation
// below uses a detached lease solely to check an effect, not to grant a claim.
func (b Book) CheckSource(s domain.State, read EffectReader) error {
	entry, err := library.ResolveStrategy(s.Strategy)
	if !b.valid() || read == nil || s.Actions == nil || len(s.Actions.Items) > MaxCommitments || err != nil || entry.EvidenceClass != "synthetic_unqualified" || s.Strategy.Symbol != "DEMO" || s.Case.ID == "" || s.Case.StrategyVersionID != s.Strategy.ID {
		return ErrInvalid
	}
	var opened, closed *domain.ExecutionOutcome
	var exitBy time.Time
	for id, c := range b.commitments {
		a, ok := s.Actions.Items[id]
		if !ok || !actions.Equal(a.Intent, c.Intent) {
			return ErrConflict
		}
		e, err := read(c.Intent.IdempotencyKey)
		if err != nil {
			return err
		}
		if e != nil && !actions.Equal(e.Intent, c.Intent) {
			return ErrConflict
		}
		switch c.Status {
		case "completed":
			if a.Status != "completed" || c.Effect == nil || e == nil || !actions.Equal(*c.Effect, *e) || !actions.Equal(a.Outcome, e.Outcome) || a.Code != e.Code {
				return ErrConflict
			}
			if c.Intent.Decision.Action == "enter" {
				if opened != nil {
					return ErrConflict
				}
				opened = c.Effect.Outcome
				exitBy = c.Intent.Decision.FinalizedAt.Add(time.Duration(c.Intent.Decision.Safety.MaxHoldingSeconds) * time.Second)
			} else {
				if closed != nil {
					return ErrConflict
				}
				closed = c.Effect.Outcome
			}
		case "released":
			if !actions.Terminal(a.Status) || a.Status == "completed" || a.Outcome != nil || !actions.Equal(e, c.Effect) {
				return ErrConflict
			}
			if e != nil && (a.Status != e.Status || a.Code != e.Code) {
				return ErrConflict
			}
		case "reserved", "held":
			if actions.Terminal(a.Status) {
				return ErrConflict
			}
			if c.Effect != nil {
				if e == nil || a.Status != c.Effect.Status || !actions.Equal(a.Outcome, c.Effect.Outcome) || a.Code != c.Effect.Code {
					return ErrConflict
				}
			} else if a.Status != "pending" && a.Status != "unknown" || a.Outcome != nil {
				return ErrConflict
			}
			if e != nil {
				check := validationState(s, a)
				at := a.UpdatedAt
				if at.IsZero() {
					return ErrInvalid
				}
				a.LeaseUntil = at.Add(time.Minute)
				check.Actions.LastClock = at
				check.Actions.Items[id] = a
				if _, err := actions.Settle(&check, domain.ActionClaim{ID: id, Worker: a.Worker, Generation: a.Generation}, *e, at); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
	}
	for id, a := range s.Actions.Items {
		if _, ok := b.commitments[id]; ok {
			continue
		}
		if a.Status != "pending" && (a.Status != "rejected" && a.Status != "expired" || a.Code != "fixture_capital_limit" && a.Code != "current_authority_unavailable") || a.Outcome != nil {
			return ErrConflict
		}
		e, err := read(a.Intent.IdempotencyKey)
		if err != nil {
			return err
		}
		if e != nil {
			return ErrConflict
		}
	}
	if !actions.Equal(s.Case.Entry, opened) || !actions.Equal(s.Case.Exit, closed) || !s.Case.ExitBy.Equal(exitBy) {
		return ErrConflict
	}
	wantStatus := "waiting"
	if opened != nil {
		wantStatus = "open"
	}
	if closed != nil {
		wantStatus = "closed"
	}
	if s.Case.Status != wantStatus {
		return ErrConflict
	}
	return nil
}
