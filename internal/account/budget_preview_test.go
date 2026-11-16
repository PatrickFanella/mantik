package account

import (
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func TestPreviewPortfolioBudgetsValuesMarginShort(t *testing.T) {
	at := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	s, err := NewCappedState(DefaultCappedConfig("budget-book", "paper", Margin, at))
	if err != nil {
		t.Fatal(err)
	}
	incumbent := allocation.Sleeve{ID: "incumbent", StrategyID: "strategy-" + strings.Repeat("a", 64), Stage: "incubation", Risk: "1", Minimum: "1"}
	req := allocation.RebalanceRequest{ID: "roster", At: at, Sleeves: []allocation.Sleeve{incumbent}, Targets: []allocation.SleeveTarget{{SleeveID: incumbent.ID, Decision: allocation.TargetDecision{HoldExposure: true}}}}
	order := BookOrder{ID: "short", SleeveID: incumbent.ID, StrategyID: incumbent.StrategyID, Symbol: "QQQ", Quantity: "0.1", Limit: "100", Fee: "0", Side: "sell", At: at}
	fill := BookFill{ID: "short-fill", OrderID: order.ID, VenueOrderID: "venue-short", Symbol: "QQQ", Quantity: "0.1", Price: "100", Fee: "0", Side: "sell", At: at, SourceSHA256: strings.Repeat("b", 64)}
	for _, command := range []BookCommand{
		{ID: req.ID, Kind: "rebalance", At: at, Rebalance: &req},
		{ID: "reserve-short", Kind: "reserve", At: at, Order: &order, Marks: map[string]string{"QQQ": "100"}},
		{ID: "bind-short", Kind: "bind", At: at, OrderID: order.ID, VenueOrderID: fill.VenueOrderID},
		{ID: fill.ID, Kind: "fill", At: at, Fill: &fill, Marks: map[string]string{"QQQ": "100"}},
	} {
		s, err = ApplyCappedCommand(s, command)
		if err != nil {
			t.Fatalf("%s: %v", command.Kind, err)
		}
	}
	prices := map[string]string{"QQQ": "100", "SPY": "100"}
	snap, err := SnapshotCapped(s, prices, at)
	if err != nil || snap.Positions["QQQ"] != "-0.100000000" || snap.Equity != "2000.000000000" {
		t.Fatal("valid margin short required", snap, err)
	}
	for i := range 3 {
		s, err = ApplyCappedCommand(s, BookCommand{ID: "mark-" + strings.Repeat("x", i+1), Kind: "risk_observe", At: at.Add(time.Duration(i+1) * 10 * time.Second), Marks: prices})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err = ApplyCappedCommand(s, BookCommand{ID: "resume", Kind: "risk_control", At: at.Add(40 * time.Second), RiskControl: &risk.Control{Action: "resume"}})
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(70 * time.Second)
	retained, err := CurrentPortfolioMarks(s, at)
	if err != nil || retained["QQQ"] != "100" {
		t.Fatal("fresh checkpoint mark was lost behind a control", retained, err)
	}
	model := allocation.Sleeve{ID: "model", StrategyID: "strategy-" + strings.Repeat("c", 64), Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}
	preview := allocation.RebalanceRequest{ID: "preview", At: at, Sleeves: []allocation.Sleeve{incumbent, model}}
	budgets, err := PreviewPortfolioBudgets(s, preview, prices, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range budgets {
		if budget.SleeveID == model.ID {
			if budget.Status != "ready" || budget.Budget != "20.000000000" {
				t.Fatal("funded analyst denied by another owner's short", budget)
			}
			return
		}
	}
	t.Fatal("model budget missing", budgets)
}
