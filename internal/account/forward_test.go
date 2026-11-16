package account

import (
	"encoding/json"
	"testing"
	"time"
)

func TestForwardHoldingOpenedUsesExecutionOrder(t *testing.T) {
	base := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	type fill struct {
		second                 int
		symbol, side, quantity string
	}
	for _, tc := range []struct {
		name   string
		fills  []fill
		opened int
	}{
		{"late earlier partial", []fill{{20, "SPY", "buy", "1"}, {10, "SPY", "buy", "1"}}, 10},
		{"late flat interval resets at reopening", []fill{{40, "SPY", "buy", "1"}, {10, "SPY", "buy", "1"}, {30, "SPY", "sell", "1"}}, 40},
		{"other asset preserves continuous exposure", []fill{{20, "QQQ", "buy", "1"}, {10, "SPY", "buy", "1"}, {30, "SPY", "sell", "1"}}, 10},
		{"fully flat resets clock", []fill{{10, "SPY", "buy", "1"}, {20, "SPY", "sell", "1"}}, 0},
		{"partial reduction cannot reset clock", []fill{{20, "SPY", "buy", "1"}, {30, "SPY", "sell", "0.5"}, {10, "SPY", "buy", "1"}}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := CappedState{Events: []BookCommand{{Kind: "portfolio_reserve", At: base, Order: &BookOrder{ID: "owned", SleeveID: "sleeve"}}}}
			for i, f := range tc.fills {
				state.Events = append(state.Events, BookCommand{Kind: "fill", At: base.Add(time.Duration(50+i) * time.Second), Fill: &BookFill{OrderID: "owned", Symbol: f.symbol, Side: f.side, Quantity: f.quantity, At: base.Add(time.Duration(f.second) * time.Second)}})
			}
			before, e := json.Marshal(state)
			if e != nil {
				t.Fatal(e)
			}
			opened, e := ForwardHoldingOpened(state, "sleeve", base.Add(time.Minute))
			if e != nil {
				t.Fatal(e)
			}
			want := time.Time{}
			if tc.opened != 0 {
				want = base.Add(time.Duration(tc.opened) * time.Second)
			}
			if !opened.Equal(want) {
				t.Fatal("execution replay", opened, want)
			}
			after, e := json.Marshal(state)
			if e != nil || string(after) != string(before) {
				t.Fatal("reordered retained receipt journal", e)
			}
		})
	}
}

func TestForwardHoldingOpenedRetainsReceiptChecks(t *testing.T) {
	base := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	reserve := BookCommand{Kind: "portfolio_reserve", At: base, Order: &BookOrder{ID: "owned", SleeveID: "sleeve"}}
	fill := BookCommand{Kind: "fill", At: base.Add(time.Second), Fill: &BookFill{OrderID: "owned", Symbol: "SPY", Side: "buy", Quantity: "1", At: base}}
	for _, tc := range []struct {
		name  string
		state CappedState
		at    time.Time
	}{
		{"future receipt", CappedState{Events: []BookCommand{reserve, fill}}, base},
		{"future owned execution", CappedState{Events: []BookCommand{reserve, {Kind: "fill", At: base, Fill: &BookFill{OrderID: "owned", Symbol: "SPY", Side: "buy", Quantity: "1", At: base.Add(time.Second)}}}}, base},
		{"invalid quantity", CappedState{Events: []BookCommand{reserve, {Kind: "fill", At: base, Fill: &BookFill{OrderID: "owned", Symbol: "SPY", Side: "buy", Quantity: "invalid", At: base}}}}, base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := ForwardHoldingOpened(tc.state, "sleeve", tc.at); e == nil {
				t.Fatal("invalid retained input accepted")
			}
		})
	}
	// Ownership is checked in receipt order: a later reservation cannot make an
	// earlier unowned fill part of this sleeve's clock.
	state := CappedState{Events: []BookCommand{fill, {Kind: "portfolio_reserve", At: base.Add(2 * time.Second), Order: reserve.Order}}}
	opened, e := ForwardHoldingOpened(state, "sleeve", base.Add(3*time.Second))
	if e != nil || !opened.IsZero() {
		t.Fatal(opened, e)
	}
}
