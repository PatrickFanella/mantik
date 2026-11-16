package mlegpaper

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
)

func admissionFixture(t *testing.T, kind string, funded bool) alpaca.MLegOpen {
	t.Helper()
	at := time.Date(2026, 11, 16, 22, 0, 0, 0, time.UTC)
	spot := options.Spot{AssetID: "asset-tst", Underlying: "TST", Date: "2026-11-16", Price: 1, At: at.Add(-time.Hour), ObservedAt: at, AvailableAt: at}
	c := options.Chain{AssetID: spot.AssetID, Underlying: "TST", Feed: "indicative", Model: options.ModelVersion, AsOf: at, Spot: spot, Assumptions: options.Assumptions{MaxAge: 24 * time.Hour}}
	for _, x := range []struct {
		symbol, right string
		strike        float64
	}{
		{"TST261218P00000900", "put", .9}, {"TST261218P00000950", "put", .95}, {"TST261218P00001000", "put", 1},
		{"TST261218C00001000", "call", 1}, {"TST261218C00001050", "call", 1.05}, {"TST261218C00001100", "call", 1.1},
	} {
		delta := .01
		if x.right == "put" {
			delta = -delta
		}
		c.Entries = append(c.Entries, options.Entry{Symbol: x.symbol, ContractID: x.symbol, Type: x.right, Expiry: "2026-12-18", Strike: x.strike, Close: .01, ReferenceObservedAt: at, ReferenceAvailableAt: at, PriceOccurredAt: spot.At, PriceObservedAt: at, PriceAvailableAt: at, FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: .3, Greeks: options.Greeks{Price: .01, Delta: delta, Gamma: .001, Vega: .001, Theta: -.001}})
	}
	slices.SortFunc(c.Entries, func(a, b options.Entry) int { return strings.Compare(a.Symbol, b.Symbol) })
	indexes := []int{0, 1, 4, 5}
	signs := []int{1, -1, -1, 1}
	switch kind {
	case "vertical":
		indexes = []int{4, 5}
		signs = []int{-1, 1}
	case "iron_butterfly":
		indexes = []int{0, 2, 3, 5}
	case "long_options":
		indexes = []int{0, 5}
		signs = []int{1, 1}
	}
	o := alpaca.MLegOpen{ExecutionPolicy: alpaca.PaperAtomicNetV2, Chain: c, Structure: options.Structure{Kind: kind}, Order: alpaca.MLegOrder{ClientOrderID: "open-admission", Qty: "1", Type: "limit", TimeInForce: "day", OrderClass: "mleg", LimitPrice: "0.02"}}
	// indexes reference the original symbol construction list, not sorted entries.
	symbols := []string{"TST261218P00000900", "TST261218P00000950", "TST261218P00001000", "TST261218C00001000", "TST261218C00001050", "TST261218C00001100"}
	for i, index := range indexes {
		side := "buy"
		if signs[i] < 0 {
			side = "sell"
		}
		o.Structure.Legs = append(o.Structure.Legs, options.Leg{Symbol: symbols[index], Contracts: signs[i], Multiplier: 100, Limit: .01})
		o.Order.Legs = append(o.Order.Legs, alpaca.MLeg{Symbol: symbols[index], RatioQty: "1", Side: side, PositionIntent: side + "_to_open"})
	}
	opened := at.Add(-time.Hour)
	if funded {
		opened = at.AddDate(0, 0, -14)
	}
	var err error
	if kind != "long_options" {
		o.Order.LimitPrice = "-0.01"
	}
	o.Funding, err = account.NewCappedState(account.DefaultCappedConfig("dedicated", "account-1", account.Margin, opened))
	if err != nil {
		t.Fatal(err)
	}
	if funded {
		deposits, err := o.Funding.Config.DueDeposits(at)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range deposits {
			o.Funding, err = account.ApplyCappedCommand(o.Funding, d)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return o
}
func TestLocalAdmissionSupportedStructuresAndFixedFunding(t *testing.T) {
	for _, kind := range []string{"long_options", "vertical", "iron_condor", "iron_butterfly"} {
		t.Run(kind, func(t *testing.T) {
			o := admissionFixture(t, kind, true)
			if err := admit(context.Background(), o, o.Chain.AsOf); err != nil {
				t.Fatal(err)
			}
		})
	}
	o := admissionFixture(t, "iron_condor", false)
	if err := admit(context.Background(), o, o.Chain.AsOf); err == nil || !strings.Contains(err.Error(), "option_cash_structure_not_allowed") {
		t.Fatal("initial $2000 must retain margin fallback", err)
	}
}
func TestLocalAdmissionCannotUseBrokerBalanceOrOtherCollateral(t *testing.T) {
	for name, change := range map[string]func(*alpaca.MLegOpen){
		"inflated opening cash": func(o *alpaca.MLegOpen) { o.Funding.Config.OpeningCash = "100000000" },
		"changed deposit":       func(o *alpaca.MLegOpen) { o.Funding.Config.DepositAmount = "100000" },
		"wrong account":         func(o *alpaca.MLegOpen) { o.Funding.Config.BrokerAccountID = "account-2" },
		"other reservation": func(o *alpaca.MLegOpen) {
			o.Funding.Events = append(o.Funding.Events, account.BookCommand{Kind: "reserve"})
		},
		"missing greeks": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				o.Chain.Entries[i].EstimateStatus = "missing_same_session_close"
				o.Chain.Entries[i].IV = 0
				o.Chain.Entries[i].Greeks = options.Greeks{}
			}
		},
		"delta": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				if o.Chain.Entries[i].Type == "call" {
					o.Chain.Entries[i].Greeks.Delta = 1
				} else {
					o.Chain.Entries[i].Greeks.Delta = -1
				}
			}
			o.Chain.Spot.Price = 10
		},
		"cash account": func(o *alpaca.MLegOpen) { o.Funding.Config.Type = account.Cash },
		"future":       func(o *alpaca.MLegOpen) { o.Chain.AsOf = o.Chain.AsOf.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			o := admissionFixture(t, "iron_condor", true)
			at := o.Chain.AsOf
			change(&o)
			// Account binding is checked by the adapter before invoking admission.
			if name == "wrong account" {
				if _, err := o.LocalFunding("account-1"); err == nil {
					t.Fatal("account mismatch accepted")
				}
				return
			}
			if err := admit(context.Background(), o, at); err == nil {
				t.Fatal("invalid funding/risk accepted")
			}
		})
	}
}
