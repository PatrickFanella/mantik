package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

type trialSource struct {
	source
	calls  int
	before func(int)
}

func (s *trialSource) AdjustedBars(ctx context.Context, id string, at time.Time) (marketdata.Series, error) {
	s.calls++
	if s.before != nil {
		s.before(s.calls)
	}
	return s.source.AdjustedBars(ctx, id, at)
}

type failingTrialStrategy struct {
	allIn
	calls int
}

func (s *failingTrialStrategy) Decide(v strategy.View) (strategy.Decision, error) {
	s.calls++
	if s.calls == 2 {
		return strategy.Decision{}, errors.New("failed decision")
	}
	return s.allIn.Decide(v)
}

func TestRecordedEngineTrials(t *testing.T) {
	for _, status := range []string{"completed", "failed", "crashed", "abandoned"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			src := &trialSource{source: newSource(t, prices)}
			store := storage.NewMemory()
			cfg := config(src.source, allIn{}, CostModel{Version: "test-v1", FeeBps: "0", SlippageBps: "0"})
			cfg.Source = src
			trial := Trial{ID: "trial-" + status, Family: "trend", DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"}
			at := func() time.Time { return time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC) }
			src.before = func(n int) {
				// The first source read must already have a retained search attempt.
				r, err := store.StrategyTrial(context.Background(), trial.ID)
				if err != nil || r.Start.Family != "trend" {
					t.Fatal("engine ran before trial was recorded", r, err)
				}
				if n == 2 {
					if status == "crashed" {
						panic("source panic")
					}
					if status == "abandoned" {
						cancel()
					}
				}
			}
			if status == "failed" {
				cfg.Strategy = &failingTrialStrategy{}
			}
			result, err := runTrial(ctx, cfg, store, trial, at)
			if status == "completed" && err != nil || status != "completed" && err == nil {
				t.Fatal(result, err)
			}
			if status == "crashed" && !errors.Is(err, ErrTrialCrashed) {
				t.Fatal(err)
			}
			if status == "abandoned" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			r, err := store.StrategyTrial(context.Background(), trial.ID)
			if err != nil || r.Outcome == nil || r.Outcome.Status != status {
				t.Fatal(r, err)
			}
			expected := 1
			if status == "completed" {
				expected = 5
			}
			if status == "failed" {
				expected = 2
			}
			if len(r.Returns) != expected {
				t.Fatal("lost partial returns", r)
			}
			if status == "completed" {
				for i, day := range result.Days {
					if r.Returns[i] != (strategytrial.DailyReturn{Date: day.Date, Return: day.Return, Equity: day.Equity}) {
						t.Fatal(r.Returns[i], day)
					}
				}
				plain, err := Run(context.Background(), config(src.source, allIn{}, cfg.Cost))
				if err != nil || !reflect.DeepEqual(plain, result) {
					t.Fatal("recording changed engine results", err)
				}
			}
			if n, err := store.StrategyTrialSearchTotal(context.Background(), "trend"); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			calls := src.calls
			if _, err := runTrial(context.Background(), cfg, store, trial, at); !errors.Is(err, strategytrial.ErrConflict) || calls != src.calls {
				t.Fatal("existing attempt reexecuted", err)
			}
		})
	}
}

type unavailableRegistry struct {
	strategytrial.Store
	startErr, dayErr, finishErr error
}

func (s unavailableRegistry) StartStrategyTrial(ctx context.Context, d strategytrial.Start) (bool, error) {
	if s.startErr != nil {
		return false, s.startErr
	}
	return s.Store.StartStrategyTrial(ctx, d)
}
func (s unavailableRegistry) AppendStrategyTrialReturn(ctx context.Context, id string, d strategytrial.DailyReturn) error {
	if s.dayErr != nil {
		return s.dayErr
	}
	return s.Store.AppendStrategyTrialReturn(ctx, id, d)
}
func (s unavailableRegistry) FinishStrategyTrial(ctx context.Context, id string, o strategytrial.Outcome) error {
	if s.finishErr != nil {
		return s.finishErr
	}
	return s.Store.FinishStrategyTrial(ctx, id, o)
}

func TestRecordedTrialStopsOnRegistryFailure(t *testing.T) {
	unavailable := errors.New("registry unavailable")
	for _, phase := range []string{"start", "day", "finish"} {
		t.Run(phase, func(t *testing.T) {
			src := &trialSource{source: newSource(t, prices)}
			cfg := config(src.source, allIn{}, DefaultCost)
			cfg.Source = src
			memory := storage.NewMemory()
			store := unavailableRegistry{Store: memory}
			switch phase {
			case "start":
				store.startErr = unavailable
			case "day":
				store.dayErr = unavailable
			case "finish":
				store.finishErr = unavailable
			}
			trial := Trial{ID: "trial", Family: "trend", DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"}
			if _, err := RunTrial(context.Background(), cfg, store, trial); !errors.Is(err, unavailable) {
				t.Fatal(err)
			}
			r, err := memory.StrategyTrial(context.Background(), trial.ID)
			switch phase {
			case "start":
				if src.calls != 0 || !errors.Is(err, strategytrial.ErrNotFound) {
					t.Fatal("ran without registry", src.calls, err)
				}
			case "day":
				if err != nil || r.Outcome == nil || r.Outcome.Status != "failed" || len(r.Returns) != 0 || src.calls != 1 {
					t.Fatal(r, src.calls, err)
				}
			case "finish":
				if err != nil || r.Outcome != nil || len(r.Returns) != 5 {
					t.Fatal(r, err)
				}
			}
		})
	}
}
