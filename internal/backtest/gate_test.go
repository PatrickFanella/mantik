package backtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func gateFixture(t *testing.T) (*storage.Memory, strategygate.Declaration, []Candidate, engine.Config) {
	t.Helper()
	dates := []string{}
	cash := strategygate.Reference{Name: "cash", DataSHA256: strings.Repeat("b", 64)}
	spy := strategygate.Reference{Name: "spy", DataSHA256: strings.Repeat("c", 64)}
	for i := 0; i < 75; i++ {
		day := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
		dates = append(dates, day)
		cash.Returns = append(cash.Returns, strategygate.ReferenceReturn{Date: day, Return: "0"})
		spy.Returns = append(spy.Returns, strategygate.ReferenceReturn{Date: day, Return: "0.02"})
	}
	cs := []Candidate{testCandidate("base", "0.2"), testCandidate("n1", "0.3"), testCandidate("n2", "0.4")}
	spec := cs[0].Spec
	sid, _ := spec.ID()
	raw, e := json.Marshal(spec)
	if e != nil {
		t.Fatal(e)
	}
	d := strategygate.Declaration{Account: "cash", ID: "gate", Family: "gate", ExperimentID: "gate-exp", StrategyID: sid, Spec: raw, BaseCandidate: "base", Balance: "2000", Policy: "backtest-gate-v1", Benchmark: "spy", Calendar: dates, Train: 20, Test: 20, Purge: 2, Embargo: 2, Cash: cash, SPY: spy, DataSHA256: strings.Repeat("a", 64), Coverage: false, Availability: false}
	d.EngineVersion = engine.Version
	d.SettlementCalendar = "us-equity-t1-v1"
	d.Assets = []string{"SPY"}
	d.BaseCost = []byte(`{"version":"base","fee_bps":"0","slippage_bps":"0"}`)
	d.StressCost = []byte(`{"version":"stress","fee_bps":"1","slippage_bps":"5"}`)
	for _, c := range cs {
		raw, e := json.Marshal(c.Spec)
		if e != nil {
			t.Fatal(e)
		}
		plan, e := json.Marshal(c.Minimum)
		if e != nil {
			t.Fatal(e)
		}
		d.Candidates = append(d.Candidates, strategygate.Variant{Name: c.Name, Spec: raw, Minimum: plan})
	}
	cfg := engine.Config{Account: account.Cash, Source: testSource{dates}, Assets: []string{"SPY"}, InitialCash: "2000", Cost: engine.CostModel{Version: "base", FeeBps: "0", SlippageBps: "0"}}
	return storage.NewMemory(), d, cs, cfg
}

