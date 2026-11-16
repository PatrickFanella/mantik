package risk

import (
	"slices"
	"testing"
	"time"
)

func TestEveryHardLimitHasAReasonAndAllowsOnlyReducingExits(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	s, e := Observe(State{}, at, "2000", "0")
	if e != nil {
		t.Fatal(e)
	}
	base := OrderView{StrategyID: "trend", Equity: "2000", CashAfter: "1000", Gross: "1000", Position: "90", Sleeve: "390"}
	for _, tc := range []struct {
		name   string
		change func(*State, *OrderView)
		reason string
	}{
		{"gross", func(_ *State, v *OrderView) { v.Gross = "2000.01" }, "risk_gross_limit"},
		{"position", func(_ *State, v *OrderView) { v.Position = "100.000000001" }, "risk_position_limit"},
		{"sleeve", func(_ *State, v *OrderView) { v.Sleeve = "400.01" }, "risk_sleeve_limit"},
		{"reserve", func(_ *State, v *OrderView) { v.CashAfter = "399.99" }, "risk_cash_reserve"},
		{"incubation", func(_ *State, v *OrderView) { v.Incubation = "100.01" }, "risk_incubation_pool"},
		{"model", func(_ *State, v *OrderView) { v.ModelVersion = "20.01" }, "risk_model_version_limit"},
		{"kill", func(s *State, _ *OrderView) { s.Kill = true }, "risk_kill_switch"},
		{"halt", func(s *State, _ *OrderView) { s.Halted = true }, "risk_hard_halt"},
		{"reduce", func(s *State, _ *OrderView) { s.ReduceOnly = true }, "risk_reduce_only"},
		{"breaker", func(s *State, _ *OrderView) { s.Breakers = map[string]bool{"trend": true} }, "risk_strategy_breaker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, v := s, base
			tc.change(&state, &v)
			reasons, e := Check(state, v)
			if e != nil || !slices.Contains(reasons, tc.reason) {
				t.Fatal(reasons, e)
			}
			v.Reducing = true
			if r, e := Check(state, v); e != nil || len(r) != 0 {
				t.Fatal("reducing exit blocked", r, e)
			}
		})
	}
}
func TestLossesIgnoreDepositsAndHardHaltSurvivesRecoveryAndResume(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	s, e := Observe(State{}, at, "2000", "0")
	if e != nil {
		t.Fatal(e)
	}
	s, e = Observe(s, at.Add(time.Hour), "2500", "500")
	if e != nil || s.NAV != "1.000000000000000000" || s.Warn {
		t.Fatal("deposit became profit", s, e)
	}
	s, e = Observe(s, at.Add(2*time.Hour), "2375", "500")
	if e != nil || !s.Warn || !s.Paused || s.PauseReason != "risk_daily_loss" {
		t.Fatal(s, e)
	}
	s, e = Observe(s, at.Add(3*time.Hour), "2250", "500")
	if e != nil || s.PauseReason != "risk_drawdown_pause" {
		t.Fatal(s, e)
	}
	s, e = Observe(s, at.Add(4*time.Hour), "2125", "500")
	if e != nil || !s.Halted {
		t.Fatal(s, e)
	}
	s, e = Observe(s, at.AddDate(0, 0, 1), "3000", "1000")
	if e != nil || !s.Halted {
		t.Fatal("new day/deposit cleared halt", s, e)
	}
	s, e = ApplyControl(s, Control{Action: "resume"})
	if e != nil || !s.Halted {
		t.Fatal("resume cleared halt", s, e)
	}
	s, e = ApplyControl(s, Control{Action: "owner_clear_halt"})
	if e != nil || s.Halted {
		t.Fatal(s, e)
	}
}

func TestInsolvencyPersistsHaltButAllowsAnActualReduction(t *testing.T) {
	at := time.Now().UTC()
	s, e := Observe(State{}, at, "2000", "0")
	if e != nil {
		t.Fatal(e)
	}
	s, e = Observe(s, at.Add(time.Second), "-100", "0")
	if e != nil || !s.Halted {
		t.Fatal(s, e)
	}
	v := OrderView{Equity: "-100", CashAfter: "-100", Gross: "2200", Position: "2200", Sleeve: "2200", Reducing: true}
	if reasons, e := Check(s, v); e != nil || len(reasons) > 0 {
		t.Fatal("insolvency trapped exit", reasons, e)
	}
	if _, e = ApplyControl(s, Control{Action: "owner_clear_halt"}); e == nil {
		t.Fatal("insolvent book cleared")
	}
}
