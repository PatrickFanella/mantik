package options

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ReadBars reads retained indicative OHLC without provider calls. Unlike a chain
// estimate, a fill needs all four observed prices. JSON null/missing fields fail
// validation rather than becoming a usable zero or a manufactured spread.
func ReadBars(ctx context.Context, store Store, underlying string, at time.Time) (map[string]IndicativeBar, error) {
	records, err := store.OptionChainAsOf(ctx, underlying, at)
	if err != nil {
		return nil, err
	}
	out := map[string]IndicativeBar{}
	for _, r := range records {
		if r.Kind != "bar" {
			continue
		}
		if r.Underlying != underlying || r.Feed != "indicative" || r.FeedSelection != "provider_default_unverified" {
			return nil, fmt.Errorf("invalid indicative bar provenance")
		}
		var prices struct {
			Open  float64 `json:"o"`
			High  float64 `json:"h"`
			Low   float64 `json:"l"`
			Close float64 `json:"c"`
		}
		if err := json.Unmarshal(r.Data, &prices); err != nil {
			return nil, err
		}
		b := IndicativeBar{Symbol: r.Symbol, Date: r.Key, Open: prices.Open, High: prices.High, Low: prices.Low, Close: prices.Close, OccurredAt: r.OccurredAt, ObservedAt: r.ObservedAt, AvailableAt: r.AvailableAt}
		if err := b.Validate(at); err != nil {
			return nil, err
		}
		if _, ok := out[b.Symbol]; ok {
			return nil, fmt.Errorf("duplicate indicative bar")
		}
		out[b.Symbol] = b
	}
	return out, nil
}
