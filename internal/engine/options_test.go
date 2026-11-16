package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// These generated prices are synthetic, not real indicative qualification.
type optionFixtureSource struct {
	change     func(*options.Chain)
	changeBars func(map[string]options.IndicativeBar)
	fail       bool
}

func (s *optionFixtureSource) OptionChain(_ context.Context, a string, at time.Time) (options.Chain, error) {
	if s.fail {
		return options.Chain{}, errors.New("source unavailable")
	}
	day := optionCivil(at)
	close := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, newYork)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, newYork)
	spot := options.Spot{AssetID: a, Underlying: "SPY", Price: 100, Date: day.Format(time.DateOnly), At: close, ObservedAt: at, AvailableAt: at}
	c := options.Chain{AssetID: a, Underlying: "SPY", Feed: "indicative", Model: options.ModelVersion, AsOf: at, Spot: spot, Assumptions: options.Assumptions{MaxAge: 4 * 24 * time.Hour}}
	for _, right := range []string{"call", "put"} {
		for _, strike := range []int{90, 95, 100, 105, 110} {
			char := "C"
			if right == "put" {
				char = "P"
			}
			symbol := fmt.Sprintf("SPY261218%s%08d", char, strike*1000)
			g, err := options.Price(options.Inputs{Type: right, Spot: 100, Strike: float64(strike), Years: .5, Volatility: .3})
			if err != nil {
				return c, err
			}
			c.Entries = append(c.Entries, options.Entry{Symbol: symbol, ContractID: symbol, Type: right, Expiry: "2026-12-18", Strike: float64(strike), Close: g.Price, ReferenceObservedAt: at, ReferenceAvailableAt: at, PriceOccurredAt: start, PriceObservedAt: at, PriceAvailableAt: at, FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: .3, Greeks: g})
		}
	}
	slices.SortFunc(c.Entries, func(a, b options.Entry) int {
		if a.Symbol < b.Symbol {
			return -1
		}
		return 1
	})
	if s.change != nil {
		s.change(&c)
	}
	return c, nil
}
func (s *optionFixtureSource) OptionBars(ctx context.Context, a string, at time.Time) (map[string]options.IndicativeBar, error) {
	c, err := s.OptionChain(ctx, a, at)
	if err != nil {
		return nil, err
	}
	out := map[string]options.IndicativeBar{}
	for _, e := range c.Entries {
		out[e.Symbol] = options.IndicativeBar{Symbol: e.Symbol, Date: c.Spot.Date, Open: e.Close, High: e.Close * 1.02, Low: e.Close * .98, Close: e.Close, OccurredAt: e.PriceOccurredAt, ObservedAt: e.PriceObservedAt, AvailableAt: e.PriceAvailableAt}
	}
	if s.changeBars != nil {
		s.changeBars(out)
	}
	return out, nil
}

var optionStart = time.Date(2026, 1, 6, 22, 0, 0, 0, time.UTC)

func optionTestLeg(symbol string, q int) options.Leg {
	limit := 100.0
	if q < 0 {
		limit = .01
	}
	return options.Leg{Symbol: symbol, Contracts: q, Multiplier: 100, Limit: limit}
}
func optionLong() options.Structure {
	return options.Structure{Kind: "long_options", Legs: []options.Leg{optionTestLeg("SPY261218C00100000", 1)}}
}
func optionVertical() options.Structure {
	return options.Structure{Kind: "vertical", Legs: []options.Leg{optionTestLeg("SPY261218C00100000", -1), optionTestLeg("SPY261218C00105000", 1)}}
}
func newOptionFixture(t *testing.T, kind account.Type, cash string, stocks map[string]int) (*OptionBook, *optionFixtureSource) {
	t.Helper()
	s := &optionFixtureSource{}
	b, err := NewOptionBook(context.Background(), OptionConfig{Source: s, Account: kind, StrategyID: "strategy-options", SettledCash: cash, Stocks: stocks, FeePerContract: .65, SlippageBps: 5}, optionStart)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}
