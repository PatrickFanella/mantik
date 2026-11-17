package account

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBookFillAttributionFractionalCashAndOriginalClocks(t *testing.T) {
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	s, err := NewCappedState(DefaultCappedConfig("attribution", "broker", Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	apply := func(c BookCommand) {
		t.Helper()
		s, err = ApplyCappedCommand(s, c)
		if err != nil {
			t.Fatal(c.Kind, err)
		}
	}
	buy := BookOrder{ID: "buy", Symbol: "SPY", Quantity: "0.123456789", Limit: "101", Fee: "0.1", Side: "buy", AllocationID: "allocation", SleeveID: "sleeve", StrategyID: "strategy", At: at}
	apply(BookCommand{ID: buy.ID, Kind: "reserve", At: at, Order: &buy, Marks: map[string]string{"SPY": "100"}})
	apply(BookCommand{ID: "bind-buy", Kind: "bind", At: at, OrderID: buy.ID, VenueOrderID: "venue-buy"})
	fill := BookFill{ID: "buy-fill", OrderID: buy.ID, VenueOrderID: "venue-buy", Symbol: "SPY", Quantity: buy.Quantity, Price: "100.123456789", Fee: "0.012345678", Side: "buy", SourceSHA256: strings.Repeat("a", 64), At: at.Add(time.Second)}
	command := BookCommand{ID: fill.ID, Kind: "fill", At: at.Add(3 * time.Second), Fill: &fill, Marks: map[string]string{"SPY": fill.Price}}
	apply(command)
	// A second order sells the same fractional position; proceeds are not PnL.
	sell := buy
	sell.ID, sell.Side, sell.Limit, sell.At = "sell", "sell", "100", at.Add(4*time.Second)
	apply(BookCommand{ID: sell.ID, Kind: "reserve", At: sell.At, Order: &sell, Marks: map[string]string{"SPY": "101"}})
	apply(BookCommand{ID: "bind-sell", Kind: "bind", At: sell.At, OrderID: sell.ID, VenueOrderID: "venue-sell"})
	close := fill
	close.ID, close.OrderID, close.VenueOrderID, close.Side, close.Price, close.At = "sell-fill", sell.ID, "venue-sell", "sell", "101.123456789", at.Add(5*time.Second)
	apply(BookCommand{ID: close.ID, Kind: "fill", At: at.Add(7 * time.Second), Fill: &close, Marks: map[string]string{"SPY": close.Price}})
	before, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ReadOwnerBook(s, at.Add(8*time.Second))
	if err != nil || v.Execution == nil {
		t.Fatal(v, err)
	}
	e := v.Execution
	if e.GrossBuys != "12.360920478750190521" || e.GrossSells != "12.484377267750190521" || e.Fees != "0.024691356" || e.CashDelta != "0.098765433" || e.ReconciledCash != "2000.098765433" || e.Validate(v.Cash) != nil || len(e.Fills) != 2 {
		t.Fatal("fractional cash conservation failed", e)
	}
	row := e.Fills[0]
	if row.Fill != fill || !row.RecordedAt.Equal(command.At) || row.AllocationID != buy.AllocationID || row.SleeveID != buy.SleeveID || row.StrategyID != buy.StrategyID {
		t.Fatal("original identity, ownership or clock lost", row)
	}
	again, err := ApplyCappedCommand(s, command)
	if err != nil || !reflect.DeepEqual(again, s) {
		t.Fatal("attribution changed fill retry", err)
	}
	after, err := json.Marshal(s)
	if err != nil || string(before) != string(after) {
		t.Fatal("readback changed journal", err)
	}
	for name, change := range map[string]func(*BookExecutionAttribution){
		"gross":             func(e *BookExecutionAttribution) { e.Fills[0].Gross = "12" },
		"fee":               func(e *BookExecutionAttribution) { e.Fills[0].Fill.Fee = "0" },
		"quantity":          func(e *BookExecutionAttribution) { e.Fills[0].Fill.Quantity = "1" },
		"duplicate":         func(e *BookExecutionAttribution) { e.Fills = append(e.Fills, e.Fills[1]) },
		"cash":              func(e *BookExecutionAttribution) { e.ReconciledCash = "2000" },
		"clock":             func(e *BookExecutionAttribution) { e.Fills[0].RecordedAt = at },
		"missing activity":  func(e *BookExecutionAttribution) { e.Fills[0].Fill.ID = "" },
		"overlong activity": func(e *BookExecutionAttribution) { e.Fills[0].Fill.ID = strings.Repeat("a", 129) },
	} {
		t.Run(name, func(t *testing.T) {
			raw, marshalErr := json.Marshal(e)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var bad BookExecutionAttribution
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			change(&bad)
			if bad.Validate(v.Cash) == nil {
				t.Fatal("inconsistent readback accepted")
			}
		})
	}
}

func TestBookFillAttributionUsesOriginalActivityIDContract(t *testing.T) {
	for name, activity := range map[string]string{
		"punctuation":       "activity:123",
		"maximum length":    strings.Repeat("a", 128),
		"terminal controls": "activity:\x1b[31m\noriginal",
	} {
		t.Run(name, func(t *testing.T) {
			at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
			s, err := NewCappedState(DefaultCappedConfig("activity-ids", "broker", Cash, at))
			if err != nil {
				t.Fatal(err)
			}
			order := BookOrder{ID: "local-order", Symbol: "SPY", Quantity: "0.125", Limit: "100", Fee: "0.02", Side: "buy", At: at}
			fill := BookFill{ID: activity, OrderID: order.ID, VenueOrderID: "venue", Symbol: "SPY", Quantity: order.Quantity, Price: "100", Fee: "0.01", Side: "buy", SourceSHA256: strings.Repeat("a", 64), At: at.Add(time.Second)}
			for _, c := range []BookCommand{
				{ID: order.ID, Kind: "reserve", At: at, Order: &order, Marks: map[string]string{"SPY": "100"}},
				{ID: "bind", Kind: "bind", At: at, OrderID: order.ID, VenueOrderID: "venue"},
				{ID: fill.ID, Kind: "fill", At: at.Add(2 * time.Second), Fill: &fill, Marks: map[string]string{"SPY": "100"}},
			} {
				s, err = ApplyCappedCommand(s, c)
				if err != nil {
					t.Fatal("valid original activity was refused by the journal", err)
				}
			}
			v, err := ReadOwnerBook(s, at.Add(3*time.Second))
			if err != nil || v.Execution == nil || v.Execution.Validate(v.Cash) != nil || v.Execution.Fills[0].Fill.ID != activity {
				t.Fatal("attribution rejected or rewrote a valid retained activity ID", err)
			}
		})
	}
}
