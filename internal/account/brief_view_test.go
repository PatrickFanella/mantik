package account

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func TestDailyBriefBookViewDoesNotObserveOrAlias(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	b, e := NewCappedState(DefaultCappedConfig("brief-view", "paper", Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	b, e = ApplyCappedCommand(b, BookCommand{ID: "pause", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "pause_strategy", StrategyID: "one"}})
	if e != nil {
		t.Fatal(e)
	}
	order := BookOrder{ID: "pending", Symbol: "SPY", Quantity: "1", Limit: "100", Fee: "0", Side: "buy", At: at}
	b, e = ApplyCappedCommand(b, BookCommand{ID: "reserve-pending", Kind: "reserve", At: at, Order: &order, Marks: map[string]string{"SPY": "100"}})
	if e != nil {
		t.Fatal(e)
	}
	before, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	view, roster, e := BriefBookView(b, at.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if view.Cash != "2000.000000000" || view.SettledCash != "2000.000000000" || view.Equity != "" || !view.Risk.At.IsZero() || len(view.Pending) != 1 || len(roster) != 0 {
		t.Fatal(view, roster)
	}
	original, e := SnapshotCapped(b, map[string]string{}, at.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(view.Risk, original.Risk) || view.Cash != original.Cash || view.SettledCash != original.SettledCash || !reflect.DeepEqual(view.Pending, original.Pending) {
		t.Fatal("book read changed account semantics")
	}
	view.Pending[0].Limit = "1"
	view.Risk.Breakers["one"] = false
	view.Positions["SPY"] = "100"
	after, e := json.Marshal(b)
	if e != nil || string(before) != string(after) {
		t.Fatal("book view aliases journal", e)
	}
	if _, _, e = BriefBookView(b, at.Add(-time.Nanosecond)); e == nil {
		t.Fatal("historical prefix silently truncated")
	}
}
