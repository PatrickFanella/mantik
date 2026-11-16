package options

import (
	"errors"
	"math"
	"slices"
	"time"
)

// StructureVersion fixes the supported shapes, dollar reserves and fill ordering.
const StructureVersion = "protected-indicative-structures-v1"

// Leg is a signed number of standard 100-share contracts. Limit is dollars per
// underlying share: maximum paid for a buy, minimum received for a sell.
type Leg struct {
	Symbol     string
	Contracts  int
	Multiplier int
	Limit      float64
}

// Structure is an opening order, not a target or broker instruction. Kind is
// long_options, covered_call, cash_secured_put, vertical, iron_condor or
// iron_butterfly. All legs share the chain's underlying and one expiry.
type Structure struct {
	Kind string
	Legs []Leg
}

// StructureRisk is in dollars. Reserve is rounded UP to cents and includes
// MaxLoss PLUS WorstPartialLoss; both include round-trip per-contract fees.
// WorstPartialLoss includes the full vertex as a conservative upper bound.
// CoveredShares is collateral required from already owned, fully paid stock.
// FillOrder requires all protective longs before any shorts. Arbitrary leg
// sequencing is unsafe for call spreads and is deliberately unsupported.
type StructureRisk struct {
	MaxLoss, WorstPartialLoss, Reserve float64
	CoveredShares                      int
	FillOrder                          []int
}

// StructureError carries a stable refusal reason.
type StructureError string

func (e StructureError) Error() string { return string(e) }

// Analyze validates against a point-in-time standard chain and bounds every
// permitted partial-fill vertex. Intermediate contract quantities are bounded
// by those vertices because loss is convex in quantities. No short order may
// rely on another pending order's protection or stock collateral.
func Analyze(c Chain, s Structure, feePerContract float64) (StructureRisk, error) {
	var out StructureRisk
	if err := c.Validate(c.AsOf, c.AssetID); err != nil {
		return out, err
	}
	if len(s.Legs) < 1 || len(s.Legs) > 4 || !finite(feePerContract) || feePerContract <= 0 {
		return out, StructureError("option_invalid_structure")
	}
	entries := make([]Entry, len(s.Legs))
	seen := map[string]bool{}
	expiry := ""
	for i, l := range s.Legs {
		if seen[l.Symbol] || l.Multiplier != 100 || l.Contracts == 0 || l.Contracts < -1000000 || l.Contracts > 1000000 || !finite(l.Limit) || l.Limit <= 0 {
			return out, StructureError("option_invalid_leg")
		}
		seen[l.Symbol] = true
		n, ok := slices.BinarySearchFunc(c.Entries, l.Symbol, func(e Entry, s string) int {
			if e.Symbol < s {
				return -1
			}
			if e.Symbol > s {
				return 1
			}
			return 0
		})
		if !ok {
			return out, StructureError("option_contract_unavailable")
		}
		e := c.Entries[n]
		entries[i] = e
		if expiry != "" && expiry != e.Expiry {
			return out, StructureError("option_mixed_expiries")
		}
		expiry = e.Expiry
		if err := UsableGreeks(e); err != nil {
			return out, err
		}
	}
	if !shape(s, entries) {
		return out, StructureError("option_unsupported_structure")
	}
	if s.Kind == "covered_call" {
		out.CoveredShares = -s.Legs[0].Contracts * 100
	}
	// Stable deterministic ordering: no short can execute before all longs.
	for i, l := range s.Legs {
		if l.Contracts > 0 {
			out.FillOrder = append(out.FillOrder, i)
		}
	}
	for i, l := range s.Legs {
		if l.Contracts < 0 {
			out.FillOrder = append(out.FillOrder, i)
		}
	}
	full := (1 << len(s.Legs)) - 1
	maxLoss, err := subsetLoss(c.Spot.Price, s, entries, full, out.CoveredShares, feePerContract)
	if err != nil {
		return out, err
	}
	out.MaxLoss = maxLoss
	// Include the full vertex as a conservative upper bound when a leg is
	// partially filled in contracts; excluding it can underreserve a put-write.
	for mask := 0; mask <= full; mask++ {
		valid := true
		hasShort := false
		for i, l := range s.Legs {
			if mask&(1<<i) != 0 && l.Contracts < 0 {
				hasShort = true
			}
		}
		if hasShort {
			for i, l := range s.Legs {
				if l.Contracts > 0 && mask&(1<<i) == 0 {
					valid = false
				}
			}
		}
		if !valid {
			continue
		}
		loss, err := subsetLoss(c.Spot.Price, s, entries, mask, out.CoveredShares, feePerContract)
		if err != nil {
			return out, err
		}
		out.WorstPartialLoss = math.Max(out.WorstPartialLoss, loss)
	}
	out.Reserve = centsUp(out.MaxLoss + out.WorstPartialLoss)
	if !finite(out.Reserve) {
		return StructureRisk{}, StructureError("option_nonfinite_reserve")
	}
	return out, nil
}

