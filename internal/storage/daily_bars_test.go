package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type dailyBarBackend interface {
	RecordDailyBars(context.Context, []DailyBar) (DailyBarReceipt, error)
	DailyBarsAsOf(context.Context, string, time.Time) ([]DailyBar, error)
}

func TestMemoryDailyBars(t *testing.T) { dailyBarContracts(t, NewMemory()) }
func TestPostgresContractsDailyBars(t *testing.T) {
	p := timerPostgres(t, "dailybars")
	dailyBarContracts(t, p)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_daily_bars SET close_price=1"); err == nil {
		t.Fatal("stored bar was rewritten")
	}
	if _, err := p.pool.Exec(ctx, "DELETE FROM mantik_daily_bars"); err == nil {
		t.Fatal("stored bar was deleted")
	}
	// A direct insert cannot skip a revision or backdate a correction.
	const insert = `INSERT INTO mantik_daily_bars(asset_id,bar_date,revision,symbol,open_price,high_price,low_price,close_price,vwap,volume,trade_count,published_at,observed_at,available_at)
		VALUES('asset-spy','2026-11-10',$1,'SPY',1,1,1,1,1,1,1,$2,$2,$2)`
	if _, err := p.pool.Exec(ctx, insert, 9, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("revision gap admitted")
	}
	if _, err := p.pool.Exec(ctx, insert, 4, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("backdated correction admitted")
	}
}

