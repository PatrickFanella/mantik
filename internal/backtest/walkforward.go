// Package backtest runs declared research experiments through the daily engine.
// Reports have no activation, sizing or qualification authority.
package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// Design declares session counts, not calendar-day approximations. Purge must
// cover the longest label/holding horizon of the candidate family. Embargo
// sessions after each test are excluded from every subsequent training set.
type Design struct{ Train, Test, Purge, Embargo int }

// Fold retains the exact permitted session lists for reproducibility.
type Fold struct {
	Train, Test    []string
	Purge, Embargo int
}

// Folds constructs expanding, strictly forward folds on an explicit calendar.
// It refuses an incomplete final test rather than silently changing its length.
func Folds(dates []string, d Design) ([]Fold, error) {
	if d.Train < 2 || d.Test < 2 || d.Purge < 1 || d.Embargo < 1 || len(dates) > 20000 {
		return nil, errors.New("invalid fold design")
	}
	for i, s := range dates {
		if _, e := time.Parse(time.DateOnly, s); e != nil || i > 0 && s <= dates[i-1] {
			return nil, errors.New("calendar must contain ordered unique sessions")
		}
	}
	excluded := map[int]bool{}
	out := []Fold{}
	for start := d.Train + d.Purge; start+d.Test <= len(dates); start += d.Test + d.Embargo {
		train := []string{}
		for i := 0; i < start-d.Purge; i++ {
			if !excluded[i] {
				train = append(train, dates[i])
			}
		}
		if len(train) < d.Train {
			return nil, errors.New("insufficient training sessions")
		}
		out = append(out, Fold{train, slices.Clone(dates[start : start+d.Test]), d.Purge, d.Embargo})
		for i := start + d.Test; i < min(start+d.Test+d.Embargo, len(dates)); i++ {
			excluded[i] = true
		}
	}
	if len(out) == 0 || len(out) > 100 {
		return nil, errors.New("no complete folds or fold limit exceeded")
	}
	return out, nil
}

// Candidate is a predeclared parameter variant. New must return a fresh strategy
// per run; the runner never tunes a candidate on test data.
type Candidate struct {
	Spec    strategy.Spec
	Minimum *TradeabilityPlan
	Name    string
	New     func() (strategy.Strategy, error)
}

// Run is one retained train, test or cost-stress attempt.
type Run struct {
	TrialID, Candidate, Role string
	Fold                     int
	Result                   engine.Result
	Error                    string
}

// Experiment reports every attempted run, including failures.
type Experiment struct {
	Version string
	Folds   []Fold
	Runs    []Run
}

// HarsherCost requires each nonnegative cost component to be at least its base.
// Version changes alone cannot claim a cost stress.
func HarsherCost(base, stress engine.CostModel) bool {
	higher := false
	for _, p := range [][2]string{{base.FeeBps, stress.FeeBps}, {base.SlippageBps, stress.SlippageBps}} {
		a, ok := new(big.Rat).SetString(p[0])
		b, ok2 := new(big.Rat).SetString(p[1])
		if !ok || !ok2 || a.Sign() < 0 || b.Cmp(a) < 0 || b.Cmp(big.NewRat(10000, 1)) >= 0 {
			return false
		}
		higher = higher || b.Cmp(a) > 0
	}
	return higher
}

type sessionSource struct {
	strategy.BarSource
	allowed map[string]bool
}

func (s sessionSource) AdjustedBars(ctx context.Context, a string, at time.Time) (marketdata.Series, error) {
	r, e := s.BarSource.AdjustedBars(ctx, a, at)
	if e != nil {
		return r, e
	}
	bars := []marketdata.AdjustedBar{}
	for _, b := range r.Bars {
		if s.allowed[b.Date] {
			bars = append(bars, b)
		}
	}
	r.Bars = bars
	return r, nil
}
func (s sessionSource) StockUniverse(ctx context.Context, at time.Time) (marketdata.UniverseReport, error) {
	u, ok := s.BarSource.(strategy.UniverseSource)
	if !ok {
		return marketdata.UniverseReport{}, errors.New("universe unavailable")
	}
	return u.StockUniverse(ctx, at)
}
func (s sessionSource) OptionChain(ctx context.Context, a string, at time.Time) (options.Chain, error) {
	c, ok := s.BarSource.(strategy.ChainSource)
	if !ok {
		return options.Chain{}, errors.New("option chain unavailable")
	}
	return c.OptionChain(ctx, a, at)
}

