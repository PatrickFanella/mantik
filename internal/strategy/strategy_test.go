package strategy

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func day(d, hour int) time.Time { return time.Date(2026, 1, d, hour, 0, 0, 0, time.UTC) }

// store holds SPY and QQQ bars for January 5–9, each available at 21:00 UTC.
func store(t *testing.T) *storage.Memory {
	t.Helper()
	m := storage.NewMemory()
	bars := []storage.DailyBar{}
	for _, asset := range []string{"asset-spy", "asset-qqq"} {
		for d := 5; d <= 9; d++ {
			at := day(d, 21)
			bars = append(bars, storage.DailyBar{AssetID: asset, Symbol: "X", Date: at.Format(time.DateOnly), Open: "10", High: "10", Low: "10", Close: "10", VWAP: "10", Volume: 1, TradeCount: 1, PublishedAt: at, ObservedAt: at, AvailableAt: at})
		}
	}
	if _, err := m.RecordDailyBars(context.Background(), bars); err != nil {
		t.Fatal(err)
	}
	return m
}

type memorySource struct{ m *storage.Memory }

func (s memorySource) AdjustedBars(ctx context.Context, asset string, asOf time.Time) (marketdata.Series, error) {
	return marketdata.Read(ctx, s.m, asset, nil, asOf)
}

// leakySource returns every stored bar whatever the clock, like a broken store.
type leakySource struct{ m *storage.Memory }

func (s leakySource) AdjustedBars(ctx context.Context, asset string, asOf time.Time) (marketdata.Series, error) {
	out, err := marketdata.Read(ctx, s.m, asset, nil, day(31, 0))
	out.AsOf = asOf.UTC()
	return out, err
}

// reusingSource returns the same backing slice for every read.
type reusingSource struct {
	m   *storage.Memory
	buf []marketdata.AdjustedBar
}

func (s *reusingSource) AdjustedBars(ctx context.Context, asset string, asOf time.Time) (marketdata.Series, error) {
	out, err := marketdata.Read(ctx, s.m, asset, nil, asOf)
	s.buf = append(s.buf[:0], out.Bars...)
	out.Bars = s.buf
	return out, err
}

type equal struct{ spec Spec }

func (e equal) Spec() Spec { return e.spec }
func (e equal) Decide(v View) (Decision, error) {
	d := Decision{State: []byte("seen")}
	for _, a := range v.Assets() {
		d.Targets = append(d.Targets, Target{Asset: a, Weight: "0.5"})
	}
	return d, nil
}

var spec = Spec{Name: "equal-weight", Version: "1", Params: map[string]string{"lookback": "3", "rebalance": "daily"}, Data: []string{KindDailyBars}, Warmup: 3}

func TestViewHoldsOnlyDataAvailableAtTheDecisionTime(t *testing.T) {
	ctx := context.Background()
	m := store(t)
	at := day(7, 22)
	v, err := Build(ctx, spec, memorySource{m}, at, []string{"asset-spy", "asset-qqq"}, Book{State: []byte("prior")})
	if err != nil {
		t.Fatal(err)
	}
	bars := v.Bars("asset-spy")
	if len(bars) != 3 || bars[2].Date != "2026-01-07" || !v.At().Equal(at) || string(v.State()) != "prior" {
		t.Fatal("view", len(bars), v.At())
	}
	for _, b := range bars {
		if b.Raw.AvailableAt.After(at) {
			t.Fatal("future bar in view", b.Date)
		}
	}
	// Changing what the view returns does not change the view.
	bars[0].Close = "999"
	if v.Bars("asset-spy")[0].Close == "999" {
		t.Fatal("view was mutable")
	}
	// A source that reuses its slice cannot change a built view.
	src := &reusingSource{m: m}
	v, err = Build(ctx, spec, src, at, []string{"asset-spy"}, Book{})
	if err != nil {
		t.Fatal(err)
	}
	src.buf[0].Close = "999"
	if v.Bars("asset-spy")[0].Close == "999" {
		t.Fatal("view shared the source's slice")
	}
	if _, err := Build(ctx, spec, leakySource{m}, at, []string{"asset-spy"}, Book{}); !errors.Is(err, ErrFutureData) {
		t.Fatal("future bar admitted", err)
	}
	late := Book{Fills: []Fill{{Asset: "asset-spy", Time: at.Add(time.Second), Quantity: "1", Price: "10"}}}
	if _, err := Build(ctx, spec, memorySource{m}, at, []string{"asset-spy"}, late); !errors.Is(err, ErrFutureData) {
		t.Fatal("future fill admitted", err)
	}
	// A spec must declare at least one data kind.
	noBars := spec
	noBars.Data = []string{}
	if _, err := Build(ctx, noBars, memorySource{m}, at, []string{"asset-spy"}, Book{}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal("spec without data kinds admitted", err)
	}
}

