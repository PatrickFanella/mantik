package account

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

func TestOwnerBookRealDepositAndLegacyReplay(t *testing.T) {
	opened := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	cfg := DefaultCappedConfig("owner-book", "owner-account", Cash, opened)
	cfg.FundingMode = OwnerReceipts
	state, err := NewCappedState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	at := opened.AddDate(0, 0, 6)
	due, err := cfg.DueDeposits(at)
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	d := due[0]
	actual := d.EffectiveAt.Add(2 * time.Hour)
	a := OwnerBookAction{ExpectedVersion: 0, Action: "deposit", Amount: "500", Source: "bank-receipt-local-1", EffectiveAt: actual}
	a.Confirmation = a.ConfirmationPin(cfg.ID, d.ID)
	d.EffectiveAt, d.OwnerAction = actual, &a
	state, err = ApplyCappedCommand(state, d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state)
	var reopened CappedState
	if err = json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	v, err := ReadOwnerBook(reopened, at)
	if err != nil || v.Cash != "2500.000000000" || v.Equity != "2500.000000000" || len(v.Deposits) != 1 || v.Deposits[0].OwnerAction.Source != a.Source {
		t.Fatal(v, err)
	}
	again, err := ApplyCappedCommand(reopened, d)
	if err != nil || again.Version != 1 {
		t.Fatal(again, err)
	}
	changed := d
	copy := a
	copy.Source = "different-receipt"
	changed.OwnerAction = &copy
	if _, err = ApplyCappedCommand(reopened, changed); err == nil {
		t.Fatal("changed receipt accepted")
	}
	for _, tc := range []struct {
		name   string
		change func(*BookCommand)
	}{
		{"early", func(c *BookCommand) {
			c.EffectiveAt = due[0].EffectiveAt.Add(-time.Second)
			c.OwnerAction.EffectiveAt = c.EffectiveAt
		}},
		{"future", func(c *BookCommand) { c.EffectiveAt = at.Add(time.Second); c.OwnerAction.EffectiveAt = c.EffectiveAt }},
		{"extra-slot", func(c *BookCommand) { c.ID = "deposit-2026-11-27" }},
		{"amount", func(c *BookCommand) { c.Amount = "1000"; c.OwnerAction.Amount = "1000" }},
		{"source", func(c *BookCommand) { c.OwnerAction.Source = "" }},
		{"version", func(c *BookCommand) { c.OwnerAction.ExpectedVersion = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewCappedState(cfg)
			bad := d
			meta := a
			bad.OwnerAction = &meta
			tc.change(&bad)
			bad.OwnerAction.Confirmation = bad.OwnerAction.ConfirmationPin(cfg.ID, bad.ID)
			if _, e := ApplyCappedCommand(s, bad); e == nil {
				t.Fatal("invalid real deposit accepted")
			}
		})
	}
	// A second due slot cannot reuse the same real receipt under a new ID.
	later := at.AddDate(0, 0, 14)
	laterDue, _ := cfg.DueDeposits(later)
	duplicate := laterDue[1]
	duplicate.At = later
	meta := a
	meta.ExpectedVersion = state.Version
	meta.EffectiveAt = duplicate.EffectiveAt
	meta.Confirmation = meta.ConfirmationPin(cfg.ID, duplicate.ID)
	duplicate.OwnerAction = &meta
	if _, err = ApplyCappedCommand(state, duplicate); err == nil {
		t.Fatal("same real receipt funded twice")
	}
	// Existing scheduled paper flows retain their original payload and semantics.
	legacyConfig := cfg
	legacyConfig.FundingMode = ""
	legacy, _ := NewCappedState(legacyConfig)
	legacy, err = ApplyCappedCommand(legacy, due[0])
	if err != nil {
		t.Fatal(err)
	}
	v, err = ReadOwnerBook(legacy, at)
	if err != nil || v.Deposits[0].OwnerAction != nil || v.Cash != "2500.000000000" {
		t.Fatal(v, err)
	}
}

func TestOwnerBookConfirmationBindsEveryTerm(t *testing.T) {
	a := OwnerBookAction{ExpectedVersion: 2, Action: "pause_strategy", StrategyID: "strategy-version"}
	a.Confirmation = a.ConfirmationPin("book", "pause-1")
	if err := a.Validate("book", "pause-1"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*OwnerBookAction)
	}{
		{"version", func(a *OwnerBookAction) { a.ExpectedVersion++ }},
		{"strategy", func(a *OwnerBookAction) { a.StrategyID = "other" }},
		{"action", func(a *OwnerBookAction) { a.Action = "retire_strategy" }},
		{"generic-yes", func(a *OwnerBookAction) { a.Confirmation = "yes" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := a
			tc.change(&b)
			if b.Validate("book", "pause-1") == nil {
				t.Fatal("changed terms accepted")
			}
		})
	}
	if a.Validate("other", "pause-1") == nil || a.Validate("book", "pause-2") == nil {
		t.Fatal("cross-book/action confirmation accepted")
	}
}

func TestOwnerBookClearRefreshesRiskAndPreservesRetirement(t *testing.T) {
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	s, _ := NewCappedState(DefaultCappedConfig("book", "account", Cash, at))
	for i, c := range []risk.Control{{Action: "retire_strategy", StrategyID: "retired"}, {Action: "kill"}} {
		var err error
		s, err = ApplyCappedCommand(s, BookCommand{ID: strings.Repeat("a", i+1), Kind: "risk_control", At: at, RiskControl: &c})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, _ = ApplyCappedCommand(s, BookCommand{ID: "halt", Kind: "halt", At: at})
	a := OwnerBookAction{ExpectedVersion: s.Version, Action: "owner_clear_halt"}
	a.Confirmation = a.ConfirmationPin("book", "clear")
	c := BookCommand{ID: "clear", Kind: "risk_control", At: at, Marks: map[string]string{}, RiskControl: &risk.Control{Action: a.Action}, OwnerAction: &a}
	s, err := ApplyCappedCommand(s, c)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ReadOwnerBook(s, at)
	if err != nil || v.Halted || !v.Risk.Retired["retired"] || !v.Risk.Kill {
		t.Fatal(v, err)
	}
}

func TestOwnerBookLogicalVersionConfirmationBounds(t *testing.T) {
	for _, version := range []int64{-1, 0, 4095, 4096, 100000, math.MaxInt64 - 1, math.MaxInt64} {
		a := OwnerBookAction{ExpectedVersion: version, Action: "owner_clear_halt"}
		a.Confirmation = a.ConfirmationPin("book", "clear")
		err := a.Validate("book", "clear")
		if valid := version >= 0 && version < math.MaxInt64; valid != (err == nil) {
			t.Fatalf("logical version %d: %v", version, err)
		}
	}
}
