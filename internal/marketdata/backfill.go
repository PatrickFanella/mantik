package marketdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// BarWriter is the append-only raw daily-bar store.
type BarWriter interface {
	RecordDailyBars(context.Context, []storage.DailyBar) (storage.DailyBarReceipt, error)
}

// HistoricalBars supplies retained pages, including their original receipt times.
type HistoricalBars func(context.Context, string, time.Time, time.Time) (alpaca.DailyBars, error)

// BackfillAsset binds a ticker to a reviewed source asset identity for this
// acquisition interval. A current ticker lookup cannot prove historical identity.
type BackfillAsset struct {
	Symbol  string `json:"symbol"`
	AssetID string `json:"asset_id"`
}

// ValidateBackfill checks the complete job before any network or database access.
func ValidateBackfill(assets []BackfillAsset, start, end time.Time) error {
	if len(assets) < 1 || len(assets) > 100 || start.Before(time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)) || !end.After(start) || end.After(time.Now().UTC().Add(-alpaca.RecentSIPDelay)) {
		return errors.New("backfill requires 1–100 assets, start since 2016, and a fixed end at least 15 minutes old")
	}
	symbols, ids := map[string]bool{}, map[string]bool{}
	for _, a := range assets {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,15}$`).MatchString(a.Symbol) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`).MatchString(a.AssetID) || symbols[a.Symbol] || ids[a.AssetID] {
			return errors.New("backfill requires distinct valid symbols and asset IDs")
		}
		symbols[a.Symbol], ids[a.AssetID] = true, true
	}
	return nil
}

// BackfillDaily fetches in calendar-year chunks and replays retained responses
// into the store. End is exclusive. AvailableAt is the actual acquisition time:
// fetching a historical bar today does not prove its historical publication.
// A repeated run emits unchanged counts and makes no requests for cached pages.
func BackfillDaily(ctx context.Context, store BarWriter, fetch HistoricalBars, assets []BackfillAsset, start, end time.Time, progress io.Writer) error {
	if err := ValidateBackfill(assets, start, end); err != nil {
		return err
	}
	assets = slices.Clone(assets)
	slices.SortFunc(assets, func(a, b BackfillAsset) int {
		if a.Symbol < b.Symbol {
			return -1
		}
		if a.Symbol > b.Symbol {
			return 1
		}
		return 0
	})
	for _, asset := range assets {
		total := storage.DailyBarReceipt{}
		count := 0
		for from := start; from.Before(end); {
			if err := ctx.Err(); err != nil {
				return err
			}
			to := time.Date(from.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
			if to.After(end) {
				to = end
			}
			result, err := fetch(ctx, asset.Symbol, from, to.Add(-time.Second))
			if err != nil {
				return fmt.Errorf("%s %s: %w", asset.Symbol, from.Format(time.DateOnly), err)
			}
			at := time.Time{}
			for _, page := range result.Pages {
				if page.ReceivedAt.After(at) {
					at = page.ReceivedAt
				}
			}
			bars := make([]storage.DailyBar, 0, len(result.Bars[asset.Symbol]))
			for _, b := range result.Bars[asset.Symbol] {
				bars = append(bars, storage.DailyBar{AssetID: asset.AssetID, Symbol: asset.Symbol, Date: b.Time.Format(time.DateOnly), Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, VWAP: b.VWAP, Volume: b.Volume, TradeCount: b.TradeCount, PublishedAt: at, ObservedAt: at, AvailableAt: at})
			}
			if len(bars) > 0 {
				receipt, err := store.RecordDailyBars(ctx, bars)
				if err != nil {
					return err
				}
				total.Inserted += receipt.Inserted
				total.Corrected += receipt.Corrected
				total.Unchanged += receipt.Unchanged
			}
			count += len(bars)
			if _, err := fmt.Fprintf(progress, "symbol=%s through=%s bars=%d inserted=%d corrected=%d unchanged=%d\n", asset.Symbol, to.Format(time.DateOnly), count, total.Inserted, total.Corrected, total.Unchanged); err != nil {
				return err
			}
			from = to
		}
	}
	return nil
}