// UsableGreeks refuses unavailable and invalid estimates. An ok status is the
// presence witness; the chain's missing-estimate zero values are never Greeks.
func UsableGreeks(e Entry) error {
	g := e.Greeks
	if e.EstimateStatus != "ok" {
		return StructureError("option_greeks_unavailable")
	}
	if !finite(e.IV) || e.IV <= 0 || !finite(g.Price) || g.Price <= 0 || !finite(g.Delta) || !finite(g.Gamma) || g.Gamma < 0 || !finite(g.Vega) || g.Vega < 0 || !finite(g.Theta) || g.Delta < -1 || g.Delta > 1 || e.Type == "call" && g.Delta < 0 || e.Type == "put" && g.Delta > 0 {
		return StructureError("option_invalid_greeks")
	}
	return nil
}

func shape(s Structure, e []Entry) bool {
	switch s.Kind {
	case "long_options":
		for _, l := range s.Legs {
			if l.Contracts <= 0 {
				return false
			}
		}
		return true
	case "covered_call":
		return len(e) == 1 && e[0].Type == "call" && s.Legs[0].Contracts < 0
	case "cash_secured_put":
		return len(e) == 1 && e[0].Type == "put" && s.Legs[0].Contracts < 0
	case "vertical":
		return len(e) == 2 && e[0].Type == e[1].Type && e[0].Strike != e[1].Strike && s.Legs[0].Contracts == -s.Legs[1].Contracts
	case "iron_condor", "iron_butterfly":
		if len(e) != 4 {
			return false
		}
		// Put long < put short <= call short < call long, with equal ratios.
		var pl, ps, cs, cl float64
		q := 0
		for i, l := range s.Legs {
			n := l.Contracts
			if n < 0 {
				n = -n
			}
			if q != 0 && q != n {
				return false
			}
			q = n
			switch {
			case e[i].Type == "put" && l.Contracts > 0:
				if pl != 0 {
					return false
				}
				pl = e[i].Strike
			case e[i].Type == "put" && l.Contracts < 0:
				if ps != 0 {
					return false
				}
				ps = e[i].Strike
			case e[i].Type == "call" && l.Contracts < 0:
				if cs != 0 {
					return false
				}
				cs = e[i].Strike
			case e[i].Type == "call" && l.Contracts > 0:
				if cl != 0 {
					return false
				}
				cl = e[i].Strike
			}
		}
		return pl > 0 && pl < ps && cs < cl && ((s.Kind == "iron_condor" && ps < cs) || (s.Kind == "iron_butterfly" && ps == cs))
	}
	return false
}

func subsetLoss(spot float64, s Structure, e []Entry, mask, shares int, fee float64) (float64, error) {
	// Value the dedicated stock collateral at current spot, including its loss
	// to zero. Long options do not collateralize a different order.
	debit := float64(shares) * spot
	slope := float64(shares)
	strikes := []float64{0}
	for i, l := range s.Legs {
		if mask&(1<<i) == 0 {
			continue
		}
		q := float64(l.Contracts * 100)
		debit += q*l.Limit + 2*float64(absContracts(l.Contracts))*fee
		strikes = append(strikes, e[i].Strike)
		if e[i].Type == "call" {
			slope += q
		}
	}
	if slope < 0 {
		return 0, StructureError("option_unbounded_partial_short")
	}
	worst := 0.0
	for _, price := range strikes {
		payoff := float64(shares) * price
		for i, l := range s.Legs {
			if mask&(1<<i) == 0 {
				continue
			}
			intrinsic := math.Max(0, price-e[i].Strike)
			if e[i].Type == "put" {
				intrinsic = math.Max(0, e[i].Strike-price)
			}
			payoff += float64(l.Contracts*100) * intrinsic
		}
		worst = math.Max(worst, debit-payoff)
	}
	return centsUp(worst), nil
}
func absContracts(q int) int {
	if q < 0 {
		return -q
	}
	return q
}
func centsUp(v float64) float64 { return math.Ceil(v*100) / 100 }

// IndicativeBar is a retained daily OHLC observation, never an execution quote.
// Date is the New York session date; all clocks must be known at consumption.
type IndicativeBar struct {
	Symbol, Date                        string
	Open, High, Low, Close              float64
	OccurredAt, ObservedAt, AvailableAt time.Time
}

// Validate rejects missing/null OHLC, contradictory ranges and future clocks.
func (b IndicativeBar) Validate(at time.Time) error {
	z, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, b.Date); err != nil {
		return err
	}
	if b.Symbol == "" || b.OccurredAt.IsZero() || b.ObservedAt.IsZero() || b.OccurredAt.After(b.ObservedAt) || b.ObservedAt.After(b.AvailableAt) || b.AvailableAt.After(at) || b.OccurredAt.In(z).Format(time.DateOnly) != b.Date {
		return errors.New("option_invalid_bar_clocks")
	}
	for _, v := range []float64{b.Open, b.High, b.Low, b.Close} {
		if !finite(v) || v <= 0 {
			return errors.New("option_invalid_bar_prices")
		}
	}
	if b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) || b.Low > b.High {
		return errors.New("option_invalid_bar_range")
	}
	return nil
}