func admitOption(t *testing.T, b *OptionBook, id string, s options.Structure) {
	t.Helper()
	r, err := b.Submit(context.Background(), optionStart, id, "asset-spy", s)
	if err != nil || len(r.Reasons) > 0 {
		t.Fatal(r, err)
	}
}

func TestOptionEngineProtectedFillsFeesSettlementAndClose(t *testing.T) {
	ctx := context.Background()
	b, _ := newOptionFixture(t, account.Margin, "1000000", nil)
	admitOption(t, b, "v", optionVertical())
	before, err := b.Snapshot(ctx, optionStart)
	if err != nil || len(before.Pending) != 1 || before.Reserved == "0" {
		t.Fatal(before, err)
	}
	same, err := b.Step(ctx, optionStart, "v")
	if err != nil || len(same.Fills) != 0 || len(same.Reasons) == 0 {
		t.Fatal("same bar", same, err)
	}
	first, err := b.Step(ctx, optionStart.Add(24*time.Hour), "v")
	if err != nil || len(first.Reasons) > 0 || len(first.Fills) != 1 || first.Fills[0].Contracts != 1 || first.Fills[0].Leg != 1 || first.Fills[0].Fee != "0.65" {
		t.Fatal(first, err)
	}
	second, err := b.Step(ctx, optionStart.Add(48*time.Hour), "v")
	if err != nil || len(second.Reasons) > 0 || len(second.Fills) != 1 || second.Fills[0].Contracts != -1 {
		t.Fatal(second, err)
	}
	marked, err := b.Snapshot(ctx, optionStart.Add(48*time.Hour))
	if err != nil || len(marked.Positions) != 2 || len(marked.Pending) != 0 || marked.Cash == marked.SettledCash {
		t.Fatal(marked, err)
	}
	// Each debit/credit is its own fee-bearing fill; gross premium and signed
	// contracts reproduce cash without trusting a result summary.
	expected := optionRat(1000000)
	for _, f := range append(first.Fills, second.Fills...) {
		expected.Sub(expected, optionDebit(f))
		price, _ := strconv.ParseFloat(f.Price, 64)
		if f.Contracts > 0 && price <= f.Bar.High || f.Contracts < 0 && price >= f.Bar.Low {
			t.Fatal("favorable price", f)
		}
	}
	if marked.Cash != render(expected) {
		t.Fatal(marked.Cash, render(expected))
	}
	if err := b.Control(risk.Control{Action: "kill"}); err != nil {
		t.Fatal(err)
	}
	close, err := b.Close(ctx, optionStart.Add(72*time.Hour), "v")
	if err != nil || len(close.Reasons) > 0 || len(close.Fills) != 2 || close.Fills[0].Contracts != 1 || !close.Fills[0].Closing {
		t.Fatal(close, err)
	}
	end, err := b.Snapshot(ctx, optionStart.Add(144*time.Hour))
	if err != nil || len(end.Positions) != 0 || end.Reserved != "0" || end.Cash != end.SettledCash {
		t.Fatal(end, err)
	}
	again, err := b.Close(ctx, optionStart.Add(144*time.Hour), "v")
	if err != nil || !slices.Contains(again.Reasons, "option_position_unavailable") {
		t.Fatal(again, err)
	}
}