// WalkForward evaluates every declared candidate on training, held-out and
// stressed held-out data. All runs, including neighbours, go through RunTrial.
// The caller supplies the complete exchange calendar; no run is selected from
// its OOS results. Returns and fold metadata land in the existing registry.
func WalkForward(ctx context.Context, store strategytrial.Store, cfg engine.Config, dates []string, design Design, stress engine.CostModel, candidates []Candidate, trial engine.Trial) (Experiment, error) {
	folds, err := Folds(dates, design)
	out := Experiment{Version: "walk-forward-v1", Folds: folds, Runs: []Run{}}
	if err != nil {
		return out, err
	}
	if cfg.Source == nil || store == nil || !HarsherCost(cfg.Cost, stress) || len(candidates) < 1 || len(candidates) > 32 || !strategytrial.ValidID(trial.ID) {
		return out, errors.New("invalid experiment")
	}
	seen := map[string]bool{}
	specs := []strategy.Spec{}
	plans := []*TradeabilityPlan{}
	for _, c := range candidates {
		if !strategytrial.ValidID(c.Name) || seen[c.Name] || c.New == nil {
			return out, errors.New("invalid candidate")
		}
		seen[c.Name] = true
		raw, err := json.Marshal(c.Spec)
		if err != nil {
			return out, err
		}
		var spec strategy.Spec
		if err = json.Unmarshal(raw, &spec); err != nil {
			return out, err
		}
		sid, err := spec.ID()
		if err != nil {
			return out, err
		}
		specs = append(specs, spec)
		if c.Minimum == nil || c.Minimum.MarksAt.After(time.Now()) {
			return out, errors.New("candidate requires a minimum trade plan with known marks before execution")
		}
		if c.Minimum.StrategyID != sid {
			return out, errors.New("minimum trade declaration differs from strategy identity")
		}
		if _, err := Tradeability(*c.Minimum); err != nil {
			return out, err
		}
		planRaw, err := json.Marshal(c.Minimum)
		if err != nil {
			return out, err
		}
		var plan TradeabilityPlan
		if err = json.Unmarshal(planRaw, &plan); err != nil {
			return out, err
		}
		plans = append(plans, &plan)

		for fi := range folds {
			for _, role := range []string{"train", "test", "stress"} {
				if !strategytrial.ValidID(fmt.Sprintf("%s-f%d-%s-%s", trial.ID, fi, c.Name, role)) {
					return out, errors.New("experiment ID exceeds registry bound")
				}
			}
		}
	}
	var failures []error
	for i, f := range folds {
		for ci, c := range candidates {
			for _, role := range []string{"train", "test", "stress"} {
				if err := ctx.Err(); err != nil {
					return out, errors.Join(append(failures, err)...)
				}
				id := fmt.Sprintf("%s-f%d-%s-%s", trial.ID, i, c.Name, role)
				s, factoryErr := safeCandidate(c.New)
				want, _ := specs[ci].ID()
				if factoryErr == nil {
					got, e := s.Spec().ID()
					if e != nil || got != want {
						factoryErr = errors.New("candidate identity changed")
					}
				}
				if factoryErr != nil {
					s = specStrategy{specs[ci]}
				}
				rc := cfg
				rc.Strategy = s
				runDates := f.Test
				if role == "train" {
					runDates = f.Train
					allowed := map[string]bool{}
					for _, date := range runDates {
						allowed[date] = true
					}
					rc.Source = sessionSource{cfg.Source, allowed}
				}
				start, _ := time.Parse(time.DateOnly, runDates[0])
				end, _ := time.Parse(time.DateOnly, runDates[len(runDates)-1])
				rc.Start = start
				rc.End = end.Add(23*time.Hour + 59*time.Minute)
				if role == "stress" {
					rc.Cost = stress
				}
				t := trial
				t.ID = id
				t.Experiment, err = json.Marshal(struct {
					Version, Candidate, Role string
					Fold                     int
					Design                   Design
					Boundaries               FoldManifest
					Minimum                  *TradeabilityPlan
				}{out.Version, c.Name, role, i, design, manifest(f, dates), plans[ci]})
				if err != nil {
					return out, err
				}
				if factoryErr != nil {
					rc.Source = failedSource{factoryErr}
				}
				result, runErr := engine.RunTrial(ctx, rc, store, t)
				r := Run{TrialID: id, Candidate: c.Name, Role: role, Fold: i, Result: result}
				if runErr != nil {
					r.Error = "run_failed"
					failures = append(failures, fmt.Errorf("experiment %s: %w", id, runErr))
				}
				out.Runs = append(out.Runs, r)
			}
		}
	}
	return out, errors.Join(failures...)
}

// FoldManifest binds exact date lists without exceeding the registry JSON bound.
type FoldManifest struct {
	TrainStart, TrainEnd, TestStart, TestEnd, TrainSHA256, TestSHA256, CalendarSHA256 string
	TrainCount, TestCount                                                             int
}

func manifest(f Fold, dates []string) FoldManifest {
	hash := func(ds []string) string { raw, _ := json.Marshal(ds); return fmt.Sprintf("%x", sha256.Sum256(raw)) }
	return FoldManifest{f.Train[0], f.Train[len(f.Train)-1], f.Test[0], f.Test[len(f.Test)-1], hash(f.Train), hash(f.Test), hash(dates), len(f.Train), len(f.Test)}
}

type specStrategy struct{ spec strategy.Spec }

func (s specStrategy) Spec() strategy.Spec { return s.spec }
func (s specStrategy) Decide(strategy.View) (strategy.Decision, error) {
	return strategy.Decision{}, errors.New("candidate creation failed")
}

type failedSource struct{ err error }

func (s failedSource) AdjustedBars(context.Context, string, time.Time) (marketdata.Series, error) {
	return marketdata.Series{}, s.err
}
func safeCandidate(newStrategy func() (strategy.Strategy, error)) (s strategy.Strategy, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("candidate factory panicked")
		}
	}()
	s, err = newStrategy()
	if s == nil && err == nil {
		err = errors.New("nil candidate")
	}
	return
}
