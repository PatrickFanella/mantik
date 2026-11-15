package options

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

// ErrChain means a source supplied inconsistent or unavailable chain inputs.
var ErrChain = errors.New("invalid point-in-time option chain")

// Store discovers known contract references and latest daily bars, as of at.
type Store interface {
	OptionChainAsOf(context.Context, string, time.Time) ([]alpacarecord.OptionRecord, error)
}

// Spot is an unadjusted underlying close for the same session as option bars.
// At is the session close, not the provider's bar-start timestamp. It and all
// acquisition clocks must be no later than the decision. AssetID is a stable ID.
type Spot struct {
	AssetID, Underlying, Date   string
	Price                       float64
	At, ObservedAt, AvailableAt time.Time
}

// Assumptions are explicit model scenarios, not claims of observed rate curves.
// MaxAge bounds elapsed time since the market close. No default is inferred.
type Assumptions struct {
	Rate, Yield float64
	MaxAge      time.Duration
}

// Entry retains reference/price clocks and the European-model estimate. When
// EstimateStatus is not "ok", IV and Greeks are unavailable, never usable zeroes.
type Entry struct {
	Symbol, ContractID, Type, Expiry                                                              string
	Strike, Close                                                                                 float64
	ReferenceObservedAt, ReferenceAvailableAt, PriceOccurredAt, PriceObservedAt, PriceAvailableAt time.Time
	FeedSelection                                                                                 string
	EstimateStatus                                                                                string
	IV                                                                                            float64
	Greeks                                                                                        Greeks
}

// Chain is an indicative, potentially incomplete set of known standard contracts.
// Estimates are valued at Spot.At, with ACT/365 to 16:00 New York expiry time.
type Chain struct {
	AssetID, Underlying, Feed, Model string
	AsOf                             time.Time
	Spot                             Spot
	Assumptions                      Assumptions
	Entries                          []Entry
}

var standard = regexp.MustCompile(`^([A-Z]{1,6})([0-9]{6})([CP])([0-9]{8})$`)

