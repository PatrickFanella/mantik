package options

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

func structureChain(t *testing.T) Chain {
	t.Helper()
	at := time.Date(2026, 1, 6, 22, 0, 0, 0, time.UTC)
	spot := Spot{AssetID: "asset-spy", Underlying: "SPY", Date: "2026-01-06", Price: 100, At: at.Add(-time.Hour), ObservedAt: at, AvailableAt: at}
	c := Chain{AssetID: spot.AssetID, Underlying: "SPY", AsOf: at, Spot: spot, Feed: "indicative", Model: ModelVersion, Assumptions: Assumptions{MaxAge: 4 * 24 * time.Hour}}
	for _, x := range []struct {
		symbol, right string
		strike        float64
	}{{"SPY260116P00090000", "put", 90}, {"SPY260116P00095000", "put", 95}, {"SPY260116P00100000", "put", 100}, {"SPY260116C00100000", "call", 100}, {"SPY260116C00105000", "call", 105}, {"SPY260116C00110000", "call", 110}} {
		g, err := Price(Inputs{Type: x.right, Spot: 100, Strike: x.strike, Years: 10.0 / 365, Volatility: .3})
		if err != nil {
			t.Fatal(err)
		}
		c.Entries = append(c.Entries, Entry{Symbol: x.symbol, ContractID: x.symbol, Type: x.right, Expiry: "2026-01-16", Strike: x.strike, Close: g.Price, ReferenceObservedAt: at, ReferenceAvailableAt: at, PriceOccurredAt: spot.At, PriceObservedAt: at, PriceAvailableAt: at, FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: .3, Greeks: g})
	}
	slices.SortFunc(c.Entries, func(a, b Entry) int {
		if a.Symbol < b.Symbol {
			return -1
		}
		return 1
	})
	return c
}
func leg(symbol string, q int, price float64) Leg {
	return Leg{Symbol: symbol, Contracts: q, Multiplier: 100, Limit: price}
}

func TestStructurePayoffAndProtectedPartialReserves(t *testing.T) {
	c := structureChain(t)
	tests := []struct {
		name         string
		s            Structure
		max, partial float64
		shares       int
	}{
		{"long", Structure{"long_options", []Leg{leg("SPY260116C00100000", 1, 3)}}, 302, 302, 0},
		{"put", Structure{"cash_secured_put", []Leg{leg("SPY260116P00095000", -1, 2)}}, 9302, 9302, 0},
		{"covered", Structure{"covered_call", []Leg{leg("SPY260116C00105000", -1, 2)}}, 9802, 10000, 100},
		{"credit call", Structure{"vertical", []Leg{leg("SPY260116C00100000", -1, 3), leg("SPY260116C00105000", 1, 1)}}, 304, 304, 0},
		{"debit call", Structure{"vertical", []Leg{leg("SPY260116C00100000", 1, 3), leg("SPY260116C00105000", -1, 1)}}, 204, 302, 0},
		{"credit put", Structure{"vertical", []Leg{leg("SPY260116P00100000", -1, 3), leg("SPY260116P00095000", 1, 1)}}, 304, 304, 0},
		{"debit put", Structure{"vertical", []Leg{leg("SPY260116P00100000", 1, 3), leg("SPY260116P00095000", -1, 1)}}, 204, 302, 0},
		{"condor", Structure{"iron_condor", []Leg{leg("SPY260116P00090000", 1, 1), leg("SPY260116P00095000", -1, 2), leg("SPY260116C00105000", -1, 2), leg("SPY260116C00110000", 1, 1)}}, 308, 506, 0},
		{"butterfly", Structure{"iron_butterfly", []Leg{leg("SPY260116P00095000", 1, 1), leg("SPY260116P00100000", -1, 2), leg("SPY260116C00100000", -1, 2), leg("SPY260116C00105000", 1, 1)}}, 308, 506, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Analyze(c, tt.s, 1)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(r.MaxLoss-tt.max) > .011 || math.Abs(r.WorstPartialLoss-tt.partial) > .011 || r.Reserve+1e-9 < r.MaxLoss+r.WorstPartialLoss || r.CoveredShares != tt.shares {
				t.Fatalf("got %+v want max=%v partial=%v shares=%v", r, tt.max, tt.partial, tt.shares)
			}
			shortSeen := false
			for _, i := range r.FillOrder {
				if tt.s.Legs[i].Contracts < 0 {
					shortSeen = true
				} else if shortSeen {
					t.Fatal("short before protective long")
				}
			}
		})
	}
	// An arbitrary naked-call subset has infinite loss; it must never be
	// represented as strike width, margin or a finite price-based placeholder.
	s := tests[3].s
	e := []Entry{optionTestEntry(c, s.Legs[0].Symbol), optionTestEntry(c, s.Legs[1].Symbol)}
	if _, err := subsetLoss(100, s, e, 1, 0, 1); err != StructureError("option_unbounded_partial_short") {
		t.Fatal(err)
	}
}
func optionTestEntry(c Chain, symbol string) Entry {
	for _, e := range c.Entries {
		if e.Symbol == symbol {
			return e
		}
	}
	panic("fixture")
}

func TestStructureRejectsMalformedOrUnknownInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Chain, *Structure){
		"zero contracts":      func(_ *Chain, s *Structure) { s.Legs[0].Contracts = 0 },
		"huge contracts":      func(_ *Chain, s *Structure) { s.Legs[0].Contracts = 1000001 },
		"adjusted multiplier": func(_ *Chain, s *Structure) { s.Legs[0].Multiplier = 10 },
		"missing multiplier":  func(_ *Chain, s *Structure) { s.Legs[0].Multiplier = 0 },
		"nan limit":           func(_ *Chain, s *Structure) { s.Legs[0].Limit = math.NaN() },
		"zero limit":          func(_ *Chain, s *Structure) { s.Legs[0].Limit = 0 },
		"duplicate":           func(_ *Chain, s *Structure) { s.Legs = append(s.Legs, s.Legs[0]) },
		"missing contract":    func(_ *Chain, s *Structure) { s.Legs[0].Symbol = "SPY260116C00115000" },
		"unknown shape":       func(_ *Chain, s *Structure) { s.Kind = "naked_call" },
		"short long":          func(_ *Chain, s *Structure) { s.Legs[0].Contracts = -1 },
		"unavailable greeks": func(c *Chain, _ *Structure) {
			for i := range c.Entries {
				c.Entries[i].EstimateStatus = "missing_same_session_close"
				c.Entries[i].IV = 0
				c.Entries[i].Greeks = Greeks{}
			}
		},
		"negative gamma": func(c *Chain, _ *Structure) {
			for i := range c.Entries {
				c.Entries[i].Greeks.Gamma = -.1
			}
		},
		"nan theta": func(c *Chain, _ *Structure) {
			for i := range c.Entries {
				c.Entries[i].Greeks.Theta = math.NaN()
			}
		},
		"wrong delta sign": func(c *Chain, _ *Structure) {
			for i := range c.Entries {
				if c.Entries[i].Type == "call" {
					c.Entries[i].Greeks.Delta = -.1
				}
			}
		},
		"expiry mismatch": func(c *Chain, _ *Structure) { c.Entries[0].Expiry = "2026-01-17" },
		"wrong right":     func(c *Chain, _ *Structure) { c.Entries[0].Type = "put" },
		"wrong strike":    func(c *Chain, _ *Structure) { c.Entries[0].Strike += 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := structureChain(t)
			s := Structure{"long_options", []Leg{leg("SPY260116C00100000", 1, 3)}}
			mutate(&c, &s)
			if _, err := Analyze(c, s, 1); err == nil {
				t.Fatal("admitted")
			}
		})
	}
	c := structureChain(t)
	for _, s := range []Structure{
		{"vertical", []Leg{leg("SPY260116C00100000", 1, 3), leg("SPY260116C00105000", -2, 1)}},
		{"vertical", []Leg{leg("SPY260116C00100000", 1, 3), leg("SPY260116P00100000", -1, 1)}},
		{"iron_condor", []Leg{leg("SPY260116P00090000", -1, 1), leg("SPY260116P00095000", 1, 2), leg("SPY260116C00105000", -1, 2), leg("SPY260116C00110000", 1, 1)}},
	} {
		if _, err := Analyze(c, s, 1); err == nil {
			t.Fatal("admitted shape", s)
		}
	}
	for _, fee := range []float64{0, -1, math.Inf(1), math.NaN()} {
		if _, err := Analyze(c, Structure{"long_options", []Leg{leg("SPY260116C00100000", 1, 3)}}, fee); err == nil {
			t.Fatal("fee", fee)
		}
	}
}

