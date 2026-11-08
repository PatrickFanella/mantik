package cli

import (
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// The CLI recomputes a row's baseline, so forged figures never reach a display.
func TestAttributionBaselineIsRecomputed(t *testing.T) {
	realized, closed := int64(150), int64(10000)
	row := storage.PaperAttributionRow{
		Economics:    storage.PaperEconomicAttribution{Status: "position_closed", RealizedPnLCents: &realized, ClosedCostCents: &closed},
		Observations: []storage.PaperOutcomeRecord{{Command: storage.PaperOutcomeCommand{ID: "forward"}, EvaluatedAt: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC), Evaluation: storage.PaperOutcomeEvaluation{Status: "observed", EntryPriceCents: 10000, ObservedPriceCents: 10100, PriceDeltaCents: 100}}},
	}
	row.Economics.BaselineStatus, row.Economics.Baseline = storage.PaperAttributionBaseline(row.Economics, row.Observations)
	if err := validateAttributionBaseline(row); err != nil {
		t.Fatal(err)
	}
	display := renderBaseline(row.Economics)
	for _, want := range []string{"passive_and_no_action_compared", "passive_return_bps=100", "strategy_return_bps=150", "excess_over_passive_bps=50", "excess_over_no_action_bps=150"} {
		if !strings.Contains(display, want) {
			t.Fatal(display, want)
		}
	}
	forged := row
	inflated := *row.Economics.Baseline
	better := int64(9000)
	inflated.ExcessOverPassiveBPS = &better
	forged.Economics.Baseline = &inflated
	if validateAttributionBaseline(forged) == nil {
		t.Fatal("forged figures accepted")
	}
	forged = row
	forged.Economics.BaselineStatus = "position_not_closed"
	if validateAttributionBaseline(forged) == nil {
		t.Fatal("forged status accepted")
	}
	row.Observations = nil
	if validateAttributionBaseline(row) == nil {
		t.Fatal("figures accepted without their observation")
	}
}