func TestOptionEngineCashStructuresAndCollateralCannotBeReused(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]options.Structure{"long": optionLong(), "put": {Kind: "cash_secured_put", Legs: []options.Leg{optionTestLeg("SPY261218P00095000", -1)}}, "call": {Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00105000", -1)}}} {
		t.Run(name, func(t *testing.T) {
			b, _ := newOptionFixture(t, account.Cash, "1000000", map[string]int{"asset-spy": 100})
			admitOption(t, b, "first", s)
			if name == "call" {
				r, err := b.Submit(ctx, optionStart, "second", "asset-spy", s)
				if err != nil || !slices.Contains(r.Reasons, "option_uncovered_call") {
					t.Fatal(r, err)
				}
			}
			if _, err := b.Step(ctx, optionStart.Add(24*time.Hour), "first"); err != nil {
				t.Fatal(err)
			}
		})
	}
	b, _ := newOptionFixture(t, account.Cash, "1000000", nil)
	r, err := b.Submit(ctx, optionStart, "spread", "asset-spy", optionVertical())
	if err != nil || !slices.Contains(r.Reasons, "option_cash_structure_not_allowed") {
		t.Fatal(r, err)
	}
	r, err = b.Submit(ctx, optionStart, "naked", "asset-spy", options.Structure{Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00105000", -1)}})
	if err != nil || !slices.Contains(r.Reasons, "option_uncovered_call") {
		t.Fatal(r, err)
	}
	low, _ := newOptionFixture(t, account.Margin, "1999", nil)
	r, err = low.Submit(ctx, optionStart, "spread", "asset-spy", optionVertical())
	if err != nil || !slices.Contains(r.Reasons, "option_cash_structure_not_allowed") {
		t.Fatal(r, err)
	}
}

func TestOptionEnginePendingAndPositionsConsumeGreekCapacity(t *testing.T) {
	ctx := context.Background()
	b, s := newOptionFixture(t, account.Margin, "1000000", nil)
	// Vega just under 10% per underlying for one leg, but over for two orders.
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			c.Entries[i].Greeks.Vega = 60000
		}
	}
	admitOption(t, b, "one", optionLong())
	r, err := b.Submit(ctx, optionStart, "two", "asset-spy", optionLong())
	if err != nil || !slices.Contains(r.Reasons, "risk_option_vega") {
		t.Fatal(r, err)
	}
	snap, err := b.Snapshot(ctx, optionStart)
	if err != nil || len(snap.Pending) != 1 {
		t.Fatal(snap, err)
	}
	fill, err := b.Step(ctx, optionStart.Add(24*time.Hour), "one")
	if err != nil || len(fill.Fills) != 1 {
		t.Fatal(fill, err)
	}
	r, err = b.Submit(ctx, optionStart.Add(24*time.Hour), "three", "asset-spy", optionLong())
	if err != nil || !slices.Contains(r.Reasons, "risk_option_vega") {
		t.Fatal(r, err)
	}
	// Cancelling an executed lot must not release collateral or exposures.
	if err := b.Cancel("one"); err != nil {
		t.Fatal(err)
	}
	r, err = b.Submit(ctx, optionStart.Add(24*time.Hour), "four", "asset-spy", optionLong())
	if err != nil || !slices.Contains(r.Reasons, "risk_option_vega") {
		t.Fatal(r, err)
	}
}

func TestOptionEngineRechecksGreekBrakesAndEvidenceBeforeFill(t *testing.T) {
	for name, change := range map[string]func(*OptionBook, *optionFixtureSource){
		"delta": func(_ *OptionBook, s *optionFixtureSource) { s.change = func(c *options.Chain) { c.Spot.Price = 5000 } },
		"gamma": func(_ *OptionBook, s *optionFixtureSource) {
			s.change = func(c *options.Chain) {
				for i := range c.Entries {
					c.Entries[i].Greeks.Gamma = 1
				}
			}
		},
		"vega": func(_ *OptionBook, s *optionFixtureSource) {
			s.change = func(c *options.Chain) {
				for i := range c.Entries {
					c.Entries[i].Greeks.Vega = 200000
				}
			}
		},
		"theta": func(_ *OptionBook, s *optionFixtureSource) {
			s.change = func(c *options.Chain) {
				for i := range c.Entries {
					c.Entries[i].Greeks.Theta = -1000
				}
			}
		},
		"kill_switch": func(b *OptionBook, _ *optionFixtureSource) {
			if err := b.Control(risk.Control{Action: "kill"}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, s := newOptionFixture(t, account.Margin, "1000000", nil)
			admitOption(t, b, "o", optionLong())
			change(b, s)
			r, err := b.Step(context.Background(), optionStart.Add(24*time.Hour), "o")
			if err != nil || len(r.Fills) > 0 || !slices.Contains(r.Reasons, "risk_option_"+name) && name != "kill_switch" || name == "kill_switch" && !slices.Contains(r.Reasons, "risk_kill_switch") {
				t.Fatal(r, err)
			}
			snap, err := b.Snapshot(context.Background(), optionStart.Add(24*time.Hour))
			if err != nil || snap.Cash != "1000000" || len(snap.Pending) != 1 {
				t.Fatal(snap, err)
			}
		})
	}
}

