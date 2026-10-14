// Package workflow owns deterministic evaluation and decision finalization.
// It has no database, model, network, or order-submission access.
package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/operations"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func InitialState() domain.State {
	return domain.State{
		Strategy: library.FixtureStrategy(),
		Case:     domain.DecisionCase{ID: "demo-case-v1", StrategyVersionID: "demo-threshold-v1", Status: "waiting"},
		Routing:  routing.NewState(),
	}
}

func InitialStateFor(id string) (domain.State, error) {
	s, err := library.Strategy(id)
	if err != nil {
		return domain.State{}, err
	}
	if id == library.FixtureStrategy().ID {
		return InitialState(), nil
	}
	return domain.State{Strategy: s, Case: domain.DecisionCase{ID: id + "-case", StrategyVersionID: id, Status: "waiting"}, Routing: routing.NewState()}, nil
}

func Validate(o domain.Observation) error {
	if !identifier.MatchString(o.ID) {
		return errors.New("observation id must contain 1–128 letters, digits, underscores, or hyphens")
	}
	if !library.FixtureEntry().AcceptsQuote(o) {
		return errors.New("bootstrap accepts only fixture quotes for DEMO")
	}
	if o.PriceCents <= 0 || o.PriceCents > 100000000 {
		return errors.New("price_cents must be between 1 and 100000000")
	}
	if o.OccurredAt.IsZero() || o.ReceivedAt.IsZero() || o.OccurredAt.After(o.ReceivedAt) {
		return errors.New("observation requires occurrence and receipt times in causal order")
	}
	return nil
}

// Apply records rejected observations without allowing them to alter the last
// usable quote or create an economic action. The store owns retry deduplication.
func Apply(state domain.State, o domain.Observation) (domain.State, []domain.Event, error) {
	next, events, err := applyFixture(state, o)
	if err != nil {
		return state, nil, err
	}
	for _, event := range events {
		if event.Decision != nil && event.Decision.Action == "enter" {
			next.Routing, err = routing.BindFuture(next.Routing, *event.Decision, next.Strategy.Symbol)
			if err != nil {
				return state, nil, err
			}
		}
	}
	next.Routing, _, err = routing.ApplyQuote(next.Routing, o)
	if err != nil {
		return state, nil, err
	}
	if next.Case.Exit != nil {
		routing.CloseApplied(next.Routing, next.Case.ID, next.Case.Exit.ExecutedAt)
	}
	return next, events, nil
}

func applyFixture(state domain.State, o domain.Observation) (domain.State, []domain.Event, error) {
	if err := Validate(o); err != nil {
		return state, nil, err
	}
	events := []domain.Event{{ID: o.ID + "-observed", Kind: "observation.recorded", ObservationID: o.ID, At: o.ReceivedAt}}
	s := state.Strategy
	entry, err := library.ResolveStrategy(s)
	if err != nil {
		return state, nil, err
	}
	checks := []domain.EvaluationResult{
		{Check: "quote_freshness", Version: "v1", Passed: o.ReceivedAt.Sub(o.OccurredAt) <= time.Duration(s.Safety.MaxQuoteAgeSeconds)*time.Second, Code: "quote_age"},
		{Check: "event_order", Version: "v1", Passed: state.LastOccurredAt.IsZero() || o.OccurredAt.After(state.LastOccurredAt), Code: "strictly_newer_quote"},
	}
	valid := checks[0].Passed && checks[1].Passed
	if !valid {
		d := decision(state, o, "abstain", "unusable_observation", checks)
		events = append(events, decisionEvent(o, d))
		return state, events, nil
	}
	state.LastOccurredAt = o.OccurredAt
	if state.Case.Status == "closed" {
		return state, events, nil
	}
	action, reason := "", ""
	if state.Case.Entry == nil {
		if o.PriceCents > s.EntryAtOrBelowCents {
			return state, events, nil
		}
		action, reason = "enter", "entry_threshold"
		checks = append(checks,
			domain.EvaluationResult{Check: "notional_limit", Version: "v1", Passed: o.PriceCents*s.Quantity <= s.Safety.MaxNotionalCents, Code: "within_max_notional"},
			domain.EvaluationResult{Check: "protective_levels", Version: "v1", Passed: s.Safety.StopCents < o.PriceCents && o.PriceCents < s.Safety.TargetCents, Code: "stop_below_entry_below_target"})
	} else {
		switch {
		case o.PriceCents <= s.Safety.StopCents:
			action, reason = "exit", "stop_crossed"
		case o.PriceCents >= s.Safety.TargetCents:
			action, reason = "exit", "target_crossed"
		case !o.ReceivedAt.Before(state.Case.ExitBy):
			action, reason = "exit", "holding_deadline"
		default:
			return state, events, nil
		}
	}
	trigger := domain.Trigger{ID: o.ID + "-trigger", Kind: reason, Behavior: "decide", ObservationID: o.ID}
	if action == "exit" {
		trigger.Behavior = "act"
	}
	events = append(events, domain.Event{ID: trigger.ID, Kind: "trigger.fired", ObservationID: o.ID, At: o.ReceivedAt, Trigger: &trigger})
	if err := entry.Admit(action, checks); err != nil {
		action, reason = "abstain", "evaluation_failed"
	}
	d := decision(state, o, action, reason, checks)
	events = append(events, decisionEvent(o, d))
	// The synthetic operations adapter enforces the finalized contract again.
	if action == "abstain" {
		return state, events, nil
	}
	outcome, err := operations.ExecutePaper(state, o, d)
	if err != nil {
		return state, nil, err
	}
	if action == "enter" {
		state.Case.Status, state.Case.Entry = "open", &outcome
		state.Case.ExitBy = o.ReceivedAt.Add(time.Duration(s.Safety.MaxHoldingSeconds) * time.Second)
	} else {
		state.Case.Status, state.Case.Exit = "closed", &outcome
	}
	events = append(events, domain.Event{ID: outcome.ID, Kind: "execution.completed", ObservationID: o.ID, At: o.ReceivedAt, Outcome: &outcome})
	return state, events, nil
}

func decision(state domain.State, o domain.Observation, action, reason string, checks []domain.EvaluationResult) domain.Decision {
	d := domain.Decision{ID: o.ID + "-decision", CaseID: state.Case.ID, StrategyVersionID: state.Strategy.ID, ObservationID: o.ID, Action: action, ReasonCode: reason, Status: "finalized", FinalizedAt: o.ReceivedAt, ValidUntil: o.ReceivedAt.Add(30 * time.Second), Quantity: state.Strategy.Quantity, Safety: state.Strategy.Safety, Evaluations: checks}
	d.FutureConditions = routing.FutureDefinitions(d, state.Strategy.Symbol)
	return d
}

func decisionEvent(o domain.Observation, d domain.Decision) domain.Event {
	return domain.Event{ID: d.ID, Kind: "decision.finalized", ObservationID: o.ID, At: o.ReceivedAt, Decision: &d}
}

func Fixtures() []domain.Observation {
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	prices := []int64{10200, 10000, 10300, 9400}
	quotes := make([]domain.Observation, len(prices))
	for i, price := range prices {
		t := at.Add(time.Duration(i) * time.Minute)
		quotes[i] = domain.Observation{ID: fmt.Sprintf("demo-quote-%d", i+1), Kind: "quote", Source: "fixture", Symbol: "DEMO", OccurredAt: t, ReceivedAt: t, PriceCents: price}
	}
	return quotes
}
