package options

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestQuantLibWorkedExamples(t *testing.T) {
	raw, err := os.ReadFile("testdata/quantlib-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input    Inputs
		Expected Greeks
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Input.Type+"-"+fmtPrice(c.Input.Strike)+"-"+fmtPrice(c.Input.Years), func(t *testing.T) {
			got, err := Price(c.Input)
			if err != nil {
				t.Fatal(err)
			}
			for name, pair := range map[string][2]float64{"price": {got.Price, c.Expected.Price}, "delta": {got.Delta, c.Expected.Delta}, "gamma": {got.Gamma, c.Expected.Gamma}, "vega": {got.Vega, c.Expected.Vega}, "theta": {got.Theta, c.Expected.Theta}} {
				if math.Abs(pair[0]-pair[1]) > 1e-10 {
					t.Errorf("%s %.17g want %.17g", name, pair[0], pair[1])
				}
			}
			iv, err := Implied(c.Input, c.Expected.Price)
			if err != nil || math.Abs(iv-c.Input.Volatility) > 1e-10 {
				t.Fatal("IV", iv, err)
			}
		})
	}
}

func TestPricingRejectsInvalidAndUnsolvableInputs(t *testing.T) {
	p := Inputs{Type: "call", Spot: 100, Strike: 100, Years: 1, Rate: .05, Volatility: .2}
	for name, change := range map[string]func(*Inputs){"type": func(p *Inputs) { p.Type = "other" }, "expiry": func(p *Inputs) { p.Years = 0 }, "negative time": func(p *Inputs) { p.Years = -1 }, "spot": func(p *Inputs) { p.Spot = 0 }, "strike": func(p *Inputs) { p.Strike = -1 }, "vol": func(p *Inputs) { p.Volatility = 0 }, "nan": func(p *Inputs) { p.Rate = math.NaN() }, "infinity": func(p *Inputs) { p.Yield = math.Inf(1) }} {
		t.Run(name, func(t *testing.T) {
			bad := p
			change(&bad)
			if _, err := Price(bad); err == nil {
				t.Fatal("invalid input admitted")
			}
		})
	}
	for _, v := range []float64{0, -1, 100, 101, math.NaN(), math.Inf(1), 100 - 100*math.Exp(-.05)} {
		if _, err := Implied(p, v); err == nil {
			t.Fatal("unsolvable price", v)
		}
	}
}
