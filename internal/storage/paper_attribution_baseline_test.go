package storage

import (
	"testing"
	"time"
)

func baselineObservation(id, status string, entry, delta int64, at time.Time) PaperOutcomeRecord {
	return PaperOutcomeRecord{Command: PaperOutcomeCommand{ID: id}, EvaluatedAt: at, Evaluation: PaperOutcomeEvaluation{Status: status, EntryPriceCents: entry, ObservedPriceCents: entry + delta, PriceDeltaCents: delta}}
}

func TestPaperAttributionBaselineKnownResults(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	number := func(v int64) *int64 { return &v }
	closed := PaperEconomicAttribution{Status: "position_closed", RealizedPnLCents: number(150), ClosedCostCents: number(10000)}
	rising := []PaperOutcomeRecord{baselineObservation("waiting", "waiting_horizon", 0, 0, at), baselineObservation("old", "observed", 10000, 10, at), baselineObservation("new", "observed", 10000, 100, at.Add(time.Hour))}
	cases := []struct {
		name                      string
		economics                 PaperEconomicAttribution
		observations              []PaperOutcomeRecord
		status                    string
		outcome                   string
		passive, strategy, excess *int64
	}{
		{"beats cash and passive", closed, rising, "passive_and_no_action_compared", "new", number(100), number(150), number(50)},
		{"closed without a forward quote", closed, []PaperOutcomeRecord{baselineObservation("waiting", "waiting_horizon", 0, 0, at)}, "passive_observation_unavailable", "", nil, nil, nil},
		{"loss trails passive", PaperEconomicAttribution{Status: "position_closed", RealizedPnLCents: number(-25), ClosedCostCents: number(10000)}, rising, "passive_and_no_action_compared", "new", number(100), number(-25), number(-125)},
		{"open position has no strategy return", PaperEconomicAttribution{Status: "position_open"}, rising, "position_not_closed", "new", number(100), nil, nil},
		{"no-action shows only the move it passed on", PaperEconomicAttribution{Status: "owned_execution_unavailable"}, rising, "no_capital_committed", "new", number(100), nil, nil},
		{"no-action without an observation", PaperEconomicAttribution{Status: "owned_execution_unavailable"}, nil, "no_capital_committed", "", nil, nil, nil},
	}
	equal := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, got := PaperAttributionBaseline(c.economics, c.observations)
			if status != c.status {
				t.Fatal(status)
			}
			if c.passive == nil {
				if got != nil {
					t.Fatal("figures from no observation", got)
				}
				return
			}
			if got.Method != PaperBaselineMethod || got.OutcomeID != c.outcome || !equal(got.PassiveReturnBPS, c.passive) || !equal(got.StrategyReturnBPS, c.strategy) || !equal(got.ExcessOverPassiveBPS, c.excess) || !equal(got.ExcessOverNoActionBPS, c.strategy) {
				t.Fatal(*got)
			}
		})
	}
}
