package engine

import (
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
)

// All data are synthetic; the existing fixture supplies valid bar clocks.
func TestOptionCloseIgnoresCancelledUnusedEstimate(t *testing.T) {
	ctx := context.Background()
	b, s := newOptionFixture(t, account.Margin, "1000000", nil)
	admitOption(t, b, "partial", optionVertical())
	r, err := b.Step(ctx, optionStart.Add(24*time.Hour), "partial")
	if err != nil || len(r.Fills) != 1 {
		t.Fatal(r, err)
	}
	if err := b.Cancel("partial"); err != nil {
		t.Fatal(err)
	}
	// The cancelled, unfilled short loses its estimate; held long retains valid
	// chain data and executable OHLC. This is a valid unavailable chain entry.
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			if c.Entries[i].Symbol == "SPY261218C00100000" {
				c.Entries[i].EstimateStatus = "unsolvable_indicative_price"
				c.Entries[i].IV = 0
				c.Entries[i].Greeks = options.Greeks{}
			}
		}
	}
	at := optionStart.Add(48 * time.Hour)
	c, err := s.OptionChain(ctx, "asset-spy", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(at, "asset-spy"); err != nil {
		t.Fatalf("reproducer supplied invalid chain: %v", err)
	}
	bars, err := s.OptionBars(ctx, "asset-spy", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := bars["SPY261218C00105000"].Validate(at); err != nil {
		t.Fatal(err)
	}
	snapshot, err := b.Snapshot(ctx, at)
	if err != nil || len(snapshot.Positions) != 1 {
		t.Fatalf("cancelled unused leg blocked held-position snapshot: %+v %v", snapshot, err)
	}
	r, err = b.Close(ctx, at, "partial")
	if err != nil || len(r.Fills) != 1 {
		t.Fatalf("cannot close valid held long due to cancelled unfilled short: result=%+v error=%v", r, err)
	}
}

func TestOptionExitRequiresHeldPricesButNotUnusedContracts(t *testing.T) {
	for _, mode := range []string{"unused_absent", "held_greeks_missing", "held_price_missing"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			b, s := newOptionFixture(t, account.Margin, "1000000", nil)
			admitOption(t, b, "partial", optionVertical())
			if r, err := b.Step(ctx, optionStart.Add(24*time.Hour), "partial"); err != nil || len(r.Fills) != 1 {
				t.Fatal(r, err)
			}
			if err := b.Cancel("partial"); err != nil {
				t.Fatal(err)
			}
			s.change = func(c *options.Chain) {
				if mode == "unused_absent" {
					c.Entries = slices.DeleteFunc(c.Entries, func(e options.Entry) bool { return e.Symbol == "SPY261218C00100000" })
					return
				}
				for i := range c.Entries {
					if c.Entries[i].Symbol != "SPY261218C00105000" {
						continue
					}
					e := &c.Entries[i]
					e.EstimateStatus, e.IV, e.Greeks = "unsolvable_indicative_price", 0, options.Greeks{}
					if mode == "held_price_missing" {
						e.Close = 0
					}
				}
			}
			at := optionStart.Add(48 * time.Hour)
			snapshot, err := b.Snapshot(ctx, at)
			if mode == "held_price_missing" {
				if err == nil || err.Error() != "option_held_price_unavailable" {
					t.Fatalf("unknown held liability became a mark: %+v %v", snapshot, err)
				}
				if r, err := b.Close(ctx, at, "partial"); err == nil || len(r.Fills) != 0 {
					t.Fatalf("closed an unpriced holding: %+v %v", r, err)
				}
				return
			}
			if err != nil || len(snapshot.Positions) != 1 {
				t.Fatal(snapshot, err)
			}
			if r, err := b.Close(ctx, at, "partial"); err != nil || len(r.Reasons) != 0 || len(r.Fills) != 1 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestOptionCoveredStockAppreciationKeepsLossCollateral(t *testing.T) {
	ctx := context.Background()
	b, source := newOptionFixture(t, account.Margin, "1000000", map[string]int{"asset-spy": 100})
	admitOption(t, b, "covered", options.Structure{Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00105000", -1)}})
	before, err := b.Snapshot(ctx, optionStart)
	if err != nil {
		t.Fatal(err)
	}
	source.change = func(c *options.Chain) { c.Spot.Price += 100 }
	after, err := b.Snapshot(ctx, optionStart.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	delta := new(big.Rat).Sub(optionRatString(t, after.Reserved), optionRatString(t, before.Reserved))
	if delta.Cmp(big.NewRat(20000, 1)) != 0 {
		t.Fatal("stock appreciation omitted complete/partial collateral", before.Reserved, after.Reserved)
	}
}

func optionRatString(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatal(s)
	}
	return r
}

func TestOptionStructuresCompleteProtectedLifecycle(t *testing.T) {
	shapes := map[string]options.Structure{
		"long":             optionLong(),
		"covered_call":     {Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00105000", -1)}},
		"cash_secured_put": {Kind: "cash_secured_put", Legs: []options.Leg{optionTestLeg("SPY261218P00095000", -1)}},
		"vertical":         optionVertical(),
		"condor":           {Kind: "iron_condor", Legs: []options.Leg{optionTestLeg("SPY261218P00090000", 1), optionTestLeg("SPY261218P00095000", -1), optionTestLeg("SPY261218C00105000", -1), optionTestLeg("SPY261218C00110000", 1)}},
		"butterfly":        {Kind: "iron_butterfly", Legs: []options.Leg{optionTestLeg("SPY261218P00095000", 1), optionTestLeg("SPY261218P00100000", -1), optionTestLeg("SPY261218C00100000", -1), optionTestLeg("SPY261218C00105000", 1)}},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			stocks := map[string]int{}
			if name == "covered_call" {
				stocks["asset-spy"] = 100
			}
			b, _ := newOptionFixture(t, account.Margin, "1000000", stocks)
			admitOption(t, b, name, shape)
			for i := range shape.Legs {
				r, err := b.Step(context.Background(), optionStart.Add(time.Duration(i+1)*24*time.Hour), name)
				if err != nil || len(r.Reasons) != 0 || len(r.Fills) != 1 {
					t.Fatalf("step %d: %+v %v", i, r, err)
				}
			}
			r, err := b.Close(context.Background(), optionStart.Add(time.Duration(len(shape.Legs)+1)*24*time.Hour), name)
			if err != nil || len(r.Reasons) != 0 || len(r.Fills) != len(shape.Legs) {
				t.Fatalf("close: %+v %v", r, err)
			}
		})
	}
}

// Model-priced deep OTM puts can appreciate while their cash reserve and
// dollar delta remain small; this leaves a legitimate cash-cap boundary.
type optionCashBoundarySource struct{}

func (optionCashBoundarySource) OptionChain(ctx context.Context, a string, at time.Time) (options.Chain, error) {
	if a == "asset-spy" {
		return (&optionFixtureSource{}).OptionChain(ctx, a, at)
	}
	day := optionCivil(at)
	close := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, newYork)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, newYork)
	spot, vol := 1000000.0, 2.0
	if !at.Before(optionStart.Add(48 * time.Hour)) {
		spot, vol = 500, .3
	}
	g, err := options.Price(options.Inputs{Type: "put", Spot: spot, Strike: 10000, Years: .5, Volatility: vol})
	if err != nil {
		return options.Chain{}, err
	}
	symbol := a + "261218P10000000"
	return options.Chain{AssetID: a, Underlying: a, Feed: "indicative", Model: options.ModelVersion, AsOf: at,
		Spot:        options.Spot{AssetID: a, Underlying: a, Price: spot, Date: day.Format(time.DateOnly), At: close, ObservedAt: at, AvailableAt: at},
		Assumptions: options.Assumptions{MaxAge: 4 * 24 * time.Hour},
		Entries: []options.Entry{{Symbol: symbol, ContractID: symbol, Type: "put", Expiry: "2026-12-18", Strike: 10000, Close: g.Price,
			ReferenceObservedAt: at, ReferenceAvailableAt: at, PriceOccurredAt: start, PriceObservedAt: at, PriceAvailableAt: at,
			FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: vol, Greeks: g}}}, nil
}
func (s optionCashBoundarySource) OptionBars(ctx context.Context, a string, at time.Time) (map[string]options.IndicativeBar, error) {
	c, err := s.OptionChain(ctx, a, at)
	if err != nil {
		return nil, err
	}
	out := map[string]options.IndicativeBar{}
	for _, e := range c.Entries {
		out[e.Symbol] = options.IndicativeBar{Symbol: e.Symbol, Date: c.Spot.Date, Open: e.Close, High: e.Close * 1.02, Low: e.Close * .98, Close: e.Close, OccurredAt: e.PriceOccurredAt, ObservedAt: e.PriceObservedAt, AvailableAt: e.PriceAvailableAt}
	}
	return out, nil
}
func TestOptionOpeningReservesPendingCash(t *testing.T) {
	ctx := context.Background()
	b, err := NewOptionBook(ctx, OptionConfig{Source: optionCashBoundarySource{}, Account: account.Margin, StrategyID: "review", SettledCash: "1000000", FeePerContract: .65, SlippageBps: 5}, optionStart)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"AAA", "BBB", "CCC", "DDD"} {
		order := options.Structure{Kind: "long_options", Legs: []options.Leg{{Symbol: a + "261218P10000000", Contracts: 1, Multiplier: 100, Limit: 20}}}
		r, e := b.Submit(ctx, optionStart, a, a, order)
		if e != nil || len(r.Reasons) > 0 {
			t.Fatalf("admit %s: %+v %v", a, r, e)
		}
	}
	for _, a := range []string{"AAA", "BBB", "CCC", "DDD"} {
		r, e := b.Step(ctx, optionStart.Add(24*time.Hour), a)
		if e != nil || len(r.Fills) != 1 {
			t.Fatalf("fill %s: %+v %v", a, r, e)
		}
	}
	at := optionStart.Add(48 * time.Hour)
	order := optionLong()
	order.Legs[0].Contracts = 10
	order.Legs[0].Limit = 9
	r, err := b.Submit(ctx, at, "last", "asset-spy", order)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Reasons, "risk_cash_reserve") {
		t.Fatalf("pending debit escaped cash floor: %+v", r)
	}
	snap, err := b.Snapshot(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(snap.Pending, "last") {
		t.Fatal("refused order remained pending")
	}
	cash, _ := new(big.Rat).SetString(snap.SettledCash)
	hold, _ := new(big.Rat).SetString(snap.Reserved)
	eq, _ := new(big.Rat).SetString(snap.Equity)
	free := new(big.Rat).Sub(cash, hold)
	required := new(big.Rat).Quo(eq, big.NewRat(5, 1))
	if free.Cmp(required) < 0 {
		t.Fatal("existing book already violated cash floor", snap)
	}
}
