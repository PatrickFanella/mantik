package engine

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

type distinctSessionSource struct{ base optionFixtureSource }

func (s distinctSessionSource) cut(at time.Time) time.Time {
	d := optionCivil(at)
	return time.Date(d.Year(), d.Month(), d.Day(), 22, 0, 0, 0, time.UTC)
}
func (s distinctSessionSource) OptionChain(ctx context.Context, a string, at time.Time) (options.Chain, error) {
	c, e := s.base.OptionChain(ctx, a, s.cut(at))
	c.AsOf = at
	return c, e
}
func (s distinctSessionSource) OptionBars(ctx context.Context, a string, at time.Time) (map[string]options.IndicativeBar, error) {
	return s.base.OptionBars(ctx, a, s.cut(at))
}
func TestOptionRecipeHoldingUsesDistinctSessions(t *testing.T) {
	cfg := recipeConfig(t, library.CoveredCall, distinctSessionSource{})
	c := cfg.Recipe.Config()
	c.HoldSessions = 5
	cfg.Recipe, _ = library.NewOption(c)
	fillAt := optionStart.Add(24 * time.Hour)
	cfg.Times = []time.Time{optionStart, fillAt, fillAt.Add(time.Minute), fillAt.Add(2 * time.Minute), fillAt.Add(3 * time.Minute), fillAt.Add(4 * time.Minute), optionStart.Add(48 * time.Hour)}
	r, err := RunOptionBacktest(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Events {
		t.Logf("action=%s at=%s positions=%d reasons=%v", e.Action, e.At.Format(time.RFC3339), len(e.Snapshot.Positions), e.Result.Reasons)
		if e.Action == "close" && len(e.Result.Fills) > 0 {
			t.Errorf("HoldSessions=5 closed after only one new session following opening fill")
		}
	}
	for day := 3; day <= 6; day++ {
		cfg.Times = append(cfg.Times, optionStart.Add(time.Duration(day)*24*time.Hour))
	}
	r, err = RunOptionBacktest(context.Background(), cfg)
	if err != nil || !r.Complete {
		t.Fatal("five later retained sessions did not close", r, err)
	}
	closed := false
	for _, e := range r.Events {
		if e.Action == "close" && len(e.Result.Fills) > 0 {
			closed = true
			if !e.At.Equal(optionStart.Add(6 * 24 * time.Hour)) {
				t.Fatal("close did not use the fifth distinct later session", e.At)
			}
		}
	}
	if !closed {
		t.Fatal("scheduled close missing")
	}

}
func TestOptionRecipeHoldingReportsRiskAndExits(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			source := &optionFixtureSource{change: func(c *options.Chain) {
				vol := .3
				if !c.AsOf.Before(optionStart.Add(72*time.Hour)) && (!retry || c.AsOf.Before(optionStart.Add(96*time.Hour))) {
					c.Spot.Price = 95
					vol = .05
				}
				{
					expires := time.Date(2026, 12, 18, 16, 0, 0, 0, newYork)
					for i := range c.Entries {
						e := &c.Entries[i]
						g, err := options.Price(options.Inputs{Type: e.Type, Spot: c.Spot.Price, Strike: e.Strike, Years: expires.Sub(c.Spot.At).Hours() / (24 * 365), Volatility: vol})
						if err != nil {
							panic(err)
						}
						e.Close = g.Price
						e.IV = vol
						e.Greeks = g
					}
				}
			}}

			if retry {
				source.changeBars = func(bars map[string]options.IndicativeBar) {
					for key, bar := range bars {
						if bar.Date == "2026-01-09" {
							delete(bars, key)
						}
					}
				}
			}
			cfg := recipeConfig(t, library.Vertical, source)
			c := cfg.Recipe.Config()
			c.HoldSessions = 5
			cfg.Recipe, _ = library.NewOption(c)
			cfg.Times = nil
			end := 7
			if retry {
				end = 4
			}
			for day := 0; day <= end; day++ {
				cfg.Times = append(cfg.Times, optionStart.Add(time.Duration(day)*24*time.Hour))
			}
			r, err := RunOptionBacktest(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			breach, exited := false, false
			for _, e := range r.Events {
				if e.Action == "risk_breach" && slices.Contains(e.Result.Reasons, "risk_option_gamma") {
					breach = true
				}
				if breach && e.Action == "close" && len(e.Result.Fills) > 0 {
					exited = true
				}
				if e.Action != "hold" {
					continue
				}
				chain, _ := source.OptionChain(context.Background(), cfg.AssetID, e.At)
				gamma := 0.0
				for _, p := range e.Snapshot.Positions {
					entry := optionEntry(chain, p.Symbol)
					gamma += float64(absOptionContracts(p.Contracts)*100) * entry.Greeks.Gamma * chain.Spot.Price * chain.Spot.Price / 2
				}
				equity, _ := strconv.ParseFloat(e.Snapshot.Equity, 64)
				t.Logf("hold at=%s grossGamma=%.2f cap=%.2f reasons=%v", e.At.Format(time.RFC3339), gamma, equity*.05, e.Result.Reasons)
				if gamma > equity*.05 && len(e.Result.Reasons) == 0 {
					t.Errorf("holding gross gamma exceeds hard ceiling without a reported breach")
				}
			}
			if !breach || !exited {
				t.Fatal("holding breach did not retain a reason and deliver an exit", r)
			}

		})
	}
}
