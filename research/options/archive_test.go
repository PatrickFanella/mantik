package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func fixture(t *testing.T, kind string) manifest {
	t.Helper()
	m := manifest{Policy: policy, AssetID: "spy", Underlying: "SPY", SettledCash: "2000000", Recipe: library.DefaultOption(kind), FeePerContract: 1, SlippageBps: 5, Assumptions: options.Assumptions{Rate: .04, Yield: .01, MaxAge: 24 * time.Hour}}
	m.Recipe.MaxContracts = 1
	if kind == library.CoveredCall {
		m.PaidShares = 100
	}
	received := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	add := func(kind, url string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		m.Pages = append(m.Pages, page{Kind: kind, URL: url, ReceivedAt: received, Body: string(raw), SHA256: digest(raw)})
	}
	contracts := []any{}
	for _, c := range []struct{ symbol, kind, strike string }{{"SPY240315C00110000", "call", "110"}, {"SPY240315P00090000", "put", "90"}} {
		contracts = append(contracts, map[string]any{"id": c.symbol, "symbol": c.symbol, "underlying_symbol": "SPY", "expiration_date": "2024-03-15", "strike_price": c.strike, "type": c.kind, "size": "100"})
	}
	add("contracts", "https://paper-api.alpaca.markets/v2/options/contracts?underlying_symbols=SPY&status=inactive&expiration_date_gte=2024-03-15&expiration_date_lte=2024-03-15&strike_price_gte=90&strike_price_lte=110", map[string]any{"option_contracts": contracts, "next_page_token": nil})
	days := []any{}
	stockBars, optionBars := []any{}, []any{}
	for _, date := range []string{"2024-02-01", "2024-02-02", "2024-02-05", "2024-02-06", "2024-02-07", "2024-02-08", "2024-02-09"} {
		at, err := time.Parse(time.RFC3339, date+"T21:15:00Z")
		if err != nil {
			t.Fatal(err)
		}
		m.Times = append(m.Times, at)
		days = append(days, map[string]string{"date": date, "open": "09:30", "close": "16:00"})
		stockBars = append(stockBars, map[string]any{"t": date + "T05:00:00Z", "c": 100})
		optionBars = append(optionBars, map[string]any{"t": date + "T05:00:00Z", "o": 3, "h": 3.1, "l": 2.9, "c": 3})
	}
	add("calendar", "https://paper-api.alpaca.markets/v2/calendar", days)
	add("stock_bars", "https://data.alpaca.markets/v2/stocks/bars?symbols=SPY&timeframe=1Day&adjustment=raw", map[string]any{"bars": map[string]any{"SPY": stockBars}, "next_page_token": nil})
	for _, symbol := range []string{"SPY240315C00110000", "SPY240315P00090000"} {
		add("option_bars", "https://data.alpaca.markets/v1beta1/options/bars?symbols="+symbol+"&timeframe=1Day", map[string]any{"bars": map[string]any{symbol: optionBars}, "next_page_token": nil})
	}
	return m
}

func execute(t *testing.T, m manifest) (report, []byte) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--input", path, "--input-sha256", digest(raw)}, &out, &out); err != nil {
		t.Fatal(err, out.String())
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r, out.Bytes()
}

func TestIncomeCashRoundTripsRemainRetrospective(t *testing.T) {
	for _, kind := range []string{library.CoveredCall, library.CashSecuredPut} {
		t.Run(kind, func(t *testing.T) {
			m := fixture(t, kind)
			before, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			r, raw := execute(t, m)
			if r.Policy != policy || r.EvidenceClass != "retrospective_indicative_unqualified" || r.QualificationCredit || !r.Backtest.Complete || r.Backtest.EvidenceStatus != r.EvidenceClass || len(r.Sources) != len(m.Pages) {
				t.Fatal(r)
			}
			opened, closed := 0, 0
			for _, e := range r.Backtest.Events {
				for _, f := range e.Result.Fills {
					if f.Closing {
						closed++
					} else {
						opened++
					}
				}
			}
			last := r.Backtest.Events[len(r.Backtest.Events)-1].Snapshot
			if opened != 1 || closed != 1 || last.Reserved != "0" || len(last.Positions) != 0 || len(last.Pending) != 0 {
				t.Fatal("missing cash round trip", r.Backtest)
			}
			if r.Sources[0].ReceivedAt != m.Pages[0].ReceivedAt || r.Sources[0].SHA256 != m.Pages[0].SHA256 {
				t.Fatal("lost actual acquisition provenance")
			}
			if len(r.OriginalRecords) != 23 {
				t.Fatal("missing original object clocks", len(r.OriginalRecords))
			}
			for _, record := range r.OriginalRecords {
				if !record.ObservedAt.Equal(m.Pages[0].ReceivedAt) || !record.AvailableAt.Equal(record.ObservedAt) || record.SourcePageSHA256 == "" {
					t.Fatal("lost original source clocks", record)
				}
			}
			after, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("mutated archive")
			}
			_, again := execute(t, m)
			if !bytes.Equal(raw, again) {
				t.Fatal("replay changed")
			}
		})
	}
}

