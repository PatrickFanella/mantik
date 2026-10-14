package workflow

import (
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/operations"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

// ProtectionClock prevents historical inputs from undoing clock-based safety.
// Idle scheduler polling alone does not change the fixture's simulation clock.
func ProtectionClock(state domain.State) time.Time {
	if state.Case.Entry == nil || state.Routing == nil || state.Routing.Timers == nil {
		return time.Time{}
	}
	var watermark time.Time
	for _, d := range state.Routing.Definitions {
		if d.Behavior == "act" && d.SubjectCaseID == state.Case.ID {
			job, ok := state.Routing.Timers.Jobs[d.ID]
			if ok && job.Generation > 0 {
				if job.ClaimedAt.After(watermark) {
					watermark = job.ClaimedAt
				}
				if job.CompletedAt.After(watermark) {
					watermark = job.CompletedAt
				}
			}
		}
	}
	return watermark
}

// CompleteTimer finalizes one leased clock obligation and its protective effect.
// Storage commits the state, retained receipt, and audit events atomically.
func CompleteTimer(state domain.State, claim domain.TimerClaim, now time.Time) (domain.State, domain.TimerReceipt, error) {
	return completeTimer(state, claim, now, false)
}

// CompleteTimerQueued prepares a protected exit for durable delivery.
func CompleteTimerQueued(state domain.State, claim domain.TimerClaim, now time.Time) (domain.State, domain.TimerReceipt, error) {
	return completeTimer(state, claim, now, true)
}

func completeTimer(state domain.State, claim domain.TimerClaim, now time.Time, queued bool) (domain.State, domain.TimerReceipt, error) {
	retained, err := routing.CheckClaim(state.Routing, claim, now)
	if err != nil {
		return state, domain.TimerReceipt{}, err
	}
	if retained != nil {
		return state, *retained, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return state, domain.TimerReceipt{}, err
	}
	var next domain.State
	if err := json.Unmarshal(raw, &next); err != nil {
		return state, domain.TimerReceipt{}, err
	}
	now = now.UTC()
	var definition domain.TriggerDefinition
	for _, d := range next.Routing.Definitions {
		if d.ID == claim.DefinitionID {
			definition = d
			break
		}
	}
	if definition.ID == "" {
		return state, domain.TimerReceipt{}, routing.ErrNotFound
	}
	receipt := domain.TimerReceipt{DefinitionID: definition.ID, At: now, Events: []domain.Event{}}
	if queued && definition.Behavior == "act" && actions.Active(next) {
		return state, receipt, errors.New("protection awaits action reconciliation")
	}
	if definition.Behavior == "act" && (next.Case.Status != "open" || next.Case.Exit != nil) {
		receipt.Status = "superseded"
	} else {
		next.Routing, receipt.Routing, receipt.Status, err = routing.ApplyTimer(next.Routing, definition.ID, now)
		if err != nil {
			return state, receipt, err
		}
		if receipt.Status == "matched" && definition.Behavior == "act" {
			events, err := enforceDeadline(&next, definition, now, queued)
			if err != nil {
				return state, receipt, err
			}
			receipt.Events = append(receipt.Events, events...)
			if next.Case.Exit != nil {
				receipt.Status = "executed"
			} else if queued && actions.Active(next) {
				receipt.Status = "queued"
			} else {
				receipt.Status = "exception"
			}
		}
	}
	receipt.Events = append(receipt.Events, domain.Event{ID: "timer-" + definition.ID + "-completed", Kind: "timer.completed", TimerID: definition.ID, At: now})
	routing.Complete(next.Routing, claim, receipt)
	return next, receipt, nil
}

func enforceDeadline(state *domain.State, d domain.TriggerDefinition, now time.Time, queued bool) ([]domain.Event, error) {
	if state.Case.Entry == nil || d.SourceDecisionID != state.Case.Entry.DecisionID || d.SubjectCaseID != state.Case.ID || d.Condition.Kind != "deadline" || !d.Condition.DueAt.Equal(state.Case.ExitBy) {
		return nil, errors.New("protective timer does not bind current finalized entry")
	}
	return enforceDeadlineWithID(state, d, now, queued, "timer-"+d.ID)
}

func enforceDeadlineWithID(state *domain.State, d domain.TriggerDefinition, now time.Time, queued bool, prefix string) ([]domain.Event, error) {
	if state.Case.Entry == nil || d.SourceDecisionID != state.Case.Entry.DecisionID || d.SubjectCaseID != state.Case.ID || d.Condition.Kind != "deadline" || !d.Condition.DueAt.Equal(state.Case.ExitBy) {
		return nil, errors.New("protective recovery lacks current finalized authority")
	}
	quote := state.LastQuote
	usable := quote != nil && !quote.ReceivedAt.After(now) && !quote.OccurredAt.After(now) && now.Sub(quote.OccurredAt) <= time.Duration(state.Strategy.Safety.MaxQuoteAgeSeconds)*time.Second
	if !usable {
		exception := domain.OperationalException{ID: prefix + "-exception", CaseID: state.Case.ID, DefinitionID: d.ID, Code: "deadline_price_unavailable", Capability: "protective_monitoring", Severity: "critical", EvidenceRefs: []string{"timer:" + d.ID, "case:" + state.Case.ID}, LastObservedAt: now, Status: "open", At: now}
		state.Routing.Timers.Exceptions[exception.ID] = exception
		return []domain.Event{{ID: exception.ID, Kind: "operations.exception", TimerID: d.ID, At: now, Exception: &exception}}, nil
	}
	checks := []domain.EvaluationResult{{Check: "quote_freshness", Version: "v1", Passed: true, Code: "retained_quote_age_at_deadline"}, {Check: "event_order", Version: "v1", Passed: state.LastOccurredAt.Equal(quote.OccurredAt), Code: "current_retained_quote"}}
	entry, err := library.ResolveStrategy(state.Strategy)
	if err != nil {
		return nil, err
	}
	if err := entry.Admit("exit", checks); err != nil {
		return nil, err
	}
	decision := decision(*state, *quote, "exit", "holding_deadline", checks)
	decision.ID, decision.FinalizedAt, decision.ValidUntil = prefix+"-decision", now, now.Add(30*time.Second)
	if queued {
		id, err := actions.Enqueue(state, decision, *quote, "deadline", d.ID)
		if err != nil {
			return nil, err
		}
		return []domain.Event{{ID: decision.ID, Kind: "decision.finalized", ActionID: id, TimerID: d.ID, At: now, Decision: &decision}}, nil
	}
	outcome, err := operations.ExecuteDeadline(*state, *quote, decision, now)
	if err != nil {
		return nil, err
	}
	state.Case.Status, state.Case.Exit = "closed", &outcome
	routing.CloseApplied(state.Routing, state.Case.ID, now)
	ResolveProtection(state, now)
	return []domain.Event{{ID: decision.ID, Kind: "decision.finalized", TimerID: d.ID, At: now, Decision: &decision}, {ID: outcome.ID, Kind: "execution.completed", TimerID: d.ID, At: now, Outcome: &outcome}}, nil
}

// ResolveProtection marks a retained exception resolved only after a real exit.
func ResolveProtection(state *domain.State, at time.Time) {
	if state.Case.Exit == nil || state.Routing == nil || state.Routing.Timers == nil {
		return
	}
	for id, e := range state.Routing.Timers.Exceptions {
		if e.CaseID == state.Case.ID && e.Status == "open" && e.Code == "deadline_price_unavailable" {
			e.Status, e.ResolvedAt, e.TerminalOutcome = "resolved", at.UTC(), "protective_exit_applied"
			state.Routing.Timers.Exceptions[id] = e
		}
	}
}

// RecoverProtection reevaluates an existing overdue safety obligation using
// current retained evidence. It never rewrites an expired decision's validity.
func RecoverProtection(state domain.State, definitionID, requestID string, now time.Time) (domain.State, []domain.Event, string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return state, nil, "", err
	}
	var next domain.State
	if err := json.Unmarshal(raw, &next); err != nil {
		return state, nil, "", err
	}
	if next.Case.Exit != nil {
		return next, nil, "already_exited", nil
	}
	if _, err := library.ResolveStrategy(next.Strategy); err != nil {
		return next, nil, "blocked_authority_unavailable", nil
	}
	if actions.Active(next) {
		return next, nil, "blocked_unresolved_action", nil
	}
	var definition domain.TriggerDefinition
	for _, d := range next.Routing.Definitions {
		if d.ID == definitionID {
			definition = d
			break
		}
	}
	if definition.ID == "" || definition.Condition.Kind != "deadline" || routing.ValidateDefinition(definition) != nil || now.Before(definition.Condition.DueAt) {
		return next, nil, "blocked_obligation_unavailable", nil
	}
	quote := next.LastQuote
	if quote == nil || quote.ReceivedAt.After(now) || now.Sub(quote.OccurredAt) > time.Duration(next.Strategy.Safety.MaxQuoteAgeSeconds)*time.Second {
		return next, nil, "blocked_price_unavailable", nil
	}
	if !next.LastOccurredAt.Equal(quote.OccurredAt) || next.Case.Entry == nil || definition.SourceDecisionID != next.Case.Entry.DecisionID || definition.SubjectCaseID != next.Case.ID || !definition.Condition.DueAt.Equal(next.Case.ExitBy) {
		return next, nil, "blocked_authority_unavailable", nil
	}
	// Reuse the same safety evaluator, with a new recovery decision identity.
	events, err := enforceDeadlineWithID(&next, definition, now, true, "recovery-"+requestID)
	if err != nil {
		return state, nil, "", err
	}
	return next, events, "queued", nil
}
