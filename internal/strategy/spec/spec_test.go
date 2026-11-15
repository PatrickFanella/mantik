package spec_test

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
)

func field(name string) *spec.Expr                  { return &spec.Expr{Op: "field", Field: name} }
func constant(v float64) *spec.Expr                 { return &spec.Expr{Op: "const", Value: v} }
func op(name string, args ...*spec.Expr) *spec.Expr { return &spec.Expr{Op: name, Args: args} }
func def(score *spec.Expr) spec.Definition {
	return spec.Definition{Name: "test-spec", Score: score, Gross: .7}
}

type source struct{}

func (source) AdjustedBars(_ context.Context, a string, at time.Time) (marketdata.Series, error) {
	s := marketdata.Series{AssetID: a, AsOf: at}
	for d := 5; d <= 9; d++ {
		close := float64(d)
		if a == "B" {
			close = 14 - close
		}
		day := time.Date(2026, 1, d, 20, 0, 0, 0, time.UTC)
		if day.After(at) {
			continue
		}
		text := map[float64]string{5: "5", 6: "6", 7: "7", 8: "8", 9: "9"}[close]
		s.Bars = append(s.Bars, marketdata.AdjustedBar{Date: day.Format(time.DateOnly), Open: text, High: text, Low: text, Close: text, Volume: "1000", Raw: storage.DailyBar{AssetID: a, Date: day.Format(time.DateOnly), Open: text, Close: text, AvailableAt: day}})
	}
	return s, nil
}
func view(t *testing.T, s strategy.Spec) strategy.View {
	t.Helper()
	v, e := strategy.Build(context.Background(), s, source{}, time.Date(2026, 1, 9, 22, 0, 0, 0, time.UTC), []string{"A", "B"}, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestTypedExpressionsAndMissingValues(t *testing.T) {
	for name, score := range map[string]*spec.Expr{
		"lag":               {Op: "lag", Window: 1, Args: []*spec.Expr{field("close")}},
		"rolling mean":      {Op: "mean", Window: 3, Args: []*spec.Expr{field("close")}},
		"rolling deviation": {Op: "std", Window: 3, Args: []*spec.Expr{field("close")}},
		"return":            {Op: "return", Window: 1, Args: []*spec.Expr{field("close")}},
		"rank":              op("rank", field("close")),
		"typed selection":   op("where", op("gt", field("close"), constant(7)), constant(1), constant(0)),
	} {
		t.Run(name, func(t *testing.T) {
			c, e := spec.Compile(def(score))
			if e != nil {
				t.Fatal(e)
			}
			d, e := strategy.Decide(c, view(t, c.Spec()))
			if e != nil || len(d.Targets) == 0 {
				t.Fatal(d, e)
			}
		})
	}
	// A forward gap or division by zero is unavailable and produces no targets.
	for _, score := range []*spec.Expr{op("div", field("close"), constant(0)), {Op: "lag", Window: 252, Args: []*spec.Expr{field("close")}}} {
		c, e := spec.Compile(def(score))
		if e != nil {
			t.Fatal(e)
		}
		d, e := strategy.Decide(c, view(t, c.Spec()))
		if e != nil || len(d.Targets) != 0 {
			t.Fatal("missing data became a signal", d, e)
		}
	}
}
func TestSpecBoundsTypesCyclesAndUnknownJSON(t *testing.T) {
	cycle := op("abs", constant(1))
	cycle.Args[0] = cycle
	deep := constant(1)
	for range spec.MaxDepth + 1 {
		deep = op("abs", deep)
	}
	for name, score := range map[string]*spec.Expr{
		"network": {Op: "http"}, "future": {Op: "lag", Window: -1, Args: []*spec.Expr{field("close")}},
		"excessive window": {Op: "mean", Window: 100000, Args: []*spec.Expr{field("close")}},
		"nonfinite":        constant(math.NaN()), "oversize": constant(1e99), "cycle": cycle, "deep": deep,
		"boolean root":        op("gt", constant(1), constant(0)),
		"wrong argument type": op("add", op("gt", constant(1), constant(0)), constant(1)),
		"wrong arity":         op("add", constant(1)), "illegal field": {Op: "field", Field: "future_close"},
		"unknown field data": {Op: "const", Value: 1, Window: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := spec.Compile(def(score)); e == nil {
				t.Fatal("invalid expression accepted")
			}
		})
	}
	for _, raw := range []string{`{"name":"x","gross":0.7,"score":{"op":"const","value":1},"network":"url"}`, `{"name":"x","gross":0.7,"score":{"op":"const","value":1}} junk`, strings.Repeat(" ", strategy.MaxStateBytes+1)} {
		if _, e := spec.Parse([]byte(raw)); e == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}
func TestCompiledSpecIdentityCopiesAndEnginePath(t *testing.T) {
	expression := field("close")
	definition := def(expression)
	c, e := spec.Compile(definition)
	if e != nil {
		t.Fatal(e)
	}
	id, e := c.Spec().ID()
	if e != nil {
		t.Fatal(e)
	}
	expression.Field = "network"
	metadata := c.Spec()
	metadata.Params["definition_sha256"] = "mutated"
	metadata.Data[0] = "network"
	again, _ := c.Spec().ID()
	if id != again {
		t.Fatal("mutable compiled identity")
	}
	other, e := spec.Compile(def(constant(1)))
	if e != nil {
		t.Fatal(e)
	}
	changed, _ := other.Spec().ID()
	if changed == id {
		t.Fatal("different expressions share identity")
	}
	result, e := engine.Run(context.Background(), engine.Config{Strategy: c, Source: source{}, Assets: []string{"A", "B"}, Start: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 9, 23, 0, 0, 0, time.UTC), InitialCash: "1000", Cost: engine.CostModel{Version: "free", FeeBps: "0", SlippageBps: "0"}})
	if e != nil || result.StrategyID != id || len(result.Fills) == 0 {
		t.Fatal(result, e)
	}
	for _, fill := range result.Fills {
		if fill.Date <= fill.DecidedOn {
			t.Fatal("decision-time fill")
		}
	}
	cfg := library.Default(library.Trend)
	block, e := spec.Compile(spec.Definition{Name: "library-block", Library: &cfg, Gross: cfg.Gross})
	if e != nil || block.Spec().Warmup != 253 {
		t.Fatal(block, e)
	}
}

type chainSource struct {
	source
	future bool
}

func (s chainSource) OptionChain(_ context.Context, a string, at time.Time) (options.Chain, error) {
	underlying := "SPY"
	iv := .2
	if a == "B" {
		underlying = "QQQ"
		iv = .4
	}
	observed := at.Add(-time.Hour)
	c := options.Chain{AssetID: a, Underlying: underlying, Feed: "indicative", Model: options.ModelVersion, AsOf: at, Spot: options.Spot{AssetID: a, Underlying: underlying, Date: "2026-01-09", Price: 100, At: observed, ObservedAt: observed, AvailableAt: observed}, Assumptions: options.Assumptions{MaxAge: 24 * time.Hour}, Entries: []options.Entry{{Symbol: underlying + "260116C00100000", ContractID: a, Type: "call", Expiry: "2026-01-16", Strike: 100, Close: 2, ReferenceObservedAt: observed, ReferenceAvailableAt: observed, PriceOccurredAt: time.Date(2026, 1, 9, 5, 0, 0, 0, time.UTC), PriceObservedAt: observed, PriceAvailableAt: observed, FeedSelection: "provider_default_unverified", EstimateStatus: "ok", IV: iv, Greeks: options.Greeks{Price: 2, Delta: .5, Gamma: .01, Vega: 1, Theta: -.01}}}}
	if s.future {
		c.Entries[0].PriceAvailableAt = at.Add(time.Second)
	}
	return c, nil
}
func TestOptionTermsUseOnlyValidatedCurrentChains(t *testing.T) {
	c, e := spec.Compile(spec.Definition{Name: "option-signal", Gross: .6, Score: &spec.Expr{Op: "option_iv"}})
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 9, 22, 0, 0, 0, time.UTC)
	v, e := strategy.Build(context.Background(), c.Spec(), chainSource{}, at, []string{"A", "B"}, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	d, e := strategy.Decide(c, v)
	if e != nil || len(d.Targets) != 2 {
		t.Fatal(d, e)
	}
	for _, target := range d.Targets {
		got, _ := strconv.ParseFloat(target.Weight, 64)
		want := .2
		if target.Asset == "B" {
			want = .4
		}
		if math.Abs(got-want) > 1e-11 {
			t.Fatal(target)
		}
	}
	if _, e := strategy.Build(context.Background(), c.Spec(), chainSource{future: true}, at, []string{"A"}, strategy.Book{}); e == nil {
		t.Fatal("future chain admitted")
	}
}

type wideSource struct{}

func (wideSource) AdjustedBars(_ context.Context, a string, at time.Time) (marketdata.Series, error) {
	i, e := strconv.Atoi(strings.TrimPrefix(a, "asset-"))
	if e != nil {
		return marketdata.Series{}, e
	}
	// Adjacent pairs tie; descending asset order exercises actual value sorting.
	price := strconv.Itoa(1000 - i/2)
	previous := strconv.Itoa(1 + i/2)
	priorAt := at.AddDate(0, 0, -1)
	return marketdata.Series{AssetID: a, AsOf: at, Bars: []marketdata.AdjustedBar{
		{Date: priorAt.Format(time.DateOnly), Open: previous, High: previous, Low: previous, Close: previous, Volume: "1", Raw: storage.DailyBar{AssetID: a, AvailableAt: priorAt}},
		{Date: at.Format(time.DateOnly), Open: price, High: price, Low: price, Close: price, Volume: "1", Raw: storage.DailyBar{AssetID: a, AvailableAt: at}},
	}}, nil
}
func TestRanksSupportTheFullAssetBoundWithAverageTies(t *testing.T) {
	for _, count := range []int{500, strategy.MaxAssets} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			c, e := spec.Compile(def(op("rank", field("close"))))
			if e != nil {
				t.Fatal(e)
			}
			assets := make([]string, count)
			for i := range assets {
				assets[i] = fmt.Sprintf("asset-%04d", i)
			}
			at := time.Date(2026, 1, 9, 22, 0, 0, 0, time.UTC)
			v, e := strategy.Build(context.Background(), c.Spec(), wideSource{}, at, assets, strategy.Book{})
			if e != nil {
				t.Fatal(e)
			}
			d, e := strategy.Decide(c, v)
			if e != nil {
				t.Fatal("ordinary supported universe exhausted rank work budget", e)
			}
			if len(d.Targets) != count {
				t.Fatal("rank dropped valid assets", len(d.Targets))
			}
			// Each pair's normalized average rank is known independently of evaluation
			// order. Its normalized weight is gross * averageRank / totalRanks.
			totalRanks := float64(count*(count+1)) / 2
			for _, target := range d.Targets {
				i, e := strconv.Atoi(strings.TrimPrefix(target.Asset, "asset-"))
				if e != nil {
					t.Fatal(e)
				}
				rank := float64(count-i/2*2) - .5
				want := math.Trunc(.7*rank/totalRanks*1e12) / 1e12
				got, e := strconv.ParseFloat(target.Weight, 64)
				if e != nil || math.Abs(got-want) > 2e-12 {
					t.Fatalf("%s rank weight %.12f want %.12f", target.Asset, got, want)
				}
			}
		})
	}
}

func TestRollingRanksCacheEachHistoricalOffsetSeparately(t *testing.T) {
	count := strategy.MaxAssets
	c, e := spec.Compile(def(&spec.Expr{Op: "mean", Window: 2, Args: []*spec.Expr{op("rank", field("close"))}}))
	if e != nil {
		t.Fatal(e)
	}
	assets := make([]string, count)
	for i := range assets {
		assets[i] = fmt.Sprintf("asset-%04d", i)
	}
	at := time.Date(2026, 1, 9, 22, 0, 0, 0, time.UTC)
	v, e := strategy.Build(context.Background(), c.Spec(), wideSource{}, at, assets, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	d, e := strategy.Decide(c, v)
	if e != nil || len(d.Targets) != count {
		t.Fatal("rolling ranks failed supported universe", len(d.Targets), e)
	}
	// Yesterday's cross-section reverses today's ranks, so each two-session
	// average is equal. A cache shared across offsets would produce unequal weights.
	for _, target := range d.Targets {
		w, e := strconv.ParseFloat(target.Weight, 64)
		if e != nil || math.Abs(w-.7/float64(count)) > 2e-12 {
			t.Fatal("rank cache mixed historical offsets", target)
		}
	}
}