func TestIdentityHashesVersionParametersAndDataKinds(t *testing.T) {
	id, err := spec.ID()
	if err != nil || !strings.HasPrefix(id, "strategy-") {
		t.Fatal(id, err)
	}
	same := Spec{Name: "equal-weight", Version: "1", Params: map[string]string{"rebalance": "daily", "lookback": "3"}, Data: []string{KindDailyBars, KindDailyBars}, Warmup: 3}
	if got, _ := same.ID(); got != id {
		t.Fatal("equal specs hashed differently")
	}
	for name, change := range map[string]func(*Spec){
		"version": func(s *Spec) { s.Version = "2" },
		"param":   func(s *Spec) { s.Params = map[string]string{"lookback": "4", "rebalance": "daily"} },
		"warmup":  func(s *Spec) { s.Warmup = 4 },
		"name":    func(s *Spec) { s.Name = "other" },
	} {
		s := spec
		change(&s)
		if got, _ := s.ID(); got == id {
			t.Error(name, "did not change the identity")
		}
	}
	bad := spec
	bad.Data = []string{"live_network"}
	if _, err := bad.ID(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal("unknown data kind admitted", err)
	}
}

type fixed struct {
	targets []Target
	panics  bool
}

func (f fixed) Spec() Spec { return spec }
func (f fixed) Decide(View) (Decision, error) {
	if f.panics {
		panic("boom")
	}
	return Decision{Targets: f.targets}, nil
}

func TestDecideChecksTargets(t *testing.T) {
	v, err := Build(context.Background(), spec, memorySource{store(t)}, day(9, 22), []string{"asset-spy", "asset-qqq"}, Book{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decide(equal{spec}, v)
	if err != nil || len(d.Targets) != 2 || d.Targets[0].Asset != "asset-qqq" || string(d.State) != "seen" {
		t.Fatal(d, err)
	}
	for name, targets := range map[string][]Target{
		"gross above one": {{"asset-spy", "0.6"}, {"asset-qqq", "-0.5"}},
		"unknown asset":   {{"asset-iwm", "0.1"}},
		"repeated asset":  {{"asset-spy", "0.1"}, {"asset-spy", "0.1"}},
		"exponent":        {{"asset-spy", "1e-1"}},
		"above one":       {{"asset-spy", "1.5"}},
	} {
		if _, err := Decide(fixed{targets: targets}, v); !errors.Is(err, ErrInvalidDecision) {
			t.Error(name, err)
		}
	}
	if _, err := Decide(fixed{targets: []Target{{"asset-spy", "-1"}}}, v); err != nil {
		t.Fatal("full short refused", err)
	}
	if _, err := Decide(fixed{panics: true}, v); !errors.Is(err, ErrInvalidDecision) {
		t.Fatal("panic escaped", err)
	}
}

func TestAuditRefusesNetworkClockAndOtherImports(t *testing.T) {
	if err := Audit("testdata/good"); err != nil {
		t.Fatal(err)
	}
	err := Audit("testdata/bad")
	for _, want := range []string{"imports net/http", "imports os", "uses time.Now", "uses time.Sleep", "dot-imports time"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Error("audit missed:", want, err)
		}
	}
}

// Every strategy package under internal/strategy must pass the audit.
func TestStrategyPackagesPassTheAudit(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == "." {
			return err
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		return Audit(path)
	})
	if err != nil {
		t.Fatal(err)
	}
}
