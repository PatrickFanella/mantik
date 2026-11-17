package backtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
)

func capitalRequest() allocation.RebalanceRequest {
	return allocation.RebalanceRequest{ID: "minimum-check", At: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), Sleeves: []allocation.Sleeve{{ID: "recipe", StrategyID: "strategy-" + strings.Repeat("a", 64), Stage: "incubation", Risk: "1", Minimum: "0"}}, Targets: []allocation.SleeveTarget{{SleeveID: "recipe", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "0.5"}}}}}, Limits: map[string]string{"SPY": "100"}, Fees: map[string]string{"SPY": "0.01"}}
}

func TestStrategyMinimumFractionalCentAndFeeBoundary(t *testing.T) {
	req := capitalRequest()
	marks := map[string]string{"SPY": "100"}
	r, err := StrategyMinimum(context.Background(), account.Cash, req, marks, "recipe", 1000000)
	if err != nil || r.Status != "minimum_at_frozen_snapshot" || r.WholeBookEquity != "40.01" || r.PreviousCent != "40.00" || len(r.Reserved.Pending) != 1 {
		t.Fatal(r, err)
	}
	if r.Distribution.Sleeves[0].Budget != "2.000000000" || r.Reserved.Pending[0].Quantity != "0.010000000" {
		t.Fatal("book equity confused with sleeve budget", r)
	}
	// An independently specified two-name basket requires both $1 legs, rather
	// than accepting one order from a partially sized decision.
	req.Targets[0].Decision.Targets = []allocation.WeightTarget{{Asset: "SPY", Weight: "0.25"}, {Asset: "QQQ", Weight: "0.25"}}
	req.Limits["QQQ"], req.Fees["QQQ"], marks["QQQ"] = "100", "0.01", "100"
	r, err = StrategyMinimum(context.Background(), account.Cash, req, marks, "recipe", 1000000)
	if err != nil || r.WholeBookEquity != "80.02" || r.PreviousCent != "80.01" || len(r.Reserved.Pending) != 2 {
		t.Fatal(r, err)
	}
}

func TestStrategyMinimumShortCannotBorrowCashOrIgnoreFees(t *testing.T) {
	req := capitalRequest()
	req.Targets[0].Decision.Targets[0].Weight = "-0.5"
	marks := map[string]string{"SPY": "100"}
	cash, err := StrategyMinimum(context.Background(), account.Cash, req, marks, "recipe", 1000000)
	if err != nil || cash.Status != "infeasible_cash_roster" || cash.WholeBookEquity != "" {
		t.Fatal(cash, err)
	}
	margin, err := StrategyMinimum(context.Background(), account.Margin, req, marks, "recipe", 1000000)
	if err != nil || margin.WholeBookEquity != "2000.01" || margin.PreviousCent != "2000.00" || margin.PreviousReason != "margin_equity_below_minimum" {
		t.Fatal(margin, err)
	}
	if len(margin.Reserved.Pending) != 1 || margin.Reserved.Pending[0].Side != "sell" {
		t.Fatal(margin)
	}
	bounded, err := StrategyMinimum(context.Background(), account.Margin, req, marks, "recipe", 100000)
	if err != nil || bounded.Status != "infeasible_within_bound" {
		t.Fatal(bounded, err)
	}
}

func TestStrategyMinimumRefusesUnprovedDomains(t *testing.T) {
	for name, mutate := range map[string]func(*allocation.RebalanceRequest){
		"qualified":           func(r *allocation.RebalanceRequest) { r.Sleeves[0].Stage = "qualified" },
		"predeclared minimum": func(r *allocation.RebalanceRequest) { r.Sleeves[0].Minimum = "100" },
		"hold": func(r *allocation.RebalanceRequest) {
			r.Targets[0].Decision = allocation.TargetDecision{HoldExposure: true}
		},
		"gross": func(r *allocation.RebalanceRequest) { r.Targets[0].Decision.Targets[0].Weight = "0.8" },
		"short gap": func(r *allocation.RebalanceRequest) {
			r.Targets[0].Decision.Targets[0].Weight = "-0.5"
			r.Limits["SPY"] = "101"
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := capitalRequest()
			mutate(&req)
			if _, err := StrategyMinimum(context.Background(), account.Margin, req, map[string]string{"SPY": "100"}, "recipe", 1000000); err == nil {
				t.Fatal("unproved search domain accepted")
			}
		})
	}
	req := capitalRequest()
	req.Targets[0].Decision.Targets = nil
	r, err := StrategyMinimum(context.Background(), account.Cash, req, map[string]string{"SPY": "100"}, "recipe", 10000)
	if err != nil || r.Status != "no_trade" || r.WholeBookEquity != "" {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = capitalRequest()
	if _, err := StrategyMinimum(ctx, account.Cash, req, map[string]string{"SPY": "100"}, "recipe", 10000); err == nil {
		t.Fatal("ignored cancellation")
	}
}
