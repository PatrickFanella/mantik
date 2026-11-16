package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func TestDeflatedSharpePublishedExample(t *testing.T) {
	// Bailey/Lopez de Prado 2014, numerical example pp9-10: annual SR 2.5,
	// cross-trial variance .5, 250 observations/year, T1250, skew -3, kurtosis10.
	m := Moments{1250, 2.5 / math.Sqrt(250), -3, 10}
	last := 1.0
	for _, n := range []int64{1, 10, 46, 100, 1000} {
		s := DeflatedSharpe(m, n, math.Sqrt(.5/250))
		if s.Value == nil || *s.Value > last {
			t.Fatal(n, s)
		}
		last = *s.Value
		if n == 46 && math.Abs(*s.Value-.9505) > .0001 {
			t.Fatal("published worked example", *s.Value)
		}
		if n == 100 && math.Abs(*s.Value-.90) > .005 {
			t.Fatal("published 90 percent example", *s.Value)
		}
	}
	if DeflatedSharpe(m, 100, 0).Value != nil {
		t.Fatal("invented trial dispersion")
	}
}
func TestCSCVExplicitUnavailabilityAndRankExtremes(t *testing.T) {
	if s := CSCV([][]float64{{.1, .2}}, 4); s.Value != nil || s.Reason == "" {
		t.Fatal(s)
	}
	rows := [][]float64{}
	for i := 0; i < 16; i++ {
		noise := float64(i%4) * .001
		rows = append(rows, []float64{.03 + noise, .02 + noise, .01 + noise, -.01 + noise})
	}
	if s := CSCV(rows, 4); s.Value == nil || *s.Value != 0 {
		t.Fatal("stable winner", s)
	}
	// Two alternating leaders; whichever half wins IS loses in its complement.
	rows = nil
	for i := 0; i < 16; i++ {
		r := []float64{}
		for j := 0; j < 4; j++ {
			v := -.01
			if i/4 == j {
				v = .03
			}
			r = append(r, v+float64(i%4)*.001)
		}
		rows = append(rows, r)
	}
	if s := CSCV(rows, 4); s.Value == nil || *s.Value <= 0 {
		t.Fatal("overfit matrix", s)
	}
	rows[0] = rows[0][:3]
	if CSCV(rows, 4).Value != nil {
		t.Fatal("ragged matrix accepted")
	}
}
func TestMinimumTradableBalanceUsesAccountRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind account.Type
		req  []TradeRequirement
		want string
	}{{"cash", account.Cash, []TradeRequirement{{"SPY", "1", "100", "1"}}, "101.00"}, {"margin-short", account.Margin, []TradeRequirement{{"SPY", "-1", "100", "0"}}, "2000.00"}, {"two-orders", account.Cash, []TradeRequirement{{"SPY", "1", "100", "0"}, {"QQQ", "2", "50", "0"}}, "200.00"}} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := MinimumBalance(tc.kind, tc.req, "0", 1000000)
			if e != nil || got != tc.want {
				t.Fatal(got, e)
			}
		})
	}
}

type testSource struct{ dates []string }

func (s testSource) AdjustedBars(_ context.Context, a string, at time.Time) (marketdata.Series, error) {
	r := marketdata.Series{AssetID: a, AsOf: at}
	for i, d := range s.dates {
		day, _ := time.Parse(time.DateOnly, d)
		known := day.Add(21 * time.Hour)
		if known.After(at) {
			continue
		}
		p := fmt.Sprint(100 + i)
		raw := storage.DailyBar{AssetID: a, Symbol: a, Date: d, Open: p, Close: p, High: p, Low: p, VWAP: p, Volume: 1000, TradeCount: 1, AvailableAt: known, PublishedAt: known, ObservedAt: known}
		r.Bars = append(r.Bars, marketdata.AdjustedBar{Date: d, Open: p, Close: p, High: p, Low: p, Raw: raw})
	}
	return r, nil
}

type testStrategy struct{ weight string }

