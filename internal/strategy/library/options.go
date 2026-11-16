package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// OptionVersion pins Mantik's reviewed recipe policy, not an upstream code port.
const OptionVersion = "reviewed-option-recipes-v1"
const (
	CoveredCall    = "covered_call"
	CashSecuredPut = "cash_secured_put"
	Vertical       = "vertical"
	IronCondor     = "iron_condor"
	IronButterfly  = "iron_butterfly"
)

// OptionConfig fixes selection, sizing and exit policy. DTE counts civil days
// from the decision in New York. WingWidth is a minimum strike distance in
// dollars. LimitBuffer is a fraction of the decision close, not a fill model.
// ReserveFraction caps this recipe's held reserve, not the engine's hard caps.
// HoldSessions counts supplied sessions after the final opening fill.
type OptionConfig struct {
	Kind            string  `json:"kind"`
	MinDTE          int     `json:"min_dte"`
	TargetDTE       int     `json:"target_dte"`
	MaxDTE          int     `json:"max_dte"`
	TargetDelta     float64 `json:"target_delta"`
	WingWidth       float64 `json:"wing_width"`
	LimitBuffer     float64 `json:"limit_buffer"`
	ReserveFraction float64 `json:"reserve_fraction"`
	MaxContracts    int     `json:"max_contracts"`
	HoldSessions    int     `json:"hold_sessions"`
}

// DefaultOption supplies explicit research scenarios, with no qualification.
func DefaultOption(kind string) OptionConfig {
	return OptionConfig{Kind: kind, MinDTE: 21, TargetDTE: 30, MaxDTE: 45, TargetDelta: .3, WingWidth: 5, LimitBuffer: .1, ReserveFraction: .05, MaxContracts: 100, HoldSessions: 5}
}

// OptionProvenance distinguishes literature motivation from executable source.
type OptionProvenance struct {
	Policy, Implementation, Evidence string
	References                       []string
}

// Option is an immutable recipe; it has no source, book or order authority.
type Option struct {
	cfg  OptionConfig
	spec strategy.Spec
}

