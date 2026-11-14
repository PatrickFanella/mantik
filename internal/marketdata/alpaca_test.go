package marketdata

import (
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

func snapshot(available time.Time, records ...alpacarecord.CorporateActionRecord) alpacarecord.CorporateActionSnapshot {
	return alpacarecord.CorporateActionSnapshot{CaptureID: "c", AvailableAt: available, Records: records}
}

func record(kind, id, body string) alpacarecord.CorporateActionRecord {
	return alpacarecord.CorporateActionRecord{Kind: kind, SourceID: id, OriginalJSON: body}
}

func TestAlpacaActionsReadsSplitsAndDividendsForOneTicker(t *testing.T) {
	first, second := at(2, 0, 0), at(3, 0, 0)
	got, err := AlpacaActions([]alpacarecord.CorporateActionSnapshot{
		snapshot(first,
			record("cash_dividends", "d1", `{"id":"d1","symbol":"SPY","rate":1.7458,"special":false,"foreign":false,"process_date":"2026-01-06","ex_date":"2026-01-06","record_date":"2026-01-06","payable_date":"2026-01-31"}`),
			record("forward_splits", "s1", `{"id":"s1","symbol":"AAPL","new_rate":4,"old_rate":1,"process_date":"2026-01-06","ex_date":"2026-01-06"}`),
			record("name_changes", "n1", `{"id":"n1","old_symbol":"SPY","new_symbol":"SPYX","process_date":"2026-01-06"}`),
		),
		snapshot(second, record("reverse_splits", "r1", `{"id":"r1","symbol":"SPY","new_rate":1,"old_rate":10,"process_date":"2026-01-07","ex_date":"2026-01-07"}`)),
	}, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	want := []CorporateAction{
		{SourceID: "d1", Kind: "cash_dividend", ExDate: "2026-01-06", PayDate: "2026-01-31", Amount: "1.7458", KnownAt: first},
		{SourceID: "r1", Kind: "split", ExDate: "2026-01-07", From: "10", To: "1", KnownAt: second},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%+v", got)
	}
}

func TestAlpacaActionsRefusesUnsupportedKindsForTheTicker(t *testing.T) {
	spin := record("spin_offs", "x", `{"id":"x","source_symbol":"SPY","new_symbol":"NEW","ex_date":"2026-01-06"}`)
	if _, err := AlpacaActions([]alpacarecord.CorporateActionSnapshot{snapshot(at(2, 0, 0), spin)}, "SPY"); !errors.Is(err, ErrUnsupportedAction) {
		t.Fatal("spin-off admitted", err)
	}
	if got, err := AlpacaActions([]alpacarecord.CorporateActionSnapshot{snapshot(at(2, 0, 0), spin)}, "QQQ"); err != nil || len(got) != 0 {
		t.Fatal("another ticker's spin-off blocked the read", got, err)
	}
	bad := record("cash_dividends", "d", `{"id":"d","symbol":"SPY","rate":"1e2","ex_date":"2026-01-06","payable_date":"2026-01-31"}`)
	if _, err := AlpacaActions([]alpacarecord.CorporateActionSnapshot{snapshot(at(2, 0, 0), bad)}, "SPY"); !errors.Is(err, ErrInvalidAction) {
		t.Fatal("malformed rate admitted", err)
	}
	if _, err := AlpacaActions([]alpacarecord.CorporateActionSnapshot{snapshot(time.Time{}, spin)}, "SPY"); !errors.Is(err, ErrInvalidAction) {
		t.Fatal("snapshot without availability admitted", err)
	}
}
