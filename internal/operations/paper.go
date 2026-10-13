// Package operations enacts finalized actions and records execution outcomes.
package operations

import (
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

// ExecutePaper is a synchronous fixture adapter. Its fill price is the observed
// price (including stop gaps), with zero fees; it produces no qualification credit.
func ExecutePaper(state domain.State, o domain.Observation, d domain.Decision) (domain.ExecutionOutcome, error) {
	if o.Kind != "quote" || o.Source != "fixture" || o.Symbol != state.Strategy.Symbol || o.PriceCents <= 0 || o.PriceCents > 100000000 || d.Quantity <= 0 || d.Quantity > 10000 {
		return domain.ExecutionOutcome{}, errors.New("unsupported paper observation or quantity")
	}
	if d.Status != "finalized" || d.Action != "enter" && d.Action != "exit" || d.ObservationID != o.ID || d.CaseID != state.Case.ID || d.StrategyVersionID != state.Strategy.ID || d.Quantity != state.Strategy.Quantity || d.Safety != state.Strategy.Safety || o.ReceivedAt.Before(d.FinalizedAt) || !o.ReceivedAt.Before(d.ValidUntil) {
		return domain.ExecutionOutcome{}, errors.New("paper action does not match a valid finalized decision")
	}
	checks := make(map[string]bool)
	for _, check := range d.Evaluations {
		if !check.Passed {
			return domain.ExecutionOutcome{}, errors.New("paper action has a failed evaluation")
		}
		if checks[check.Check] {
			return domain.ExecutionOutcome{}, errors.New("duplicate evaluation")
		}
		checks[check.Check] = true
	}
	if !checks["quote_freshness"] || !checks["event_order"] || d.Action == "enter" && (!checks["notional_limit"] || !checks["protective_levels"]) {
		return domain.ExecutionOutcome{}, errors.New("paper action lacks evaluations")
	}
	if d.Safety.MaxQuoteAgeSeconds <= 0 || d.Safety.MaxQuoteAgeSeconds > 86400 || d.Safety.MaxHoldingSeconds <= 0 || d.Safety.MaxHoldingSeconds > 31536000 || o.OccurredAt.After(o.ReceivedAt) || o.ReceivedAt.Sub(o.OccurredAt) > time.Duration(d.Safety.MaxQuoteAgeSeconds)*time.Second || !state.LastOccurredAt.Equal(o.OccurredAt) {
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
	out := domain.ExecutionOutcome{ID: o.ID + "-execution", DecisionID: d.ID, Mode: "paper_fixture", Action: d.Action, Quantity: d.Quantity, PriceCents: o.PriceCents, FillModel: "observed_quote_zero_cost_v1", ExecutedAt: o.ReceivedAt}
	if d.Action == "exit" {
		out.RealizedPnLCents = (o.PriceCents - state.Case.Entry.PriceCents) * d.Quantity
	}
	return out, nil
}
