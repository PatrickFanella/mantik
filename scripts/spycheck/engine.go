package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

// spyHistoricalSource is a computation fixture, with synthetic availability
// clocks. It never changes retained source observations or claims historical
// provider acquisition. The run checks engine accounting, not signal knowledge.
type spyHistoricalSource struct {
	asset   string
	bars    []storage.DailyBar
	actions []marketdata.CorporateAction
}

func (s spyHistoricalSource) AdjustedBars(ctx context.Context, a string, at time.Time) (marketdata.Series, error) {
	if e := ctx.Err(); e != nil {
		return marketdata.Series{}, e
	}
	bars := []storage.DailyBar{}
	for _, b := range s.bars {
		if !b.AvailableAt.After(at) {
			bars = append(bars, b)
		}
	}
	return marketdata.Adjust(a, bars, s.actions, at)
}

type spyHold struct{ asset string }

func (s spyHold) Spec() strategy.Spec {
	return strategy.Spec{Name: "spy-buy-and-hold", Version: "next-open-unlevered-v1", Params: map[string]string{"asset": s.asset}, Data: []string{strategy.KindDailyBars}}
}
func (s spyHold) Decide(strategy.View) (strategy.Decision, error) {
	return strategy.Decision{Targets: []strategy.Target{{Asset: s.asset, Weight: "1"}}}, nil
}

type oracleDay struct{ Date, Return, TWR string }
type oracleRun struct {
	Days             []oracleDay
	FinalEquity, TWR string
}
type engineOracle struct {
	Version, InputSHA256, InitialCash, DepositAmount string
	EveryWeeks                                       int
	ZeroCost, ModeledCost                            oracleRun
}

// custom JSON keys match the standalone Python reference; decimals stay text.
func (o *engineOracle) UnmarshalJSON(raw []byte) error {
	var w struct {
		Version string    `json:"version"`
		Input   string    `json:"input_sha256"`
		Initial string    `json:"initial_cash"`
		Deposit string    `json:"deposit_amount"`
		Weeks   int       `json:"every_weeks"`
		Zero    oracleRun `json:"zero_cost"`
		Cost    oracleRun `json:"modeled_cost"`
	}
	if e := json.Unmarshal(raw, &w); e != nil {
		return e
	}
	*o = engineOracle{w.Version, w.Input, w.Initial, w.Deposit, w.Weeks, w.Zero, w.Cost}
	return nil
}
func (o *oracleRun) UnmarshalJSON(raw []byte) error {
	var w struct {
		Days   []oracleDay `json:"days"`
		Equity string      `json:"final_equity"`
		TWR    string      `json:"twr"`
	}
	if e := json.Unmarshal(raw, &w); e != nil {
		return e
	}
	*o = oracleRun{w.Days, w.Equity, w.TWR}
	return nil
}

// engineComparison keeps complete computation results outside Git in the report.
type engineComparison struct {
	MaximumEquityErrorUSD, DollarToleranceUSD                                                                                      string
	AlignedSPYMultiple, EntryClose, FirstFillOpen, ZeroCostSPYErrorBps, ActualSPYErrorBps, ModeledCostRelativeBps, SPYToleranceBps string
	Version, InputSHA256, ReferenceSHA256, EvidenceClass, PriceBasis, ClockPolicy                                                  string
	FirstSession, LastSession                                                                                                      string
	Sessions                                                                                                                       int
	Cash, Margin, ZeroCost                                                                                                         engine.Result
	CashMarginEqual                                                                                                                bool
	MaximumDailyErrorBps, CostDragBps                                                                                              string
	Passed                                                                                                                         bool
}