func TestProjectionCannotReadLaterSessionsOrAlterOriginalClocks(t *testing.T) {
	m := fixture(t, library.CoveredCall)
	a, err := readArchive(m)
	if err != nil {
		t.Fatal(err)
	}
	at := m.Times[0]
	rows, err := a.OptionChainAsOf(context.Background(), "SPY", at.Add(-time.Nanosecond))
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	rows, err = a.OptionChainAsOf(context.Background(), "SPY", at)
	if err != nil || len(rows) != 4 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		if !r.AvailableAt.Equal(at) || r.Key == "2024-02-02" {
			t.Fatal("future session visible", r)
		}
	}
	for _, r := range a.refs {
		if !r.ObservedAt.Equal(m.Pages[0].ReceivedAt) {
			t.Fatal("mutated original reference")
		}
	}
	c, err := a.OptionChain(context.Background(), "spy", at)
	if err != nil {
		t.Fatal(err)
	}
	if c.Spot.Date != "2024-02-01" || len(c.Entries) != 2 {
		t.Fatal(c)
	}
	_, err = a.OptionChain(context.Background(), "wrong-asset", at)
	if err == nil {
		t.Fatal("unbound asset")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.OptionChainAsOf(cancelled, "SPY", at); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestArchiveRefusesAmbiguousOrMislabelledSources(t *testing.T) {
	for name, mutate := range map[string]func(*manifest){
		"policy": func(m *manifest) { m.Policy = "point_in_time" },
		"hash":   func(m *manifest) { m.Pages[0].Body += " " },
		"source": func(m *manifest) { m.Pages[0].URL = "https://example.com/v2/options/contracts" },
		"adjusted": func(m *manifest) {
			m.Pages[2].URL = "https://data.alpaca.markets/v2/stocks/bars?symbols=SPY&timeframe=1Day&adjustment=all"
		},
		"duplicate":        func(m *manifest) { m.Pages = append(m.Pages, m.Pages[3]) },
		"receipt":          func(m *manifest) { m.Pages[3].ReceivedAt = m.Times[0] },
		"missing calendar": func(m *manifest) { m.Pages = append(m.Pages[:1], m.Pages[2:]...) },
		"margin recipe":    func(m *manifest) { m.Recipe.Kind = library.IronCondor },
		"put shares":       func(m *manifest) { m.Recipe.Kind = library.CashSecuredPut },
	} {
		t.Run(name, func(t *testing.T) {
			m := fixture(t, library.CoveredCall)
			mutate(&m)
			if _, err := readArchive(m); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestManifestPinAndPartialEvidence(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	m := fixture(t, library.CoveredCall)
	path := filepath.Join(t.TempDir(), "input.json")
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--input", path, "--input-sha256", digest(nil)}, &out, &out); err == nil {
		t.Fatal("wrong pin accepted")
	}
	m.Times = m.Times[:2]
	r, _ := execute(t, m)
	if r.Backtest.Complete || len(r.Backtest.Events[len(r.Backtest.Events)-1].Snapshot.Positions) != 1 {
		t.Fatal("horizon erased liability")
	}
	// The source retains detached original records, not the projected records
	// returned to the engine. Reading does not change that original evidence.
	a, err := readArchive(m)
	if err != nil {
		t.Fatal(err)
	}
	before := a.refs["SPY240315C00110000"]
	if _, err := a.OptionChainAsOf(context.Background(), "SPY", m.Times[0]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, a.refs[before.Symbol]) {
		t.Fatal("source mutated")
	}
}

func TestListingFiltersAndPaginationAreValidated(t *testing.T) {
	for name, change := range map[string]func(*manifest){
		"active": func(m *manifest) {
			m.Pages[0].URL = strings.Replace(m.Pages[0].URL, "status=inactive", "status=active", 1)
		},
		"terminal page": func(m *manifest) { m.Pages[0].URL += "&page_token=omitted-prefix" },
		"strike": func(m *manifest) {
			m.Pages[0].URL = strings.Replace(m.Pages[0].URL, "strike_price_gte=90", "strike_price_gte=100", 1)
		},
		"expiry": func(m *manifest) { m.Pages[0].URL = strings.ReplaceAll(m.Pages[0].URL, "2024-03-15", "2024-03-14") },
		"pagination": func(m *manifest) {
			m.Pages[0].Body = strings.Replace(m.Pages[0].Body, `"next_page_token":null`, `"next_page_token":"another-page"`, 1)
			m.Pages[0].SHA256 = digest([]byte(m.Pages[0].Body))
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := fixture(t, library.CoveredCall)
			change(&m)
			_, err := readArchive(m)
			if err == nil || strings.Contains(err.Error(), "hash") {
				t.Fatal("validation did not reach source boundary", err)
			}
		})
	}
}

func TestMissingSessionIsReportedWithoutInventingABar(t *testing.T) {
	m := fixture(t, library.CoveredCall)
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(m.Pages[3].Body), &body); err != nil {
		t.Fatal(err)
	}
	var bars map[string][]json.RawMessage
	if err := json.Unmarshal(body["bars"], &bars); err != nil {
		t.Fatal(err)
	}
	symbol := "SPY240315C00110000"
	bars[symbol] = append(bars[symbol][:1], bars[symbol][2:]...)
	raw, err := json.Marshal(bars)
	if err != nil {
		t.Fatal(err)
	}
	body["bars"] = raw
	raw, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	m.Pages[3].Body, m.Pages[3].SHA256 = string(raw), digest(raw)
	a, err := readArchive(m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archiveCoverage(a).MissingOptionDates[symbol], []string{"2024-02-02"}) {
		t.Fatal(archiveCoverage(a))
	}
	c, err := a.OptionChain(context.Background(), m.AssetID, m.Times[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range c.Entries {
		if entry.Symbol == symbol && entry.EstimateStatus != "missing_same_session_close" {
			t.Fatal("filled missing session", entry)
		}
	}
}
