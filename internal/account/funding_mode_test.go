package account

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCappedFundingModeLegacyJSONAndValidation(t *testing.T) {
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	cfg := DefaultCappedConfig("book", "account", Cash, at)
	raw, err := json.Marshal(cfg)
	const legacy = `{"ID":"book","BrokerAccountID":"account","Type":"cash","OpenedAt":"2026-11-16T15:00:00Z","OpeningCash":"2000","DepositAmount":"500","EveryWeeks":2}`
	if err != nil || string(raw) != legacy {
		t.Fatal("default legacy config bytes changed", string(raw), err)
	}
	state, err := NewCappedState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := json.Marshal(state)
	if err != nil || string(empty) != `{"Config":`+legacy+`,"Version":0,"Events":[]}` {
		t.Fatal("empty legacy state bytes/hash changed", string(empty), err)
	}
	for _, mode := range []string{"", SyntheticSchedule, OwnerReceipts} {
		copy := cfg
		copy.FundingMode = mode
		s, e := NewCappedState(copy)
		if e != nil || s.Config.FundingMode != mode {
			t.Fatal(mode, s, e)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*CappedConfig)
	}{
		{"unknown", func(c *CappedConfig) { c.FundingMode = "owner_receipt" }},
		{"opening", func(c *CappedConfig) { c.OpeningCash = "5000" }},
		{"amount", func(c *CappedConfig) { c.DepositAmount = "1000" }},
		{"interval", func(c *CappedConfig) { c.EveryWeeks = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cfg
			bad.FundingMode = OwnerReceipts
			tc.change(&bad)
			if _, e := NewCappedState(bad); e == nil {
				t.Fatal("malformed funding config accepted")
			}
		})
	}
}

func TestCappedFundingModeRejectsUnconfirmedMixedAndSidecarDeposits(t *testing.T) {
	opened := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	at := opened.AddDate(0, 0, 6)
	cfg := DefaultCappedConfig("book", "account", Cash, opened)
	due, _ := cfg.DueDeposits(at)
	a := OwnerBookAction{ExpectedVersion: 0, Action: "deposit", Amount: "500", Source: "fixture-receipt", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
	a.Confirmation = a.ConfirmationPin(cfg.ID, due[0].ID)
	confirmed := BookCommand{ID: due[0].ID, Kind: "deposit", Amount: a.Amount, At: at, EffectiveAt: a.EffectiveAt, OwnerAction: &a}
	for _, mode := range []string{"", SyntheticSchedule, OwnerReceipts} {
		t.Run("mode="+mode, func(t *testing.T) {
			c := cfg
			c.FundingMode = mode
			s, err := NewCappedState(c)
			if err != nil {
				t.Fatal(err)
			}
			if mode != OwnerReceipts {
				if _, err = ApplyCappedCommand(s, confirmed); err == nil {
					t.Fatal("synthetic book accepted real receipt")
				}
				raw := s
				raw.Events = []BookCommand{confirmed}
				raw.Version = 1
				if _, err = SnapshotCapped(raw, nil, at); err == nil {
					t.Fatal("mixed raw replay accepted")
				}
				legacy, err := ApplyCappedCommand(s, due[0])
				if err != nil {
					t.Fatal(err)
				}
				if v, e := ReadOwnerBook(legacy, at); e != nil || v.Cash != "2500.000000000" || v.Deposits[0].OwnerAction != nil {
					t.Fatal(v, e)
				}
			} else {
				if _, err = ApplyCappedCommand(s, due[0]); err == nil {
					t.Fatal("unconfirmed scheduled cash credited")
				}
				raw := s
				raw.Events = []BookCommand{due[0]}
				raw.Version = 1
				if _, err = SnapshotCapped(raw, nil, at); err == nil {
					t.Fatal("raw unconfirmed deposit replayed")
				}
				for _, tc := range []struct {
					name   string
					change func(*BookCommand)
				}{
					{"sidecar-order", func(c *BookCommand) { c.Order = &BookOrder{ID: "hidden"} }},
					{"sidecar-fill", func(c *BookCommand) { c.Fill = &BookFill{ID: "hidden"} }},
					{"sidecar-marks", func(c *BookCommand) { c.Marks = map[string]string{"SPY": "100"} }},
					{"sidecar-order-id", func(c *BookCommand) { c.OrderID = "hidden" }},
					{"wrong-event-kind", func(c *BookCommand) { c.Kind = "risk_observe" }},
					{"wrong-amount", func(c *BookCommand) { c.Amount = "1000" }},
					{"wrong-economic-clock", func(c *BookCommand) { c.EffectiveAt = c.EffectiveAt.Add(time.Second) }},
					{"fake-attestation", func(c *BookCommand) {
						c.OwnerAction = &OwnerBookAction{Action: "deposit", Amount: "500", Source: "fake", EffectiveAt: a.EffectiveAt}
					}},
				} {
					t.Run(tc.name, func(t *testing.T) {
						bad := confirmed
						tc.change(&bad)
						if _, e := ApplyCappedCommand(s, bad); e == nil {
							t.Fatal("malformed owner receipt accepted")
						}
					})
				}
			}
		})
	}
}