// Read builds a deterministic chain from retained observations; it never fetches
// a listing or substitutes today's reference metadata into an earlier decision.
func Read(ctx context.Context, store Store, spot Spot, at time.Time, a Assumptions) (Chain, error) {
	out := Chain{AssetID: spot.AssetID, Underlying: spot.Underlying, Feed: "indicative", Model: ModelVersion, AsOf: at.UTC(), Spot: spot, Assumptions: a, Entries: []Entry{}}
	if err := out.Validate(at, spot.AssetID); err != nil {
		return Chain{}, err
	}
	records, err := store.OptionChainAsOf(ctx, spot.Underlying, at)
	if err != nil {
		return Chain{}, err
	}
	refs, bars := map[string]alpacarecord.OptionRecord{}, map[string]alpacarecord.OptionRecord{}
	for _, r := range records {
		if r.Underlying != spot.Underlying || r.ObservedAt.IsZero() || r.AvailableAt.Before(r.ObservedAt) || r.AvailableAt.After(at) || r.OccurredAt.IsZero() || r.OccurredAt.After(r.AvailableAt) {
			return Chain{}, ErrChain
		}
		var dst map[string]alpacarecord.OptionRecord
		switch r.Kind {
		case "contract":
			if r.Feed != "reference" {
				return Chain{}, ErrChain
			}
			dst = refs
		case "bar":
			if r.Feed != "indicative" || r.FeedSelection != "provider_default_unverified" {
				return Chain{}, ErrChain
			}
			dst = bars
		default:
			return Chain{}, ErrChain
		}
		if _, ok := dst[r.Symbol]; ok {
			return Chain{}, fmt.Errorf("%w: duplicate %s", ErrChain, r.Symbol)
		}
		dst[r.Symbol] = r
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Chain{}, err
	}
	for symbol, r := range refs {
		parts := standard.FindStringSubmatch(symbol)
		if parts == nil {
			continue
		}
		var ref struct {
			ID, Symbol string
			Underlying string `json:"underlying_symbol"`
			Expiry     string `json:"expiration_date"`
			Strike     string `json:"strike_price"`
			Type       string `json:"type"`
			Size       string `json:"size"`
		}
		if err := json.Unmarshal(r.Data, &ref); err != nil {
			return Chain{}, err
		}
		strike, err := strconv.ParseFloat(ref.Strike, 64)
		exp, e := time.Parse(time.DateOnly, ref.Expiry)
		encodedStrike, _ := strconv.ParseFloat(parts[4], 64)
		wantType := "call"
		if parts[3] == "P" {
			wantType = "put"
		}
		if err != nil || e != nil || !finite(strike) || strike <= 0 || ref.ID != r.Key || ref.Symbol != symbol || ref.Underlying != spot.Underlying || parts[1] != spot.Underlying || parts[2] != exp.Format("060102") || ref.Type != wantType || strike != encodedStrike/1000 {
			return Chain{}, ErrChain
		}
		// An adjusted deliverable cannot be priced as 100 ordinary shares.
		if ref.Size != "100" {
			continue
		}
		expires := time.Date(exp.Year(), exp.Month(), exp.Day(), 16, 0, 0, 0, zone)
		if !expires.After(at) {
			continue
		}
		entry := Entry{Symbol: symbol, ContractID: ref.ID, Type: ref.Type, Expiry: ref.Expiry, Strike: strike, ReferenceObservedAt: r.ObservedAt, ReferenceAvailableAt: r.AvailableAt, EstimateStatus: "missing_same_session_close"}
		bar, ok := bars[symbol]
		if ok {
			entry.PriceOccurredAt = bar.OccurredAt
			entry.PriceObservedAt = bar.ObservedAt
			entry.PriceAvailableAt = bar.AvailableAt
			entry.FeedSelection = bar.FeedSelection
			var price struct {
				Close float64 `json:"c"`
			}
			if json.Unmarshal(bar.Data, &price) != nil || !finite(price.Close) || price.Close <= 0 {
				return Chain{}, ErrChain
			}
			entry.Close = price.Close
			if bar.Key == spot.Date && bar.OccurredAt.In(zone).Format(time.DateOnly) == spot.Date && !bar.OccurredAt.After(spot.At) {
				p := Inputs{Type: ref.Type, Spot: spot.Price, Strike: strike, Years: expires.Sub(spot.At).Hours() / (24 * 365), Rate: a.Rate, Yield: a.Yield}
				iv, e := Implied(p, price.Close)
				entry.EstimateStatus = "unsolvable_indicative_price"
				if e == nil {
					p.Volatility = iv
					g, e := Price(p)
					if e != nil {
						return Chain{}, e
					}
					entry.IV = iv
					entry.Greeks = g
					entry.EstimateStatus = "ok"
				}
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	slices.SortFunc(out.Entries, func(a, b Entry) int {
		if a.Symbol < b.Symbol {
			return -1
		}
		if a.Symbol > b.Symbol {
			return 1
		}
		return 0
	})
	return out, nil
}

// Validate guards the clocks and labels at the strategy-source boundary.
func (c Chain) Validate(at time.Time, asset string) error {
	s := c.Spot
	a := c.Assumptions
	_, dateErr := time.Parse(time.DateOnly, s.Date)
	if at.IsZero() || !c.AsOf.Equal(at) || c.AssetID != asset || asset == "" || s.AssetID != asset || c.Underlying != s.Underlying || s.Underlying == "" || c.Feed != "indicative" || c.Model != ModelVersion || dateErr != nil || !finite(s.Price) || s.Price <= 0 || !finite(a.Rate) || !finite(a.Yield) || a.MaxAge <= 0 || s.At.IsZero() || s.At.After(at) || at.Sub(s.At) > a.MaxAge || s.ObservedAt.Before(s.At) || s.AvailableAt.Before(s.ObservedAt) || s.AvailableAt.After(at) {
		return ErrChain
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	if s.At.In(zone).Format(time.DateOnly) != s.Date {
		return ErrChain
	}
	for i, e := range c.Entries {
		parts := standard.FindStringSubmatch(e.Symbol)
		exp, err := time.Parse(time.DateOnly, e.Expiry)
		if parts == nil || err != nil || parts[1] != c.Underlying || parts[2] != exp.Format("060102") || e.ContractID == "" || !finite(e.Strike) || e.Strike <= 0 {
			return ErrChain
		}
		strike, _ := strconv.ParseFloat(parts[4], 64)
		wantType := "call"
		if parts[3] == "P" {
			wantType = "put"
		}
		if e.Strike != strike/1000 || e.Type != wantType || !time.Date(exp.Year(), exp.Month(), exp.Day(), 16, 0, 0, 0, zone).After(at) {
			return ErrChain
		}
		if e.ReferenceObservedAt.IsZero() || e.ReferenceAvailableAt.Before(e.ReferenceObservedAt) || e.ReferenceAvailableAt.After(at) || e.PriceAvailableAt.After(at) || e.PriceObservedAt.After(e.PriceAvailableAt) || e.PriceOccurredAt.After(e.PriceAvailableAt) || i > 0 && c.Entries[i-1].Symbol >= e.Symbol {
			return ErrChain
		}
		if e.EstimateStatus == "ok" {
			if e.PriceObservedAt.IsZero() || e.PriceOccurredAt.IsZero() || e.PriceOccurredAt.After(s.At) || e.PriceOccurredAt.In(zone).Format(time.DateOnly) != s.Date || e.FeedSelection != "provider_default_unverified" || !finite(e.Close) || e.Close <= 0 || !finite(e.IV) || e.IV <= 0 {
				return ErrChain
			}
			for _, v := range []float64{e.Greeks.Price, e.Greeks.Delta, e.Greeks.Gamma, e.Greeks.Vega, e.Greeks.Theta} {
				if !finite(v) {
					return ErrChain
				}
			}
		} else if (e.EstimateStatus != "missing_same_session_close" && e.EstimateStatus != "unsolvable_indicative_price") || e.IV != 0 || e.Greeks != (Greeks{}) {
			return ErrChain
		}
	}
	return nil
}
