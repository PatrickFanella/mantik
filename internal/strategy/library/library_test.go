package library_test

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

type source struct {
	bars  map[string][]marketdata.AdjustedBar
	dates []string
	index map[string]int
}

func (s source) AdjustedBars(ctx context.Context, a string, at time.Time) (marketdata.Series, error) {
	out := marketdata.Series{AssetID: a, AsOf: at}
	for _, b := range s.bars[a] {
		if !b.Raw.AvailableAt.After(at) {
			out.Bars = append(out.Bars, b)
		}
	}
	return out, nil
}
func (s source) StockUniverse(_ context.Context, at time.Time) (marketdata.UniverseReport, error) {
	idx, ok := s.index[at.Format(time.DateOnly)]
	if !ok {
		return marketdata.UniverseReport{}, fmt.Errorf("not a fixture session")
	}
	r := marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	for _, a := range []string{"IWM", "QQQ", "SPY"} {
		if a == "QQQ" && idx >= 420 && idx < 480 {
			continue
		}
		prior := at.AddDate(0, 0, -1).Format(time.DateOnly)
		r.Members = append(r.Members, marketdata.UniverseMember{AssetID: a, Symbol: a, LastSession: prior})
	}
	return r, nil
}
func fixture(t *testing.T) source {
	t.Helper()
	raw, e := os.ReadFile("testdata/adjusted-v1.csv")
	if e != nil {
		t.Fatal(e)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "f6fa15c42a70121716541e962d1a5d27bd48f41d63bd980428702a69d5ccd391" {
		t.Fatal("source hash", got)
	}
	f, e := os.Open("testdata/adjusted-v1.csv")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	rows, e := csv.NewReader(f).ReadAll()
	if e != nil {
		t.Fatal(e)
	}
	s := source{bars: map[string][]marketdata.AdjustedBar{}, index: map[string]int{}}
	batch := []storage.DailyBar{}
	for _, r := range rows[1:] {
		day, e := time.Parse(time.DateOnly, r[1])
		if e != nil {
			t.Fatal(e)
		}
		at := day.Add(20*time.Hour + 15*time.Minute)
		volume, e := strconv.ParseInt(r[6], 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		batch = append(batch, storage.DailyBar{AssetID: r[0], Symbol: r[0], Date: r[1], Open: r[2], High: r[3], Low: r[4], Close: r[5], VWAP: r[5], Volume: volume, TradeCount: 1, PublishedAt: at, ObservedAt: at, AvailableAt: at})
		if r[0] == "IWM" {
			s.index[r[1]] = len(s.dates)
			s.dates = append(s.dates, r[1])
		}
	}
	for _, b := range batch {
		s.bars[b.AssetID] = append(s.bars[b.AssetID], marketdata.AdjustedBar{Date: b.Date, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: strconv.FormatInt(b.Volume, 10), Raw: b})
	}
	return s
}
func clock(date string) time.Time {
	d, _ := time.Parse(time.DateOnly, date)
	return d.Add(22 * time.Hour)
}

type reference struct {
	Config       library.Config
	ConfigSHA256 string
	Decisions    []struct {
		Date    string
		Hold    bool
		Targets map[string]string
	}
	Fills []struct{ Date, Asset, Shares string }
}

func TestPinnedUpstreamRecipeReplication(t *testing.T) {
	raw, e := os.ReadFile("testdata/recipes-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var refs struct {
		Version, InputSHA256 string
		Pins                 map[string]string
		Cases                map[string]reference
	}
	if e := json.Unmarshal(raw, &refs); e != nil {
		t.Fatal(e)
	}
	if refs.Version != library.Version || refs.InputSHA256 != "f6fa15c42a70121716541e962d1a5d27bd48f41d63bd980428702a69d5ccd391" || refs.Pins["bt"] != "763ae01047fce2d09411faeba436855ecf8b679c" || refs.Pins["ffn"] != "eb9354c5c82dbada5645569dc34e5861d156e471" || len(refs.Cases) != 7 {
		t.Fatal("unbound source reference metadata")
	}
	for _, name := range []string{"trend_default", "momentum_default", "reversal_default", "lowvol_default", "momentum_selection", "reversal_selection", "lowvol_selection"} {
		kind := strings.Split(name, "_")[0]
		t.Run(name, func(t *testing.T) {
			src := fixture(t)
			cfg := library.Default(kind)
			if strings.HasSuffix(name, "_selection") {
				cfg.Count = 2
			}
			ref := refs.Cases[name]
			cfgRaw, _ := json.Marshal(cfg)
			wantRaw, _ := json.Marshal(ref.Config)
			if string(cfgRaw) != string(wantRaw) || fmt.Sprintf("%x", sha256.Sum256(cfgRaw)) != ref.ConfigSHA256 {
				t.Fatal("source reference configuration differs from shipped recipe")
			}
			s, e := library.New(cfg)
			if e != nil {
				t.Fatal(e)
			}
			if s.Spec().Params["recipe_sha256"] != ref.ConfigSHA256 {
				t.Fatal("recipe hash not bound to reference")
			}
			book := strategy.Book{}
			for _, r := range ref.Decisions {
				v, e := strategy.Build(context.Background(), s.Spec(), src, clock(r.Date), []string{"IWM", "QQQ", "SPY"}, book)
				if e != nil {
					t.Fatal(e)
				}
				d, e := strategy.Decide(s, v)
				if e != nil {
					t.Fatal(e)
				}
				if d.KeepPositions != r.Hold || len(d.Targets) != len(r.Targets) {
					t.Fatal(r.Date, "decision alignment", d, r)
				}
				for _, target := range d.Targets {
					got, _ := strconv.ParseFloat(target.Weight, 64)
					want, ok := r.Targets[target.Asset]
					if !ok {
						t.Fatal("unexpected target", target)
					}
					w, _ := strconv.ParseFloat(want, 64)
					if math.Abs(got-w) > 2e-11 {
						t.Fatalf("%s %s: %s want %s", r.Date, target.Asset, target.Weight, want)
					}
				}
				if kind != library.Trend {
					for _, target := range d.Targets {
						idx := src.index[r.Date]
						if idx >= 420 && idx < 480 && target.Asset == "QQQ" {
							t.Fatal("ranked a nonmember")
						}
					}
				}
				book.State = d.State
			}
			start := clock(src.dates[0]).Add(-time.Hour)
			end := clock(src.dates[len(src.dates)-1])
			result, e := engine.Run(context.Background(), engine.Config{Strategy: s, Source: src, Assets: []string{"IWM", "QQQ", "SPY"}, Start: start, End: end, InitialCash: "100000", Account: account.Margin, Cost: engine.CostModel{Version: "reference-zero-v1", FeeBps: "0", SlippageBps: "0"}})
			if e != nil {
				t.Fatal(e)
			}
			if len(result.Fills) != len(ref.Fills) {
				t.Fatalf("fill count %d want %d (refusals %v)", len(result.Fills), len(ref.Fills), result.Refusals)
			}
			// The engine reduces positions first, so compare complete per-session trades
			// by asset rather than imposing the reference adapter's iteration order.
			want := map[string]float64{}
			for _, f := range ref.Fills {
				x, _ := strconv.ParseFloat(f.Shares, 64)
				want[f.Date+"/"+f.Asset] = x
			}
			for _, f := range result.Fills {
				q, e := strconv.ParseFloat(f.Shares, 64)
				if e != nil {
					t.Fatal(e)
				}
				w, ok := want[f.Date+"/"+f.Asset]
				if !ok || math.Abs(q-w) > 1e-6 {
					t.Fatalf("fill %s/%s: %s want %.12f", f.Date, f.Asset, f.Shares, w)
				}
				if f.Date <= f.DecidedOn {
					t.Fatal("same-session fill")
				}
			}
		})
	}
}
func TestConfigurationIdentityAndImmutability(t *testing.T) {
	c := library.Default(library.Trend)
	s, e := library.New(c)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := s.Spec().ID()
	c.Lookbacks[0] = 1
	metadata := s.Spec()
	metadata.Params["recipe_sha256"] = "edited"
	metadata.Data[0] = "network"
	after, _ := s.Spec().ID()
	if after != before {
		t.Fatal("mutable compiled strategy")
	}
	c = library.Default(library.Trend)
	c.CashRate = .05
	other, e := library.New(c)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := other.Spec().ID()
	if id == before {
		t.Fatal("rate scenario omitted from identity")
	}
	for _, change := range []func(*library.Config){func(c *library.Config) { c.Gross = math.NaN() }, func(c *library.Config) { c.Lookbacks = []int{-1} }, func(c *library.Config) { c.Count = 0 }, func(c *library.Config) { c.Cadence = "clock" }, func(c *library.Config) { c.VolWindow = 0 }} {
		c := library.Default(library.Trend)
		change(&c)
		if _, e := library.New(c); e == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestRepeatedViewsAreDeterministicAndDelayedMembersAreUnavailable(t *testing.T) {
	src := fixture(t)
	cfg := library.Default(library.Momentum)
	s, e := library.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	at := clock(src.dates[len(src.dates)-1])
	// One member's source is seven sessions behind. It remains in membership but
	// cannot compete with up-to-date members or influence their ranks/weights.
	src.bars["QQQ"] = src.bars["QQQ"][:len(src.bars["QQQ"])-7]
	v, e := strategy.Build(context.Background(), s.Spec(), src, at, []string{"IWM", "QQQ", "SPY"}, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	var first []byte
	for range 10 {
		d, e := strategy.Decide(s, v)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(d)
		if e != nil {
			t.Fatal(e)
		}
		if first == nil {
			first = raw
		} else if string(raw) != string(first) {
			t.Fatal("nondeterministic decisions/state")
		}
		for _, target := range d.Targets {
			if target.Asset == "QQQ" {
				t.Fatal("delayed member was ranked")
			}
		}
	}
	if _, _, e := library.Feature(cfg, v, "QQQ"); e == nil {
		t.Fatal("stale feature accepted")
	}
}

func TestMembershipExitRetriesAnActualHoldingAfterTargetAlreadyReachedZero(t *testing.T) {
	src := fixture(t)
	cfg := library.Default(library.Momentum)
	s, e := library.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	date := src.dates[430]
	state, _ := json.Marshal(map[string]any{"period": date[:7], "weights": map[string]float64{"QQQ": 0, "SPY": .7}})
	v, e := strategy.Build(context.Background(), s.Spec(), src, clock(date), []string{"IWM", "QQQ", "SPY"}, strategy.Book{State: state, Positions: []strategy.Position{{Asset: "QQQ", Quantity: "1"}}})
	if e != nil {
		t.Fatal(e)
	}
	d, e := strategy.Decide(s, v)
	if e != nil || d.KeepPositions {
		t.Fatal("held ineligible asset was not retried", d, e)
	}
	for _, target := range d.Targets {
		if target.Asset == "QQQ" {
			t.Fatal("ineligible holding became a new target")
		}
	}
}

func TestReversalDoesNotRestoreUnavailableHoldingsThroughTurnover(t *testing.T) {
	for _, reason := range []string{"delayed", "zero-volatility", "short-history"} {
		t.Run(reason, func(t *testing.T) {
			src := fixture(t)
			cfg := library.Default(library.Reversal)
			cfg.Count = 1
			s, e := library.New(cfg)
			if e != nil {
				t.Fatal(e)
			}
			date := src.dates[len(src.dates)-1]
			switch reason {
			case "delayed":
				src.bars["QQQ"] = src.bars["QQQ"][:len(src.bars["QQQ"])-1]
			case "short-history":
				src.bars["QQQ"] = src.bars["QQQ"][len(src.bars["QQQ"])-40:]
			case "zero-volatility":
				for i := len(src.bars["QQQ"]) - 64; i < len(src.bars["QQQ"]); i++ {
					bar := &src.bars["QQQ"][i]
					bar.Open = "100"
					bar.High = "100"
					bar.Low = "100"
					bar.Close = "100"
					bar.Raw.Open = "100"
					bar.Raw.High = "100"
					bar.Raw.Low = "100"
					bar.Raw.Close = "100"
				}
			}
			state, _ := json.Marshal(map[string]any{"period": "older-week", "weights": map[string]float64{"QQQ": .7}})
			v, e := strategy.Build(context.Background(), s.Spec(), src, clock(date), []string{"IWM", "QQQ", "SPY"}, strategy.Book{State: state, Positions: []strategy.Position{{Asset: "QQQ", Quantity: "1"}}})
			if e != nil {
				t.Fatal(e)
			}
			d, e := strategy.Decide(s, v)
			if e != nil || d.KeepPositions {
				t.Fatal(d, e)
			}
			if len(d.Targets) != 1 {
				t.Fatal("expected a healthy replacement", d)
			}
			for _, target := range d.Targets {
				w, e := strconv.ParseFloat(target.Weight, 64)
				if e != nil || target.Asset == "QQQ" || w <= 0 || w > cfg.Turnover+1e-12 {
					t.Fatal("turnover restored unavailable holding or exceeded remaining-change cap", target)
				}
			}
			var retained struct{ Weights map[string]float64 }
			if e := json.Unmarshal(d.State, &retained); e != nil {
				t.Fatal(e)
			}
			if retained.Weights["QQQ"] != 0 {
				t.Fatal("unavailable target retained in state", retained.Weights)
			}
		})
	}
}

func TestReversalStillLimitsTurnoverForHealthyRankedOutHoldings(t *testing.T) {
	src := fixture(t)
	cfg := library.Default(library.Reversal)
	cfg.Count = 1
	s, e := library.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	at := clock(src.dates[len(src.dates)-1])
	v, e := strategy.Build(context.Background(), s.Spec(), src, at, []string{"IWM", "QQQ", "SPY"}, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	best, worst := "", ""
	bestScore, worstScore := math.Inf(-1), math.Inf(1)
	for _, a := range v.Universe() {
		score, _, e := library.Feature(cfg, v, a)
		if e != nil {
			t.Fatal(e)
		}
		if score > bestScore {
			best, bestScore = a, score
		}
		if score < worstScore {
			worst, worstScore = a, score
		}
	}
	if best == worst {
		t.Fatal("fixture needs distinct ranks")
	}
	state, _ := json.Marshal(map[string]any{"period": "older-week", "weights": map[string]float64{worst: .7}})
	v, e = strategy.Build(context.Background(), s.Spec(), src, at, []string{"IWM", "QQQ", "SPY"}, strategy.Book{State: state, Positions: []strategy.Position{{Asset: worst, Quantity: "1"}}})
	if e != nil {
		t.Fatal(e)
	}
	d, e := strategy.Decide(s, v)
	if e != nil {
		t.Fatal(e)
	}
	weights := map[string]float64{}
	for _, target := range d.Targets {
		weights[target.Asset], e = strconv.ParseFloat(target.Weight, 64)
		if e != nil {
			t.Fatal(e)
		}
	}
	if math.Abs(weights[worst]-.575) > 2e-12 || math.Abs(weights[best]-.125) > 2e-12 {
		t.Fatal("healthy rank exit bypassed turnover cap", weights)
	}
}
