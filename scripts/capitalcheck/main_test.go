package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func testSnapshot() snapshot {
	dates := []string{}
	day := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	for len(dates) < 300 {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			dates = append(dates, day.Format(time.DateOnly))
		}
		day = day.AddDate(0, 0, 1)
	}
	at := day.Add(12 * time.Hour)
	known := at.Add(-time.Minute)
	s := snapshot{At: at, ReviewedAt: at.Add(-time.Second), ReviewedBy: "synthetic source review", CalendarDates: dates, MaximumCents: 1000000, Bars: map[string][]marketdata.AdjustedBar{}, Marks: map[string]string{}, Limits: map[string]string{}, Fees: map[string]string{}, Sources: []sourceRef{{URL: "https://example.test/calendar", SHA256: strings.Repeat("a", 64), ObservedAt: known}}, Universe: marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: known}}
	s.ExpectedSessions = len(dates)
	calendar := []map[string]string{}
	for _, date := range dates {
		calendar = append(calendar, map[string]string{"date": date})
	}
	raw, _ := json.Marshal(calendar)
	sum := sha256.Sum256(raw)
	s.CalendarSource = sourceDocument{sourceRef: sourceRef{URL: "https://paper-api.alpaca.markets/v2/calendar", SHA256: hex.EncodeToString(sum[:]), ObservedAt: known}, Body: string(raw)}
	body := "all four base recipe versions failed and remain at `idea`\n"
	for _, kind := range []string{library.Trend, library.Momentum, library.Reversal, library.LowVol} {
		recipe, _ := library.New(library.Default(kind))
		id, _ := recipe.Spec().ID()
		body += "**wave9-" + kind + "**\nStrategy `" + id + "`\nDeclaration SHA-256: `" + strings.Repeat("a", 64) + "`\nFamily snapshot SHA-256: `" + strings.Repeat("b", 64) + "`\n"
		for code, reason := range expectedGateChecks() {
			body += "| `" + code + "` | `" + reason + "` |\n"
		}
	}
	sum = sha256.Sum256([]byte(body))
	s.GateSource = sourceDocument{sourceRef: sourceRef{URL: "https://github.com/subculture-collective/mantik/issues/72#issuecomment-6908455626", SHA256: hex.EncodeToString(sum[:]), ObservedAt: known}, Body: body}
	for i := 0; i < 8; i++ {
		id, symbol := fmt.Sprintf("asset-%d", i), fmt.Sprintf("NAME%d", i)
		a := asset{ID: id, Symbol: symbol, Class: "large_cap_stock", Fractionable: true, LargeCapReview: "synthetic reviewed cap evidence", Sources: s.Sources}
		if i < 3 {
			a.Class = "etf"
		}
		s.Assets = append(s.Assets, a)
		for j, date := range dates {
			price := strconv.FormatFloat(100+float64(j)*.1+float64((j%5)*(i+1))*.01, 'f', 6, 64)
			s.Bars[id] = append(s.Bars[id], marketdata.AdjustedBar{Date: date, Open: price, High: price, Low: price, Close: price, Raw: storage.DailyBar{AssetID: id, Symbol: symbol, Date: date, ObservedAt: known, PublishedAt: known, AvailableAt: known}})
		}
		s.Marks[symbol], s.Limits[symbol], s.Fees[symbol] = "100", "100", "0.01"
		if i >= 3 {
			s.Universe.Members = append(s.Universe.Members, marketdata.UniverseMember{AssetID: id, Symbol: symbol, LastSession: dates[len(dates)-1]})
		}
	}
	return s
}

func TestCapitalSnapshotDerivesEveryRecipeAndReservesItsFractionalBasket(t *testing.T) {
	s := testSnapshot()
	r, err := evaluate(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Results) != 4 || r.QualificationCredit || r.EvidenceClass != "snapshot_financial_diagnostic_unqualified" {
		t.Fatal(r)
	}
	for _, v := range r.Results {
		if v.Capital.Status != "minimum_at_frozen_snapshot" || v.Capital.WholeBookEquity == "" || len(v.Decision.Targets) == 0 || len(v.Capital.Reserved.Pending) != len(v.Decision.Targets) {
			t.Fatal(v)
		}
	}
	if r.JointPortfolio.Status != "minimum_at_frozen_snapshot" {
		t.Fatal(r.JointPortfolio)
	}
}

func TestCapitalSnapshotChecksJointBoundaryWithIdleTrend(t *testing.T) {
	s := testSnapshot()
	for _, a := range s.Assets {
		if a.Class != "etf" {
			continue
		}
		bars := s.Bars[a.ID]
		// Equal endpoint prices cancel every trend signal while the intervening
		// prices retain nonzero volatility and the stock recipes remain active.
		for _, lag := range []int{0, 21, 63, 252} {
			b := &bars[len(bars)-1-lag]
			b.Open, b.High, b.Low, b.Close = "100", "100", "100", "100"
		}
	}
	r, err := evaluate(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Results[0].Kind != library.Trend || len(r.Results[0].Decision.Targets) != 0 || r.Results[0].Capital.Status != "no_trade" {
		t.Fatal("expected an idle trend recipe", r.Results[0])
	}
	expectedOrders := 0
	for _, v := range r.Results[1:] {
		if len(v.Decision.Targets) == 0 {
			t.Fatal("expected an active stock recipe", v)
		}
		expectedOrders += len(v.Decision.Targets)
	}
	joint := r.JointPortfolio
	if joint.Status != "minimum_at_frozen_snapshot" || joint.WholeBookEquity == "" || len(joint.Reserved.Pending) != expectedOrders || len(joint.Distribution.Sleeves) != 4 {
		t.Fatal("joint boundary must reserve every active target and retain the idle sleeve", joint)
	}
}

func TestCapitalSnapshotRefusesMissingScopeAndWarmup(t *testing.T) {
	for name, change := range map[string]func(*snapshot){
		"membership":             func(s *snapshot) { s.Universe.Members = s.Universe.Members[:4] },
		"missing warmup session": func(s *snapshot) { id := s.Assets[3].ID; b := s.Bars[id]; s.Bars[id] = append(b[:150], b[151:]...) },
		"warmup":                 func(s *snapshot) { id := s.Assets[0].ID; s.Bars[id] = s.Bars[id][:20] },
		"cap provenance":         func(s *snapshot) { s.Assets[3].LargeCapReview = "" },
		"future": func(s *snapshot) {
			s.Assets[3].Sources = []sourceRef{{URL: "https://example.test/future", SHA256: strings.Repeat("a", 64), ObservedAt: s.At.Add(time.Second)}}
		},
		"fractionability":      func(s *snapshot) { s.Assets[3].Fractionable = false },
		"wrong published gate": func(s *snapshot) { s.GateSource.URL = "https://example.test/not-original" },
		"truncated gate": func(s *snapshot) {
			s.GateSource.Body = strings.Replace(s.GateSource.Body, "| `point_in_time` | `historical_availability_evidence_required` |\n", "", 1)
			sum := sha256.Sum256([]byte(s.GateSource.Body))
			s.GateSource.SHA256 = hex.EncodeToString(sum[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := testSnapshot()
			change(&s)
			if _, err := evaluate(context.Background(), s); err == nil {
				t.Fatal("incomplete snapshot claimed a minimum")
			}
		})
	}
}
