package marketdata

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

const asset = "asset-x"

func at(day int, hour, minute int) time.Time {
	return time.Date(2026, 1, day, hour, minute, 0, 0, time.UTC)
}

// barsStore holds five sessions: a 2-for-1 split on the 7th and a 0.5
// dividend going ex on the 8th, paid on the 9th.
func barsStore(t *testing.T) *storage.Memory {
	t.Helper()
	m := storage.NewMemory()
	closes := []string{"100", "102", "50", "49", "50"}
	bars := []storage.DailyBar{}
	for i, c := range closes {
		day := 5 + i
		available := at(day, 21, 15)
		bars = append(bars, storage.DailyBar{AssetID: asset, Symbol: "XYZ", Date: available.Format(time.DateOnly), Open: c, High: c, Low: c, Close: c, VWAP: c, Volume: 1000, TradeCount: 10, PublishedAt: available, ObservedAt: available, AvailableAt: available})
	}
	if _, err := m.RecordDailyBars(context.Background(), bars); err != nil {
		t.Fatal(err)
	}
	return m
}

var (
	split    = CorporateAction{SourceID: "split-1", Kind: "split", ExDate: "2026-01-07", From: "1", To: "2", KnownAt: at(6, 12, 0)}
	dividend = CorporateAction{SourceID: "div-1", Kind: "cash_dividend", ExDate: "2026-01-08", PayDate: "2026-01-09", Amount: "0.5", KnownAt: at(8, 14, 0)}
)

func closes(s Series) []string {
	out := []string{}
	for _, b := range s.Bars {
		out = append(out, b.Close)
	}
	return out
}

func TestReadAdjustsOnReadAndLeavesStoredBarsRaw(t *testing.T) {
	ctx := context.Background()
	m := barsStore(t)
	asOf := at(10, 0, 0)
	before, err := m.DailyBarsAsOf(ctx, asset, asOf)
	if err != nil {
		t.Fatal(err)
	}
	actions := []CorporateAction{dividend, split}
	s, err := Read(ctx, m, asset, actions, asOf)
	if err != nil {
		t.Fatal(err)
	}
	// The dividend scales earlier bars by 1 - 0.5/50 = 0.99, the split by 1/2.
	if got, want := closes(s), []string{"49.5", "50.49", "49.5", "49", "50"}; !reflect.DeepEqual(got, want) {
		t.Fatal("adjusted closes", got, want)
	}
	if s.Bars[0].Volume != "2000" || s.Bars[2].Volume != "1000" || s.Bars[1].VWAP != "50.49" {
		t.Fatalf("volume or VWAP not split adjusted: %+v", s.Bars[:3])
	}
	if len(s.Applied) != 2 || s.Applied[0].SourceID != "split-1" {
		t.Fatal("applied actions", s.Applied)
	}
	after, err := m.DailyBarsAsOf(ctx, asset, asOf)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed stored bars", err)
	}
	for i, b := range s.Bars {
		if b.Raw != before[i] {
			t.Fatal("series lost the raw bar", i)
		}
	}
	if actions[0] != dividend || actions[1] != split {
		t.Fatal("read changed its actions")
	}
}

