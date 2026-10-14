// Package actions owns finalized intent and fenced delivery state, never providers.
package actions

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operations"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

// ErrLeaseLost rejects expired or replaced delivery claims.
var ErrLeaseLost = errors.New("action lease lost")

// ErrConflict rejects changed content under an immutable action key.
var ErrConflict = errors.New("action content conflicts")
var workerKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// NewState initializes the durable action queue.
func NewState() *domain.ActionState { return &domain.ActionState{Items: map[string]domain.Action{}} }

// Enqueue binds one stable idempotency key to a finalized decision and evidence.
func Enqueue(s *domain.State, d domain.Decision, o domain.Observation, mode, timerID string) (string, error) {
	if d.ID == "" || d.CaseID != s.Case.ID || d.StrategyVersionID != s.Strategy.ID || d.ObservationID != o.ID || d.Status != "finalized" || d.Action != "enter" && d.Action != "exit" || mode != "quote" && mode != "deadline" || mode == "quote" && timerID != "" || mode == "deadline" && (timerID == "" || d.Action != "exit" || d.ReasonCode != "holding_deadline") {
		return "", errors.New("action lacks finalized authority")
	}
	if s.Actions == nil {
		s.Actions = NewState()
	}
	hash := sha256.Sum256([]byte(d.ID))
	id := fmt.Sprintf("action-%x", hash[:])
	intent := domain.ActionIntent{Version: "paper-action-v1", ID: id, IdempotencyKey: id, Decision: d, Quote: o, Mode: mode, TimerID: timerID}
	if old, ok := s.Actions.Items[id]; ok {
		if !Equal(old.Intent, intent) {
			return "", ErrConflict
		}
		return id, nil
	}
	if len(s.Actions.Items) >= 256 {
		return "", errors.New("fixture action history capacity reached")
	}
	s.Actions.Items[id] = domain.Action{Intent: intent, Status: "pending", UpdatedAt: d.FinalizedAt}
	return id, nil
}

// Equal compares immutable structured adapter evidence.
func Equal(a, b any) bool {
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && string(left) == string(right)
}

// Active prevents a second intent while the current economic effect is unresolved.
func Active(s domain.State) bool {
	if s.Actions == nil {
		return false
	}
	for _, a := range s.Actions.Items {
		if !Terminal(a.Status) {
			return true
		}
	}
	return false
}

// Terminal distinguishes settled delivery states from unresolved outcomes.
func Terminal(status string) bool {
	return slices.Contains([]string{"completed", "rejected", "expired"}, status)
}

