package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"github.com/jackc/pgx/v5"
)

// ErrRecoveryConflict rejects changed content under a retained operator request.
var ErrRecoveryConflict = errors.New("recovery request conflicts")

// ErrRecoveryLimit bounds attempts for one retained exception.
var ErrRecoveryLimit = errors.New("exception recovery attempt limit reached")

// ErrExceptionNotFound identifies an unknown exception.
var ErrExceptionNotFound = errors.New("operational exception not found")

// ErrRecoveryInvalid rejects unsupported operations or clocks.
var ErrRecoveryInvalid = errors.New("invalid recovery request")
var recoveryKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// OperationalStore persists worker health and bounded requests under the aggregate lock.
type OperationalStore interface {
	ReportWorker(context.Context, domain.WorkerReport) error
	RecoverException(context.Context, string, string, string, time.Time) (domain.RecoveryReceipt, error)
}

func reportWorker(tx actionTx, r domain.WorkerReport) (any, []domain.Event, error) {
	events, err := operational.Report(tx.state, r)
	return nil, events, err
}

func recoverException(tx actionTx, id, requestID, operation string, now time.Time) (any, []domain.Event, error) {
	if !recoveryKey.MatchString(requestID) || !slices.Contains([]string{"recheck", "reconcile-action", "retry-protection"}, operation) || now.IsZero() {
		return nil, nil, ErrRecoveryInvalid
	}
	operational.Normalize(tx.state)
	e, ok := tx.state.Routing.Timers.Exceptions[id]
	if !ok {
		return nil, nil, ErrExceptionNotFound
	}
	for _, attempt := range e.RecoveryAttempts {
		if attempt.RequestID == requestID {
			if attempt.Operation != operation {
				return nil, nil, ErrRecoveryConflict
			}
			return domain.RecoveryReceipt{ExceptionID: id, Attempt: attempt, Duplicate: true}, nil, nil
		}
	}
	if len(e.RecoveryAttempts) >= 3 {
		return nil, nil, ErrRecoveryLimit
	}
	if now.Before(tx.state.Operations.LastClock) || tx.state.Actions != nil && now.Before(tx.state.Actions.LastClock) || now.Before(tx.state.Routing.Timers.LastClock) {
		return nil, nil, ErrRecoveryInvalid
	}
	events := operational.Sync(tx.state, now)
	e = tx.state.Routing.Timers.Exceptions[id]
	attempt := domain.RecoveryAttempt{RequestID: requestID, Operation: operation, At: now.UTC(), Outcome: "still_open"}
	if e.Status == "resolved" {
		attempt.Outcome = "already_resolved"
	} else {
		switch operation {
		case "recheck":
			attempt.Outcome = "still_open"
		case "reconcile-action":
			if e.ActionID == "" || tx.state.Actions == nil {
				attempt.Outcome = "blocked_no_action"
				break
			}
			a, exists := tx.state.Actions.Items[e.ActionID]
			if !exists {
				attempt.Outcome = "blocked_no_action"
				break
			}
			if actions.Terminal(a.Status) {
				e.Status, e.ResolvedAt, e.TerminalOutcome = "resolved", now.UTC(), "terminal_action_acknowledged"
				attempt.Outcome = "terminal_action_acknowledged"
				break
			}
			if a.Generation > 0 && now.Before(a.LeaseUntil) {
				attempt.Outcome = "blocked_live_lease"
				break
			}
			hash := sha256.Sum256([]byte(id + "/" + requestID))
			a.Worker, a.Generation, a.LeaseUntil = fmt.Sprintf("operator-%x", hash[:8]), a.Generation+1, now.Add(30*time.Second)
			tx.state.Actions.Items[e.ActionID] = a
			claim := domain.ActionClaim{ID: e.ActionID, Worker: a.Worker, Generation: a.Generation}
			value, changes, err := reconcileAction(tx, claim, now)
			if err != nil {
				if errors.Is(err, actions.ErrConflict) {
					attempt.Outcome = "blocked_reconciliation_conflict"
				} else {
					return nil, nil, err
				}
			} else {
				events = append(events, changes...)
				result := value.(domain.Action)
				attempt.Outcome = "reconciled_" + result.Status
				attempt.ActionID = result.Intent.ID
			}
			a = tx.state.Actions.Items[e.ActionID]
			if !actions.Terminal(a.Status) {
				a.LeaseUntil = now.UTC()
				tx.state.Actions.Items[e.ActionID] = a
			}
		case "retry-protection":
			if e.Capability != "protective_monitoring" || e.CaseID != tx.state.Case.ID {
				attempt.Outcome = "blocked_no_obligation"
				break
			}
			definitionID := e.DefinitionID
			if definitionID == "" {
				for _, d := range tx.state.Routing.Definitions {
					if d.SubjectCaseID == tx.state.Case.ID && d.Condition.Kind == "deadline" {
						definitionID = d.ID
						break
					}
				}
			}
			hash := sha256.Sum256([]byte(id + "/" + requestID))
			next, changes, outcome, err := workflow.RecoverProtection(*tx.state, definitionID, fmt.Sprintf("%x", hash[:16]), now)
			if err != nil {
				return nil, nil, err
			}
			*tx.state = next
			events = append(events, changes...)
			attempt.Outcome = outcome
			for _, event := range changes {
				if event.ActionID != "" {
					attempt.ActionID = event.ActionID
				}
			}
		}
	}
	// Reconciliation may resolve the price fault; preserve that new terminal state.
	current := tx.state.Routing.Timers.Exceptions[id]
	if current.Status == "resolved" && e.Status != "resolved" {
		e = current
	}
	e.RecoveryAttempts = append(e.RecoveryAttempts, attempt)
	tx.state.Routing.Timers.Exceptions[id] = e
	events = append(events, operational.Sync(tx.state, now)...)
	hash := sha256.Sum256([]byte(id + "/" + requestID))
	events = append(events, domain.Event{ID: fmt.Sprintf("recovery-%x", hash[:16]), Kind: "operations.recovery", ExceptionID: id, At: now.UTC(), Recovery: &attempt})
	tx.state.Operations.LastClock = now.UTC()
	return domain.RecoveryReceipt{ExceptionID: id, Attempt: attempt}, events, nil
}

