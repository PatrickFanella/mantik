package library_test

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

// Deliberately synthetic, fixed selection inputs: these test Mantik's declared
// policy, not a paper's return series or an upstream executable strategy.
func policyChain() options.Chain {
	at := time.Date(2026, 1, 6, 22, 0, 0, 0, time.UTC)
	c := options.Chain{AssetID: "spy", Underlying: "SPY", AsOf: at, Feed: "indicative", Model: options.ModelVersion, Assumptions: options.Assumptions{MaxAge: 24 * time.Hour}, Spot: options.Spot{AssetID: "spy", Underlying: "SPY", Date: "2026-01-06", Price: 100, At: at.Add(-time.Hour), ObservedAt: at, AvailableAt: at}}
	for _, right := range []string{"call", "put"} {
		for i, k := range []int{90, 95, 100, 105, 110} {
			delta := []float64{.8, .65, .5, .35, .2}[i]
			char := "C"
			if right == "put" {
				delta--
				char = "P"
			}
			symbol := fmt.Sprintf("SPY260220%s%08d", char, k*1000)
			c.Entries = append(c.Entries, options.Entry{Symbol: symbol, ContractID: symbol, Type: right, Expiry: "2026-02-20", Strike: float64(k), Close: 10, ReferenceObservedAt: at, ReferenceAvailableAt: at, PriceOccurredAt: at.Add(-17 * time.Hour), PriceObservedAt: at, PriceAvailableAt: at, FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: .3, Greeks: options.Greeks{Price: 10, Delta: delta, Gamma: .01, Vega: 5, Theta: -.01}})
		}
	}
	return c
}

func TestOptionReviewedPolicySelectionsAndSizing(t *testing.T) {
	cases := []struct {
		kind    string
		symbols []string
		signs   []int
	}{
		{library.CoveredCall, []string{"SPY260220C00105000"}, []int{-1}},
		{library.CashSecuredPut, []string{"SPY260220P00095000"}, []int{-1}},
		{library.Vertical, []string{"SPY260220P00090000", "SPY260220P00095000"}, []int{1, -1}},
		{library.IronCondor, []string{"SPY260220P00090000", "SPY260220P00095000", "SPY260220C00105000", "SPY260220C00110000"}, []int{1, -1, -1, 1}},
		{library.IronButterfly, []string{"SPY260220P00095000", "SPY260220P00100000", "SPY260220C00100000", "SPY260220C00105000"}, []int{1, -1, -1, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			c := library.DefaultOption(tc.kind)
			c.MaxContracts = 2
			s, err := library.NewOption(c)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := s.Plan(policyChain(), 1000000, 1000000, 200, .65)
			if err != nil || len(plan.Reasons) > 0 || len(plan.Structure.Legs) != len(tc.symbols) {
				t.Fatal(plan, err)
			}
			for i, leg := range plan.Structure.Legs {
				if leg.Symbol != tc.symbols[i] || leg.Contracts != 2*tc.signs[i] || leg.Multiplier != 100 {
					t.Fatal(leg, tc)
				}
				want := 11.0
				if tc.signs[i] < 0 {
					want = 9
				}
				if math.Abs(leg.Limit-want) > 1e-12 {
					t.Fatal(leg)
				}
			}
			if plan.Risk.Reserve < plan.Risk.MaxLoss+plan.Risk.WorstPartialLoss || plan.Risk.Reserve > 50000 {
				t.Fatal(plan.Risk)
			}
			// The returned chain cannot rewrite the source cut.
			chain := policyChain()
			p, err := s.Plan(chain, 1000000, 1000000, 200, .65)
			if err != nil {
				t.Fatal(err)
			}
			p.Chain.Entries[0].Close = 99
			if chain.Entries[0].Close != 10 {
				t.Fatal("aliased chain")
			}
		})
	}
	c := library.DefaultOption(library.CashSecuredPut)
	s, err := library.NewOption(c)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Plan(policyChain(), 400000, 20000, 0, .65)
	if err != nil || len(p.Reasons) > 0 || p.Structure.Legs[0].Contracts != -1 {
		t.Fatal("reserve budget sizing", p, err)
	}
	p, err = s.Plan(policyChain(), 2000, 2000, 0, .65)
	if err != nil || !slices.Contains(p.Reasons, "option_recipe_no_funded_contract") || len(p.Structure.Legs) > 0 {
		t.Fatal(p, err)
	}
	c = library.DefaultOption(library.CoveredCall)
	s, err = library.NewOption(c)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Plan(policyChain(), 1000000, 1000000, 99, .65)
	if err != nil || !slices.Contains(p.Reasons, "option_recipe_no_funded_contract") {
		t.Fatal(p, err)
	}
}

