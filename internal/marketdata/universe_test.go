package marketdata

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestUniverseUsesPriorAvailableBarsAndStableIDs(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	at := day.AddDate(0, 0, 3)
	assets := []StockAsset{
		{ID: "old-company", Symbol: "REUSED", Class: "us_equity", Exchange: "TXSE", Status: "inactive", Tradable: false},
		{ID: "new-company", Symbol: "REUSED", Class: "us_equity", Exchange: "NYSE", Status: "active", Tradable: true},
		{ID: "cheap", Symbol: "CHEAP", Class: "us_equity", Exchange: "NASDAQ"},
		{ID: "thin", Symbol: "THIN", Class: "us_equity", Exchange: "ARCA"},
		{ID: "future", Symbol: "FUTURE", Class: "us_equity", Exchange: "NYSE"},
	}
	for _, a := range assets {
		if a.ID == "new-company" {
			continue
		}
		for i := 0; i < 2; i++ {
			stamp := day.AddDate(0, 0, i)
			available := stamp.Add(23 * time.Hour)
			close := "5"
			volume := int64(200000)
			if a.ID == "cheap" {
				close = "4.999999999999"
			}
			if a.ID == "thin" {
				volume--
			}
			if a.ID == "future" {
				available = at
			}
			b := storage.DailyBar{AssetID: a.ID, Symbol: a.Symbol, Date: stamp.Format(time.DateOnly), Open: close, High: close, Low: close, Close: close, VWAP: close, Volume: volume, PublishedAt: available, ObservedAt: available, AvailableAt: available}
			if _, err := store.RecordDailyBars(ctx, []storage.DailyBar{b}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := DefaultUniverseConfig()
	cfg.Sessions = 2
	reference := AssetSnapshot{ObservedAt: at.AddDate(1, 0, 0), Assets: assets}
	report, err := StockUniverse(ctx, store, reference, at, cfg)
	if err != nil || len(report.Members) != 1 || report.Members[0].AssetID != "old-company" || !report.PossiblySurvivorBiased || report.HistoricalTradabilityVerified {
		t.Fatalf("report %+v: %v", report, err)
	}
	// A later correction cannot rewrite the old universe.
	b := storage.DailyBar{AssetID: "old-company", Symbol: "REUSED", Date: day.AddDate(0, 0, 1).Format(time.DateOnly), Open: "1", High: "1", Low: "1", Close: "1", VWAP: "1", Volume: 200000, PublishedAt: at.Add(time.Hour), ObservedAt: at.Add(time.Hour), AvailableAt: at.Add(time.Hour)}
	if _, err := store.RecordDailyBars(ctx, []storage.DailyBar{b}); err != nil {
		t.Fatal(err)
	}
	report, err = StockUniverse(ctx, store, reference, at, cfg)
	if err != nil || len(report.Members) != 1 {
		t.Fatal(report, err)
	}
	report, err = StockUniverse(ctx, store, reference, at.Add(2*time.Hour), cfg)
	if err != nil || (len(report.Members) != 1 || report.Members[0].AssetID != "future") {
		t.Fatal(report, err)
	}
	// Delisted bars stop passing once the latest session is stale.
	report, err = StockUniverse(ctx, store, reference, at.AddDate(0, 0, 30), cfg)
	if err != nil || len(report.Members) != 0 {
		t.Fatal(report, err)
	}
}

func TestUniverseSessionAgeUsesUTCCalendarDates(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	available := time.Date(2024, 1, 1, 22, 0, 0, 0, time.UTC)
	bar := storage.DailyBar{AssetID: "asset-spy", Symbol: "SPY", Date: "2024-01-01", Open: "5", High: "5", Low: "5", Close: "5", VWAP: "5", Volume: 200000, PublishedAt: available, ObservedAt: available, AvailableAt: available}
	if _, err := store.RecordDailyBars(ctx, []storage.DailyBar{bar}); err != nil {
		t.Fatal(err)
	}
	reference := AssetSnapshot{ObservedAt: available, Assets: []StockAsset{{ID: "asset-spy", Symbol: "SPY", Class: "us_equity", Exchange: "ARCA", Status: "active", Tradable: true}}}
	cfg := DefaultUniverseConfig()
	cfg.Sessions, cfg.MaxAgeDays = 1, 1
	for _, tc := range []struct {
		name, clock string
		members     int
	}{
		{"next-midnight", "2024-01-02T00:00:00Z", 1},
		{"next-trading-day", "2024-01-02T14:00:00Z", 1},
		{"end-of-next-day", "2024-01-02T23:59:59Z", 1},
		{"two-days-old", "2024-01-03T00:00:00Z", 0},
		{"offset-still-next-UTC-day", "2024-01-03T01:00:00+02:00", 1},
		{"offset-now-two-UTC-days", "2024-01-02T23:00:00-02:00", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.clock)
			if err != nil {
				t.Fatal(err)
			}
			report, err := StockUniverse(ctx, store, reference, at, cfg)
			if err != nil || len(report.Members) != tc.members {
				t.Fatalf("clock %s: members=%d want=%d error=%v", tc.clock, len(report.Members), tc.members, err)
			}
		})
	}
}
