package indicators

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func fixture(t *testing.T) marketdata.Series {
	t.Helper()
	raw, err := os.ReadFile("testdata/spy-adjusted-v1.csv")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "5a7293defdb4639fdc3d0fb96b71954e49f27e4b921a75dd884ebbfe776a4755" {
		t.Fatal("input hash", got)
	}
	f, err := os.Open("testdata/spy-adjusted-v1.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	s := marketdata.Series{AssetID: "spy", AsOf: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	for _, r := range rows[1:] {
		s.Bars = append(s.Bars, marketdata.AdjustedBar{Date: r[0][:10], Open: r[1], High: r[2], Low: r[3], Close: r[4], Raw: storage.DailyBar{AssetID: "spy", AvailableAt: s.AsOf}})
	}
	return s
}

func TestTALibParityOnRetainedAdjustedSPY(t *testing.T) {
	s := fixture(t)
	var refs map[string]Series
	raw, err := os.ReadFile("testdata/talib-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func() (Series, error){
		"sma": func() (Series, error) { return SMA(s, 14) }, "ema": func() (Series, error) { return EMA(s, 14) },
		"rsi": func() (Series, error) { return RSI(s, 14) }, "atr": func() (Series, error) { return ATR(s, 14) },
		"volatility": func() (Series, error) { return Volatility(s, 20) },
	}
	b, err := BBands(s, 20)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]Series{"upper": b.Upper, "middle": b.Middle, "lower": b.Lower} {
		checks[name] = func() (Series, error) { return v, nil }
	}
	for name, run := range checks {
		t.Run(name, func(t *testing.T) {
			got, err := run()
			if err != nil {
				t.Fatal(err)
			}
			want := refs[name]
			if got.Start != want.Start || len(got.Values) != len(want.Values) || len(got.Values) != len(s.Bars)-got.Start {
				t.Fatal("alignment", got.Start, want.Start, len(got.Values))
			}
			// Price and oscillator outputs: 1e-8 absolute; daily return volatility: 1e-12.
			tolerance := 1e-8
			if name == "volatility" {
				tolerance = 1e-12
			}
			for i, v := range got.Values {
				if !finite(v) || v-want.Values[i] > tolerance || want.Values[i]-v > tolerance {
					t.Fatalf("bar %d: %.17g want %.17g", i+got.Start, v, want.Values[i])
				}
			}
		})
	}
}

func TestInvalidAndShortInputs(t *testing.T) {
	runners := map[string]struct {
		start int
		run   func(marketdata.Series, int) error
	}{
		"sma":        {13, func(s marketdata.Series, p int) error { _, e := SMA(s, p); return e }},
		"ema":        {13, func(s marketdata.Series, p int) error { _, e := EMA(s, p); return e }},
		"rsi":        {14, func(s marketdata.Series, p int) error { _, e := RSI(s, p); return e }},
		"atr":        {14, func(s marketdata.Series, p int) error { _, e := ATR(s, p); return e }},
		"bands":      {13, func(s marketdata.Series, p int) error { _, e := BBands(s, p); return e }},
		"volatility": {14, func(s marketdata.Series, p int) error { _, e := Volatility(s, p); return e }},
	}
	for name, r := range runners {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			for _, p := range []int{-1, 0, 1, len(s.Bars) + 1} {
				if r.run(s, p) == nil {
					t.Fatal("invalid period", p)
				}
			}
			short := s
			short.Bars = s.Bars[:r.start]
			if r.run(short, 14) == nil {
				t.Fatal("short input")
			}
			short.Bars = s.Bars[:r.start+1]
			if err := r.run(short, 14); err != nil {
				t.Fatal("first available", err)
			}
			for _, change := range []func(*marketdata.Series){
				func(s *marketdata.Series) { s.Bars[0].Close = "NaN" }, func(s *marketdata.Series) { s.Bars[0].High = "+Inf" },
				func(s *marketdata.Series) { s.Bars[0].Open = "NaN" },
				func(s *marketdata.Series) { s.Bars[0].Low = "0" }, func(s *marketdata.Series) { s.Bars[0].Close = "99999" },
				func(s *marketdata.Series) { s.Bars[0].Raw.AvailableAt = s.AsOf.Add(time.Second) },
				func(s *marketdata.Series) { s.Bars[0].Raw.AssetID = "other" },
				func(s *marketdata.Series) { s.Bars[1].Date = s.Bars[0].Date },
				func(s *marketdata.Series) {
					s.Applied = []marketdata.CorporateAction{{KnownAt: s.AsOf.Add(time.Second)}}
				},
			} {
				bad := s
				bad.Bars = slices.Clone(s.Bars)
				change(&bad)
				if r.run(bad, 14) == nil {
					t.Fatal("invalid series admitted")
				}
			}
			// A constant positive price has valid zero-valued volatility/ATR/RSI;
			// availability is expressed by Start, never inferred from nonzero values.
			flat := s
			flat.Bars = slices.Clone(s.Bars)
			for i := range flat.Bars {
				flat.Bars[i].Open = "10"
				flat.Bars[i].High = "10"
				flat.Bars[i].Low = "10"
				flat.Bars[i].Close = "10"
			}
			if err := r.run(flat, 14); err != nil {
				t.Fatal("flat input", err)
			}
		})
	}
}
