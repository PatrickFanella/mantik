// Package library implements reviewed stock recipes from pinned bt/ffn building
// blocks and original option recipes with explicit literature provenance. Recipes
// return targets or candidate structures; the engine owns admission and execution.
package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/indicators"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// Version pins both source revisions and the reviewed recipe/execution adapter.
const Version = "bt-763ae010-ffn-eb9354c5-recipes-v2"
const (
	Trend    = "trend"
	Momentum = "momentum"
	Reversal = "reversal"
	LowVol   = "lowvol"
)

// Config declares the whole recipe. Periods count trading sessions; CashRate
// is an annual simple scenario used to compute compounded excess returns.
// Turnover is the maximum L1 target-weight change on a scheduled reversal
// rebalance; universe exits always take precedence. No parameter changes risk
// admission or execution rules outside the strategy.
type Config struct {
	Kind      string  `json:"kind"`
	Lookbacks []int   `json:"lookbacks"`
	Skip      int     `json:"skip"`
	VolWindow int     `json:"vol_window"`
	Count     int     `json:"count"`
	Cadence   string  `json:"cadence"`
	Gross     float64 `json:"gross"`
	CashRate  float64 `json:"cash_rate"`
	Turnover  float64 `json:"turnover"`
}

// Default fixes the initial paper-research recipe; it is not a qualified version.
func Default(kind string) Config {
	c := Config{Kind: kind, Lookbacks: []int{252}, VolWindow: 63, Count: 5, Cadence: "monthly", Gross: .7, Turnover: .25}
	switch kind {
	case Trend:
		c.Lookbacks = []int{21, 63, 252}
	case Momentum:
		c.Lookbacks = []int{63, 126, 252}
		c.Skip = 21
	case Reversal:
		c.Lookbacks = []int{5, 21}
		c.Cadence = "weekly"
	case LowVol:
		c.VolWindow = 252
		c.Lookbacks = nil
	}
	return c
}

// Stock is an immutable configured strategy.
type Stock struct {
	cfg  Config
	spec strategy.Spec
}

