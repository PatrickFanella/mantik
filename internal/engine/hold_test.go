package engine

import (
	"context"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

type once struct{}

func (once) Spec() strategy.Spec { return allIn{}.Spec() }
func (once) Decide(v strategy.View) (strategy.Decision, error) {
	if len(v.State()) == 0 {
		return strategy.Decision{Targets: []strategy.Target{{Asset: asset, Weight: "0.5"}}, State: []byte("held")}, nil
	}
	return strategy.Decision{KeepPositions: true, State: []byte("held")}, nil
}
func TestKeepPositionsPreservesSharesWithoutDailyRebalance(t *testing.T) {
	cfg := config(newSource(t, prices), once{}, CostModel{Version: "free", FeeBps: "0", SlippageBps: "0"})
	r, e := Run(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Fills) != 1 || r.Fills[0].Date != "2026-01-06" || len(r.Pending) != 0 {
		t.Fatal("hold created replacement orders", r.Fills, r.Pending)
	}
	if r.Days[1].Positions[0].Shares != r.Days[4].Positions[0].Shares {
		t.Fatal("hold changed shares")
	}
}