// NewOption validates a reviewed recipe. These are original Mantik adaptations
// of standard structures, not unchanged-source algorithms or paper replications.
func NewOption(c OptionConfig) (*Option, error) {
	if !slices.Contains([]string{CoveredCall, CashSecuredPut, Vertical, IronCondor, IronButterfly}, c.Kind) || c.MinDTE < 1 || c.TargetDTE < c.MinDTE || c.MaxDTE < c.TargetDTE || c.MaxDTE > 365 || !finite(c.TargetDelta) || c.TargetDelta <= 0 || c.TargetDelta > .5 || !finite(c.WingWidth) || c.WingWidth <= 0 || !finite(c.LimitBuffer) || c.LimitBuffer <= 0 || c.LimitBuffer >= 1 || !finite(c.ReserveFraction) || c.ReserveFraction <= 0 || c.ReserveFraction > .1 || c.MaxContracts < 1 || c.MaxContracts > 100 || c.HoldSessions < 1 || c.HoldSessions > 252 {
		return nil, errors.New("invalid option recipe configuration")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	spec := strategy.Spec{Name: c.Kind, Version: OptionVersion, Data: []string{strategy.KindOptionChains}, Params: map[string]string{"recipe_sha256": hex.EncodeToString(sum[:])}}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &Option{cfg: c, spec: spec}, nil
}

// Config returns a value copy of every recipe parameter.
func (s *Option) Config() OptionConfig { return s.cfg }

// Spec returns independent deterministic strategy identity metadata.
func (s *Option) Spec() strategy.Spec {
	v := s.spec
	v.Data = slices.Clone(v.Data)
	v.Params = map[string]string{"recipe_sha256": s.spec.Params["recipe_sha256"]}
	return v
}

// Provenance records the reviewed policy and economic references. The papers
// motivate investigating option risk premia; they do not specify these recipes.
func (s *Option) Provenance() OptionProvenance {
	return OptionProvenance{Policy: OptionVersion, Implementation: "original Mantik reviewed recipe; explicit adaptations in docs/reference/option-library.md", Evidence: "indicative_unqualified", References: []string{
		"Coval & Shumway (2001), Expected Option Returns, doi:10.1111/0022-1082.00352",
		"Bakshi & Kapadia (2003), Delta-Hedged Gains and the Negative Market Volatility Risk Premium, doi:10.1093/rfs/hhg002",
	}}
}

// OptionPlan is a candidate sized to the recipe reserve budget. The engine
// still admits every order against all book limits and may reduce its size.
// Chain retains the exact selection inputs, including assumptions and clocks.
type OptionPlan struct {
	Structure options.Structure
	Chain     options.Chain
	Risk      options.StructureRisk
	Reasons   []string
}

// Plan selects only usable entries from this known (possibly incomplete) chain.
// Equity and settledCash are local book dollars; paidShares cannot be borrowed.
// Margin-only permission is enforced by OptionBook, never inferred here.
func (s *Option) Plan(c options.Chain, equity, settledCash float64, paidShares int, fee float64) (OptionPlan, error) {
	p := OptionPlan{Chain: c}
	p.Chain.Entries = slices.Clone(c.Entries)
	if err := c.Validate(c.AsOf, c.AssetID); err != nil {
		return p, err
	}
	if !finite(equity) || equity <= 0 || !finite(settledCash) || settledCash < 0 || paidShares < 0 || !finite(fee) || fee <= 0 {
		return p, errors.New("invalid option recipe funding")
	}
	legs := s.selectLegs(c)
	if len(legs) == 0 {
		p.Reasons = []string{"option_recipe_chain_incomplete"}
		return p, nil
	}
	p.Structure = options.Structure{Kind: s.cfg.Kind, Legs: legs}
	one, err := options.Analyze(c, p.Structure, fee)
	if err != nil {
		return p, err
	}
	hold := one.Reserve
	if s.cfg.Kind == CashSecuredPut {
		for _, e := range c.Entries {
			if e.Symbol == legs[0].Symbol {
				hold = math.Max(hold, e.Strike*100+2*fee)
			}
		}
	}
	if !finite(hold) || hold <= 0 {
		return p, errors.New("invalid option recipe reserve")
	}
	budget := math.Min(equity*s.cfg.ReserveFraction, settledCash)
	q := min(s.cfg.MaxContracts, int(math.Min(float64(s.cfg.MaxContracts), math.Floor(budget/hold))))
	if s.cfg.Kind == CoveredCall {
		q = min(q, paidShares/100)
	}
	if q < 1 {
		p.Structure.Legs = nil
		p.Reasons = []string{"option_recipe_no_funded_contract"}
		return p, nil
	}
	for i := range p.Structure.Legs {
		p.Structure.Legs[i].Contracts *= q
	}
	p.Risk, err = options.Analyze(c, p.Structure, fee)
	return p, err
}

func (s *Option) selectLegs(c options.Chain) []options.Leg {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil
	}
	now := c.AsOf.In(zone)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	expiries := []string{}
	for _, e := range c.Entries {
		exp, err := time.Parse(time.DateOnly, e.Expiry)
		if err != nil {
			continue
		}
		d := int(exp.Sub(day) / (24 * time.Hour))
		if d >= s.cfg.MinDTE && d <= s.cfg.MaxDTE && options.UsableGreeks(e) == nil && !slices.Contains(expiries, e.Expiry) {
			expiries = append(expiries, e.Expiry)
		}
	}
	// Nearest target expiry first, ties to the earlier expiry. Try the next known
	// expiry if the preferred one lacks a complete structure; never mix expiries.
	slices.SortFunc(expiries, func(a, b string) int {
		x, _ := time.Parse(time.DateOnly, a)
		y, _ := time.Parse(time.DateOnly, b)
		dx := math.Abs(x.Sub(day).Hours()/24 - float64(s.cfg.TargetDTE))
		dy := math.Abs(y.Sub(day).Hours()/24 - float64(s.cfg.TargetDTE))
		if dx < dy {
			return -1
		}
		if dx > dy {
			return 1
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	for _, expiry := range expiries {
		entries := []options.Entry{}
		for _, e := range c.Entries {
			if e.Expiry == expiry && options.UsableGreeks(e) == nil {
				entries = append(entries, e)
			}
		}
		// Chain is symbol sorted, so strict improvement preserves symbol tie breaks.
		best := func(kind string, score func(options.Entry) float64) (options.Entry, bool) {
			var out options.Entry
			v := math.Inf(1)
			for _, e := range entries {
				if e.Type == kind {
					if n := score(e); n < v {
						out = e
						v = n
					}
				}
			}
			return out, out.Symbol != ""
		}
		wing := func(e options.Entry) (options.Entry, bool) {
			return best(e.Type, func(w options.Entry) float64 {
				d := w.Strike - e.Strike
				if e.Type == "put" {
					d = -d
				}
				if d < s.cfg.WingWidth {
					return math.Inf(1)
				}
				return d
			})
		}
		short := func(kind string) (options.Entry, bool) {
			return best(kind, func(e options.Entry) float64 {
				if kind == "put" && e.Strike > c.Spot.Price || kind == "call" && e.Strike < c.Spot.Price {
					return math.Inf(1)
				}
				if s.cfg.Kind == Vertical || s.cfg.Kind == IronCondor {
					if _, ok := wing(e); !ok {
						return math.Inf(1)
					}
				}
				return math.Abs(math.Abs(e.Greeks.Delta) - s.cfg.TargetDelta)
			})
		}
		leg := func(e options.Entry, q int) options.Leg {
			factor := 1 + s.cfg.LimitBuffer
			if q < 0 {
				factor = 1 - s.cfg.LimitBuffer
			}
			return options.Leg{Symbol: e.Symbol, Contracts: q, Multiplier: 100, Limit: e.Close * factor}
		}
		switch s.cfg.Kind {
		case CoveredCall, CashSecuredPut:
			kind := "call"
			if s.cfg.Kind == CashSecuredPut {
				kind = "put"
			}
			e, ok := short(kind)
			if ok {
				return []options.Leg{leg(e, -1)}
			}
		case Vertical:
			p, ok := short("put")
			w, found := wing(p)
			if ok && found {
				return []options.Leg{leg(w, 1), leg(p, -1)}
			}
		case IronCondor, IronButterfly:
			p, pok := short("put")
			a, aok := short("call")
			if s.cfg.Kind == IronCondor {
				// Rank complete feasible pairs. Independently best shorts may
				// share the ATM strike even when a valid outer pair exists.
				puts, calls := []options.Entry{}, []options.Entry{}
				for _, e := range entries {
					if _, ok := wing(e); !ok {
						continue
					}
					if e.Type == "put" && e.Strike <= c.Spot.Price {
						puts = append(puts, e)
					}
					if e.Type == "call" && e.Strike >= c.Spot.Price {
						calls = append(calls, e)
					}
				}
				pok, aok = false, false
				score := math.Inf(1)
				for _, put := range puts {
					for _, call := range calls {
						if put.Strike >= call.Strike {
							continue
						}
						n := math.Abs(math.Abs(put.Greeks.Delta)-s.cfg.TargetDelta) + math.Abs(math.Abs(call.Greeks.Delta)-s.cfg.TargetDelta)
						if n < score {
							p, a, pok, aok, score = put, call, true, true, n
						}
					}
				}
			}
			if s.cfg.Kind == IronButterfly {
				p, pok = best("put", func(e options.Entry) float64 {
					for _, call := range entries {
						if call.Type == "call" && call.Strike == e.Strike {
							_, pok := wing(e)
							_, cok := wing(call)
							if !pok || !cok {
								continue
							}
							return math.Abs(e.Strike - c.Spot.Price)
						}
					}
					return math.Inf(1)
				})
				aok = false
				for _, e := range entries {
					if e.Type == "call" && e.Strike == p.Strike {
						a = e
						aok = true
						break
					}
				}
			}
			pl, plok := wing(p)
			cl, clok := wing(a)
			if pok && aok && plok && clok && (s.cfg.Kind == IronButterfly || p.Strike < a.Strike) {
				return []options.Leg{leg(pl, 1), leg(p, -1), leg(a, -1), leg(cl, 1)}
			}
		}
	}
	return nil
}