func dailyBarContracts(t *testing.T, s dailyBarBackend) {
	t.Helper()
	ctx := context.Background()
	const spy, other = "asset-spy", "b0b6dd9d-8b9b-48a9-ba46-b9d54906e415"
	close10 := time.Date(2026, 11, 10, 21, 15, 0, 0, time.UTC)
	close11 := close10.AddDate(0, 0, 1)
	fetched := time.Date(2026, 11, 13, 6, 0, 0, 123456789, time.UTC)
	bar := func(asset, symbol, date, closePrice string, available time.Time) DailyBar {
		return DailyBar{AssetID: asset, Symbol: symbol, Date: date, Open: "601.115", High: "603", Low: "600.0001", Close: closePrice, VWAP: "602.003321", Volume: 61000000, TradeCount: 820000, PublishedAt: available, ObservedAt: fetched, AvailableAt: available}
	}
	first := []DailyBar{bar(spy, "SPY", "2026-11-11", "604.50", close11), bar(spy, "SPY", "2026-11-10", "602.4475", close10), bar(other, "SPY", "2026-11-10", "0.0042", close10)}

	if got, err := s.DailyBarsAsOf(ctx, spy, fetched); err != nil || len(got) != 0 {
		t.Fatal("empty store returned bars", got, err)
	}
	receipt, err := s.RecordDailyBars(ctx, first)
	if err != nil || receipt != (DailyBarReceipt{Inserted: 3}) {
		t.Fatal(receipt, err)
	}
	stored, err := s.DailyBarsAsOf(ctx, spy, fetched)
	if err != nil || len(stored) != 2 {
		t.Fatal(stored, err)
	}
	if stored[0].Date != "2026-11-10" || stored[1].Date != "2026-11-11" || stored[0].Revision != 1 || stored[1].Close != "604.5" || stored[0].Low != "600.0001" ||
		!stored[0].AvailableAt.Equal(close10) || !stored[0].ObservedAt.Equal(fetched.Truncate(time.Microsecond)) || stored[0].ObservedAt.Location() != time.UTC {
		t.Fatalf("unexpected stored bars: %+v", stored)
	}

	// Writing the same bars again changes nothing, even when fetched later and
	// with a different but equal decimal spelling.
	again := []DailyBar{first[0], first[1], first[2]}
	for i := range again {
		again[i].ObservedAt = fetched.Add(48 * time.Hour)
		again[i].AvailableAt = again[i].ObservedAt
	}
	again[1].High = "603.000"
	receipt, err = s.RecordDailyBars(ctx, again)
	if err != nil || receipt != (DailyBarReceipt{Unchanged: 3}) {
		t.Fatal(receipt, err)
	}
	if got, err := s.DailyBarsAsOf(ctx, spy, fetched.Add(72*time.Hour)); err != nil || !reflect.DeepEqual(got, stored) {
		t.Fatalf("repeat write changed the store: %+v %v", got, err)
	}

	// The same ticker under another asset is a separate series.
	if got, err := s.DailyBarsAsOf(ctx, other, fetched); err != nil || len(got) != 1 || got[0].Close != "0.0042" || got[0].AssetID != other {
		t.Fatal(got, err)
	}

	// A read never returns a bar that became available after its clock.
	if got, err := s.DailyBarsAsOf(ctx, spy, close10.Add(-time.Microsecond)); err != nil || len(got) != 0 {
		t.Fatal("bar visible before it was available", got, err)
	}
	if got, err := s.DailyBarsAsOf(ctx, spy, close10); err != nil || len(got) != 1 || got[0].Date != "2026-11-10" {
		t.Fatal(got, err)
	}
	if got, err := s.DailyBarsAsOf(ctx, spy, close11.Add(-time.Second)); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}

	// A changed bar is a correction. The original stays visible to earlier clocks.
	correctedAt := fetched.Add(96 * time.Hour).Truncate(time.Microsecond)
	fix := bar(spy, "SPY", "2026-11-10", "602.45", correctedAt)
	fix.PublishedAt, fix.ObservedAt = close10, correctedAt
	receipt, err = s.RecordDailyBars(ctx, []DailyBar{fix, again[0]})
	if err != nil || receipt != (DailyBarReceipt{Corrected: 1, Unchanged: 1}) {
		t.Fatal(receipt, err)
	}
	if got, err := s.DailyBarsAsOf(ctx, spy, correctedAt.Add(-time.Microsecond)); err != nil || !reflect.DeepEqual(got, stored) {
		t.Fatalf("correction overwrote the original: %+v %v", got, err)
	}
	got, err := s.DailyBarsAsOf(ctx, spy, correctedAt)
	if err != nil || len(got) != 2 || got[0].Close != "602.45" || got[0].Revision != 2 || !got[0].AvailableAt.Equal(correctedAt) || !reflect.DeepEqual(got[1], stored[1]) {
		t.Fatalf("correction not served: %+v %v", got, err)
	}
	if receipt, err = s.RecordDailyBars(ctx, []DailyBar{fix}); err != nil || receipt != (DailyBarReceipt{Unchanged: 1}) {
		t.Fatal(receipt, err)
	}
	// Returning to the first values is another correction, not a merge.
	back := first[1]
	back.ObservedAt, back.AvailableAt = correctedAt.Add(time.Hour), correctedAt.Add(time.Hour)
	if receipt, err = s.RecordDailyBars(ctx, []DailyBar{back}); err != nil || receipt != (DailyBarReceipt{Corrected: 1}) {
		t.Fatal(receipt, err)
	}
	if got, err = s.DailyBarsAsOf(ctx, spy, back.AvailableAt); err != nil || got[0].Revision != 3 || got[0].Close != "602.4475" {
		t.Fatal(got, err)
	}

	// A refused batch stores nothing, including its valid bars.
	late := bar(spy, "SPY", "2026-11-12", "605", close11.AddDate(0, 0, 1))
	backdated := bar(spy, "SPY", "2026-11-10", "1", correctedAt)
	if _, err = s.RecordDailyBars(ctx, []DailyBar{late, backdated}); !errors.Is(err, ErrDailyBarOrder) {
		t.Fatal("correction available before its predecessor admitted", err)
	}
	invalid := map[string]func(*DailyBar){
		"asset":           func(b *DailyBar) { b.AssetID = "" },
		"symbol":          func(b *DailyBar) { b.Symbol = "spy" },
		"date":            func(b *DailyBar) { b.Date = "2026-02-30" },
		"date form":       func(b *DailyBar) { b.Date = "2026-11-12T00:00:00Z" },
		"float":           func(b *DailyBar) { b.Open = "6.05e2" },
		"negative":        func(b *DailyBar) { b.Low = "-1" },
		"zero close":      func(b *DailyBar) { b.Close = "0.00" },
		"empty vwap":      func(b *DailyBar) { b.VWAP = "" },
		"volume":          func(b *DailyBar) { b.Volume = -1 },
		"trade count":     func(b *DailyBar) { b.TradeCount = -1 },
		"published":       func(b *DailyBar) { b.PublishedAt = time.Time{} },
		"observed":        func(b *DailyBar) { b.ObservedAt = time.Time{} },
		"available early": func(b *DailyBar) { b.AvailableAt = b.PublishedAt.Add(-time.Second) },
		"revision":        func(b *DailyBar) { b.Revision = 2 },
	}
	for name, mutate := range invalid {
		bad := late
		mutate(&bad)
		if _, err = s.RecordDailyBars(ctx, []DailyBar{late, bad}); !errors.Is(err, ErrInvalidDailyBar) {
			t.Fatalf("%s admitted: %v", name, err)
		}
	}
	if _, err = s.RecordDailyBars(ctx, []DailyBar{late, late}); !errors.Is(err, ErrInvalidDailyBar) {
		t.Fatal("repeated bar in one write admitted", err)
	}
	if _, err = s.RecordDailyBars(ctx, nil); !errors.Is(err, ErrInvalidDailyBar) {
		t.Fatal("empty write admitted", err)
	}
	if _, err = s.DailyBarsAsOf(ctx, "", fetched); !errors.Is(err, ErrInvalidDailyBar) {
		t.Fatal("read without an asset admitted", err)
	}
	if _, err = s.DailyBarsAsOf(ctx, spy, time.Time{}); !errors.Is(err, ErrInvalidDailyBar) {
		t.Fatal("read without a clock admitted", err)
	}
	if got, err = s.DailyBarsAsOf(ctx, spy, fetched.AddDate(1, 0, 0)); err != nil || len(got) != 2 {
		t.Fatalf("refused writes left bars behind: %+v %v", got, err)
	}

	// Concurrent identical writes store each bar once.
	fresh := []DailyBar{late, bar(other, "SPY", "2026-11-12", "0.0041", late.AvailableAt)}
	var wg sync.WaitGroup
	receipts := make([]DailyBarReceipt, 8)
	errs := make([]error, 8)
	for i := range receipts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipts[i], errs[i] = s.RecordDailyBars(ctx, fresh)
		}()
	}
	wg.Wait()
	var total DailyBarReceipt
	for i, r := range receipts {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		total.Inserted += r.Inserted
		total.Corrected += r.Corrected
		total.Unchanged += r.Unchanged
	}
	if total != (DailyBarReceipt{Inserted: 2, Unchanged: 14}) {
		t.Fatalf("concurrent writes: %+v", total)
	}
	if got, err = s.DailyBarsAsOf(ctx, spy, fetched.AddDate(1, 0, 0)); err != nil || len(got) != 3 || got[2].Revision != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}