func (s testStrategy) Spec() strategy.Spec {
	return strategy.Spec{Name: "neighbour", Version: "1", Params: map[string]string{"weight": s.weight}, Data: []string{strategy.KindDailyBars}}
}
func (s testStrategy) Decide(v strategy.View) (strategy.Decision, error) {
	return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: s.weight}}}, nil
}
func TestWalkForwardRegistersEveryNeighbourAndExcludesPurgeEmbargo(t *testing.T) {
	dates := []string{}
	for i := 0; i < 30; i++ {
		dates = append(dates, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
	}
	design := Design{Train: 8, Test: 4, Purge: 2, Embargo: 2}
	folds, e := Folds(dates, design)
	if e != nil {
		t.Fatal(e)
	}
	for i, f := range folds {
		start := slicesIndex(dates, f.Test[0])
		for _, d := range f.Train {
			idx := slicesIndex(dates, d)
			if idx >= start-design.Purge {
				t.Fatal("purge leakage", f)
			}
			for _, prior := range folds[:i] {
				end := slicesIndex(dates, prior.Test[len(prior.Test)-1])
				if idx > end && idx <= end+design.Embargo {
					t.Fatal("embargo leakage", f)
				}
			}
		}
	}
	store := storage.NewMemory()
	cfg := engine.Config{Source: testSource{dates}, Assets: []string{"SPY"}, InitialCash: "2000", Cost: engine.CostModel{Version: "base", FeeBps: "0", SlippageBps: "0"}}
	candidates := []Candidate{testCandidate("base", "0.2"), testCandidate("neighbour", "0.3")}
	trial := engine.Trial{ID: "wf", Family: "test", DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"}
	out, e := WalkForward(context.Background(), store, cfg, dates, design, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, candidates, trial)
	if e != nil {
		t.Fatal(e)
	}
	n, e := store.StrategyTrialSearchTotal(context.Background(), "test")
	if e != nil || int(n) != len(folds)*6 || len(out.Runs) != int(n) {
		t.Fatal(n, e)
	}
	for _, run := range out.Runs {
		record, e := store.StrategyTrial(context.Background(), run.TrialID)
		if e != nil || record.Outcome == nil || record.Outcome.Status != "completed" {
			t.Fatal(record, e)
		}
		var config struct {
			Experiment struct {
				Boundaries      FoldManifest
				Role, Candidate string
			}
		}
		if e = json.Unmarshal(record.Start.Config, &config); e != nil || config.Experiment.Role != run.Role {
			t.Fatal("unbound fold", e)
		}
		if run.Role == "train" {
			for _, r := range record.Returns {
				if slicesIndex(folds[run.Fold].Train, r.Date) < 0 {
					t.Fatal("purge/embargo return entered training", r)
				}
			}
		}
	}
	if HarsherCost(cfg.Cost, cfg.Cost) || HarsherCost(engine.CostModel{FeeBps: "2", SlippageBps: "5"}, engine.CostModel{FeeBps: "1", SlippageBps: "10"}) {
		t.Fatal("favourable cost component accepted")
	}
}
func slicesIndex(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestFailedCandidateDoesNotHideRemainingNeighbourTrials(t *testing.T) {
	dates := []string{}
	for i := 0; i < 20; i++ {
		dates = append(dates, time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
	}
	store := storage.NewMemory()
	bad := testCandidate("bad", "0.2")
	bad.New = func() (strategy.Strategy, error) { return nil, fmt.Errorf("initial factory failed") }
	good := testCandidate("good", "0.3")
	cfg := engine.Config{Source: testSource{dates}, Assets: []string{"SPY"}, InitialCash: "2000", Cost: engine.CostModel{Version: "base", FeeBps: "0", SlippageBps: "0"}}
	trial := engine.Trial{ID: "failure", Family: "failures", DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"}
	out, e := WalkForward(context.Background(), store, cfg, dates, Design{8, 4, 2, 2}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, []Candidate{bad, good}, trial)
	if e == nil {
		t.Fatal("failure omitted")
	}
	n, e := store.StrategyTrialSearchTotal(context.Background(), trial.Family)
	if e != nil || int(n) != len(out.Folds)*6 || len(out.Runs) != int(n) {
		t.Fatal("later attempts missing", n, len(out.Runs), e)
	}
	for _, run := range out.Runs {
		record, e := store.StrategyTrial(context.Background(), run.TrialID)
		if e != nil || record.Outcome == nil {
			t.Fatal(record, e)
		}
		if run.Candidate == "bad" && record.Outcome.Status != "failed" {
			t.Fatal(record)
		}
	}
}
func TestMultiYearFoldManifestStaysWithinRegistryBound(t *testing.T) {
	dates := []string{}
	for i := 0; i < 10000; i++ {
		dates = append(dates, time.Date(2000, 1, i+1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
	}
	folds, e := Folds(dates, Design{5000, 1000, 20, 20})
	if e != nil {
		t.Fatal(e)
	}
	f := folds[len(folds)-1]
	raw, e := json.Marshal(manifest(f, dates))
	if e != nil || len(raw) > 2048 || manifest(f, dates).TrainCount != len(f.Train) {
		t.Fatal(len(raw), e)
	}
}

func TestAssessmentUsesCompleteRegistryFamilyAndDatedBenchmarks(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	target := ""
	var cash, spy Benchmark
	cash = Benchmark{Name: "cash-zero-v1", DataSHA256: strings.Repeat("c", 64)}
	spy = Benchmark{Name: "SPY-test", DataSHA256: strings.Repeat("d", 64)}
	for j := 0; j < 4; j++ {
		s := testStrategy{fmt.Sprintf("0.%d", j+1)}
		sid, e := s.Spec().ID()
		if e != nil {
			t.Fatal(e)
		}
		id := fmt.Sprintf("candidate%d", j)
		if j == 0 {
			target = id
		}
		plan := TradeabilityPlan{StrategyID: sid, Account: account.Cash, Requirements: []TradeRequirement{{"SPY", "1", "100", "0"}}, FixedReserve: "0", MaximumCents: 1000000, MarksAt: now.Add(-time.Hour)}
		cfg, e := json.Marshal(map[string]any{"start": "2026-01-01", "end": "2026-01-16", "cost": "same", "experiment": map[string]any{"Minimum": plan, "Candidate": id}})
		if e != nil {
			t.Fatal(e)
		}
		spec, e := json.Marshal(s.Spec())
		if e != nil {
			t.Fatal(e)
		}
		_, e = store.StartStrategyTrial(ctx, strategytrial.Start{ID: id, Family: "assessment", StrategyID: sid, Spec: spec, Config: cfg, DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: now})
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 16; i++ {
			date := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
			r := fmt.Sprintf("%.6f", .002*float64(j+1)+float64(i%4)*.001)
			if e = store.AppendStrategyTrialReturn(ctx, id, strategytrial.DailyReturn{Date: date, Return: r, Equity: "2000"}); e != nil {
				t.Fatal(e)
			}
			if j == 0 {
				cash.Returns = append(cash.Returns, BenchmarkReturn{date, "0"})
				spy.Returns = append(spy.Returns, BenchmarkReturn{date, "0.001"})
			}
		}
		if e = store.FinishStrategyTrial(ctx, id, strategytrial.Outcome{Status: "completed", FinishedAt: now.Add(time.Minute)}); e != nil {
			t.Fatal(e)
		}
	}
	before, e := Assess(ctx, store, target, cash, spy, 4)
	if e != nil || before.SearchTotal != 4 || before.DSR.Value == nil || before.PBO.Value == nil || before.Minimum == nil || before.Minimum.MinimumBalance != "100.00" || before.CashExcess.Value == nil || before.SPYExcess.Value == nil {
		t.Fatal(before, e)
	}
	record, e := store.StrategyTrial(ctx, target)
	if e != nil {
		t.Fatal(e)
	}
	start := record.Start
	start.ID = "failed-extra"
	start.StartedAt = now.Add(time.Hour)
	if _, e = store.StartStrategyTrial(ctx, start); e != nil {
		t.Fatal(e)
	}
	after, e := Assess(ctx, store, target, cash, spy, 4)
	if e != nil || after.SearchTotal != 5 || after.PBO.Value != nil || after.PBO.Reason != "incomplete_comparable_trials" || after.DSR.Value == nil || *after.DSR.Value >= *before.DSR.Value {
		t.Fatal("failed attempt omitted from penalty", before, after, e)
	}
	cash.Returns[0].Date = "2025-01-01"
	after, e = Assess(ctx, store, target, cash, spy, 4)
	if e != nil || after.CashExcess.Value != nil {
		t.Fatal("undated/wrong benchmark accepted", after, e)
	}
}

func testCandidate(name, weight string) Candidate {
	s := testStrategy{weight}
	spec := s.Spec()
	sid, _ := spec.ID()
	return Candidate{Name: name, Spec: spec, New: func() (strategy.Strategy, error) { return s, nil }, Minimum: &TradeabilityPlan{StrategyID: sid, Account: account.Cash, Requirements: []TradeRequirement{{"SPY", "1", "100", "0"}}, FixedReserve: "0", MaximumCents: 1000000, MarksAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
}
func TestCandidateMinimumMustBeDeclaredBeforeAnyExecution(t *testing.T) {
	for _, reason := range []string{"missing", "future"} {
		t.Run(reason, func(t *testing.T) {
			dates := []string{}
			for i := 0; i < 20; i++ {
				dates = append(dates, time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
			}
			candidate := testCandidate("a", "0.2")
			if reason == "missing" {
				candidate.Minimum = nil
			} else {
				candidate.Minimum.MarksAt = time.Now().Add(time.Hour)
			}
			called := false
			candidate.New = func() (strategy.Strategy, error) { called = true; return testStrategy{"0.2"}, nil }
			store := storage.NewMemory()
			cfg := engine.Config{Source: testSource{dates}, Assets: []string{"SPY"}, InitialCash: "2000", Cost: engine.CostModel{Version: "base", FeeBps: "0", SlippageBps: "0"}}
			_, err := WalkForward(context.Background(), store, cfg, dates, Design{8, 4, 2, 2}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, []Candidate{candidate}, engine.Trial{ID: "min", Family: "minimum", DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture"})
			n, e := store.StrategyTrialSearchTotal(context.Background(), "minimum")
			if err == nil || e != nil || n != 0 || called {
				t.Fatal("invalid plan ran or registered", n, called, err, e)
			}
		})
	}
}
