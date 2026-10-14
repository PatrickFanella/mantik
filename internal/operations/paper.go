// Package operations enacts finalized actions and records execution outcomes.
package operations

import (
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

// ExecutePaper is a synchronous fixture adapter. Its fill price is the observed
// price (including stop gaps), with zero fees; it produces no qualification credit.
func ExecutePaper(state domain.State, o domain.Observation, d domain.Decision) (domain.ExecutionOutcome, error) {
	return executePaperAt(state, o, d, o.ReceivedAt)
}

// ExecuteDeadline uses retained price evidence at an independent execution time.
// It never changes the observation timestamp or fills from stale evidence.
func ExecuteDeadline(state domain.State, o domain.Observation, d domain.Decision, at time.Time) (domain.ExecutionOutcome, error) {
	if d.Action != "exit" || d.ReasonCode != "holding_deadline" || state.Case.ExitBy.IsZero() || at.Before(state.Case.ExitBy) {
		return domain.ExecutionOutcome{}, errors.New("deadline action lacks a due exit obligation")
	}
	out, err := executePaperAt(state, o, d, at)
	if err != nil {
		return out, err
	}
	out.ID = d.ID + "-execution"
	return out, nil
}

func executePaperAt(state domain.State, o domain.Observation, d domain.Decision, at time.Time) (domain.ExecutionOutcome, error) {
	if o.Kind != "quote" || o.Source != "fixture" || o.Symbol != state.Strategy.Symbol || o.PriceCents <= 0 || o.PriceCents > 100000000 || d.Quantity <= 0 || d.Quantity > 10000 {
		return domain.ExecutionOutcome{}, errors.New("unsupported paper observation or quantity")
	}
	if d.Status != "finalized" || d.Action != "enter" && d.Action != "exit" || d.ObservationID != o.ID || d.CaseID != state.Case.ID || d.StrategyVersionID != state.Strategy.ID || d.Quantity != state.Strategy.Quantity || d.Safety != state.Strategy.Safety || at.Before(d.FinalizedAt) || !at.Before(d.ValidUntil) || o.ReceivedAt.After(at) {
		return domain.ExecutionOutcome{}, errors.New("paper action does not match a valid finalized decision")
	}
	entry, err := library.ResolveStrategy(state.Strategy)
	if err != nil {
		return domain.ExecutionOutcome{}, err
	}
	if !entry.AcceptsQuote(o) {
		return domain.ExecutionOutcome{}, errors.New("observation does not match library inputs")
	}
	if err := entry.Admit(d.Action, d.Evaluations); err != nil {
		return domain.ExecutionOutcome{}, err
	}

	if d.Safety.MaxQuoteAgeSeconds <= 0 || d.Safety.MaxQuoteAgeSeconds > 86400 || d.Safety.MaxHoldingSeconds <= 0 || d.Safety.MaxHoldingSeconds > 31536000 || o.OccurredAt.After(o.ReceivedAt) || at.Sub(o.OccurredAt) > time.Duration(d.Safety.MaxQuoteAgeSeconds)*time.Second || !state.LastOccurredAt.Equal(o.OccurredAt) {
		return domain.ExecutionOutcome{}, errors.New("paper action lacks a current usable quote")
	}
	if d.Action == "enter" && (state.Case.Status != "waiting" || state.Case.Entry != nil || o.PriceCents*d.Quantity > d.Safety.MaxNotionalCents) {
		return domain.ExecutionOutcome{}, errors.New("entry exceeds current authority")
	}
	if d.Action == "enter" && (o.PriceCents > state.Strategy.EntryAtOrBelowCents || d.Safety.StopCents <= 0 || d.Safety.StopCents >= o.PriceCents || d.Safety.TargetCents <= o.PriceCents) {
		return domain.ExecutionOutcome{}, errors.New("entry protective levels are invalid")
	}
	if d.Action == "exit" && (state.Case.Status != "open" || state.Case.Entry == nil || state.Case.Exit != nil) {
		return domain.ExecutionOutcome{}, errors.New("exit requires an open position")
	}
	out := domain.ExecutionOutcome{ID: o.ID + "-execution", DecisionID: d.ID, Mode: "paper_fixture", Action: d.Action, Quantity: d.Quantity, PriceCents: o.PriceCents, FillModel: "observed_quote_zero_cost_v1", ExecutedAt: at}
	if d.Action == "exit" {
		out.RealizedPnLCents = (o.PriceCents - state.Case.Entry.PriceCents) * d.Quantity
	}
	return out, nil
}