// ReportWorker records capability progress without adding a heartbeat audit event.
func (m *Memory) ReportWorker(ctx context.Context, r domain.WorkerReport) error {
	_, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return reportWorker(tx, r) })
	return err
}

// ReportWorker records capability progress with transactional fault history.
func (p *Postgres) ReportWorker(ctx context.Context, r domain.WorkerReport) error {
	_, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) { return reportWorker(tx, r) })
	return err
}

// RecoverException records a bounded request without bypassing execution checks.
func (m *Memory) RecoverException(ctx context.Context, id, requestID, operation string, now time.Time) (domain.RecoveryReceipt, error) {
	v, err := m.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) {
		return recoverException(tx, id, requestID, operation, now)
	})
	if err != nil {
		return domain.RecoveryReceipt{}, err
	}
	return clone(v.(domain.RecoveryReceipt)), nil
}

// RecoverException serializes operator requests with delivery and timer workers.
func (p *Postgres) RecoverException(ctx context.Context, id, requestID, operation string, now time.Time) (domain.RecoveryReceipt, error) {
	v, err := p.actionMutation(ctx, func(tx actionTx) (any, []domain.Event, error) {
		return recoverException(tx, id, requestID, operation, now)
	})
	if err != nil {
		return domain.RecoveryReceipt{}, err
	}
	return clone(v.(domain.RecoveryReceipt)), nil
}

func backfillOperations(ctx context.Context, tx pgx.Tx) error {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		return err
	}
	var state domain.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	operational.Normalize(&state)
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", raw)
	return err
}
