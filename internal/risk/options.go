package risk

import (
	"errors"
	"math/big"
	"slices"
)

// OptionExposure contains NONNEGATIVE gross dollar sensitivities. Offsets are
// deliberately not credited, including between independently pending orders.
// Delta = |delta shares| * spot; Gamma = .5*|gamma|*spot^2 (full spot move);
// Vega = |vega|*.01 (one volatility percentage point); Theta = |theta|/day.
// Underlying is the stable underlying asset ID. Reserve is the dollars at risk.
type OptionExposure struct {
	Underlying                         string
	Delta, Gamma, Vega, Theta, Reserve string
}

// CheckOptions enforces immutable equity fractions: delta .20, gamma .05,
// vega .10, theta .02/day. For each underlying every dollar sensitivity and the
// reserved loss must fit .10 of equity. Inputs must include all live positions,
// remaining independent orders and stock collateral. Invalid/unknown input
// fails closed. Returns every breach in deterministic order.
func CheckOptions(equity string, exposures []OptionExposure) ([]string, error) {
	e, err := amount(equity)
	if err != nil || e.Sign() <= 0 {
		return nil, errors.New("invalid option risk equity")
	}
	totals := [5]*big.Rat{}
	for i := range totals {
		totals[i] = new(big.Rat)
	}
	byAsset := map[string][5]*big.Rat{}
	for _, v := range exposures {
		if v.Underlying == "" {
			return nil, errors.New("missing option underlying")
		}
		row, ok := byAsset[v.Underlying]
		if !ok {
			for i := range row {
				row[i] = new(big.Rat)
			}
		}
		for i, s := range []string{v.Delta, v.Gamma, v.Vega, v.Theta, v.Reserve} {
			n, err := amount(s)
			if err != nil {
				return nil, err
			}
			totals[i].Add(totals[i], n)
			row[i].Add(row[i], n)
		}
		byAsset[v.Underlying] = row
	}
	reasons := []string{}
	for i, pct := range []int64{20, 5, 10, 2} {
		if totals[i].Cmp(new(big.Rat).Mul(e, big.NewRat(pct, 100))) > 0 {
			reasons = append(reasons, []string{"risk_option_delta", "risk_option_gamma", "risk_option_vega", "risk_option_theta"}[i])
		}
	}
	assets := make([]string, 0, len(byAsset))
	for a := range byAsset {
		assets = append(assets, a)
	}
	slices.Sort(assets)
	cap := new(big.Rat).Mul(e, big.NewRat(10, 100))
	for _, a := range assets {
		for _, v := range byAsset[a] {
			if v.Cmp(cap) > 0 {
				reasons = append(reasons, "risk_option_underlying:"+a)
				break
			}
		}
	}
	return reasons, nil
}
