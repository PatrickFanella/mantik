package marketdata

import (
	"errors"
	"math/big"
	"time"
)

// ReferenceClose is a separately acquired adjusted daily close.
type ReferenceClose struct {
	Date  string `json:"date"`
	Close string `json:"close"`
}

// CloseDifference lists every common date outside the declared relative tolerance.
type CloseDifference struct {
	Date        string `json:"date"`
	Actual      string `json:"actual"`
	Reference   string `json:"reference"`
	BasisPoints string `json:"basis_points"`
}

// Comparison distinguishes exchange-calendar coverage from numerical agreement.
// Total-return multiples use the endpoints of the declared calendar interval.
// Pay-date reinvestment and adjusted-close ratios have different conventions.
type Comparison struct {
	Passed                  bool              `json:"passed"`
	Sessions                int               `json:"sessions"`
	Compared                int               `json:"compared"`
	ToleranceBPS            string            `json:"tolerance_bps"`
	MissingBars             []string          `json:"missing_bars"`
	MissingReference        []string          `json:"missing_reference"`
	UnexpectedBars          []string          `json:"unexpected_bars"`
	UnexpectedReference     []string          `json:"unexpected_reference"`
	Differences             []CloseDifference `json:"differences"`
	MaxDifferenceBPS        string            `json:"max_difference_bps"`
	MantikPayDateReturn     string            `json:"mantik_pay_date_return_multiple,omitempty"`
	ReferencePayDateReturn  string            `json:"reference_pay_date_return_multiple"`
	ReturnDifferenceBPS     string            `json:"return_difference_bps"`
	ReferenceAdjustedReturn string            `json:"reference_adjusted_return_multiple,omitempty"`
}

// CompareCloses refuses invalid/duplicate dates and prices, and reports all
// missing or extra sessions. It never silently aligns away a coverage gap.
func CompareCloses(series Series, reference []ReferenceClose, calendar []string, toleranceBPS string, referencePayDateReturn string) (Comparison, error) {
	out := Comparison{ToleranceBPS: toleranceBPS, Sessions: len(calendar), MissingBars: []string{}, MissingReference: []string{}, UnexpectedBars: []string{}, UnexpectedReference: []string{}, Differences: []CloseDifference{}}
	tol, ok := positive(toleranceBPS)
	if !ok || tol.Cmp(big.NewRat(10000, 1)) > 0 || len(calendar) == 0 || len(calendar) > 10000 {
		return out, errors.New("comparison needs a calendar and positive tolerance at most 10000 bps")
	}
	dates := map[string]bool{}
	for i, d := range calendar {
		if _, err := time.Parse(time.DateOnly, d); err != nil || dates[d] || i > 0 && d <= calendar[i-1] {
			return out, errors.New("comparison calendar must be ordered unique dates")
		}
		dates[d] = true
	}
	actual := map[string]AdjustedBar{}
	refs := map[string]*big.Rat{}
	for _, b := range series.Bars {
		if _, err := time.Parse(time.DateOnly, b.Date); err != nil {
			return out, err
		}
		if _, ok := positive(b.Close); !ok {
			return out, errors.New("invalid adjusted close")
		}
		if _, exists := actual[b.Date]; exists {
			return out, errors.New("duplicate adjusted close")
		}
		actual[b.Date] = b
		if !dates[b.Date] {
			out.UnexpectedBars = append(out.UnexpectedBars, b.Date)
		}
	}
	for _, b := range reference {
		v, ok := positive(b.Close)
		_, err := time.Parse(time.DateOnly, b.Date)
		if !ok || err != nil || refs[b.Date] != nil {
			return out, errors.New("invalid or duplicate reference close")
		}
		refs[b.Date] = v
		if !dates[b.Date] {
			out.UnexpectedReference = append(out.UnexpectedReference, b.Date)
		}
	}
	maxDiff := new(big.Rat)
	for _, d := range calendar {
		b, have := actual[d]
		r := refs[d]
		if !have {
			out.MissingBars = append(out.MissingBars, d)
		}
		if r == nil {
			out.MissingReference = append(out.MissingReference, d)
		}
		if !have || r == nil {
			continue
		}
		out.Compared++
		v, _ := positive(b.Close)
		diff := new(big.Rat).Sub(v, r)
		diff.Abs(diff)
		diff.Quo(diff, r)
		diff.Mul(diff, big.NewRat(10000, 1))
		if diff.Cmp(maxDiff) > 0 {
			maxDiff.Set(diff)
		}
		if diff.Cmp(tol) > 0 {
			out.Differences = append(out.Differences, CloseDifference{Date: d, Actual: b.Close, Reference: render(r), BasisPoints: render(diff)})
		}
	}
	out.MaxDifferenceBPS = render(maxDiff)
	first, last := calendar[0], calendar[len(calendar)-1]
	if refs[first] != nil && refs[last] != nil {
		out.ReferenceAdjustedReturn = render(new(big.Rat).Quo(refs[last], refs[first]))
	}
	if actual[first].TotalReturn != "" && actual[last].TotalReturn != "" {
		a, aok := positive(actual[first].TotalReturn)
		b, bok := positive(actual[last].TotalReturn)
		if !aok || !bok {
			return out, errors.New("invalid total return")
		}
		out.MantikPayDateReturn = render(new(big.Rat).Quo(b, a))
	}
	if out.MantikPayDateReturn == "" {
		return out, nil
	}
	referenceReturn, valid := positive(referencePayDateReturn)
	actualReturn, actualValid := positive(out.MantikPayDateReturn)
	if !valid || !actualValid {
		return out, errors.New("comparison requires both pay-date return multiples")
	}
	out.ReferencePayDateReturn = referencePayDateReturn
	returnDiff := new(big.Rat).Sub(actualReturn, referenceReturn)
	returnDiff.Abs(returnDiff)
	returnDiff.Quo(returnDiff, referenceReturn)
	returnDiff.Mul(returnDiff, big.NewRat(10000, 1))
	out.ReturnDifferenceBPS = render(returnDiff)
	out.Passed = returnDiff.Cmp(tol) <= 0 && out.Compared == len(calendar) && len(out.Differences) == 0 && len(out.UnexpectedBars) == 0 && len(out.UnexpectedReference) == 0
	return out, nil
}