// New validates and copies a bounded recipe. See docs/reference/stock-library.md
// for upstream building blocks and the explicit adaptations of those sources.
func New(c Config) (*Stock, error) {
	if !slices.Contains([]string{Trend, Momentum, Reversal, LowVol}, c.Kind) || c.VolWindow < 2 || c.VolWindow > 252 || c.Skip < 0 || c.Skip > 252 || c.Count < 1 || c.Count > 100 || !slices.Contains([]string{"daily", "weekly", "monthly"}, c.Cadence) || !finite(c.Gross) || c.Gross <= 0 || c.Gross > 1 || !finite(c.CashRate) || c.CashRate < 0 || c.CashRate > 1 || !finite(c.Turnover) || c.Turnover <= 0 || c.Turnover > 2 || len(c.Lookbacks) > 3 {
		return nil, errors.New("invalid stock strategy configuration")
	}
	if c.Kind != LowVol && len(c.Lookbacks) == 0 {
		return nil, errors.New("return strategy needs lookbacks")
	}
	if c.Kind == LowVol && len(c.Lookbacks) != 0 {
		return nil, errors.New("lowvol uses only the volatility window")
	}
	c.Lookbacks = slices.Clone(c.Lookbacks)
	slices.Sort(c.Lookbacks)
	for i, p := range c.Lookbacks {
		if p < 1 || p > 252 || p+c.Skip > 504 || i > 0 && p == c.Lookbacks[i-1] {
			return nil, errors.New("invalid or repeated lookback")
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	data := []string{strategy.KindDailyBars}
	if c.Kind != Trend {
		data = append(data, strategy.KindStockUniverse)
	}
	warm := c.VolWindow + 1
	for _, p := range c.Lookbacks {
		warm = max(warm, p+c.Skip+1)
	}
	spec := strategy.Spec{Name: c.Kind, Version: Version, Params: map[string]string{"recipe_sha256": hex.EncodeToString(sum[:])}, Data: data, Warmup: warm}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &Stock{c, spec}, nil
}

// Spec returns independent metadata; callers cannot mutate the compiled recipe.
func (s *Stock) Spec() strategy.Spec {
	out := s.spec
	out.Data = slices.Clone(out.Data)
	out.Params = map[string]string{"recipe_sha256": s.spec.Params["recipe_sha256"]}
	return out
}

// Feature computes the reviewed return/sign/volatility signal on one asset's
// already filtered adjusted bars. Insufficient history is unavailable, not zero.
func Feature(c Config, v strategy.View, asset string) (score, risk float64, err error) {
	if _, e := New(c); e != nil {
		return 0, 0, e
	}
	warm := c.VolWindow + 1
	for _, p := range c.Lookbacks {
		warm = max(warm, p+c.Skip+1)
	}
	bars := v.RecentBars(asset, warm)
	if len(bars) < warm {
		return 0, 0, errors.New("unavailable or stale strategy history")
	}
	last, parseErr := time.Parse(time.DateOnly, bars[len(bars)-1].Date)
	if parseErr != nil || last.After(v.At()) || v.At().UTC().Truncate(24*time.Hour).Sub(last) > 7*24*time.Hour {
		return 0, 0, errors.New("unavailable or stale strategy history")
	}
	series := marketdata.Series{AssetID: asset, AsOf: v.At(), Bars: bars}
	vol, e := indicators.Volatility(series, c.VolWindow)
	if e != nil {
		return 0, 0, e
	}
	risk = vol.Values[len(vol.Values)-1]
	if !finite(risk) || risk <= 1e-12 {
		return 0, 0, errors.New("unavailable volatility")
	}
	if c.Kind == LowVol {
		return -risk, risk, nil
	}
	end := len(bars) - 1 - c.Skip
	current, e := strconv.ParseFloat(bars[end].Close, 64)
	if e != nil {
		return 0, 0, e
	}
	for _, p := range c.Lookbacks {
		prior, e := strconv.ParseFloat(bars[end-p].Close, 64)
		if e != nil {
			return 0, 0, e
		}
		r := current/prior - 1
		switch c.Kind {
		case Trend:
			r -= math.Pow(1+c.CashRate, float64(p)/252) - 1
			if r > 0 {
				score++
			} else if r < 0 {
				score--
			}
		case Reversal:
			score -= r
		case Momentum:
			score += r
		default:
			return 0, 0, errors.New("unknown strategy feature")
		}
	}
	score /= float64(len(c.Lookbacks))
	if !finite(score) {
		return 0, 0, errors.New("nonfinite strategy signal")
	}
	return score, risk, nil
}

type saved struct {
	Period  string             `json:"period"`
	Weights map[string]float64 `json:"weights"`
}
type candidate struct {
	asset       string
	score, risk float64
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Decide rebalances on the configured schedule and on membership exits. The
// state carries the previous targets solely for the reversal turnover budget.
func (s *Stock) Decide(v strategy.View) (strategy.Decision, error) {
	c := s.cfg
	period := v.At().UTC().Format("2006-01-02")
	if c.Cadence == "monthly" {
		period = period[:7]
	} else if c.Cadence == "weekly" {
		y, w := v.At().UTC().ISOWeek()
		period = fmt.Sprintf("%04d-W%02d", y, w)
	}
	prior := saved{Weights: map[string]float64{}}
	if len(v.State()) > 0 {
		if err := json.Unmarshal(v.State(), &prior); err != nil || len(prior.Weights) > strategy.MaxAssets {
			return strategy.Decision{}, errors.New("invalid stock strategy state")
		}
	}
	assets := v.Assets()
	if c.Kind != Trend {
		assets = v.Universe()
	}
	forced := false
	for a, w := range prior.Weights {
		if !finite(w) || math.Abs(w) > 1 {
			return strategy.Decision{}, errors.New("invalid retained weight")
		}
		if !slices.Contains(assets, a) && w != 0 {
			forced = true
		}
	}
	for _, position := range v.Positions() {
		quantity, err := strconv.ParseFloat(position.Quantity, 64)
		if err != nil || !finite(quantity) {
			return strategy.Decision{}, errors.New("invalid strategy holding")
		}
		if quantity != 0 && !slices.Contains(assets, position.Asset) {
			forced = true
		}
	}
	if prior.Period == period && !forced {
		return strategy.Decision{KeepPositions: true, State: v.State()}, nil
	}
	selected := []candidate{}
	available := map[string]bool{}
	latest := ""
	for _, a := range assets {
		recent := v.RecentBars(a, 1)
		if len(recent) > 0 && recent[0].Date > latest {
			latest = recent[0].Date
		}
	}
	for _, a := range assets {
		recent := v.RecentBars(a, 1)
		if len(recent) == 0 || recent[0].Date != latest {
			continue
		}
		score, risk, err := Feature(c, v, a)
		if err == nil {
			available[a] = true
			selected = append(selected, candidate{a, score, risk})
		}
	}
	slices.SortFunc(selected, func(a, b candidate) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		return strings.Compare(a.asset, b.asset)
	})
	if c.Kind != Trend && len(selected) > c.Count {
		selected = selected[:c.Count]
	}
	weights := map[string]float64{}
	denom := 0.0
	for _, p := range selected {
		w := 1.0
		if c.Kind == Trend {
			w = p.score / p.risk
		} else if c.Kind == Momentum {
			w = 1 / p.risk
		}
		weights[p.asset] = w
		denom += math.Abs(w)
	}
	for a, w := range weights {
		if denom > 0 {
			weights[a] = c.Gross * w / denom
		} else {
			weights[a] = 0
		}
	}
	if c.Kind == Reversal && !forced {
		// Unavailable holdings must exit without consuming the turnover budget.
		// A healthy asset that merely ranks below Count remains subject to it.
		for a := range prior.Weights {
			if !available[a] {
				weights[a] = 0
				continue
			}
			if _, ok := weights[a]; !ok {
				weights[a] = 0
			}
		}
		change := 0.0
		keys := make([]string, 0, len(weights))
		for a := range weights {
			keys = append(keys, a)
		}
		slices.Sort(keys)
		for _, a := range keys {
			if !available[a] {
				continue
			}
			change += math.Abs(weights[a] - prior.Weights[a])
		}
		if change > c.Turnover {
			scale := c.Turnover / change
			for a, w := range weights {
				if !available[a] {
					continue
				}
				weights[a] = prior.Weights[a] + scale*(w-prior.Weights[a])
			}
		}
	}
	d := strategy.Decision{}
	for a, w := range weights {
		w = math.Trunc(w*1e12) / 1e12
		weights[a] = w
		if w != 0 {
			d.Targets = append(d.Targets, strategy.Target{Asset: a, Weight: strconv.FormatFloat(w, 'f', 12, 64)})
		}
	}
	slices.SortFunc(d.Targets, func(a, b strategy.Target) int { return strings.Compare(a.Asset, b.Asset) })
	state, err := json.Marshal(saved{period, weights})
	if err != nil {
		return strategy.Decision{}, err
	}
	d.State = state
	return d, nil
}
