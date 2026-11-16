package research

import (
	"fmt"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
)

// recipeSignal projects the reviewed library's signal formula into the reviewed
// expression vocabulary for structural comparison only. Execution still uses
// library.Stock: scheduling, volatility sizing, universe selection and turnover
// are additional behavior tokens, not claims of equivalent expression execution.
func recipeSignal(c library.Config) *spec.Expr {
	closePrice := &spec.Expr{Op: "field", Field: "close"}
	price := closePrice
	if c.Skip > 0 {
		price = &spec.Expr{Op: "lag", Window: c.Skip, Args: []*spec.Expr{price}}
	}
	if c.Kind == library.LowVol {
		return &spec.Expr{Op: "sub", Args: []*spec.Expr{{Op: "const"}, {Op: "std", Window: c.VolWindow, Args: []*spec.Expr{{Op: "return", Window: 1, Args: []*spec.Expr{closePrice}}}}}}
	}
	var total *spec.Expr
	for _, window := range c.Lookbacks {
		n := &spec.Expr{Op: "return", Window: window, Args: []*spec.Expr{price}}
		if c.Kind == library.Trend {
			// A positive cash rate subtracts a positive hurdle. The signature
			// records its sign; this is not a return or price calculation.
			if c.CashRate > 0 {
				n = &spec.Expr{Op: "sub", Args: []*spec.Expr{n, {Op: "const", Value: 1}}}
			}
			n = &spec.Expr{Op: "sign", Args: []*spec.Expr{n}}
		} else if c.Kind == library.Reversal {
			n = &spec.Expr{Op: "sub", Args: []*spec.Expr{{Op: "const"}, n}}
		}
		if total == nil {
			total = n
		} else {
			total = &spec.Expr{Op: "add", Args: []*spec.Expr{total, n}}
		}
	}
	if len(c.Lookbacks) > 1 {
		total = &spec.Expr{Op: "div", Args: []*spec.Expr{total, {Op: "const", Value: float64(len(c.Lookbacks))}}}
	}
	return total
}

func definitionTokens(d spec.Definition) map[string]int {
	out := map[string]int{}
	if d.Library == nil {
		expressionTokens(d.Score, out)
	} else {
		c := *d.Library
		// Decode through a sorted copy for deterministic recipe ordering.
		c.Lookbacks = append([]int(nil), c.Lookbacks...)
		slices.Sort(c.Lookbacks)
		expressionTokens(recipeSignal(c), out)
		out["library:"+c.Kind]++
		out["cadence:"+c.Cadence]++
		out[fmt.Sprintf("count:%d", c.Count)]++
		out[fmt.Sprintf("risk_window:%d", (c.VolWindow+2)/5)]++
		out[fmt.Sprintf("cash_hurdle:%d", int(c.CashRate*100+.5))]++
		out[fmt.Sprintf("turnover:%d", int(c.Turnover*100+.5))]++
	}
	policyTokens(d, out)
	return out
}

func expressionComplexity(n *spec.Expr) int {
	units := 10
	if n.Window > 0 {
		units += (n.Window + 20) / 21
	}
	for _, a := range n.Args {
		units += expressionComplexity(a)
	}
	return units
}

func penalties(d spec.Definition) (complexity, similarity int, nearest string) {
	if d.Library != nil {
		complexity = expressionComplexity(recipeSignal(*d.Library)) + 60
	} else {
		complexity = expressionComplexity(d.Score)
	}
	tokens := definitionTokens(d)
	for _, kind := range []string{library.Trend, library.Momentum, library.LowVol, library.Reversal} {
		c := library.Default(kind)
		other := spec.Definition{Name: "canonical", Library: &c, Gross: c.Gross}
		units := tokenSimilarity(tokens, definitionTokens(other))
		if nearest == "" || units > similarity {
			similarity, nearest = units, kind
		}
	}
	return complexity, similarity, nearest
}
