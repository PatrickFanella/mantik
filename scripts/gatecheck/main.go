// Command gatecheck records honest base-library gate decisions on retained real
// SPY prices. Historical availability/membership in this accounting fixture is
// synthetic: every decision fails those checks and grants no incubation budget.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/backtest"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

type source struct {
	asset     string
	bars      []marketdata.AdjustedBar
	reference time.Time
}

func (s source) AdjustedBars(_ context.Context, asset string, at time.Time) (marketdata.Series, error) {
	if asset != s.asset {
		return marketdata.Series{}, errors.New("fixture asset differs")
	}
	n := sort.Search(len(s.bars), func(i int) bool { return s.bars[i].Raw.AvailableAt.After(at) })
	return marketdata.Series{AssetID: asset, AsOf: at, Bars: s.bars[:n]}, nil
}
func (s source) StockUniverse(_ context.Context, at time.Time) (marketdata.UniverseReport, error) {
	u := marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: s.reference, PossiblySurvivorBiased: true, HistoricalTradabilityVerified: false, Members: []marketdata.UniverseMember{}}
	for i := len(s.bars) - 1; i >= 0; i-- {
		b := s.bars[i]
		if b.Raw.AvailableAt.Before(at) && b.Date < at.Format(time.DateOnly) {
			u.Members = append(u.Members, marketdata.UniverseMember{AssetID: s.asset, Symbol: "SPY", LastSession: b.Date, Close: b.Close, Sessions: i + 1})
			break
		}
	}
	return u, nil
}
func run() (err error) {
	input := flag.String("input", "", "retained spycheck normalized input")
	expected := flag.String("sha256", "", "reviewed normalized input SHA-256")
	database := flag.String("database-env", "MANTIK_GATECHECK_DATABASE_URL", "fresh disposable PostgreSQL URL environment variable")
	prefix := flag.String("prefix", "", "unique experiment identity")
	flag.Parse()
	if *input == "" || *prefix == "" || os.Getenv(*database) == "" || len(*expected) != 64 || flag.NArg() != 0 {
		return errors.New("gatecheck requires input, sha256, prefix and disposable database environment")
	}
	f, e := os.Open(*input)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	raw, e := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if e != nil {
		return e
	}
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	if len(raw) > 32<<20 || digest != *expected {
		return errors.New("gatecheck input hash/size differs")
	}
	var in struct {
		AssetID  string                       `json:"asset_id"`
		AsOf     time.Time                    `json:"as_of"`
		Raw      []storage.DailyBar           `json:"raw"`
		Actions  []marketdata.CorporateAction `json:"actions"`
		Calendar []string                     `json:"calendar"`
	}
	if e = json.Unmarshal(raw, &in); e != nil {
		return e
	}
	series, e := marketdata.Adjust(in.AssetID, in.Raw, in.Actions, in.AsOf)
	if e != nil {
		return e
	}
	if len(series.Bars) < 520 {
		return errors.New("gatecheck needs 520 complete retained sessions")
	}
	// Keep observed input clocks in the original hashed file. This separate replay
	// fixture explicitly assigns synthetic historical clocks, never qualification.
	bars := append([]marketdata.AdjustedBar(nil), series.Bars[len(series.Bars)-520:]...)
	dates := []string{}
	cash := strategygate.Reference{Name: "zero-rate-cash-scenario", DataSHA256: digest}
	spy := strategygate.Reference{Name: "retained-SPY-adjusted-close", DataSHA256: digest}
	for i := range bars {
		day, e := time.Parse(time.DateOnly, bars[i].Date)
		if e != nil {
			return e
		}
		known := day.Add(21 * time.Hour)
		bars[i].Raw.PublishedAt, bars[i].Raw.ObservedAt, bars[i].Raw.AvailableAt = known, known, known
		dates = append(dates, bars[i].Date)
		cash.Returns = append(cash.Returns, strategygate.ReferenceReturn{Date: bars[i].Date, Return: "0"})
		ret := "0"
		if i > 0 {
			var a, b float64
			if _, e = fmt.Sscan(bars[i].Close, &a); e != nil {
				return e
			}
			if _, e = fmt.Sscan(bars[i-1].Close, &b); e != nil {
				return e
			}
			ret = fmt.Sprintf("%.12f", a/b-1)
		}
		spy.Returns = append(spy.Returns, strategygate.ReferenceReturn{Date: bars[i].Date, Return: ret})
	}
	start, e := time.Parse(time.DateOnly, dates[0])
	if e != nil {
		return e
	}
	src := source{asset: in.AssetID, bars: bars, reference: start.Add(-time.Hour)}
	ctx := context.Background()
	store, e := storage.OpenPostgres(ctx, os.Getenv(*database))
	if e != nil {
		return e
	}
	defer store.Close()
	reports := []strategygate.Record{}
	families := map[string][]strategytrial.Record{}
	for _, kind := range []string{library.Trend, library.Momentum, library.Reversal, library.LowVol} {
		base, e := library.New(library.Default(kind))
		if e != nil {
			return e
		}
		sid, e := base.Spec().ID()
		if e != nil {
			return e
		}
		spec, e := json.Marshal(base.Spec())
		if e != nil {
			return e
		}
		d := strategygate.Declaration{EngineVersion: engine.Version, Account: "cash", ID: *prefix + "-" + kind, Family: "base-" + kind, ExperimentID: *prefix + "-" + kind, StrategyID: sid, BaseCandidate: "base", Balance: "2000", Policy: "backtest-gate-v1", Benchmark: "spy", Spec: spec, Calendar: dates, Train: 300, Test: 100, Purge: 5, Embargo: 5, Cash: cash, SPY: spy, DataSHA256: digest, Coverage: false, Availability: false}
		d.Assets = []string{in.AssetID}
		d.SettlementCalendar = "us-equity-t1-v1"
		d.BaseCost = []byte(`{"version":"base-v1","fee_bps":"1","slippage_bps":"5"}`)
		d.StressCost = []byte(`{"version":"stress-v1","fee_bps":"2","slippage_bps":"10"}`)
		cs := []backtest.Candidate{}
		for i, gross := range []float64{.7, .63, .77} {
			cfg := library.Default(kind)
			cfg.Gross = gross
			s, e := library.New(cfg)
			if e != nil {
				return e
			}
			name := "base"
			if i > 0 {
				name = fmt.Sprintf("neighbour%d", i)
			}
			variantID, e := s.Spec().ID()
			if e != nil {
				return e
			}
			plan := &backtest.TradeabilityPlan{StrategyID: variantID, Account: account.Cash, Requirements: []backtest.TradeRequirement{{Asset: in.AssetID, Quantity: "1", Price: bars[0].Close, Fee: "1"}}, FixedReserve: "0", MaximumCents: 100000000, MarksAt: time.Now().UTC()}
			cs = append(cs, backtest.Candidate{Name: name, Spec: s.Spec(), Minimum: plan, New: func() (strategy.Strategy, error) { return library.New(cfg) }})
			variant, e := json.Marshal(s.Spec())
			if e != nil {
				return e
			}
			frozenMinimum, e := json.Marshal(plan)
			if e != nil {
				return e
			}
			d.Candidates = append(d.Candidates, strategygate.Variant{Name: name, Spec: variant, Minimum: frozenMinimum})
		}
		if _, e = backtest.DeclareGate(ctx, store, d); e != nil {
			return e
		}
		cfg := engine.Config{Source: src, Assets: []string{in.AssetID}, InitialCash: "2000", Account: account.Cash, Cost: engine.CostModel{Version: "base-v1", FeeBps: "1", SlippageBps: "5"}}
		_, runErr := backtest.WalkForward(ctx, store, cfg, dates, backtest.Design{Train: d.Train, Test: d.Test, Purge: d.Purge, Embargo: d.Embargo}, engine.CostModel{Version: "stress-v1", FeeBps: "2", SlippageBps: "10"}, cs, engine.Trial{ID: d.ExperimentID, Family: d.Family, DataSHA256: digest, EvidenceClass: "historical_market_unqualified"})
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "retained experiment has failed attempts for", kind)
		}
		r, e := backtest.EvaluateGate(ctx, store, d.ID)
		if e != nil {
			return e
		}
		reports = append(reports, r)
		records, e := store.StrategyTrials(ctx, d.Family)
		if e != nil {
			return e
		}
		familyHash, e := strategygate.Hash(records)
		if e != nil {
			return e
		}
		if familyHash != r.Decision.FamilySHA256 {
			return errors.New("exported trial evidence differs from gate snapshot")
		}
		families[d.Family] = records
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		InputSHA256, Boundary string
		Reports               []strategygate.Record
		Families              map[string][]strategytrial.Record
	}{digest, "real retained SPY prices; synthetic historical clocks and one-ETF membership; no coverage/availability qualification", reports, families})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