func rat(s string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("invalid reference decimal")
	}
	return r, nil
}
func compareEngine(in comparisonInput, inputRaw, referenceRaw []byte) (engineComparison, error) {
	var oracle engineOracle
	if e := json.Unmarshal(referenceRaw, &oracle); e != nil {
		return engineComparison{}, e
	}
	if oracle.Version != "spy-next-open-decimal-receivables-v2" || oracle.InputSHA256 != digest(inputRaw) || oracle.InitialCash != "2000" || oracle.DepositAmount != "500" || oracle.EveryWeeks != 2 {
		return engineComparison{}, errors.New("SPY engine oracle identity or configuration mismatch")
	}
	if len(in.Calendar) < 2 || in.Calendar[0] != "2016-01-04" || len(in.SessionCloses) != len(in.Raw) {
		return engineComparison{}, errors.New("SPY engine check requires complete 2016-to-date projection")
	}
	zone, e := time.LoadLocation("America/New_York")
	if e != nil {
		return engineComparison{}, e
	}
	bars := slices.Clone(in.Raw)
	for i := range bars {
		day, e := time.Parse(time.DateOnly, bars[i].Date)
		if e != nil {
			return engineComparison{}, e
		}
		known := time.Date(day.Year(), day.Month(), day.Day(), 16, 15, 0, 0, zone)
		bars[i].Close = in.SessionCloses[i].Price
		bars[i].AvailableAt = known
		bars[i].ObservedAt = known
		bars[i].PublishedAt = known
	}
	actions := slices.Clone(in.Actions)
	for i := range actions {
		day, e := time.Parse(time.DateOnly, actions[i].ExDate)
		if e != nil {
			return engineComparison{}, e
		}
		actions[i].KnownAt = day
	}
	src := spyHistoricalSource{in.AssetID, bars, actions}
	end, e := time.Parse(time.DateOnly, in.Calendar[len(in.Calendar)-1])
	if e != nil {
		return engineComparison{}, e
	}
	chi, e := time.LoadLocation("America/Chicago")
	if e != nil {
		return engineComparison{}, e
	}
	deposits := engine.DefaultDeposits
	cfg := engine.Config{Strategy: spyHold{in.AssetID}, Source: src, Assets: []string{in.AssetID}, Start: time.Date(2016, 1, 1, 0, 0, 0, 0, chi), End: end.Add(23 * time.Hour), InitialCash: "2000", Deposits: &deposits, Cost: engine.DefaultCost, Account: account.Cash, Unlevered: true}
	cash, e := engine.Run(context.Background(), cfg)
	if e != nil {
		return engineComparison{}, e
	}
	cfg.Account = account.Margin
	margin, e := engine.Run(context.Background(), cfg)
	if e != nil {
		return engineComparison{}, e
	}
	cfg.Account = account.Cash
	cfg.Cost = engine.CostModel{Version: "reference-zero-v1", FeeBps: "0", SlippageBps: "0"}
	zero, e := engine.Run(context.Background(), cfg)
	if e != nil {
		return engineComparison{}, e
	}
	result := engineComparison{Version: "spy-engine-next-open-receivables-v2", InputSHA256: digest(inputRaw), ReferenceSHA256: digest(referenceRaw), EvidenceClass: "historical_market_unqualified", PriceBasis: "manifest_verified_primary_close_with_explained_gap", ClockPolicy: "synthetic_historical_availability_for_accounting_only", FirstSession: in.Calendar[0], LastSession: in.Calendar[len(in.Calendar)-1], Sessions: len(in.Calendar), Cash: cash, Margin: margin, ZeroCost: zero, CashMarginEqual: true, Passed: true}
	maxError := new(big.Rat)
	maxEquityError := new(big.Rat)
	for _, tc := range []struct {
		actual    engine.Result
		reference oracleRun
	}{{cash, oracle.ModeledCost}, {margin, oracle.ModeledCost}, {zero, oracle.ZeroCost}} {
		if len(tc.actual.Days) != len(in.Calendar) || len(tc.reference.Days) != len(in.Calendar) || len(tc.actual.Refusals) != 0 {
			return result, errors.New("SPY engine sessions or refusals differ")
		}
		equityError, err := compareOracleEnd(tc.actual, tc.reference)
		if err != nil {
			return result, err
		}
		if equityError.Cmp(maxEquityError) > 0 {
			maxEquityError.Set(equityError)
		}
		for i, d := range tc.actual.Days {
			r := tc.reference.Days[i]
			if d.Date != in.Calendar[i] || r.Date != d.Date {
				return result, errors.New("SPY engine dates differ")
			}
			for _, pair := range [][2]string{{d.Return, r.Return}, {d.TWR, r.TWR}} {
				a, e := rat(pair[0])
				if e != nil {
					return result, e
				}
				b, e := rat(pair[1])
				if e != nil {
					return result, e
				}
				difference := new(big.Rat).Abs(new(big.Rat).Sub(a, b))
				difference.Mul(difference, big.NewRat(10000, 1))
				if difference.Cmp(maxError) > 0 {
					maxError.Set(difference)
				}
			}
			cashValue, e := rat(d.Cash)
			if e != nil || cashValue.Sign() < 0 {
				return result, errors.New("unlevered run borrowed cash")
			}
		}
	}
	for i, d := range cash.Days {
		m := margin.Days[i]
		if d.Equity != m.Equity || d.Cash != m.Cash || d.Return != m.Return || d.TWR != m.TWR {
			result.CashMarginEqual = false
		}
	}
	a, _ := rat(zero.Days[len(zero.Days)-1].TWR)
	b, _ := rat(cash.Days[len(cash.Days)-1].TWR)
	drag := new(big.Rat).Sub(a, b)
	drag.Mul(drag, big.NewRat(10000, 1))
	result.MaximumDailyErrorBps = maxError.FloatString(9)
	result.MaximumEquityErrorUSD = maxEquityError.FloatString(9)
	result.DollarToleranceUSD = "0.01"
	result.CostDragBps = drag.FloatString(9)
	// Arithmetic tolerance is 0.0001bp, fixed before running real data. The
	// separate zero-cost run reports modeled implementation drag explicitly.
	independent, e := rat(in.PrimaryReferenceReturn)
	if e != nil || independent.Sign() <= 0 {
		return result, errors.New("independent SPY total return unavailable")
	}
	if len(cash.Fills) == 0 || cash.Fills[0].Date != in.Calendar[1] {
		return result, errors.New("SPY entry did not use the first executable session")
	}
	entryClose, e := rat(in.SessionCloses[0].Price)
	if e != nil {
		return result, e
	}
	entryOpen, e := rat(cash.Fills[0].Open)
	if e != nil || entryOpen.Sign() <= 0 {
		return result, errors.New("invalid SPY entry open")
	}
	aligned := new(big.Rat).Mul(independent, new(big.Rat).Quo(entryClose, entryOpen))
	zeroGrowth := new(big.Rat).Add(a, big.NewRat(1, 1))
	actualGrowth := new(big.Rat).Add(b, big.NewRat(1, 1))
	relative := func(x, y *big.Rat) *big.Rat {
		return new(big.Rat).Mul(new(big.Rat).Quo(new(big.Rat).Sub(x, y), aligned), big.NewRat(10000, 1))
	}
	zeroError := new(big.Rat).Abs(relative(zeroGrowth, aligned))
	actualError := new(big.Rat).Abs(relative(actualGrowth, aligned))
	modeled := relative(zeroGrowth, actualGrowth)
	result.AlignedSPYMultiple = aligned.FloatString(12)
	result.EntryClose = entryClose.FloatString(12)
	result.FirstFillOpen = entryOpen.FloatString(12)
	result.ZeroCostSPYErrorBps = zeroError.FloatString(9)
	result.ActualSPYErrorBps = actualError.FloatString(9)
	result.ModeledCostRelativeBps = modeled.FloatString(9)
	result.SPYToleranceBps = "5"
	result.Passed = result.CashMarginEqual && maxError.Cmp(big.NewRat(1, 10000)) <= 0 && drag.Sign() >= 0 && zeroError.Cmp(big.NewRat(5, 1)) <= 0 && actualError.Cmp(new(big.Rat).Add(modeled, big.NewRat(5, 1))) <= 0
	if !result.Passed {
		return result, fmt.Errorf("SPY engine/reference comparison failed")
	}
	return result, nil
}

func compareOracleEnd(actual engine.Result, reference oracleRun) (*big.Rat, error) {
	if len(actual.Days) == 0 {
		return nil, errors.New("missing final equity")
	}
	a, e := rat(actual.Days[len(actual.Days)-1].Equity)
	if e != nil {
		return nil, e
	}
	b, e := rat(reference.FinalEquity)
	if e != nil {
		return nil, e
	}
	difference := new(big.Rat).Abs(new(big.Rat).Sub(a, b))
	if difference.Cmp(big.NewRat(1, 100)) > 0 {
		return difference, errors.New("SPY engine/reference final equity differs by more than one cent")
	}
	last, e := rat(actual.Days[len(actual.Days)-1].TWR)
	if e != nil {
		return nil, e
	}
	want, e := rat(reference.TWR)
	if e != nil {
		return nil, e
	}
	if new(big.Rat).Abs(new(big.Rat).Sub(last, want)).Cmp(big.NewRat(1, 100000000)) > 0 {
		return difference, errors.New("SPY reference final TWR differs")
	}
	return difference, nil
}
