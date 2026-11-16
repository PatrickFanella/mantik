package engine

import (
	"context"
	"encoding/json"
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
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func recipeConfig(t *testing.T, kind string, source OptionSource) OptionBacktestConfig {
	t.Helper()
	c := library.DefaultOption(kind)
	// The shared synthetic chain has December expiry. These explicit scenarios
	// test lifecycle policy, not a claim about a naturally observed long-dated edge.
	c.MinDTE = 1
	c.TargetDTE = 346
	c.MaxDTE = 365
	c.HoldSessions = 1
	c.MaxContracts = 3
	recipe, err := library.NewOption(c)
	if err != nil {
		t.Fatal(err)
	}
	n := 2
	if kind == library.Vertical {
		n = 3
	}
	if kind == library.IronCondor || kind == library.IronButterfly {
		n = 5
	}
	times := []time.Time{}
	for i := 0; i <= n; i++ {
		times = append(times, optionStart.Add(time.Duration(i)*24*time.Hour))
	}
	cfg := OptionBacktestConfig{Recipe: recipe, AssetID: "asset-spy", Times: times, Book: OptionConfig{Source: source, Account: account.Margin, SettledCash: "1000000", FeePerContract: .65, SlippageBps: 5}}
	if kind == library.CoveredCall || kind == library.CashSecuredPut {
		cfg.Book.Account = account.Cash
	}
	if kind == library.CoveredCall {
		cfg.Book.Stocks = map[string]int{"asset-spy": 200}
	}
	return cfg
}

func TestOptionRecipeBacktestsRunAllFiveThroughBook(t *testing.T) {
	for _, kind := range []string{library.CoveredCall, library.CashSecuredPut, library.Vertical, library.IronCondor, library.IronButterfly} {
		t.Run(kind, func(t *testing.T) {
			cfg := recipeConfig(t, kind, &optionFixtureSource{})
			result, err := RunOptionBacktest(context.Background(), cfg)
			if err != nil || !result.Complete || result.Version != library.OptionVersion || result.Provenance.Policy != library.OptionVersion || len(result.Provenance.References) != 2 {
				t.Fatal(result, err)
			}
			id, err := cfg.Recipe.Spec().ID()
			if err != nil || result.StrategyID != id {
				t.Fatal(result.StrategyID, err)
			}
			var plan *library.OptionPlan
			opening, closing := 0, 0
			last := OptionSnapshot{}
			reserve := 0.0
			for _, event := range result.Events {
				if len(event.Result.Reasons) > 0 && event.Action != "submit" {
					t.Fatal("unexpected refusal", event)
				}
				if event.Plan != nil {
					plan = event.Plan
				}
				if event.Action == "submit" && len(event.Result.Reasons) == 0 {
					reserve = event.Result.Risk.Reserve
					if event.Contracts < 1 || event.Contracts > 3 {
						t.Fatal(event)
					}
				}
				if event.Action == "step" {
					opening += len(event.Result.Fills)
				}
				if event.Action == "close" {
					closing += len(event.Result.Fills)
				}
				if len(event.Snapshot.Positions) > 0 {
					held, err := strconv.ParseFloat(event.Snapshot.Reserved, 64)
					if err != nil || held < reserve {
						t.Fatal("lost reserve", event, err)
					}
				}
				if event.Action == "step" && len(event.Result.Fills) != 1 {
					t.Fatal("not one protected leg per session", event)
				}
				for _, f := range event.Result.Fills {
					price, err := strconv.ParseFloat(f.Price, 64)
					if err != nil {
						t.Fatal(err)
					}
					if f.Contracts > 0 && price <= f.Bar.High || f.Contracts < 0 && price >= f.Bar.Low {
						t.Fatal("non-adverse fill", f)
					}
					if !f.Closing && f.Contracts < 0 && plan != nil {
						for _, l := range plan.Structure.Legs {
							if l.Contracts > 0 && !slices.ContainsFunc(event.Snapshot.Positions, func(p OptionHolding) bool { return p.Symbol == l.Symbol }) {
								t.Fatal("unprotected short", event)
							}
						}
					}
				}
				last = event.Snapshot
			}
			if plan == nil || opening != len(plan.Structure.Legs) || closing != opening || len(last.Positions) != 0 || len(last.Pending) != 0 || last.Reserved != "0" || last.Cash == cfg.Book.SettledCash {
				t.Fatal("missing round trip", result)
			}
			assertRecipeCaps(t, cfg, result)
			if kind == library.CoveredCall && absOptionContracts(plan.Structure.Legs[0].Contracts) != 2 {
				t.Fatal("did not size to paid shares", plan)
			}
			// Identical inputs produce byte-identical output, including fills/identity.
			again, err := RunOptionBacktest(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			a, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(again)
			if err != nil {
				t.Fatal(err)
			}
			if string(a) != string(b) {
				t.Fatal("nondeterministic backtest")
			}
		})
	}
}

func TestOptionRecipesRefuseCashAndBelowMarginFallback(t *testing.T) {
	for _, kind := range []string{library.Vertical, library.IronCondor, library.IronButterfly} {
		for _, a := range []account.Type{account.Cash, account.Margin} {
			t.Run(fmt.Sprintf("%s/%s", kind, a), func(t *testing.T) {
				cfg := recipeConfig(t, kind, &optionFixtureSource{})
				cfg.Book.Account = a
				cfg.Times = cfg.Times[:1]
				if a == account.Margin {
					cfg.Book.SettledCash = "1999"
				}
				r, err := RunOptionBacktest(context.Background(), cfg)
				if err != nil || len(r.Events) != 1 || !slices.Contains(r.Events[0].Result.Reasons, "option_cash_structure_not_allowed") || len(r.Events[0].Snapshot.Pending) > 0 || len(r.Events[0].Snapshot.Fills) > 0 {
					t.Fatal(r, err)
				}
			})
		}
	}
}

func TestOptionRecipeSizingAndGreekRecheck(t *testing.T) {
	cfg := recipeConfig(t, library.Vertical, &optionFixtureSource{change: func(c *options.Chain) {
		// One lot fits the gamma cap; three do not. This forces actual engine
		// admission downsizing, not an independently loosened sizing risk policy.
		for i := range c.Entries {
			c.Entries[i].Greeks.Gamma = .04
		}
	}})
	cfg.Times = cfg.Times[:1]
	r, err := RunOptionBacktest(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	qs := []int{}
	for _, e := range r.Events {
		if e.Action == "submit" {
			qs = append(qs, e.Contracts)
		}
	}
	if !slices.Equal(qs, []int{3, 2, 1}) || !slices.Contains(r.Events[1].Result.Reasons, "risk_option_gamma") {
		t.Fatal(r)
	}
	for _, kind := range []string{library.Vertical, library.IronCondor, library.IronButterfly} {
		t.Run(kind, func(t *testing.T) {
			cfg := recipeConfig(t, kind, &optionFixtureSource{change: func(c *options.Chain) {
				if c.AsOf.After(optionStart) {
					for i := range c.Entries {
						c.Entries[i].Greeks.Gamma = 1
					}
				}
			}})
			r, err := RunOptionBacktest(context.Background(), cfg)
			if err != nil || !r.Complete {
				t.Fatal(r, err)
			} // untouched cancellation at horizon
			steps := 0
			for _, e := range r.Events {
				if e.Action == "step" {
					steps++
					if !slices.Contains(e.Result.Reasons, "risk_option_gamma") || len(e.Result.Fills) != 0 || e.Snapshot.Reserved == "0" {
						t.Fatal(e)
					}
				}
			}
			if steps == 0 || r.Events[len(r.Events)-1].Action != "cancel" {
				t.Fatal(r)
			}
		})
	}
}

func TestOptionRecipePartialHorizonAndLimitsRetainRisk(t *testing.T) {
	cfg := recipeConfig(t, library.IronCondor, &optionFixtureSource{})
	cfg.Times = cfg.Times[:2]
	r, err := RunOptionBacktest(context.Background(), cfg)
	if err != nil || r.Complete {
		t.Fatal(r, err)
	}
	last := r.Events[len(r.Events)-1].Snapshot
	if len(last.Positions) != 1 || len(last.Pending) != 1 || last.Reserved == "0" {
		t.Fatal(last)
	}
	cfg = recipeConfig(t, library.CoveredCall, &optionFixtureSource{changeBars: func(bars map[string]options.IndicativeBar) {
		for key, b := range bars {
			b.Low = b.Close * .5
			bars[key] = b
		}
	}})
	r, err = RunOptionBacktest(context.Background(), cfg)
	if err != nil || !r.Complete {
		t.Fatal(r, err)
	}
	if !slices.ContainsFunc(r.Events, func(e OptionRecipeEvent) bool {
		return e.Action == "step" && slices.Contains(e.Result.Reasons, "option_limit_not_reached") && len(e.Result.Fills) == 0
	}) {
		t.Fatal(r)
	}
}

type recipeSpots struct{ spots []options.Spot }

func (s recipeSpots) OptionSpot(ctx context.Context, asset string, at time.Time) (options.Spot, error) {
	if err := ctx.Err(); err != nil {
		return options.Spot{}, err
	}
	var latest options.Spot
	for _, v := range s.spots {
		if v.AssetID == asset && !v.AvailableAt.After(at) && v.At.After(latest.At) {
			latest = v
		}
	}
	if latest.AssetID == "" {
		return latest, options.ErrChain
	}
	return latest, nil
}

func TestOptionRecipeRetainedPointInTimeRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	spots := recipeSpots{}
	records := []alpacarecord.OptionRecord{}
	for _, right := range []string{"call", "put"} {
		for _, strike := range []int{90, 95, 100, 105, 110} {
			char := "C"
			if right == "put" {
				char = "P"
			}
			symbol := fmt.Sprintf("SPY260220%s%08d", char, strike*1000)
			data := fmt.Sprintf(`{"id":%q,"symbol":%q,"underlying_symbol":"SPY","expiration_date":"2026-02-20","strike_price":%q,"type":%q,"size":"100"}`, symbol, symbol, strconv.Itoa(strike), right)
			records = append(records, alpacarecord.OptionRecord{Kind: "contract", Symbol: symbol, Underlying: "SPY", Key: symbol, Feed: "reference", OccurredAt: optionStart, ObservedAt: optionStart, AvailableAt: optionStart, Data: json.RawMessage(data)})
			for day := 0; day < 7; day++ {
				at := optionStart.AddDate(0, 0, day)
				date := optionCivil(at)
				g, err := options.Price(options.Inputs{Type: right, Spot: 100, Strike: float64(strike), Years: float64(45-day) / 365, Volatility: .25})
				if err != nil {
					t.Fatal(err)
				}
				raw := fmt.Sprintf(`{"o":%.12f,"h":%.12f,"l":%.12f,"c":%.12f}`, g.Price, g.Price*1.02, g.Price*.98, g.Price)
				records = append(records, alpacarecord.OptionRecord{Kind: "bar", Symbol: symbol, Underlying: "SPY", Key: date.Format(time.DateOnly), Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, newYork), ObservedAt: at, AvailableAt: at, Data: json.RawMessage(raw)})
			}
		}
	}
	for day := 0; day < 7; day++ {
		at := optionStart.AddDate(0, 0, day)
		date := optionCivil(at)
		spots.spots = append(spots.spots, options.Spot{AssetID: "asset-spy", Underlying: "SPY", Date: date.Format(time.DateOnly), Price: 100, At: time.Date(date.Year(), date.Month(), date.Day(), 16, 0, 0, 0, newYork), ObservedAt: at, AvailableAt: at})
	}
	if _, err := store.RecordOptionData(ctx, records); err != nil {
		t.Fatal(err)
	}
	source := RetainedOptionSource{Store: store, Spots: spots, Assumptions: options.Assumptions{MaxAge: 24 * time.Hour}}
	for _, kind := range []string{library.CoveredCall, library.CashSecuredPut, library.Vertical, library.IronCondor, library.IronButterfly} {
		t.Run(kind, func(t *testing.T) {
			cfg := recipeConfig(t, kind, source)
			c := cfg.Recipe.Config()
			c.MinDTE = 21
			c.TargetDTE = 30
			c.MaxDTE = 45
			c.LimitBuffer = .25
			var err error
			cfg.Recipe, err = library.NewOption(c)
			if err != nil {
				t.Fatal(err)
			}
			// Realistic model gamma still consumes gross capacity; this is declared
			// synthetic capital, never an observed balance or strategy performance.
			cfg.Book.SettledCash = "20000000"
			r, err := RunOptionBacktest(ctx, cfg)
			if err != nil || !r.Complete {
				t.Fatal(r, err)
			}
			last := r.Events[len(r.Events)-1]
			if last.Action != "close" || len(last.Snapshot.Fills) == 0 {
				t.Fatal(r)
			}
			assertRecipeCaps(t, cfg, r)
			for _, e := range r.Events {
				if len(e.Result.Reasons) > 0 {
					t.Fatal(e)
				}
			}
		})
	}
	// Preserve reference/bar/spot availability: pre-acquisition has no chain.
	if _, err := source.OptionChain(ctx, "asset-spy", optionStart.Add(-time.Second)); err == nil {
		t.Fatal("invented early spot availability")
	}
	early, err := store.OptionChainAsOf(ctx, "SPY", optionStart.Add(-time.Second))
	if err != nil || len(early) != 0 {
		t.Fatal(early, err)
	}
	wrong := source
	wrong.Spots = recipeSpots{spots: []options.Spot{spots.spots[0]}}
	if _, err := wrong.OptionChain(ctx, "other", optionStart); err == nil {
		t.Fatal("asset alias accepted")
	}
	// A future correction must not change the exact earlier chain cut.
	before, err := source.OptionChain(ctx, "asset-spy", optionStart)
	if err != nil {
		t.Fatal(err)
	}
	correction := records[1]
	correction.ObservedAt = optionStart.AddDate(0, 0, 1)
	correction.AvailableAt = correction.ObservedAt
	correction.Data = json.RawMessage(`{"o":1,"h":2,"l":0.5,"c":1}`)
	if _, err := store.RecordOptionData(ctx, []alpacarecord.OptionRecord{correction}); err != nil {
		t.Fatal(err)
	}
	after, err := source.OptionChain(ctx, "asset-spy", optionStart)
	if err != nil || math.Abs(before.Entries[0].Close-after.Entries[0].Close) > 0 {
		t.Fatal(before, after, err)
	}
}

// Recompute gross sensitivities at every accepted/pending/filled/closed cut,
// independently of the runner result. A plan's larger pre-admission quantity
// must not be substituted for the actual engine-admitted contracts.
func assertRecipeCaps(t *testing.T, cfg OptionBacktestConfig, result OptionBacktest) {
	t.Helper()
	var plan *library.OptionPlan
	quantity := 0
	for _, event := range result.Events {
		if event.Plan != nil {
			plan = event.Plan
		}
		if event.Action == "submit" && len(event.Result.Reasons) == 0 {
			quantity = event.Contracts
		}
		if event.Action == "close" && len(event.Result.Reasons) == 0 || event.Action == "cancel" {
			quantity = 0
		}
		if len(event.Result.Reasons) > 0 {
			continue
		}
		c, err := cfg.Book.Source.OptionChain(context.Background(), cfg.AssetID, event.At)
		if err != nil {
			t.Fatal(err)
		}
		d, g, v, theta := 0.0, 0.0, 0.0, 0.0
		if quantity > 0 {
			if plan == nil {
				t.Fatal("admission without plan")
			}
			for _, leg := range plan.Structure.Legs {
				entry := optionEntry(c, leg.Symbol)
				n := float64(quantity * 100)
				d += n * math.Abs(entry.Greeks.Delta) * c.Spot.Price
				g += n * entry.Greeks.Gamma * c.Spot.Price * c.Spot.Price / 2
				v += n * entry.Greeks.Vega * .01
				theta += n * math.Abs(entry.Greeks.Theta)
			}
		}
		d += float64(cfg.Book.Stocks[cfg.AssetID]) * c.Spot.Price
		dollar := func(v float64) string { return strconv.FormatFloat(v, 'f', 12, 64) }
		reasons, err := risk.CheckOptions(event.Snapshot.Equity, []risk.OptionExposure{{Underlying: cfg.AssetID, Delta: dollar(d), Gamma: dollar(g), Vega: dollar(v), Theta: dollar(theta), Reserve: event.Snapshot.Reserved}})
		if err != nil || len(reasons) > 0 {
			t.Fatal("backtest exceeded Greek/reserve cap", event.Action, reasons, err)
		}
	}
}

func TestOptionBacktestRejectsInvalidTimelineIdentityAndCancellation(t *testing.T) {
	cfg := recipeConfig(t, library.Vertical, &optionFixtureSource{})
	for name, change := range map[string]func(*OptionBacktestConfig){
		"nil recipe":       func(c *OptionBacktestConfig) { c.Recipe = nil },
		"empty asset":      func(c *OptionBacktestConfig) { c.AssetID = "" },
		"empty timeline":   func(c *OptionBacktestConfig) { c.Times = nil },
		"repeated clock":   func(c *OptionBacktestConfig) { c.Times = []time.Time{optionStart, optionStart} },
		"reversed clock":   func(c *OptionBacktestConfig) { c.Times = []time.Time{optionStart, optionStart.Add(-time.Hour)} },
		"foreign identity": func(c *OptionBacktestConfig) { c.Book.StrategyID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			v := cfg
			change(&v)
			if _, err := RunOptionBacktest(context.Background(), v); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunOptionBacktest(ctx, cfg); err != context.Canceled {
		t.Fatal(err)
	}
	failed := cfg
	failed.Book.Source = &optionFixtureSource{fail: true}
	if _, err := RunOptionBacktest(context.Background(), failed); err == nil {
		t.Fatal("source error hidden")
	}
}

func TestOptionRecipeSelectionRejectsWrongSourceCut(t *testing.T) {
	for name, change := range map[string]func(*options.Chain){
		"future clock":       func(c *options.Chain) { c.AsOf = c.AsOf.Add(time.Hour) },
		"asset substitution": func(c *options.Chain) { c.AssetID = "other"; c.Spot.AssetID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := recipeConfig(t, library.Vertical, &optionFixtureSource{change: change})
			if _, err := RunOptionBacktest(context.Background(), cfg); err == nil {
				t.Fatal("selected from the wrong source cut")
			}
		})
	}
}
