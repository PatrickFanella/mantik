package workflow

import (
	"encoding/json"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
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
	if definition.Behavior == "act" && (next.Case.Status != "open" || next.Case.Exit != nil) {
		receipt.Status = "superseded"
	} else {
		next.Routing, receipt.Routing, receipt.Status, err = routing.ApplyTimer(next.Routing, definition.ID, now)
		if err != nil {
			return state, receipt, err
		}
		if receipt.Status == "matched" && definition.Behavior == "act" {
			events, err := enforceDeadline(&next, definition, now)
			if err != nil {
				return state, receipt, err
			}
			receipt.Events = append(receipt.Events, events...)
			if next.Case.Exit != nil {
				receipt.Status = "executed"
			} else {
				receipt.Status = "exception"
			}
		}
	}
	receipt.Events = append(receipt.Events, domain.Event{ID: "timer-" + definition.ID + "-completed", Kind: "timer.completed", TimerID: definition.ID, At: now})
	routing.Complete(next.Routing, claim, receipt)
	return next, receipt, nil
}

func enforceDeadline(state *domain.State, d domain.TriggerDefinition, now time.Time) ([]domain.Event, error) {
	if state.Case.Entry == nil || d.SourceDecisionID != state.Case.Entry.DecisionID || d.SubjectCaseID != state.Case.ID || d.Condition.Kind != "deadline" || !d.Condition.DueAt.Equal(state.Case.ExitBy) {
		return nil, errors.New("protective timer does not bind current finalized entry")
	}
	prefix := "timer-" + d.ID
	quote := state.LastQuote
	usable := quote != nil && !quote.ReceivedAt.After(now) && !quote.OccurredAt.After(now) && now.Sub(quote.OccurredAt) <= time.Duration(state.Strategy.Safety.MaxQuoteAgeSeconds)*time.Second
	if !usable {
		exception := domain.OperationalException{ID: prefix + "-exception", CaseID: state.Case.ID, DefinitionID: d.ID, Code: "deadline_price_unavailable", Status: "open", At: now}
		state.Routing.Timers.Exceptions[exception.ID] = exception
		return []domain.Event{{ID: exception.ID, Kind: "operations.exception", TimerID: d.ID, At: now, Exception: &exception}}, nil
	}
	checks := []domain.EvaluationResult{{Check: "quote_freshness", Version: "v1", Passed: true, Code: "retained_quote_age_at_deadline"}, {Check: "event_order", Version: "v1", Passed: state.LastOccurredAt.Equal(quote.OccurredAt), Code: "current_retained_quote"}}
	decision := decision(*state, *quote, "exit", "holding_deadline", checks)
	decision.ID, decision.FinalizedAt, decision.ValidUntil = prefix+"-decision", now, now.Add(30*time.Second)
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
		if e.CaseID == state.Case.ID && e.Status == "open" {
			e.Status, e.ResolvedAt = "resolved", at.UTC()
			state.Routing.Timers.Exceptions[id] = e
		}
	}
}