func TestOptionRecipeIdentityCopiesAndInvalidInputs(t *testing.T) {
	c := library.DefaultOption(library.Vertical)
	s, err := library.NewOption(c)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Spec().ID()
	if err != nil {
		t.Fatal(err)
	}
	other := s.Spec()
	other.Params["recipe_sha256"] = "changed"
	other.Data[0] = "changed"
	if got, err := s.Spec().ID(); err != nil || id != got {
		t.Fatal(got, err)
	}
	provenance := s.Provenance()
	provenance.References[0] = "changed"
	if s.Provenance().References[0] == "changed" || s.Provenance().Evidence != "indicative_unqualified" {
		t.Fatal("aliased provenance")
	}
	for _, change := range []func(*library.OptionConfig){
		func(c *library.OptionConfig) { c.Kind = "unreviewed" }, func(c *library.OptionConfig) { c.MinDTE = 0 },
		func(c *library.OptionConfig) { c.TargetDTE = 0 }, func(c *library.OptionConfig) { c.MaxDTE = 366 },
		func(c *library.OptionConfig) { c.TargetDelta = math.NaN() }, func(c *library.OptionConfig) { c.WingWidth = 0 },
		func(c *library.OptionConfig) { c.LimitBuffer = 1 }, func(c *library.OptionConfig) { c.ReserveFraction = .11 },
		func(c *library.OptionConfig) { c.MaxContracts = 101 }, func(c *library.OptionConfig) { c.HoldSessions = 0 },
	} {
		bad := c
		change(&bad)
		if _, err := library.NewOption(bad); err == nil {
			t.Fatal("invalid config accepted", bad)
		}
	}
	// Every configurable field participates in identity, even if this specific
	// selection cut happens to produce the same order.
	for _, change := range []func(*library.OptionConfig){
		func(c *library.OptionConfig) { c.Kind = library.CashSecuredPut }, func(c *library.OptionConfig) { c.MinDTE++ },
		func(c *library.OptionConfig) { c.TargetDTE++ }, func(c *library.OptionConfig) { c.MaxDTE++ },
		func(c *library.OptionConfig) { c.TargetDelta = .2 }, func(c *library.OptionConfig) { c.WingWidth++ },
		func(c *library.OptionConfig) { c.LimitBuffer = .2 }, func(c *library.OptionConfig) { c.ReserveFraction = .04 },
		func(c *library.OptionConfig) { c.MaxContracts-- }, func(c *library.OptionConfig) { c.HoldSessions++ },
	} {
		v := c
		change(&v)
		other, err := library.NewOption(v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := other.Spec().ID()
		if err != nil || got == id {
			t.Fatal("identity omitted policy", v, err)
		}
	}
	chain := policyChain()
	chain.Entries[0].PriceAvailableAt = chain.AsOf.Add(time.Hour)
	if _, err := s.Plan(chain, 1000000, 1000000, 0, .65); err == nil {
		t.Fatal("future source accepted")
	}
	chain = policyChain()
	for i := range chain.Entries {
		chain.Entries[i].EstimateStatus = "missing_same_session_close"
		chain.Entries[i].IV = 0
		chain.Entries[i].Greeks = options.Greeks{}
	}
	p, err := s.Plan(chain, 1000000, 1000000, 0, .65)
	if err != nil || !slices.Contains(p.Reasons, "option_recipe_chain_incomplete") {
		t.Fatal(p, err)
	}
	if _, err := s.Plan(policyChain(), math.Inf(1), 100, 0, .65); err == nil {
		t.Fatal("nonfinite equity")
	}
}