func TestOptionEngineLimitsMissingBarsAndCancellation(t *testing.T) {
	ctx := context.Background()
	b, s := newOptionFixture(t, account.Margin, "1000000", nil)
	order := optionLong()
	order.Legs[0].Limit = 1
	admitOption(t, b, "gap", order)
	r, err := b.Step(ctx, optionStart.Add(24*time.Hour), "gap")
	if err != nil || !slices.Contains(r.Reasons, "option_limit_not_reached") {
		t.Fatal(r, err)
	}
	if err := b.Cancel("gap"); err != nil {
		t.Fatal(err)
	}
	snap, err := b.Snapshot(ctx, optionStart.Add(24*time.Hour))
	if err != nil || snap.Reserved != "0" {
		t.Fatal(snap, err)
	}
	r, err = b.Submit(ctx, optionStart.Add(24*time.Hour), "gap", "asset-spy", optionLong())
	if err != nil || !slices.Contains(r.Reasons, "option_duplicate_order") {
		t.Fatal(r, err)
	}
	r, err = b.Submit(ctx, optionStart.Add(24*time.Hour), "bar", "asset-spy", optionLong())
	if err != nil || len(r.Reasons) > 0 {
		t.Fatal(r, err)
	}
	s.changeBars = func(bars map[string]options.IndicativeBar) { delete(bars, "SPY261218C00100000") }
	r, err = b.Step(ctx, optionStart.Add(48*time.Hour), "bar")
	if err != nil || len(r.Fills) > 0 || len(r.Reasons) == 0 {
		t.Fatal(r, err)
	}
	s.changeBars = nil
	s.fail = true
	if _, err := b.Step(ctx, optionStart.Add(48*time.Hour), "bar"); err == nil {
		t.Fatal("unknown source accepted")
	}
	s.fail = false
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			c.Entries[i].Greeks.Gamma = math.NaN()
		}
	}
	if _, err := b.Step(ctx, optionStart.Add(48*time.Hour), "bar"); err == nil {
		t.Fatal("unknown greek accepted")
	}
}

