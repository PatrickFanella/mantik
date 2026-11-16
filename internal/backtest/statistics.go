package backtest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// Statistic carries an explicit unavailability reason; missing evidence is not zero.
type Statistic struct {
	Value  *float64 `json:"value"`
	Reason string   `json:"reason,omitempty"`
}

func missing(reason string) Statistic { return Statistic{Reason: reason} }

var fixedDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,15})(\.[0-9]{1,12})?$`)

func decimal(s string) (*big.Rat, bool) {
	if !fixedDecimal.MatchString(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func value(v float64) Statistic {
	if !finite(v) {
		return missing("non_finite")
	}
	return Statistic{Value: new(v)}
}

// Moments uses the sample standard deviation for Sharpe and population central
// standardized moments for skewness and Pearson (not excess) kurtosis.
type Moments struct {
	N                          int
	Sharpe, Skewness, Kurtosis float64
}

// ReturnMoments computes per-observation, nonannualized moments.
func ReturnMoments(xs []float64) (Moments, error) {
	if len(xs) < 4 {
		return Moments{}, errors.New("too few returns")
	}
	mean := 0.0
	for _, x := range xs {
		if !finite(x) || x < -1 {
			return Moments{}, errors.New("invalid return")
		}
		mean += x / float64(len(xs))
	}
	var v, m3, m4 float64
	for _, x := range xs {
		d := x - mean
		v += d * d
		m3 += d * d * d
		m4 += d * d * d * d
	}
	n := float64(len(xs))
	if v <= 0 {
		return Moments{}, errors.New("zero return variance")
	}
	return Moments{len(xs), mean / math.Sqrt(v/(n-1)), m3 / n / math.Pow(v/n, 1.5), m4 / n / math.Pow(v/n, 2)}, nil
}
func normalCDF(x float64) float64 { return .5 * math.Erfc(-x/math.Sqrt2) }

// invNormal uses bisection of the standard-library normal CDF, avoiding an
// external statistics dependency. The declared trial bound keeps tails finite.
func invNormal(p float64) float64 {
	lo, hi := -12.0, 12.0
	for i := 0; i < 100; i++ {
		m := (lo + hi) / 2
		if normalCDF(m) < p {
			lo = m
		} else {
			hi = m
		}
	}
	return (lo + hi) / 2
}

// DeflatedSharpe implements Bailey/Lopez de Prado (2014), equations 1-3.
// TrialSharpeSD is the cross-trial standard deviation in the SAME frequency
// as m.Sharpe. N is the registry search total, including unfinished attempts;
// treating all starts as independent is an explicit conservative assumption.
func DeflatedSharpe(m Moments, n int64, trialSharpeSD float64) Statistic {
	if m.N < 4 || n < 1 || n > 1000000000 || !finite(m.Sharpe) || !finite(m.Skewness) || !finite(m.Kurtosis) || m.Kurtosis < 1 || !finite(trialSharpeSD) || trialSharpeSD < 0 {
		return missing("invalid_moments_or_search_count")
	}
	if n > 1 && trialSharpeSD == 0 {
		return missing("trial_dispersion_unavailable")
	}
	benchmark := 0.0
	if n > 1 {
		const gamma = .5772156649015329
		f := float64(n)
		benchmark = trialSharpeSD * ((1-gamma)*invNormal(1-1/f) + gamma*invNormal(1-1/(f*math.E)))
	}
	variance := 1 - m.Skewness*m.Sharpe + (m.Kurtosis-1)*m.Sharpe*m.Sharpe/4
	if variance <= 0 {
		return missing("invalid_sharpe_variance")
	}
	return value(normalCDF((m.Sharpe - benchmark) * math.Sqrt(float64(m.N-1)) / math.Sqrt(variance)))
}

// CSCV enumerates every half-partition combination using Sharpe for selection.
// Rows are synchronous sessions, columns distinct trial configurations. Ties in
// IS are resolved by column order; OOS ties use average ranks, as declared here.
// At least four candidates, four even partitions, and two rows/partition are
// required. Missing, ragged, constant or indivisible inputs are unavailable.
func CSCV(rows [][]float64, partitions int) Statistic {
	if len(rows) < 8 || partitions < 4 || partitions > 12 || partitions%2 != 0 || len(rows)%partitions != 0 || len(rows)/partitions < 2 {
		return missing("insufficient_or_unbalanced_partitions")
	}
	n := len(rows[0])
	if n < 4 || n > 128 {
		return missing("too_few_or_many_trials")
	}
	comb := 1
	for i := 0; i < partitions/2; i++ {
		comb = comb * (partitions - i) / (i + 1)
	}
	if len(rows) > 20000 || int64(len(rows))*int64(n)*int64(comb) > 20000000 {
		return missing("work_limit")
	}
	for _, r := range rows {
		if len(r) != n {
			return missing("unaligned_returns")
		}
		for _, v := range r {
			if !finite(v) || v < -1 {
				return missing("invalid_return")
			}
		}
	}
	selected := make([]bool, partitions)
	total, bad := 0, 0
	invalid := false
	var visit func(int, int)
	visit = func(start, left int) {
		if left > 0 {
			for i := start; i <= partitions-left; i++ {
				selected[i] = true
				visit(i+1, left-1)
				selected[i] = false
			}
			return
		}
		is, oos := make([]float64, n), make([]float64, n)
		winner := 0
		for j := 0; j < n; j++ {
			a, b := []float64{}, []float64{}
			for i, r := range rows {
				if selected[i/(len(rows)/partitions)] {
					a = append(a, r[j])
				} else {
					b = append(b, r[j])
				}
			}
			ma, ea := ReturnMoments(a)
			mb, eb := ReturnMoments(b)
			if ea != nil || eb != nil {
				invalid = true
				return
			}
			is[j] = ma.Sharpe
			oos[j] = mb.Sharpe
			if is[j] > is[winner] {
				winner = j
			}
		}
		lower, equal := 0, 0
		for _, v := range oos {
			if v < oos[winner] {
				lower++
			}
			if v == oos[winner] {
				equal++
			}
		}
		rank := float64(lower) + (float64(equal)+1)/2
		omega := rank / float64(n+1)
		if omega <= .5 {
			bad++
		}
		total++
	}
	visit(0, partitions/2)
	if invalid || total == 0 {
		return missing("undefined_partition_sharpe")
	}
	return value(float64(bad) / float64(total))
}

// Benchmark is an explicitly dated reference series with retained input identity.
type Benchmark struct {
	Name, DataSHA256 string
	Returns          []BenchmarkReturn
}

// BenchmarkReturn identifies each reference session without implying equity.
type BenchmarkReturn struct{ Date, Return string }

// Assessment reports diagnostics and the predeclared per-strategy tradeability.
type Assessment struct {
	TrialID, Family                 string
	SearchTotal                     int64
	DSR, PBO, CashExcess, SPYExcess Statistic
	Minimum                         *TradeabilityReport
	MinimumReason                   string
	CashBenchmark, SPYBenchmark     Benchmark
}

// Assess uses the complete retained family snapshot. All starts contribute to N;
// all completed comparable configurations contribute to dispersion and CSCV.
// Failed/unfinished comparable runs make CSCV unavailable, never zero.
func Assess(ctx context.Context, store strategytrial.Store, target string, cash, spy Benchmark, partitions int) (Assessment, error) {
	r, err := store.StrategyTrial(ctx, target)
	if err != nil {
		return Assessment{}, err
	}
	out := Assessment{TrialID: target, Family: r.Start.Family, DSR: missing("family_history_unavailable"), PBO: missing("family_history_unavailable"), CashExcess: missing("benchmark_unavailable"), SPYExcess: missing("benchmark_unavailable"), MinimumReason: "not_declared_before_run", CashBenchmark: cash, SPYBenchmark: spy}
	reader, ok := store.(strategytrial.FamilyReader)
	if !ok {
		return out, nil
	}
	family, err := reader.StrategyTrials(ctx, r.Start.Family)
	if err != nil {
		return out, err
	}
	out.SearchTotal = int64(len(family))
	found := false
	for _, v := range family {
		if v.Start.ID == target {
			r = v
			found = true
		}
	}
	if !found {
		return out, errors.New("target missing from family snapshot")
	}
	var cfg struct {
		Experiment struct{ Minimum *TradeabilityPlan }
	}
	if err = json.Unmarshal(r.Start.Config, &cfg); err != nil {
		return out, err
	}
	if cfg.Experiment.Minimum != nil {
		plan := *cfg.Experiment.Minimum
		if plan.MarksAt.After(r.Start.StartedAt) {
			return out, errors.New("minimum trade marks were not declared before execution")
		}
		if plan.StrategyID != r.Start.StrategyID {
			return out, errors.New("retained minimum declaration has wrong strategy identity")
		}
		v, err := Tradeability(plan)
		if err != nil {
			return out, err
		}
		out.Minimum = &v
		out.MinimumReason = ""
	}
	if r.Outcome == nil || r.Outcome.Status != "completed" {
		out.DSR = missing("target_incomplete")
		out.PBO = missing("target_incomplete")
		return out, nil
	}
	xs := []float64{}
	for _, d := range r.Returns {
		x, err := strconv.ParseFloat(d.Return, 64)
		if err != nil {
			return out, err
		}
		xs = append(xs, x)
	}
	out.CashExcess = benchmarkExcess(r.Returns, cash)
	out.SPYExcess = benchmarkExcess(r.Returns, spy)
	m, err := ReturnMoments(xs)
	if err != nil {
		out.DSR = missing("target_variance_unavailable")
		out.PBO = missing("target_variance_unavailable")
		return out, nil
	}
	configKey := func(raw []byte) (string, error) {
		var v map[string]json.RawMessage
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		delete(v, "experiment")
		data, err := json.Marshal(v)
		return string(data), err
	}
	key, err := configKey(r.Start.Config)
	if err != nil {
		return out, err
	}
	records := []strategytrial.Record{}
	srs := []float64{}
	seen := map[string]strategytrial.Record{}
	incomplete := false
	for _, v := range family {
		otherKey, err := configKey(v.Start.Config)
		if err != nil {
			return out, err
		}
		if otherKey != key || v.Start.DataSHA256 != r.Start.DataSHA256 {
			continue
		}
		if v.Outcome == nil || v.Outcome.Status != "completed" {
			incomplete = true
			continue
		}
		if len(v.Returns) != len(r.Returns) {
			return out, errors.New("comparable completed trial has different session count")
		}
		a := []float64{}
		for i, d := range v.Returns {
			if d.Date != r.Returns[i].Date {
				return out, errors.New("comparable completed trial has different dates")
			}
			x, err := strconv.ParseFloat(d.Return, 64)
			if err != nil {
				return out, err
			}
			a = append(a, x)
		}
		if previous, ok := seen[v.Start.StrategyID]; ok {
			before, _ := json.Marshal(previous.Returns)
			after, _ := json.Marshal(v.Returns)
			if string(before) != string(after) {
				out.DSR = missing("nondeterministic_duplicate")
				out.PBO = out.DSR
				return out, nil
			}
			continue
		}
		seen[v.Start.StrategyID] = v
		mm, err := ReturnMoments(a)
		if err != nil {
			out.DSR = missing("comparison_variance_unavailable")
			out.PBO = out.DSR
			return out, nil
		}
		srs = append(srs, mm.Sharpe)
		records = append(records, v)
	}
	sd := 0.0
	if len(srs) > 1 {
		mean := 0.0
		for _, x := range srs {
			mean += x / float64(len(srs))
		}
		for _, x := range srs {
			sd += (x - mean) * (x - mean)
		}
		sd = math.Sqrt(sd / float64(len(srs)-1))
	}
	out.DSR = DeflatedSharpe(m, out.SearchTotal, sd)
	out.PBO = missing("too_few_trials")
	if incomplete {
		out.PBO = missing("incomplete_comparable_trials")
	} else if len(records) >= 4 {
		rows := make([][]float64, len(xs))
		for i := range rows {
			for _, record := range records {
				x, _ := strconv.ParseFloat(record.Returns[i].Return, 64)
				rows[i] = append(rows[i], x)
			}
		}
		out.PBO = CSCV(rows, partitions)
	}
	return out, nil
}
func benchmarkExcess(xs []strategytrial.DailyReturn, b Benchmark) Statistic {
	if b.Name == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(b.DataSHA256) || len(xs) == 0 || len(xs) != len(b.Returns) {
		return missing("benchmark_unaligned_or_unidentified")
	}
	a, c := 1.0, 1.0
	for i, d := range xs {
		r, err := strconv.ParseFloat(d.Return, 64)
		v, e := strconv.ParseFloat(b.Returns[i].Return, 64)
		if err != nil || e != nil || !finite(r) || !finite(v) || r < -1 || v < -1 || d.Date != b.Returns[i].Date {
			return missing("benchmark_unaligned_or_invalid")
		}
		a *= 1 + r
		c *= 1 + v
	}
	return value(a - c)
}

// TradeabilityPlan is reviewed minimum order sizing and marked evidence declared
// before execution. It is bound to a content-addressed strategy, not inferred
// from a profitable run. Minimums are valid only for these prices/units/reserves.
type TradeabilityPlan struct {
	StrategyID   string
	Account      account.Type
	Requirements []TradeRequirement
	FixedReserve string
	MaximumCents int64
	MarksAt      time.Time
}

// TradeabilityReport is the minimum balance per declared strategy/version.
type TradeabilityReport struct {
	Plan           TradeabilityPlan
	MinimumBalance string
	RuleVersion    string
}

// Tradeability evaluates a predeclared basket using the shared account rules.
func Tradeability(p TradeabilityPlan) (TradeabilityReport, error) {
	if !regexp.MustCompile(`^strategy-[a-f0-9]{64}$`).MatchString(p.StrategyID) || p.MarksAt.IsZero() {
		return TradeabilityReport{}, errors.New("tradeability needs strategy identity and dated marks")
	}
	minimum, err := MinimumBalance(p.Account, p.Requirements, p.FixedReserve, p.MaximumCents)
	if err != nil {
		return TradeabilityReport{}, err
	}
	return TradeabilityReport{p, minimum, "equity-account-rules-v1"}, nil
}

// TradeRequirement declares the smallest actual order quantities and marked
// prices required by a strategy, including fees and fixed option-risk reserves.
// The result applies only to these declared marks/units and account rule version.
type TradeRequirement struct{ Asset, Quantity, Price, Fee string }

// MinimumBalance finds the first cent that admits ALL required orders together
// under the existing account rules. Required settled capital is reported even
// for a margin configuration; existing holdings and market losses are excluded.
func MinimumBalance(kind account.Type, requirements []TradeRequirement, reserve string, maxCents int64) (string, error) {
	if !kind.Valid() || len(requirements) == 0 || len(requirements) > 1000 || maxCents < 1 || maxCents > 100000000000 {
		return "", errors.New("invalid trade requirements")
	}
	fixed, ok := decimal(reserve)
	if !ok || fixed.Sign() < 0 {
		return "", errors.New("invalid fixed reserve")
	}
	positions := []account.Position{}
	trades := []account.Trade{}
	seen := map[string]bool{}
	for _, r := range requirements {
		q, qok := decimal(r.Quantity)
		p, pok := decimal(r.Price)
		f, fok := decimal(r.Fee)
		if !qok || !pok || !fok || r.Asset == "" || seen[r.Asset] || q.Sign() == 0 || p.Sign() <= 0 || f.Sign() < 0 {
			return "", errors.New("invalid minimum trade")
		}
		seen[r.Asset] = true
		positions = append(positions, account.Position{Asset: r.Asset, Quantity: new(big.Rat), Price: p})
		trades = append(trades, account.Trade{Asset: r.Asset, Quantity: q, Price: p, Fee: f})
	}
	accepts := func(cents int64) bool {
		b := account.New(kind, new(big.Rat).Sub(big.NewRat(cents, 100), fixed))
		if b == nil {
			return false
		}
		ps := slices.Clone(positions)
		day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
		for i, t := range trades {
			if b.Execute(ps, t, account.Overnight, day, account.SettlementDate(day)) != "" {
				return false
			}
			ps[i].Quantity = new(big.Rat).Set(t.Quantity)
		}
		return true
	}
	if !accepts(maxCents) {
		return "", errors.New("not tradable within search bound")
	}
	lo, hi := int64(1), maxCents
	for lo < hi {
		mid := lo + (hi-lo)/2
		if accepts(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return big.NewRat(lo, 100).FloatString(2), nil
}
