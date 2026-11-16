// Package executioncost shares exact fee and adverse-slippage arithmetic between
// daily replay and quote-based incubation. Inputs are declared assumptions.
package executioncost

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
)

// Model pins the cost method and basis-point assumptions used by a strategy.
type Model struct {
	Version     string `json:"version"`
	FeeBps      string `json:"fee_bps"`
	SlippageBps string `json:"slippage_bps"`
}

var decimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var version = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Parse returns fee and slippage fractions. Values must be below 100 percent.
func (m Model) Parse() (fee, slip *big.Rat, err error) {
	fee, ok := new(big.Rat).SetString(m.FeeBps)
	slip, sok := new(big.Rat).SetString(m.SlippageBps)
	if !version.MatchString(m.Version) || !decimal.MatchString(m.FeeBps) || !decimal.MatchString(m.SlippageBps) || !ok || !sok || fee.Cmp(big.NewRat(10000, 1)) >= 0 || slip.Cmp(big.NewRat(10000, 1)) >= 0 {
		return nil, nil, errors.New("invalid execution cost model")
	}
	return fee.Quo(fee, big.NewRat(10000, 1)), slip.Quo(slip, big.NewRat(10000, 1)), nil
}

// Price applies adverse slippage to an independent copy of a reference price.
func Price(reference, slip *big.Rat, buy bool) *big.Rat {
	move := new(big.Rat).Mul(reference, slip)
	p := new(big.Rat).Set(reference)
	if buy {
		return p.Add(p, move)
	}
	return p.Sub(p, move)
}

// Default returns the initial zero-commission, five-basis-point cost assumptions.
func Default() Model { return Model{Version: "next-open-bps-v1", FeeBps: "0", SlippageBps: "5"} }

// FromDeclaration reads frozen gate costs. Only a forward-only strategy without
// historical costs may use the initial assumptions. JSON null means absent.
func FromDeclaration(raw []byte, allowDefault bool) (Model, error) {
	var model Model
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if !allowDefault {
			return model, errors.New("gate-declared execution costs required")
		}
		model = Default()
	} else if e := json.Unmarshal(raw, &model); e != nil {
		return model, e
	}
	_, _, e := model.Parse()
	return model, e
}
