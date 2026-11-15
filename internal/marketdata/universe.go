package marketdata

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// StockAsset is a retained asset-reference observation. ID, rather than symbol,
// joins bars. Current status and tradability are not historical listing dates.
type StockAsset = alpaca.StockAsset

// AssetSnapshot is the retained active and inactive asset reference.
type AssetSnapshot = alpaca.AssetSnapshot

// UniverseConfig defines a trailing-session liquidity screen in exact dollars.
type UniverseConfig struct {
	MinPrice               string `json:"min_price"`
	MinAverageDollarVolume string `json:"min_average_dollar_volume"`
	Sessions               int    `json:"sessions"`
	MaxAgeDays             int    `json:"max_age_days"`
}

// DefaultUniverseConfig requires $5, $1 million average close*volume over 20
// sessions, and a latest session no more than seven calendar days old.
func DefaultUniverseConfig() UniverseConfig {
	return UniverseConfig{MinPrice: "5", MinAverageDollarVolume: "1000000", Sessions: 20, MaxAgeDays: 7}
}

// UniverseMember identifies a passing asset and the bars that supported it.
type UniverseMember struct {
	AssetID     string `json:"asset_id"`
	Symbol      string `json:"symbol"`
	LastSession string `json:"last_session"`
	Close       string `json:"close"`
	Sessions    int    `json:"sessions"`
}

// UniverseReport always exposes the incomplete reference history. Membership is
// a historical bar screen, not proof of past broker order admission.
type UniverseReport struct {
	AsOf                          time.Time        `json:"as_of"`
	ReferenceObservedAt           time.Time        `json:"reference_observed_at"`
	Config                        UniverseConfig   `json:"config"`
	PossiblySurvivorBiased        bool             `json:"possibly_survivor_biased"`
	HistoricalTradabilityVerified bool             `json:"historical_tradability_verified"`
	Members                       []UniverseMember `json:"members"`
}

// UniverseBarReader supplies revision-aware reads by stable asset identity.
type UniverseBarReader interface {
	DailyBarsAsOf(context.Context, string, time.Time) ([]storage.DailyBar, error)
}

// StockUniverse screens US stocks and ETFs using only sessions and revisions
// available strictly before asOf. Inactive assets remain eligible for past
// dates when their bars pass; today's inactive/tradable flags do not rewrite
// history. A reused ticker never joins bars from another asset ID.
func StockUniverse(ctx context.Context, reader UniverseBarReader, reference AssetSnapshot, asOf time.Time, cfg UniverseConfig) (UniverseReport, error) {
	out := UniverseReport{AsOf: asOf, ReferenceObservedAt: reference.ObservedAt, Config: cfg, PossiblySurvivorBiased: true, Members: []UniverseMember{}}
	price, pok := new(big.Rat).SetString(cfg.MinPrice)
	adv, aok := new(big.Rat).SetString(cfg.MinAverageDollarVolume)
	if !pok || !aok || price.Sign() <= 0 || adv.Sign() < 0 || cfg.Sessions < 1 || cfg.Sessions > 252 || cfg.MaxAgeDays < 1 || cfg.MaxAgeDays > 366 || asOf.IsZero() || reference.ObservedAt.IsZero() || len(reference.Assets) > 100000 {
		return out, errors.New("universe requires valid thresholds, 1–252 sessions, 1–366 age days, reference and clock")
	}
	seen := map[string]bool{}
	for _, asset := range reference.Assets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if asset.ID == "" || asset.Symbol == "" || seen[asset.ID] {
			return out, errors.New("invalid or duplicate universe asset ID")
		}
		seen[asset.ID] = true
		if asset.Class != "us_equity" || (asset.Exchange == "" || asset.Exchange == "OTC") {
			continue
		}
		if !reference.ObservedAt.After(asOf) && (asset.Status != "active" || !asset.Tradable) {
			continue
		}
		bars, err := reader.DailyBarsAsOf(ctx, asset.ID, asOf.Add(-time.Microsecond))
		if err != nil {
			return out, err
		}
		eligible := []storage.DailyBar{}
		for _, bar := range bars {
			if bar.AssetID != asset.ID {
				return out, errors.New("bar reader returned a different asset")
			}
			// A same-day daily bar cannot set a pre-session membership.
			if bar.AvailableAt.Before(asOf) && bar.PublishedAt.Before(asOf) && bar.Date < asOf.UTC().Format(time.DateOnly) {
				eligible = append(eligible, bar)
			}
		}
		slices.SortFunc(eligible, func(a, b storage.DailyBar) int {
			if a.Date < b.Date {
				return -1
			}
			if a.Date > b.Date {
				return 1
			}
			return 0
		})
		if len(eligible) < cfg.Sessions {
			continue
		}
		eligible = eligible[len(eligible)-cfg.Sessions:]
		last := eligible[len(eligible)-1]
		day, err := time.Parse(time.DateOnly, last.Date)
		if err != nil {
			return out, err
		}
		if asOf.UTC().Truncate(24*time.Hour).Sub(day) > time.Duration(cfg.MaxAgeDays)*24*time.Hour {
			continue
		}
		close, ok := new(big.Rat).SetString(last.Close)
		if !ok || close.Sign() <= 0 {
			return out, errors.New("invalid universe price")
		}
		if close.Cmp(price) < 0 {
			continue
		}
		sum := new(big.Rat)
		for _, bar := range eligible {
			p, ok := new(big.Rat).SetString(bar.Close)
			if !ok || p.Sign() <= 0 || bar.Volume < 0 {
				return out, errors.New("invalid liquidity bar")
			}
			sum.Add(sum, new(big.Rat).Mul(p, new(big.Rat).SetInt64(bar.Volume)))
		}
		if sum.Cmp(new(big.Rat).Mul(adv, new(big.Rat).SetInt64(int64(cfg.Sessions)))) < 0 {
			continue
		}
		// Use the bar's historical ticker, not a potentially renamed reference.
		out.Members = append(out.Members, UniverseMember{AssetID: asset.ID, Symbol: last.Symbol, LastSession: last.Date, Close: last.Close, Sessions: len(eligible)})
	}
	slices.SortFunc(out.Members, func(a, b UniverseMember) int {
		if a.AssetID < b.AssetID {
			return -1
		}
		if a.AssetID > b.AssetID {
			return 1
		}
		return 0
	})
	return out, nil
}
