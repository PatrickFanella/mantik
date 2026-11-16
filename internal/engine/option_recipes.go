package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

// OptionBacktestConfig selects one isolated recipe sleeve and an explicit
// ordered timeline. Times are decision/read clocks, not invented availability.
// Each time supplies one session opportunity; missing sessions are not filled in.
// Book.StrategyID must be empty or match Recipe.Spec().ID(). Stocks, if any, are
// imported fully paid; the runner does not buy or sell underlying shares.
type OptionBacktestConfig struct {
	Recipe  *library.Option
	Book    OptionConfig
	AssetID string
	Times   []time.Time
}

// OptionRecipeEvent retains selection, admission attempts, fills and marked book
// readbacks. Plan is present on selection; each attempted quantity has a separate
// event, including stable engine refusal reasons. No event is broker evidence.
type OptionRecipeEvent struct {
	At              time.Time
	Action, OrderID string
	Contracts       int
	Plan            *library.OptionPlan
	Result          OptionResult
	Snapshot        OptionSnapshot
}

// OptionBacktest retains a recipe's exact identity, policy, parameters and
// lifecycle evidence. Complete means no option position or outstanding reservation at the
// end, not qualification or successful performance. Errors return partial evidence.
type OptionBacktest struct {
	StrategyID, Version, StructureVersion, EvidenceStatus string
	Config                                                library.OptionConfig
	Provenance                                            library.OptionProvenance
	Inputs                                                OptionBacktestInputs
	Events                                                []OptionRecipeEvent
	Complete                                              bool
}

// OptionBacktestInputs retains explicit local execution/funding scenarios.
// These are caller declarations, never inferred account balances or source data.
type OptionBacktestInputs struct {
	AssetID                     string
	Account                     account.Type
	SettledCash                 string
	Stocks                      map[string]int
	FeePerContract, SlippageBps float64
	Times                       []time.Time
}

