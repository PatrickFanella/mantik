package library_test

import (
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func TestOptionCondorSelectsFeasiblePair(t *testing.T) {
	c := policyChain()
	// Sparse deltas: the ATM contracts are closest to the default .30 target.
	// Other known outer strikes can still form a supported complete condor.
	for i := range c.Entries {
		e := &c.Entries[i]
		g, err := options.Price(options.Inputs{Type: e.Type, Spot: 100, Strike: e.Strike, Years: 45.0 / 365, Volatility: .1})
		if err != nil {
			t.Fatal(err)
		}
		e.Close = g.Price
		e.IV = .1
		e.Greeks = g
	}
	cfg := library.DefaultOption(library.IronCondor)
	cfg.MaxContracts = 1
	recipe, _ := library.NewOption(cfg)
	p, err := recipe.Plan(c, 1000000, 1000000, 0, .65)
	if err != nil {
		t.Fatal(err)
	}
	viable := options.Structure{Kind: library.IronCondor, Legs: []options.Leg{
		{Symbol: "SPY260220P00090000", Contracts: 1, Multiplier: 100, Limit: 11},
		{Symbol: "SPY260220P00095000", Contracts: -1, Multiplier: 100, Limit: 9},
		{Symbol: "SPY260220C00105000", Contracts: -1, Multiplier: 100, Limit: 9},
		{Symbol: "SPY260220C00110000", Contracts: 1, Multiplier: 100, Limit: 11},
	}}
	for i := range viable.Legs {
		for _, e := range c.Entries {
			if e.Symbol == viable.Legs[i].Symbol {
				factor := 1.1
				if viable.Legs[i].Contracts < 0 {
					factor = .9
				}
				viable.Legs[i].Limit = e.Close * factor
			}
		}
	}
	risk, err := options.Analyze(c, viable, .65)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("known supported condor reserve=%.2f; recipe reasons=%v legs=%v", risk.Reserve, p.Reasons, p.Structure.Legs)
	if len(p.Structure.Legs) != 4 {
		t.Error("recipe reports incomplete despite a complete eligible known condor")
	}
}