func TestTotalReturnCreditsDividendsOnPayDate(t *testing.T) {
	s, err := Read(context.Background(), barsStore(t), asset, []CorporateAction{split, dividend}, at(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// One share at 100. The split doubles it to two shares at 50 (1.00). The
	// ex-date drops the price to 49 with the dollar not yet paid (0.98). On
	// the pay date it buys 0.02 shares at 50: 2.02 * 50 / 100.
	got := []string{}
	for _, b := range s.Bars {
		got = append(got, b.TotalReturn)
	}
	if want := []string{"1", "1.02", "1", "0.98", "1.01"}; !reflect.DeepEqual(got, want) {
		t.Fatal("total return", got, want)
	}
	// Read before the pay date's bar exists, the dividend is owed but uncounted.
	s, err = Read(context.Background(), barsStore(t), asset, []CorporateAction{split, dividend}, at(9, 20, 0))
	if err != nil || s.Bars[len(s.Bars)-1].TotalReturn != "0.98" {
		t.Fatal("unpaid dividend counted", s.Bars, err)
	}
}

func TestReadAppliesOnlyActionsKnownAtTheClock(t *testing.T) {
	ctx := context.Background()
	m := barsStore(t)
	actions := []CorporateAction{split, dividend}
	// The split is known but its ex-date has not arrived: nothing changes.
	s, err := Read(ctx, m, asset, actions, at(6, 22, 0))
	if err != nil || len(s.Applied) != 0 || !reflect.DeepEqual(closes(s), []string{"100", "102"}) {
		t.Fatal("future ex-date applied", closes(s), s.Applied, err)
	}
	// On the dividend's ex-date, before it is known, only the split applies.
	s, err = Read(ctx, m, asset, actions, at(8, 13, 0))
	if err != nil || len(s.Applied) != 1 || !reflect.DeepEqual(closes(s), []string{"50", "51", "50"}) {
		t.Fatal("unknown dividend applied", closes(s), s.Applied, err)
	}
	// A correction known later replaces the dividend from then on only.
	corrected := dividend
	corrected.Amount, corrected.KnownAt = "1", at(9, 0, 0)
	actions = append(actions, corrected)
	s, err = Read(ctx, m, asset, actions, at(8, 23, 0))
	if err != nil || s.Bars[2].Close != "49.5" {
		t.Fatal("correction applied before it was known", closes(s), err)
	}
	s, err = Read(ctx, m, asset, actions, at(10, 0, 0))
	if err != nil || s.Bars[2].Close != "49" || len(s.Applied) != 2 || s.Applied[1].Amount != "1" {
		t.Fatal("correction not applied", closes(s), s.Applied, err)
	}
}

func TestSameExDateSplitAndDividendUseThePreSplitShare(t *testing.T) {
	bars := []storage.DailyBar{
		{AssetID: asset, Date: "2026-01-05", Open: "100", High: "100", Low: "100", Close: "100", VWAP: "100", Volume: 10},
		{AssetID: asset, Date: "2026-01-06", Open: "49", High: "49", Low: "49", Close: "49", VWAP: "49", Volume: 20},
	}
	known := at(1, 0, 0)
	actions := []CorporateAction{
		{SourceID: "d", Kind: "cash_dividend", ExDate: "2026-01-06", PayDate: "2026-01-06", Amount: "2", KnownAt: known},
		{SourceID: "s", Kind: "split", ExDate: "2026-01-06", From: "1", To: "2", KnownAt: known},
	}
	s, err := Adjust(asset, bars, actions, at(7, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// 100 * (1 - 2/100) / 2 = 49; two shares plus 2/49 of a share at 49 = 100.
	if s.Bars[0].Close != "49" || s.Bars[0].Volume != "20" || s.Bars[1].TotalReturn != "1" {
		t.Fatalf("%+v", s.Bars)
	}
}

func TestAdjustRejectsBadActionsAndBars(t *testing.T) {
	bars := []storage.DailyBar{{AssetID: asset, Date: "2026-01-05", Open: "1", High: "1", Low: "1", Close: "1", VWAP: "1"}, {AssetID: asset, Date: "2026-01-06", Open: "1", High: "1", Low: "1", Close: "1", VWAP: "1"}}
	known := at(1, 0, 0)
	asOf := at(7, 0, 0)
	for name, a := range map[string]CorporateAction{
		"no pay date":       {SourceID: "a", Kind: "cash_dividend", ExDate: "2026-01-06", Amount: "0.1", KnownAt: known},
		"pay before ex":     {SourceID: "a", Kind: "cash_dividend", ExDate: "2026-01-06", PayDate: "2026-01-05", Amount: "0.1", KnownAt: known},
		"zero amount":       {SourceID: "a", Kind: "cash_dividend", ExDate: "2026-01-06", PayDate: "2026-01-06", Amount: "0", KnownAt: known},
		"exponent ratio":    {SourceID: "a", Kind: "split", ExDate: "2026-01-06", From: "1", To: "1e1", KnownAt: known},
		"no known time":     {SourceID: "a", Kind: "split", ExDate: "2026-01-06", From: "1", To: "2"},
		"dividend >= close": {SourceID: "a", Kind: "cash_dividend", ExDate: "2026-01-06", PayDate: "2026-01-06", Amount: "1", KnownAt: known},
	} {
		if _, err := Adjust(asset, bars, []CorporateAction{a}, asOf); !errors.Is(err, ErrInvalidAction) {
			t.Error(name, err)
		}
	}
	spin := CorporateAction{SourceID: "a", Kind: "spin_off", ExDate: "2026-01-06", KnownAt: known}
	if _, err := Adjust(asset, bars, []CorporateAction{spin}, asOf); !errors.Is(err, ErrUnsupportedAction) {
		t.Error("unsupported kind", err)
	}
	a := CorporateAction{SourceID: "a", Kind: "split", ExDate: "2026-01-06", From: "1", To: "2", KnownAt: known}
	b := a
	b.To = "3"
	if _, err := Adjust(asset, bars, []CorporateAction{a, b}, asOf); !errors.Is(err, ErrInvalidAction) {
		t.Error("conflicting versions", err)
	}
	if _, err := Adjust(asset, []storage.DailyBar{bars[1], bars[0]}, nil, asOf); err == nil {
		t.Error("out of order bars")
	}
	if _, err := Adjust("other", bars, nil, asOf); err == nil {
		t.Error("another asset's bars")
	}
}

func TestKnownPostponementWithdrawsTheEarlierExDate(t *testing.T) {
	postponed := split
	postponed.ExDate, postponed.KnownAt = "2026-01-15", at(8, 0, 0)
	s, err := Read(context.Background(), barsStore(t), asset, []CorporateAction{split, postponed}, at(10, 0, 0))
	if err != nil || len(s.Applied) != 0 || s.Bars[0].Close != "100" {
		t.Fatal("obsolete ex-date applied", closes(s), s.Applied, err)
	}
	// Before the postponement was known, the original date applied.
	s, err = Read(context.Background(), barsStore(t), asset, []CorporateAction{split, postponed}, at(7, 22, 0))
	if err != nil || len(s.Applied) != 1 || s.Bars[0].Close != "50" {
		t.Fatal("original split missing", closes(s), err)
	}
}

func TestActionsInABarGapApplyInExDateOrder(t *testing.T) {
	bars := []storage.DailyBar{
		{AssetID: asset, Date: "2026-01-05", Open: "100", High: "100", Low: "100", Close: "100", VWAP: "100", Volume: 10},
		{AssetID: asset, Date: "2026-01-08", Open: "49", High: "49", Low: "49", Close: "49", VWAP: "49", Volume: 20},
	}
	known := at(1, 0, 0)
	actions := []CorporateAction{
		{SourceID: "d", Kind: "cash_dividend", ExDate: "2026-01-07", PayDate: "2026-01-08", Amount: "1", KnownAt: known},
		{SourceID: "s", Kind: "split", ExDate: "2026-01-06", From: "1", To: "2", KnownAt: known},
	}
	s, err := Adjust(asset, bars, actions, at(9, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Two post-split shares earn $2, reinvested at 49: (2 + 2/49) * 49 / 100.
	// The dividend's prior close in its basis is 50: 100 * (1 - 1/50) / 2.
	if s.Bars[1].TotalReturn != "1" || s.Bars[0].Close != "49" {
		t.Fatalf("%+v", s.Bars)
	}
}