func TestOptionEngineFourLegProtectionAndPartialCancel(t *testing.T) {
	ctx := context.Background()
	b, _ := newOptionFixture(t, account.Margin, "1000000", nil)
	s := options.Structure{Kind: "iron_condor", Legs: []options.Leg{optionTestLeg("SPY261218P00090000", 1), optionTestLeg("SPY261218P00095000", -1), optionTestLeg("SPY261218C00105000", -1), optionTestLeg("SPY261218C00110000", 1)}}
	admitOption(t, b, "condor", s)
	for day, want := range []int{1, 1, -1, -1} {
		r, err := b.Step(ctx, optionStart.Add(time.Duration(day+1)*24*time.Hour), "condor")
		if err != nil || len(r.Reasons) > 0 || len(r.Fills) != 1 || r.Fills[0].Contracts != want {
			t.Fatal(day, r, err)
		}
	}
	snap, err := b.Snapshot(ctx, optionStart.Add(4*24*time.Hour))
	if err != nil || len(snap.Positions) != 4 || snap.EvidenceStatus != "indicative_unqualified" {
		t.Fatal(snap, err)
	}
	b, _ = newOptionFixture(t, account.Margin, "1000000", nil)
	admitOption(t, b, "partial", optionVertical())
	r, err := b.Step(ctx, optionStart.Add(24*time.Hour), "partial")
	if err != nil || len(r.Fills) != 1 {
		t.Fatal(r, err)
	}
	before, err := b.Snapshot(ctx, optionStart.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel("partial"); err != nil {
		t.Fatal(err)
	}
	after, err := b.Snapshot(ctx, optionStart.Add(24*time.Hour))
	if err != nil || after.Reserved != before.Reserved || len(after.Positions) != 1 || len(after.Pending) != 0 {
		t.Fatal(before, after, err)
	}
	r, err = b.Step(ctx, optionStart.Add(48*time.Hour), "partial")
	if err != nil || !slices.Contains(r.Reasons, "option_order_not_pending") {
		t.Fatal(r, err)
	}
	r, err = b.Close(ctx, optionStart.Add(48*time.Hour), "partial")
	if err != nil || len(r.Fills) != 1 || r.Fills[0].Contracts != -1 {
		t.Fatal(r, err)
	}
}

func TestOptionEngineDoesNotNetOpposingIndependentOrders(t *testing.T) {
	ctx := context.Background()
	b, s := newOptionFixture(t, account.Margin, "1000000", nil)
	// Set gamma so either standalone long or covered call fits, but both breach.
	// Opposing signed contracts would net to zero; gross risk must refuse.
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			c.Entries[i].Greeks.Gamma = .08
		}
	}
	admitOption(t, b, "long", optionLong())
	r, err := b.Submit(ctx, optionStart, "covered", "asset-spy", options.Structure{Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00100000", -1)}})
	if err != nil || !slices.Contains(r.Reasons, "option_uncovered_call") {
		t.Fatal(r, err)
	}
	b, s = newOptionFixture(t, account.Margin, "1000000", map[string]int{"asset-spy": 100})
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			c.Entries[i].Greeks.Gamma = .08
		}
	}
	admitOption(t, b, "long", optionLong())
	r, err = b.Submit(ctx, optionStart, "covered", "asset-spy", options.Structure{Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00100000", -1)}})
	if err != nil || !slices.Contains(r.Reasons, "risk_option_gamma") {
		t.Fatal(r, err)
	}
}

// This adapter exercises the existing retained-data contracts. The records are
// synthetic and live only in Memory; no database or provider is contacted.
type retainedOptions struct{ store *storage.Memory }

func (s retainedOptions) OptionChain(ctx context.Context, a string, at time.Time) (options.Chain, error) {
	day := optionCivil(at)
	spot := options.Spot{AssetID: a, Underlying: "SPY", Date: day.Format(time.DateOnly), Price: 100, At: time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, newYork), ObservedAt: at, AvailableAt: at}
	return options.Read(ctx, s.store, spot, at, options.Assumptions{MaxAge: 4 * 24 * time.Hour})
}
func (s retainedOptions) OptionBars(ctx context.Context, _ string, at time.Time) (map[string]options.IndicativeBar, error) {
	return options.ReadBars(ctx, s.store, "SPY", at)
}

