package engine

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// planned chooses a complete target set by session date.
type planned map[string][]strategy.Target

func (planned) Spec() strategy.Spec { return allIn{}.Spec() }
func (p planned) Decide(v strategy.View) (strategy.Decision, error) {
	b := v.Bars(asset)
	return strategy.Decision{Targets: p[b[len(b)-1].Date]}, nil
}

var free = CostModel{Version: "free", FeeBps: "0", SlippageBps: "0"}

func TestCashRefusesUnfundedTargetAndShort(t *testing.T) {
	for _, tc := range []struct {
		name, weight string
		cost         CostModel
		want         account.Code
	}{
		{"fees", "1", DefaultCost, account.FreeRiding},
		{"short", "-1", free, account.ShortNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planned{"2026-01-05": {{Asset: asset, Weight: tc.weight}}}
			r, err := Run(context.Background(), config(newSource(t, prices), p, tc.cost))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Fills) != 0 || len(r.Refusals) != 1 || r.Refusals[0].Code != tc.want || r.Refusals[0].Date != "2026-01-06" || r.Refusals[0].DecidedOn != "2026-01-05" {
				t.Fatalf("%+v", r)
			}
			for _, d := range r.Days {
				if d.Cash != "1000" || d.Equity != "1000" || len(d.Positions) != 0 {
					t.Fatal(d)
				}
			}
		})
	}
}