// Claim leases pending and uncertain work, preserving status across recovery.
func Claim(s *domain.State, worker string, now time.Time, lease time.Duration) ([]domain.ActionClaim, error) {
	if !workerKey.MatchString(worker) || now.IsZero() || lease < time.Second || lease > 5*time.Minute {
		return nil, errors.New("invalid action claim")
	}
	if s.Actions == nil {
		s.Actions = NewState()
	}
	if now.Before(s.Actions.LastClock) {
		return nil, routing.ErrClockBackwards
	}
	s.Actions.LastClock = now.UTC()
	ids := make([]string, 0, len(s.Actions.Items))
	for id := range s.Actions.Items {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	claims := []domain.ActionClaim{}
	for _, id := range ids {
		a := s.Actions.Items[id]
		if Terminal(a.Status) || a.Generation > 0 && now.Before(a.LeaseUntil) {
			continue
		}
		a.Worker, a.Generation, a.LeaseUntil = worker, a.Generation+1, now.Add(lease).UTC()
		s.Actions.Items[id] = a
		claims = append(claims, domain.ActionClaim{ID: id, Worker: worker, Generation: a.Generation})
		if len(claims) == 16 {
			break
		}
	}
	return claims, nil
}

// CheckClaim fences all mutations, including adapter submission.
func CheckClaim(s domain.State, c domain.ActionClaim, now time.Time) (domain.Action, error) {
	if s.Actions == nil {
		return domain.Action{}, ErrLeaseLost
	}
	a, ok := s.Actions.Items[c.ID]
	if !ok || c.Worker != a.Worker || c.Generation != a.Generation {
		return a, ErrLeaseLost
	}
	if Terminal(a.Status) {
		return a, nil
	}
	if now.IsZero() || !now.Before(a.LeaseUntil) {
		return a, ErrLeaseLost
	}
	if now.Before(s.Actions.LastClock) {
		return a, routing.ErrClockBackwards
	}
	return a, nil
}

// Evaluate rechecks current validity, quote age, safety, and position authority.
func Evaluate(s domain.State, a domain.Action, now time.Time) (domain.ExecutionOutcome, string, error) {
	d := a.Intent.Decision
	if a.Intent.Version != "paper-action-v1" || a.Intent.ID != a.Intent.IdempotencyKey || d.CaseID != s.Case.ID {
		return domain.ExecutionOutcome{}, "rejected", errors.New("unsupported action authority")
	}
	if !now.Before(d.ValidUntil) {
		return domain.ExecutionOutcome{}, "expired", errors.New("finalized action expired")
	}
	var out domain.ExecutionOutcome
	var err error
	if a.Intent.Mode == "deadline" {
		out, err = operations.ExecuteDeadline(s, a.Intent.Quote, d, now)
	} else {
		out, err = operations.ExecuteAt(s, a.Intent.Quote, d, now)
	}
	if err != nil {
		return out, "rejected", err
	}
	out.ID = a.Intent.ID + "-execution"
	return out, "", nil
}

// Settle records a reconciled adapter result. Known effects are reconciled before
// applying current expiry rules; an already enacted effect cannot be erased.
func Settle(s *domain.State, c domain.ActionClaim, e domain.ActionEffect, now time.Time) ([]domain.Event, error) {
	a, err := CheckClaim(*s, c, now)
	if err != nil {
		return nil, err
	}
	if Terminal(a.Status) {
		return nil, nil
	}
	if !Equal(a.Intent, e.Intent) || !slices.Contains([]string{"accepted", "partial", "completed", "rejected", "expired"}, e.Status) {
		return nil, ErrConflict
	}
	if e.Status == "completed" {
		o := e.Outcome
		d := a.Intent.Decision
		if o == nil || o.ID != a.Intent.ID+"-execution" || o.DecisionID != d.ID || o.Action != d.Action || o.Quantity != d.Quantity || o.Mode != "paper_fixture" || o.FillModel != "observed_quote_zero_cost_v1" {
			return nil, ErrConflict
		}
		if o.PriceCents != a.Intent.Quote.PriceCents || o.ExecutedAt.Before(d.FinalizedAt) || !o.ExecutedAt.Before(d.ValidUntil) {
			return nil, ErrConflict
		}
		pnl := int64(0)
		if d.Action == "exit" {
			if s.Case.Entry == nil {
				return nil, ErrConflict
			}
			pnl = (o.PriceCents - s.Case.Entry.PriceCents) * o.Quantity
		}
		if o.RealizedPnLCents != pnl {
			return nil, ErrConflict
		}
		if d.Action == "enter" {
			if s.Case.Entry != nil && !Equal(s.Case.Entry, o) {
				return nil, ErrConflict
			}
			s.Case.Entry = o
			s.Case.Status = "open"
			s.Case.ExitBy = d.FinalizedAt.Add(time.Duration(d.Safety.MaxHoldingSeconds) * time.Second)
		} else {
			if s.Case.Exit != nil && !Equal(s.Case.Exit, o) {
				return nil, ErrConflict
			}
			s.Case.Exit = o
			s.Case.Status = "closed"
			routing.CloseApplied(s.Routing, s.Case.ID, o.ExecutedAt)
		}
	}
	changed := a.Status != e.Status || !Equal(a.Outcome, e.Outcome) || a.Code != e.Code
	a.Status, a.Code, a.Outcome, a.UpdatedAt = e.Status, e.Code, e.Outcome, now.UTC()
	s.Actions.Items[c.ID] = a
	s.Actions.LastClock = now.UTC()
	if !changed {
		return nil, nil
	}
	effectRaw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	revision := sha256.Sum256(effectRaw)
	event := domain.Event{ID: fmt.Sprintf("%s-%d-%s-%x", c.ID, c.Generation, e.Status, revision[:8]), Kind: "action." + e.Status, ActionID: c.ID, At: now.UTC()}
	if e.Status == "completed" {
		event.ID = a.Intent.ID + "-execution"
		event.Kind = "execution.completed"
		event.Outcome = e.Outcome
	}
	return []domain.Event{event}, nil
}