func TestReadBarsRefusesIncompleteAndLeakyOHLC(t *testing.T) {
	at := time.Date(2026, 1, 6, 22, 0, 0, 0, time.UTC)
	good := alpacarecord.OptionRecord{Kind: "bar", Symbol: "SPY260116C00100000", Underlying: "SPY", Key: "2026-01-06", Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: at.Add(-17 * time.Hour), ObservedAt: at, AvailableAt: at, Data: json.RawMessage(`{"o":2,"h":3,"l":1,"c":2}`)}
	bars, err := ReadBars(context.Background(), recordSource{[]alpacarecord.OptionRecord{good}}, "SPY", at)
	if err != nil || len(bars) != 1 {
		t.Fatal(bars, err)
	}
	for name, change := range map[string]func(*alpacarecord.OptionRecord){
		"close only":          func(r *alpacarecord.OptionRecord) { r.Data = json.RawMessage(`{"c":2}`) },
		"null":                func(r *alpacarecord.OptionRecord) { r.Data = json.RawMessage(`{"o":2,"h":null,"l":1,"c":2}`) },
		"bad range":           func(r *alpacarecord.OptionRecord) { r.Data = json.RawMessage(`{"o":2,"h":1,"l":3,"c":2}`) },
		"unknown provenance":  func(r *alpacarecord.OptionRecord) { r.FeedSelection = "" },
		"future":              func(r *alpacarecord.OptionRecord) { r.AvailableAt = at.Add(time.Second) },
		"wrong session":       func(r *alpacarecord.OptionRecord) { r.Key = "2026-01-05" },
		"observed before bar": func(r *alpacarecord.OptionRecord) { r.ObservedAt = r.OccurredAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			r := good
			change(&r)
			if _, err := ReadBars(context.Background(), recordSource{[]alpacarecord.OptionRecord{r}}, "SPY", at); err == nil {
				t.Fatal("invalid bar admitted")
			}
		})
	}
	if _, err := ReadBars(context.Background(), recordSource{[]alpacarecord.OptionRecord{good, good}}, "SPY", at); err == nil {
		t.Fatal("duplicate")
	}
}

func TestStructureRefusesIndividuallyValidMixedExpiries(t *testing.T) {
	c := structureChain(t)
	e := optionTestEntry(c, "SPY260116C00105000")
	e.Symbol = "SPY260123C00105000"
	e.Expiry = "2026-01-23"
	e.ContractID = e.Symbol
	c.Entries = append(c.Entries, e)
	slices.SortFunc(c.Entries, func(a, b Entry) int {
		if a.Symbol < b.Symbol {
			return -1
		}
		return 1
	})
	s := Structure{"vertical", []Leg{leg("SPY260116C00100000", -1, 3), leg("SPY260123C00105000", 1, 1)}}
	if _, err := Analyze(c, s, 1); err != StructureError("option_mixed_expiries") {
		t.Fatal(err)
	}
}