func TestCashRotationWaitsForSettlement(t *testing.T) {
	// Sell the lexically later asset and buy the earlier one in the same
	// session. Reordering reductions first cannot make the proceeds settled.
	const other = "asset-a"
	src := newSource(t, [][2]string{{"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}})
	for day := 5; day <= 9; day++ {
		at := closeAt(day)
		_, err := src.m.RecordDailyBars(context.Background(), []storage.DailyBar{{AssetID: other, Symbol: "ABC", Date: at.Format(time.DateOnly), Open: "100", High: "100", Low: "100", Close: "100", VWAP: "100", Volume: 100, TradeCount: 1, PublishedAt: at, ObservedAt: at, AvailableAt: at}})
		if err != nil {
			t.Fatal(err)
		}
	}
	p := planned{
		"2026-01-05": {{Asset: asset, Weight: "1"}},
		"2026-01-06": {{Asset: other, Weight: "1"}},
		"2026-01-07": {{Asset: other, Weight: "1"}},
		"2026-01-08": {{Asset: other, Weight: "1"}},
	}
	for _, kind := range []account.Type{account.Cash, account.Margin} {
		c := config(src, p, free)
		c.Assets, c.Account, c.InitialCash = []string{other, asset}, kind, "3000"
		r, err := Run(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if kind == account.Cash {
			if len(r.Refusals) != 1 || r.Refusals[0].Code != account.UnsettledFunds || r.Refusals[0].Asset != other || r.Refusals[0].Date != "2026-01-07" {
				t.Fatalf("%+v", r.Refusals)
			}
			if r.Days[2].Cash != "3000" || r.Days[2].Account.SettledCash != "0" || r.Days[2].Account.UnsettledCash != "3000" || r.Fills[2].Date != "2026-01-08" {
				t.Fatalf("%+v", r)
			}
		} else if len(r.Refusals) != 0 || r.Fills[2].Date != "2026-01-07" || r.Days[2].Positions[0].Asset != other {
			t.Fatalf("margin rotation failed: %+v", r)
		}
	}
}

func TestSettlementHolidayAndGap(t *testing.T) {
	// A January 15 sale settles January 16 even if there is no bar then.
	// A January 16 sale waits across the weekend and MLK Day to January 20.
	for _, tc := range []struct {
		days             []int
		sale, settlement string
		blocked          bool
	}{
		{[]int{13, 14, 15, 20}, "2026-01-15", "2026-01-20", false},
		{[]int{14, 15, 16, 19, 20}, "2026-01-16", "2026-01-20", true},
	} {
		ps := make([][2]string, len(tc.days))
		for i := range ps {
			ps[i] = [2]string{"100", "100"}
		}
		p := planned{}
		for _, day := range tc.days {
			p[closeAt(day).Format(time.DateOnly)] = []strategy.Target{{Asset: asset, Weight: "1"}}
		}
		p[closeAt(tc.days[1]).Format(time.DateOnly)] = nil // sell at the next open
		c := config(sourceOn(t, tc.days, ps), p, free)
		c.End = closeAt(20).Add(time.Hour)
		r, err := Run(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Fills) != 3 || r.Fills[1].Date != tc.sale || r.Fills[2].Date != tc.settlement {
			t.Fatalf("%+v", r)
		}
		if tc.blocked && (len(r.Refusals) != 1 || r.Refusals[0].Code != account.UnsettledFunds) {
			t.Fatal(r.Refusals)
		}
	}
}

func TestMarginFallsBackAndStillClosesAShort(t *testing.T) {
	p := planned{
		"2026-01-05": {{Asset: asset, Weight: "-1"}},
		"2026-01-06": {{Asset: asset, Weight: "-0.25"}},
		"2026-01-07": {{Asset: asset, Weight: "-1"}},
		"2026-01-08": nil,
	}
	// The short loses $1,200 at the Jan 6 close. Rebalancing first reduces
	// it to a quarter-weight short, then the full-weight target is refused.
	c := config(newSource(t, [][2]string{{"100", "100"}, {"100", "140"}, {"140", "140"}, {"140", "140"}, {"140", "140"}}), p, free)
	c.Account, c.InitialCash = account.Margin, "3000"
	r, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Days[1].Account.EffectiveType != account.Cash || len(r.Refusals) != 1 || r.Refusals[0].Code != account.BelowMinimum || r.Refusals[0].Date != "2026-01-08" {
		t.Fatalf("%+v", r)
	}
	if len(r.Days[4].Positions) != 0 || r.Fills[len(r.Fills)-1].Date != "2026-01-09" {
		t.Fatalf("short not closed: %+v", r)
	}
}

func TestShortCorporateActionsAndMaintenanceShortfall(t *testing.T) {
	p := planned{"2026-01-05": {{Asset: asset, Weight: "-1"}}, "2026-01-06": {{Asset: asset, Weight: "-1"}}}
	src := newSource(t, [][2]string{{"100", "100"}, {"100", "100"}, {"50", "50"}, {"150", "150"}, {"150", "150"}},
		marketdata.CorporateAction{SourceID: "s", Kind: "split", ExDate: "2026-01-07", From: "1", To: "2", KnownAt: closeAt(1)},
		marketdata.CorporateAction{SourceID: "d", Kind: "cash_dividend", ExDate: "2026-01-07", PayDate: "2026-01-08", Amount: "1", KnownAt: closeAt(1)})
	c := config(src, p, free)
	c.Account, c.InitialCash = account.Margin, "3000"
	r, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Days[2].Positions[0].Shares != "-60" || r.Days[2].Equity != "2970" || r.Days[3].Cash != "-3030" || r.Days[3].Equity != "-3030" {
		t.Fatalf("%+v", r)
	}
	// Losses are reported at the close, before a later liquidation.
	src = newSource(t, [][2]string{{"100", "100"}, {"100", "100"}, {"100", "1000"}})
	c.Source = src
	c.Strategy = planned{"2026-01-05": {{Asset: asset, Weight: "-1"}}, "2026-01-06": {{Asset: asset, Weight: "-1"}}}
	r, err = Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Days[2].Account.MaintenanceShortfall != "33000" {
		t.Fatal(r.Days[2])
	}
}

func TestInvalidAccountAndSettlementCalendar(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Account = "unknown" },
		func(c *Config) { c.SettlementDate = func(d time.Time) time.Time { return d } },
	} {
		c := config(newSource(t, prices), allIn{}, free)
		mutate(&c)
		if _, err := Run(context.Background(), c); !errors.Is(err, ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
}

func TestUnleveredFullTargetUsesSettledCashIncludingCostsForBothAccounts(t *testing.T) {
	src := newSource(t, prices)
	var previous Result
	for _, kind := range []account.Type{account.Cash, account.Margin} {
		cfg := config(src, allIn{}, DefaultCost)
		cfg.InitialCash = "2000"
		cfg.Account = kind
		cfg.Unlevered = true
		cfg.Deposits = &DepositSchedule{Amount: "500", EveryWeeks: 2}
		r, e := Run(context.Background(), cfg)
		if e != nil || len(r.Fills) == 0 || len(r.Refusals) != 0 {
			t.Fatal(r, e)
		}
		for i, d := range r.Days {
			cash, ok := new(big.Rat).SetString(d.Cash)
			if !ok || cash.Sign() < 0 {
				t.Fatal("unlevered cash borrowed", d)
			}
			if kind == account.Margin && previous.Days[i].TWR != d.TWR {
				t.Fatal("account type changed unlevered return", d)
			}
		}
		previous = r
	}
}

func TestUnpaidShortDividendCannotFundNewMarginExposure(t *testing.T) {
	s := newSource(t, [][2]string{{"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}}, marketdata.CorporateAction{SourceID: "short-dividend", Kind: "cash_dividend", ExDate: "2026-01-07", PayDate: "2026-01-13", Amount: "1", KnownAt: closeAt(1)})
	p := planned{"2026-01-05": {{Asset: asset, Weight: "-0.95"}}, "2026-01-06": {{Asset: asset, Weight: "-1"}}}
	cfg := config(s, p, free)
	cfg.Account = account.Margin
	cfg.InitialCash = "2000"
	r, e := Run(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Refusals) == 0 || r.Refusals[0].Code != account.BelowMinimum || r.Days[2].Receivable != "-19" || r.Days[2].Account.OvernightBuyingPower != "1981" {
		t.Fatal("unpaid dividend liability granted margin", r)
	}
}