// RunOptionBacktest executes a reviewed recipe through the shared OptionBook.
// One independently collateralized lot is active at a time. Quantity descends
// from the recipe budget until Submit admits it or even one contract refuses.
// All hard limits, fallback rules, fees and adverse next-session fills belong
// to OptionBook; the runner cannot override them. Failed fills/exits remain
// visible and are retried on later supplied sessions, with collateral retained.
func RunOptionBacktest(ctx context.Context, cfg OptionBacktestConfig) (OptionBacktest, error) {
	out := OptionBacktest{Version: library.OptionVersion, StructureVersion: options.StructureVersion, EvidenceStatus: "indicative_unqualified"}
	if cfg.Recipe == nil || cfg.AssetID == "" || len(cfg.Times) == 0 || len(cfg.Times) > 10000 {
		return out, ErrInvalidConfig
	}
	for i, at := range cfg.Times {
		if at.IsZero() || i > 0 && !at.After(cfg.Times[i-1]) {
			return out, ErrInvalidConfig
		}
	}
	id, err := cfg.Recipe.Spec().ID()
	if err != nil {
		return out, err
	}
	if cfg.Book.StrategyID != "" && cfg.Book.StrategyID != id {
		return out, ErrInvalidConfig
	}
	cfg.Book.StrategyID = id
	out.StrategyID = id
	out.Config = cfg.Recipe.Config()
	out.Provenance = cfg.Recipe.Provenance()
	out.Inputs = OptionBacktestInputs{AssetID: cfg.AssetID, Account: cfg.Book.Account, SettledCash: cfg.Book.SettledCash, Stocks: map[string]int{}, FeePerContract: cfg.Book.FeePerContract, SlippageBps: cfg.Book.SlippageBps, Times: append([]time.Time(nil), cfg.Times...)}
	for asset, q := range cfg.Book.Stocks {
		out.Inputs.Stocks[asset] = q
	}
	b, err := NewOptionBook(ctx, cfg.Book, cfg.Times[0])
	if err != nil {
		return out, err
	}
	active := ""
	filled := 0
	hold := 0
	lastSession := ""
	riskExit := false
	sequence := 0
	record := func(at time.Time, action, order string, q int, p *library.OptionPlan, r OptionResult) error {
		snap, err := b.Snapshot(ctx, at)
		if err != nil {
			return err
		}
		out.Events = append(out.Events, OptionRecipeEvent{At: at, Action: action, OrderID: order, Contracts: q, Plan: p, Result: r, Snapshot: snap})
		return nil
	}
	for _, at := range cfg.Times {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if active == "" {
			snap, err := b.Snapshot(ctx, at)
			if err != nil {
				return out, err
			}
			equity, err := strconv.ParseFloat(snap.Equity, 64)
			if err != nil {
				return out, err
			}
			cash, err := strconv.ParseFloat(snap.SettledCash, 64)
			if err != nil {
				return out, err
			}
			marginOnly := out.Config.Kind == library.Vertical || out.Config.Kind == library.IronCondor || out.Config.Kind == library.IronButterfly
			if marginOnly && (cfg.Book.Account != account.Margin || equity < 2000) {
				if err := record(at, "refuse", "", 0, nil, refused("option_cash_structure_not_allowed")); err != nil {
					return out, err
				}
				continue
			}
			c, err := cfg.Book.Source.OptionChain(ctx, cfg.AssetID, at)
			if err != nil {
				return out, err
			}
			if err := c.Validate(at, cfg.AssetID); err != nil {
				return out, err
			}
			plan, err := cfg.Recipe.Plan(c, equity, cash, cfg.Book.Stocks[cfg.AssetID], cfg.Book.FeePerContract)
			if err != nil {
				return out, err
			}
			if err := record(at, "select", "", 0, &plan, OptionResult{Reasons: plan.Reasons}); err != nil {
				return out, err
			}
			if len(plan.Reasons) > 0 {
				continue
			}
			sequence++
			order := fmt.Sprintf("recipe-%d", sequence)
			quantity := absOptionContracts(plan.Structure.Legs[0].Contracts)
			for q := quantity; q >= 1; q-- {
				s := options.Structure{Kind: plan.Structure.Kind, Legs: append([]options.Leg(nil), plan.Structure.Legs...)}
				for i := range s.Legs {
					s.Legs[i].Contracts = s.Legs[i].Contracts / quantity * q
				}
				r, err := b.Submit(ctx, at, order, cfg.AssetID, s)
				if err != nil {
					return out, err
				}
				if err := record(at, "submit", order, q, nil, r); err != nil {
					return out, err
				}
				if len(r.Reasons) == 0 {
					active = order
					filled = 0
					hold = 0
					lastSession = ""
					riskExit = false
					break
				}
			}
			continue
		}
		lot := b.find(active)
		if filled < len(lot.structure.Legs) {
			r, err := b.Step(ctx, at, active)
			if err != nil {
				return out, err
			}
			filled += len(r.Fills)
			if filled == len(lot.structure.Legs) && len(r.Fills) > 0 {
				lastSession = r.Fills[len(r.Fills)-1].Bar.Date
			}
			if err := record(at, "step", active, 0, nil, r); err != nil {
				return out, err
			}
			continue
		}
		chains, err := b.observe(ctx, at, lot.asset)
		if err != nil {
			return out, err
		}
		reasons, err := b.check(chains)
		if err != nil {
			return out, err
		}
		if len(reasons) > 0 {
			riskExit = true
			if err := record(at, "risk_breach", active, 0, nil, OptionResult{Reasons: reasons}); err != nil {
				return out, err
			}
		}
		if session := chains[lot.asset].Spot.Date; session > lastSession {
			hold++
			lastSession = session
		}
		if !riskExit && hold < out.Config.HoldSessions {
			if err := record(at, "hold", active, 0, nil, OptionResult{}); err != nil {
				return out, err
			}
			continue
		}
		r, err := b.Close(ctx, at, active)
		if err != nil {
			return out, err
		}
		if err := record(at, "close", active, 0, nil, r); err != nil {
			return out, err
		}
		if len(r.Reasons) == 0 {
			active = ""
		}
	}
	// Release only untouched orders. Open/partially filled lots retain their
	// reserve and are reported incomplete; the horizon cannot manufacture an exit.
	if active != "" && filled == 0 {
		if err := b.Cancel(active); err != nil {
			return out, err
		}
		if err := record(cfg.Times[len(cfg.Times)-1], "cancel", active, 0, nil, OptionResult{}); err != nil {
			return out, err
		}
		active = ""
	}
	out.Complete = active == ""
	return out, nil
}

// OptionSpotSource supplies retained unadjusted closes with their original
// clocks and stable identity. Adjusted bars are not interchangeable with these.
type OptionSpotSource interface {
	OptionSpot(context.Context, string, time.Time) (options.Spot, error)
}

// RetainedOptionSource connects recipes to the existing point-in-time store.
// It opens no database or provider and never rewrites acquisition clocks.
// Store and Spots must select only observations available by the supplied time.
type RetainedOptionSource struct {
	Store       options.Store
	Spots       OptionSpotSource
	Assumptions options.Assumptions
}

// OptionChain prices known retained contracts at a retained underlying close.
func (s RetainedOptionSource) OptionChain(ctx context.Context, asset string, at time.Time) (options.Chain, error) {
	if s.Store == nil || s.Spots == nil {
		return options.Chain{}, errors.New("option retained source unavailable")
	}
	spot, err := s.Spots.OptionSpot(ctx, asset, at)
	if err != nil {
		return options.Chain{}, err
	}
	if spot.AssetID != asset {
		return options.Chain{}, options.ErrChain
	}
	return options.Read(ctx, s.Store, spot, at, s.Assumptions)
}

// OptionBars returns full retained OHLC under the same asset/underlying binding.
func (s RetainedOptionSource) OptionBars(ctx context.Context, asset string, at time.Time) (map[string]options.IndicativeBar, error) {
	c, err := s.OptionChain(ctx, asset, at)
	if err != nil {
		return nil, err
	}
	return options.ReadBars(ctx, s.Store, c.Underlying, at)
}
