// Package options reads indicative option chains and computes European-model
// estimates, validates structures and bounds their simulated loss reserves.
// It supplies no execution quotes, exercise or broker order authority.
package options

import (
	"errors"
	"math"
)

// ErrPricing marks invalid inputs or an unsolvable indicative price.
var ErrPricing = errors.New("invalid Black-Scholes input or implied volatility")

// ModelVersion fixes units and numerical conventions for retained estimates.
const ModelVersion = "black-scholes-merton-european-v1"

// Inputs are spot/strike in dollars, time in ACT/365 years, and continuously
// compounded annual rate/yield and annual volatility as fractions (0.2 = 20%).
type Inputs struct {
	Type                                         string
	Spot, Strike, Years, Rate, Yield, Volatility float64
}

// Greeks are per underlying share. Vega is per unit volatility (1.0), Theta
// per calendar day, and Gamma per dollar change in spot. No multiplier is applied.
type Greeks struct{ Price, Delta, Gamma, Vega, Theta float64 }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func valid(p Inputs) bool {
	for _, v := range []float64{p.Spot, p.Strike, p.Years, p.Rate, p.Yield, p.Volatility} {
		if !finite(v) {
			return false
		}
	}
	return (p.Type == "call" || p.Type == "put") && p.Spot > 0 && p.Strike > 0 && p.Years > 0 && p.Volatility > 0
}
func cdf(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// Price computes Black-Scholes-Merton with continuous dividend yield. These
// European estimates do not model American early exercise or discrete dividends.
func Price(p Inputs) (Greeks, error) {
	if !valid(p) {
		return Greeks{}, ErrPricing
	}
	root := math.Sqrt(p.Years)
	d1 := (math.Log(p.Spot/p.Strike) + (p.Rate-p.Yield+p.Volatility*p.Volatility/2)*p.Years) / (p.Volatility * root)
	d2 := d1 - p.Volatility*root
	dq, dr := math.Exp(-p.Yield*p.Years), math.Exp(-p.Rate*p.Years)
	pdf := math.Exp(-d1*d1/2) / math.Sqrt(2*math.Pi)
	g := Greeks{Gamma: dq * pdf / (p.Spot * p.Volatility * root), Vega: p.Spot * dq * pdf * root}
	decay := -p.Spot * dq * pdf * p.Volatility / (2 * root)
	if p.Type == "call" {
		g.Price = p.Spot*dq*cdf(d1) - p.Strike*dr*cdf(d2)
		g.Delta = dq * cdf(d1)
		g.Theta = (decay - p.Rate*p.Strike*dr*cdf(d2) + p.Yield*p.Spot*dq*cdf(d1)) / 365
	} else {
		g.Price = p.Strike*dr*cdf(-d2) - p.Spot*dq*cdf(-d1)
		g.Delta = -dq * cdf(-d1)
		g.Theta = (decay + p.Rate*p.Strike*dr*cdf(-d2) - p.Yield*p.Spot*dq*cdf(-d1)) / 365
	}
	for _, v := range []float64{g.Price, g.Delta, g.Gamma, g.Vega, g.Theta} {
		if !finite(v) {
			return Greeks{}, ErrPricing
		}
	}
	return g, nil
}

// Implied solves a strictly interior European price by bisection in annual
// volatility [1e-6, 8]. Boundary/out-of-bounds and unidentifiable prices fail;
// they are not clipped into apparently usable risk estimates.
func Implied(p Inputs, price float64) (float64, error) {
	p.Volatility = 1
	if !valid(p) || !finite(price) || price <= 0 {
		return 0, ErrPricing
	}
	a, b := p.Spot*math.Exp(-p.Yield*p.Years), p.Strike*math.Exp(-p.Rate*p.Years)
	lower, upper := math.Max(0, a-b), a
	if p.Type == "put" {
		lower, upper = math.Max(0, b-a), b
	}
	if !finite(lower) || !finite(upper) || price <= lower || price >= upper {
		return 0, ErrPricing
	}
	lo, hi := 1e-6, 8.0
	p.Volatility = lo
	gl, e := Price(p)
	if e != nil {
		return 0, e
	}
	p.Volatility = hi
	gh, e := Price(p)
	if e != nil {
		return 0, e
	}
	if price <= gl.Price || price >= gh.Price {
		return 0, ErrPricing
	}
	for range 100 {
		mid := (lo + hi) / 2
		p.Volatility = mid
		g, e := Price(p)
		if e != nil {
			return 0, e
		}
		if g.Price < price {
			lo = mid
		} else {
			hi = mid
		}
	}
	vol := (lo + hi) / 2
	p.Volatility = vol
	g, e := Price(p)
	if e != nil || g.Vega < 1e-8 || math.Abs(g.Price-price) > 1e-8*math.Max(1, price) {
		return 0, ErrPricing
	}
	return vol, nil
}