func TestGateFrozenPlansCalendarsAndMalformedAttempts(t *testing.T) {
	for _, scenario := range []string{"minimum", "settlement", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			s, d, cs, cfg := gateFixture(t)
			d.Coverage, d.Availability = true, true
			d.Benchmark = "cash"
			for i := range d.SPY.Returns {
				d.SPY.Returns[i].Return = "0"
			}
			ctx := context.Background()
			if _, e := DeclareGate(ctx, s, d); e != nil {
				t.Fatal(e)
			}
			trial := engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: d.DataSHA256, EvidenceClass: "synthetic_fixture"}
			if scenario == "malformed" {
				_, e := s.StartStrategyTrial(ctx, strategytrial.Start{ID: d.ExperimentID + "-f0-base-train", Family: d.Family, StrategyID: d.StrategyID, Spec: d.Spec, Config: json.RawMessage(`{"assets":5}`), DataSHA256: d.DataSHA256, EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if scenario == "minimum" {
					for i := range cs {
						cs[i].Minimum.Requirements = []TradeRequirement{{Asset: "SPY", Quantity: "0.01", Price: "1", Fee: "0"}}
					}
				}
				if scenario == "settlement" {
					cfg.SettlementDate = func(day time.Time) time.Time { return account.SettlementDate(day).AddDate(0, 0, 10) }
					trial.CalendarID = "unreviewed-plus-ten-days"
				}
				if _, e := WalkForward(ctx, s, cfg, d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, cs, trial); e != nil {
					t.Fatal(e)
				}
			}
			r, e := EvaluateGate(ctx, s, d.ID)
			if e != nil || r.Decision == nil || r.Decision.Status != "failed" {
				t.Fatal("bad retained evidence did not yield a failed report", r, e)
			}
			code := map[string]string{"minimum": "/minimum_plan", "settlement": "/engine_settings", "malformed": "/config_integrity"}[scenario]
			found := false
			for _, c := range r.Decision.Checks {
				if strings.HasSuffix(c.Code, code) && !c.Passed {
					found = true
				}
			}
			if !found || r.Decision.Minimum == "0.01" {
				t.Fatal("declaration changed or failure hidden", r.Decision)
			}
		})
	}
}
func TestGateRecordsEveryFailureAndDoesNotPassMissingTrials(t *testing.T) {
	s, d, _, _ := gateFixture(t)
	ctx := context.Background()
	if _, e := DeclareGate(ctx, s, d); e != nil {
		t.Fatal(e)
	}
	r, e := EvaluateGate(ctx, s, d.ID)
	if e != nil || r.Decision == nil || r.Decision.Status != "failed" || r.Decision.Stage != "idea" {
		t.Fatal(r, e)
	}
	failures := 0
	for _, c := range r.Decision.Checks {
		if !c.Passed && !c.Skipped {
			failures++
			if c.Reason == "" {
				t.Fatal(c)
			}
		}
	}
	if failures < 21 {
		t.Fatal("missing failures were hidden", failures)
	}
	again, e := EvaluateGate(ctx, s, d.ID)
	if e != nil || !again.Decision.At.Equal(r.Decision.At) {
		t.Fatal("retry changed decision", again, e)
	}
}
func TestGateUsesRegisteredWalkForwardAndReportsUnavailablePBONotZero(t *testing.T) {
	s, d, cs, cfg := gateFixture(t)
	ctx := context.Background()
	if _, e := DeclareGate(ctx, s, d); e != nil {
		t.Fatal(e)
	}
	_, e := WalkForward(ctx, s, cfg, d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, cs, engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: d.DataSHA256, EvidenceClass: "synthetic_fixture"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := EvaluateGate(ctx, s, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if r.Decision.Status != "failed" || r.Decision.SearchTotal != 18 {
		t.Fatal(r.Decision)
	}
	declarations, skipped := 0, 0
	for _, c := range r.Decision.Checks {
		if strings.HasSuffix(c.Code, "/declaration") {
			declarations++
			if !c.Passed {
				t.Fatal(c)
			}
		}
		if strings.HasSuffix(c.Code, "/PBO") && c.Skipped {
			skipped++
		}
	}
	if declarations != 18 || skipped != 2 || len(r.Decision.Diagnostics) != 2 || r.Decision.Diagnostics[0].PBO.Value != nil {
		t.Fatal("registry or unavailable PBO lost", r.Decision)
	}
}
func TestRuntimeModelSkipsBacktestAndUsesForwardOnlyIncubation(t *testing.T) {
	s, d, _, _ := gateFixture(t)
	var spec strategy.Spec
	if e := json.Unmarshal(d.Spec, &spec); e != nil {
		t.Fatal(e)
	}
	spec.ModelDriven = true
	d.StrategyID, _ = spec.ID()
	d.Spec, _ = json.Marshal(spec)
	d.Minimum = "1"
	d.Candidates = nil
	d.Calendar = nil
	ctx := context.Background()
	if _, e := DeclareGate(ctx, s, d); e != nil {
		t.Fatal(e)
	}
	r, e := EvaluateGate(ctx, s, d.ID)
	if e != nil || r.Decision.Status != "forward_only" || r.Decision.Stage != "incubation" || len(r.Decision.Checks) != 1 || !r.Decision.Checks[0].Skipped {
		t.Fatal(r, e)
	}
}
func TestGateRejectsRetrospectiveDeclarationAndChangedVersion(t *testing.T) {
	s, d, cs, cfg := gateFixture(t)
	ctx := context.Background()
	if _, e := WalkForward(ctx, s, cfg, d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, cs, engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: d.DataSHA256, EvidenceClass: "synthetic_fixture"}); e != nil {
		t.Fatal(e)
	}
	if _, e := DeclareGate(ctx, s, d); e != nil {
		t.Fatal(e)
	}
	r, e := EvaluateGate(ctx, s, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range r.Decision.Checks {
		if strings.HasSuffix(c.Code, "/declaration") && c.Passed {
			t.Fatal("retrospective run admitted", c)
		}
	}
	d.ID = "changed"
	d.StrategyID = "strategy-" + strings.Repeat("f", 64)
	if _, e = DeclareGate(ctx, s, d); e == nil {
		t.Fatal("wrong strategy identity accepted")
	}
}

func TestSyntheticTrialsCannotGrantAdmissionDespiteSourceAssertions(t *testing.T) {
	for _, class := range []string{"synthetic_fixture", "historical_market_unqualified"} {
		t.Run(class, func(t *testing.T) {
			s, d, cs, cfg := gateFixture(t)
			d.Coverage, d.Availability = true, true
			d.Benchmark = "cash"
			for i := range d.SPY.Returns {
				d.SPY.Returns[i].Return = "0"
			}
			ctx := context.Background()
			if _, e := DeclareGate(ctx, s, d); e != nil {
				t.Fatal(e)
			}
			if _, e := WalkForward(ctx, s, cfg, d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, cs, engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: d.DataSHA256, EvidenceClass: class}); e != nil {
				t.Fatal(e)
			}
			if class == "historical_market_unqualified" {
				// Even an extra synthetic search attempt must not enter the family
				// used for operational statistics and admission.
				_, e := s.StartStrategyTrial(ctx, strategytrial.Start{ID: "synthetic-search", Family: d.Family, StrategyID: d.StrategyID, Spec: d.Spec, Config: json.RawMessage(`{"additional":true}`), DataSHA256: d.DataSHA256, EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
				if e != nil {
					t.Fatal(e)
				}
			}
			r, e := EvaluateGate(ctx, s, d.ID)
			if e != nil || r.Decision == nil || r.Decision.Status != "failed" || r.Decision.Stage != "idea" {
				t.Fatal("synthetic trials granted operational admission", r.Decision, e)
			}
			found := false
			for _, c := range r.Decision.Checks {
				if c.Code == "evidence_class" && !c.Passed && !c.Skipped {
					found = true
				}
			}
			if !found {
				t.Fatal("synthetic evidence refusal missing", r.Decision)
			}
		})
	}
}

func TestGatePassMovesOnlyToIncubationAndRejectsDifferentCapital(t *testing.T) {
	for _, balance := range []string{"2000", "20000"} {
		t.Run(balance, func(t *testing.T) {
			s, d, cs, cfg := gateFixture(t)
			d.Coverage, d.Availability = true, true
			d.Benchmark = "cash"
			for i := range d.SPY.Returns {
				d.SPY.Returns[i].Return = "0"
			}
			ctx := context.Background()
			if _, e := DeclareGate(ctx, s, d); e != nil {
				t.Fatal(e)
			}
			cfg.InitialCash = balance
			// Simulate the historical-class protocol with an in-memory source.
			// These generated returns remain unit fixtures, not real evidence.
			if _, e := WalkForward(ctx, s, cfg, d.Calendar, Design{d.Train, d.Test, d.Purge, d.Embargo}, engine.CostModel{Version: "stress", FeeBps: "1", SlippageBps: "5"}, cs, engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: d.DataSHA256, EvidenceClass: "historical_market_unqualified"}); e != nil {
				t.Fatal(e)
			}
			r, e := EvaluateGate(ctx, s, d.ID)
			if e != nil {
				t.Fatal(e)
			}
			if balance == "2000" {
				if r.Decision.Status != "passed" || r.Decision.Stage != "incubation" {
					t.Fatal("positive fixture did not enter incubation", r.Decision)
				}
			} else {
				found := false
				for _, c := range r.Decision.Checks {
					if strings.HasSuffix(c.Code, "/engine_settings") && !c.Passed {
						found = true
					}
				}
				if r.Decision.Status != "failed" || !found {
					t.Fatal("larger capital bypassed frozen declaration", r.Decision)
				}
			}
		})
	}
}
