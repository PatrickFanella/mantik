package marketdata

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestBackfillReplaysIntoFreshStoreWithoutChangingPast(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	end := from.AddDate(2, 0, 0)
	observed := end.AddDate(1, 0, 0)
	fetch := func(_ context.Context, s string, start, end time.Time) (alpaca.DailyBars, error) {
		return alpaca.DailyBars{Pages: []alpaca.Response{{ReceivedAt: observed}}, Bars: map[string][]alpaca.Bar{s: {{Time: start, Open: "10", High: "11", Low: "9", Close: "10", VWAP: "10", Volume: 100}}}}, nil
	}
	assets := []BackfillAsset{{Symbol: "SPY", AssetID: "asset-spy"}}
	store := storage.NewMemory()
	var out bytes.Buffer
	if err := BackfillDaily(ctx, store, fetch, assets, from, end, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bars=2 inserted=2") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := BackfillDaily(ctx, store, fetch, assets, from, end, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bars=2 inserted=0 corrected=0 unchanged=2") {
		t.Fatal(out.String())
	}
	past, err := store.DailyBarsAsOf(ctx, "asset-spy", end)
	if err != nil || len(past) != 0 {
		t.Fatal("backfill invented historical availability", past, err)
	}
	bars, err := store.DailyBarsAsOf(ctx, "asset-spy", observed)
	if err != nil || len(bars) != 2 || bars[0].Revision != 1 {
		t.Fatal(bars, err)
	}
	fresh := storage.NewMemory()
	if err := BackfillDaily(ctx, fresh, fetch, assets, from, end, io.Discard); err != nil {
		t.Fatal(err)
	}
	bars, err = fresh.DailyBarsAsOf(ctx, "asset-spy", observed)
	if err != nil || len(bars) != 2 {
		t.Fatal("fresh store lost retained bars", bars, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := BackfillDaily(canceled, store, fetch, assets, from, end, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