func TestOptionEngineConsumesRetainedIndicativeObservations(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	symbol := "SPY261218C00100000"
	records := []alpacarecord.OptionRecord{{Kind: "contract", Symbol: symbol, Underlying: "SPY", Key: "contract-spy", Feed: "reference", OccurredAt: optionStart, ObservedAt: optionStart, AvailableAt: optionStart, Data: json.RawMessage(`{"id":"contract-spy","symbol":"SPY261218C00100000","underlying_symbol":"SPY","expiration_date":"2026-12-18","strike_price":"100","type":"call","size":"100"}`)}}
	for _, day := range []int{0, 1, 2} {
		at := optionStart.Add(time.Duration(day) * 24 * time.Hour)
		civil := optionCivil(at)
		records = append(records, alpacarecord.OptionRecord{Kind: "bar", Symbol: symbol, Underlying: "SPY", Key: civil.Format(time.DateOnly), Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: time.Date(civil.Year(), civil.Month(), civil.Day(), 0, 0, 0, 0, newYork), ObservedAt: at, AvailableAt: at, Data: json.RawMessage(`{"o":8,"h":9,"l":7,"c":8}`)})
	}
	if _, err := store.RecordOptionData(ctx, records); err != nil {
		t.Fatal(err)
	}
	b, err := NewOptionBook(ctx, OptionConfig{Source: retainedOptions{store}, Account: account.Margin, StrategyID: "retained-indicative", SettledCash: "1000000", FeePerContract: .65, SlippageBps: 5}, optionStart)
	if err != nil {
		t.Fatal(err)
	}
	order := optionLong()
	order.Legs[0].Limit = 10
	admitOption(t, b, "retained", order)
	r, err := b.Step(ctx, optionStart.Add(24*time.Hour), "retained")
	if err != nil || len(r.Reasons) > 0 || len(r.Fills) != 1 || r.Fills[0].Price != "9.0045" {
		t.Fatal(r, err)
	}
	// Correction acquired tomorrow must not change yesterday's retained fill.
	correction := records[2]
	correction.ObservedAt = optionStart.Add(48 * time.Hour)
	correction.AvailableAt = correction.ObservedAt
	correction.Data = json.RawMessage(`{"o":8,"h":15,"l":7,"c":8}`)
	if _, err := store.RecordOptionData(ctx, []alpacarecord.OptionRecord{correction}); err != nil {
		t.Fatal(err)
	}
	snap, err := b.Snapshot(ctx, optionStart.Add(24*time.Hour))
	if err != nil || snap.Fills[0].Bar.High != 9 {
		t.Fatal(snap, err)
	}
	close, err := b.Close(ctx, optionStart.Add(48*time.Hour), "retained")
	if err != nil || len(close.Fills) != 1 || close.Fills[0].Price != "6.9965" {
		t.Fatal(close, err)
	}
}

func TestOptionEngineCloseCannotSpendAnotherOrdersCollateral(t *testing.T) {
	ctx := context.Background()
	b, s := newOptionFixture(t, account.Margin, "1000000", map[string]int{"asset-spy": 100})
	covered := options.Structure{Kind: "covered_call", Legs: []options.Leg{optionTestLeg("SPY261218C00105000", -1)}}
	admitOption(t, b, "open", covered)
	fill, err := b.Step(ctx, optionStart.Add(24*time.Hour), "open")
	if err != nil || len(fill.Fills) != 1 {
		t.Fatal(fill, err)
	}
	r, err := b.Submit(ctx, optionStart.Add(24*time.Hour), "pending", "asset-spy", optionLong())
	if err != nil || len(r.Reasons) > 0 {
		t.Fatal(r, err)
	}
	// An extreme adverse close fits cash alone, but would consume the other
	// pending order's reserve. The whole close must refuse before any mutation.
	s.changeBars = func(bars map[string]options.IndicativeBar) {
		v := bars["SPY261218C00105000"]
		v.High = 9900
		bars[v.Symbol] = v
	}
	before := render(b.cash)
	r, err = b.Close(ctx, optionStart.Add(48*time.Hour), "open")
	if err != nil || !slices.Contains(r.Reasons, "option_close_unfunded") || render(b.cash) != before {
		t.Fatal(r, err)
	}
}

func TestOptionFillRoundingNeverImprovesPriceOrErasesFee(t *testing.T) {
	for _, v := range []float64{.00000000000001, 1.0000000000001, 8.273923577771976} {
		if optionRounded(v, true).Cmp(optionRat(v)) < 0 || optionRounded(v, false).Cmp(optionRat(v)) > 0 || optionRounded(v, true).Sign() <= 0 {
			t.Fatal("nonconservative rounding", v)
		}
	}
}

func TestOptionEngineMalformedHugePriceFailsWithoutPanicOrReservation(t *testing.T) {
	b, s := newOptionFixture(t, account.Margin, "1000000", nil)
	s.change = func(c *options.Chain) {
		for i := range c.Entries {
			c.Entries[i].Close = 1e308
		}
	}
	order := options.Structure{Kind: "cash_secured_put", Legs: []options.Leg{optionTestLeg("SPY261218P00095000", -1)}}
	r, err := b.Submit(context.Background(), optionStart, "overflow", "asset-spy", order)
	if err == nil || len(r.Fills) > 0 || len(b.lots) != 0 || render(b.cash) != "1000000" {
		t.Fatal(r, err)
	}
}
