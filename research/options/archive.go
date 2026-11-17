package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

type page struct {
	Kind, URL, SHA256 string
	ReceivedAt        time.Time
	Body              string `json:",omitempty"`
}

// originalRecord links each archived object to its real receipt, separately
// from the detached scenario clocks used by the simulator.
type originalRecord struct {
	Kind, Symbol, Key, SourcePageSHA256 string
	OccurredAt, ObservedAt, AvailableAt time.Time
}

type manifest struct {
	Policy, AssetID, Underlying, SettledCash string
	PaidShares                               int
	Recipe                                   library.OptionConfig
	Assumptions                              options.Assumptions
	FeePerContract, SlippageBps              float64
	Times                                    []time.Time
	Pages                                    []page
}

// archive is deliberately private to this offline command. Its projected
// clocks are scenario assumptions, never writes to the point-in-time store.
type archive struct {
	input        manifest
	closes       map[string]time.Time
	spots        map[string]float64
	spotReceipts map[string]time.Time
	refs         map[string]alpacarecord.OptionRecord
	bars         map[string]map[string]alpacarecord.OptionRecord
	first        map[string]time.Time
	originals    []originalRecord
}

func readArchive(m manifest) (*archive, error) {
	if m.Policy != policy || m.AssetID == "" || m.Underlying == "" || m.PaidShares < 0 ||
		(m.Recipe.Kind != library.CoveredCall && m.Recipe.Kind != library.CashSecuredPut) ||
		(m.Recipe.Kind == library.CashSecuredPut && m.PaidShares != 0) ||
		len(m.Pages) == 0 || len(m.Pages) > 128 || len(m.Times) == 0 || len(m.Times) > 10000 {
		return nil, errors.New("invalid retrospective research manifest or policy")
	}
	a := &archive{input: m, closes: map[string]time.Time{}, spots: map[string]float64{}, spotReceipts: map[string]time.Time{},
		refs: map[string]alpacarecord.OptionRecord{}, bars: map[string]map[string]alpacarecord.OptionRecord{}, first: map[string]time.Time{}}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	count := 0
	for _, p := range m.Pages {
		if len(p.Body) > 1<<20 || p.ReceivedAt.IsZero() || digest([]byte(p.Body)) != p.SHA256 {
			return nil, errors.New("invalid archive page receipt or hash")
		}
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Query().Has("page_token") {
			return nil, errors.New("invalid archive request URL")
		}
		paths := map[string]string{"contracts": "paper-api.alpaca.markets/v2/options/contracts", "calendar": "paper-api.alpaca.markets/v2/calendar", "stock_bars": "data.alpaca.markets/v2/stocks/bars", "option_bars": "data.alpaca.markets/v1beta1/options/bars"}
		if paths[p.Kind] == "" || u.Host+u.Path != paths[p.Kind] {
			return nil, errors.New("unsupported archive page source")
		}
		if p.Kind == "calendar" {
			var days []struct{ Date, Open, Close string }
			if json.Unmarshal([]byte(p.Body), &days) != nil || len(days) == 0 {
				return nil, errors.New("invalid retained calendar")
			}
			for _, d := range days {
				closeAt, err := time.ParseInLocation("2006-01-02 15:04", d.Date+" "+d.Close, zone)
				openAt, openErr := time.ParseInLocation("2006-01-02 15:04", d.Date+" "+d.Open, zone)
				if err != nil || openErr != nil || !closeAt.After(openAt) || closeAt.After(p.ReceivedAt) || !a.closes[d.Date].IsZero() {
					return nil, errors.New("invalid or duplicate calendar session")
				}
				a.closes[d.Date] = closeAt
			}
			continue
		}
		var envelope struct {
			Contracts []json.RawMessage            `json:"option_contracts"`
			Bars      map[string][]json.RawMessage `json:"bars"`
			Next      *string                      `json:"next_page_token"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(p.Body), &envelope) != nil || json.Unmarshal([]byte(p.Body), &fields) != nil || fields["next_page_token"] == nil || envelope.Next != nil {
			return nil, errors.New("archive requires complete nonpaginated pages")
		}
		if p.Kind == "contracts" {
			q := u.Query()
			from, fromErr := time.Parse(time.DateOnly, q.Get("expiration_date_gte"))
			through, throughErr := time.Parse(time.DateOnly, q.Get("expiration_date_lte"))
			lo, loErr := strconv.ParseFloat(q.Get("strike_price_gte"), 64)
			hi, hiErr := strconv.ParseFloat(q.Get("strike_price_lte"), 64)
			if envelope.Contracts == nil || q.Get("underlying_symbols") != m.Underlying || q.Get("status") != "inactive" || fromErr != nil || throughErr != nil || through.Before(from) || loErr != nil || hiErr != nil || !(lo > 0 && hi >= lo) {
				return nil, errors.New("invalid contract page binding")
			}
			for _, raw := range envelope.Contracts {
				var c struct {
					ID, Symbol string
					Underlying string `json:"underlying_symbol"`
					Expiry     string `json:"expiration_date"`
					Strike     string `json:"strike_price"`
					Type       string `json:"type"`
				}
				if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.Symbol == "" || c.Underlying != m.Underlying || !slices.Contains([]string{"call", "put"}, c.Type) {
					return nil, errors.New("invalid archived contract")
				}
				exp, expErr := time.Parse(time.DateOnly, c.Expiry)
				strike, strikeErr := strconv.ParseFloat(c.Strike, 64)
				if expErr != nil || strikeErr != nil || exp.Before(from) || exp.After(through) || !(strike >= lo && strike <= hi) {
					return nil, errors.New("contract outside archived request filters")
				}
				if _, ok := a.refs[c.Symbol]; ok {
					return nil, errors.New("duplicate archived contract")
				}
				a.refs[c.Symbol] = alpacarecord.OptionRecord{Kind: "contract", Symbol: c.Symbol, Underlying: c.Underlying, Key: c.ID, Feed: "reference", OccurredAt: p.ReceivedAt, ObservedAt: p.ReceivedAt, AvailableAt: p.ReceivedAt, Data: raw}
				a.originals = append(a.originals, originalRecord{Kind: "contract", Symbol: c.Symbol, Key: c.ID, SourcePageSHA256: p.SHA256, OccurredAt: p.ReceivedAt, ObservedAt: p.ReceivedAt, AvailableAt: p.ReceivedAt})
			}
			continue
		}
		if envelope.Bars == nil || u.Query().Get("timeframe") != "1Day" || p.Kind == "stock_bars" && (u.Query().Get("symbols") != m.Underlying || u.Query().Get("adjustment") != "raw") {
			return nil, errors.New("invalid daily bar request binding")
		}
		for symbol, rows := range envelope.Bars {
			if symbol != u.Query().Get("symbols") {
				return nil, errors.New("unrequested bar symbol")
			}
			for _, raw := range rows {
				count++
				var b struct {
					At    time.Time `json:"t"`
					Close float64   `json:"c"`
				}
				if count > 50000 || json.Unmarshal(raw, &b) != nil || b.At.IsZero() || b.At.After(p.ReceivedAt) || b.Close <= 0 {
					return nil, errors.New("invalid archived bar")
				}
				date := b.At.In(zone).Format(time.DateOnly)
				a.originals = append(a.originals, originalRecord{Kind: p.Kind, Symbol: symbol, Key: date, SourcePageSHA256: p.SHA256, OccurredAt: b.At, ObservedAt: p.ReceivedAt, AvailableAt: p.ReceivedAt})
				if p.Kind == "stock_bars" {
					if _, ok := a.spots[date]; ok {
						return nil, errors.New("duplicate underlying bar")
					}
					a.spots[date] = b.Close
					a.spotReceipts[date] = p.ReceivedAt
					continue
				}
				if a.bars[symbol] == nil {
					a.bars[symbol] = map[string]alpacarecord.OptionRecord{}
				}
				if _, ok := a.bars[symbol][date]; ok {
					return nil, errors.New("duplicate option bar")
				}
				a.bars[symbol][date] = alpacarecord.OptionRecord{Kind: "bar", Symbol: symbol, Underlying: m.Underlying, Key: date, Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: b.At, ObservedAt: p.ReceivedAt, AvailableAt: p.ReceivedAt, Data: raw}
			}
		}
	}
	for date := range a.spots {
		if a.closes[date].IsZero() || a.closes[date].After(a.spotReceipts[date]) {
			return nil, errors.New("underlying bar lacks completed retained session")
		}
	}
	for symbol, bars := range a.bars {
		if _, ok := a.refs[symbol]; !ok {
			return nil, errors.New("option bars lack archived contract")
		}
		for date, b := range bars {
			closeAt := a.closes[date]
			if closeAt.IsZero() || b.OccurredAt.After(closeAt) || closeAt.After(b.ObservedAt) {
				return nil, errors.New("option bar lacks completed retained session")
			}
			if a.first[symbol].IsZero() || closeAt.Before(a.first[symbol]) {
				a.first[symbol] = closeAt
			}
		}
	}
	for i, at := range m.Times {
		if at.IsZero() || i > 0 && !at.After(m.Times[i-1]) {
			return nil, errors.New("research clocks must increase")
		}
		if _, err := a.spot(at); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *archive) spot(at time.Time) (options.Spot, error) {
	var out options.Spot
	for date, closeAt := range a.closes {
		assumed := closeAt.Add(15 * time.Minute)
		if !assumed.After(at) && closeAt.After(out.At) && a.spots[date] > 0 {
			out = options.Spot{AssetID: a.input.AssetID, Underlying: a.input.Underlying, Date: date, Price: a.spots[date], At: closeAt, ObservedAt: assumed, AvailableAt: assumed}
		}
	}
	if out.At.IsZero() {
		return out, errors.New("no completed underlying session at research clock")
	}
	return out, nil
}

// OptionChainAsOf supplies detached scenario records to the ordinary pricing
// and fill validators. A reference appears only with its first retained bar.
func (a *archive) OptionChainAsOf(ctx context.Context, underlying string, at time.Time) ([]alpacarecord.OptionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if underlying != a.input.Underlying {
		return nil, options.ErrChain
	}
	out := []alpacarecord.OptionRecord{}
	for symbol, first := range a.first {
		assumed := first.Add(15 * time.Minute)
		if assumed.After(at) {
			continue
		}
		r := a.refs[symbol]
		r.OccurredAt, r.ObservedAt, r.AvailableAt = assumed, assumed, assumed
		out = append(out, r)
		var latest alpacarecord.OptionRecord
		for date, b := range a.bars[symbol] {
			available := a.closes[date].Add(15 * time.Minute)
			if !available.After(at) && b.OccurredAt.After(latest.OccurredAt) {
				latest = b
				latest.ObservedAt, latest.AvailableAt = available, available
			}
		}
		if latest.Kind != "" {
			out = append(out, latest)
		}
	}
	slices.SortFunc(out, func(a, b alpacarecord.OptionRecord) int { return strings.Compare(a.Kind+a.Symbol, b.Kind+b.Symbol) })
	return out, nil
}

func (a *archive) OptionChain(ctx context.Context, asset string, at time.Time) (options.Chain, error) {
	if asset != a.input.AssetID {
		return options.Chain{}, fmt.Errorf("research asset differs")
	}
	spot, err := a.spot(at)
	if err != nil {
		return options.Chain{}, err
	}
	return options.Read(ctx, a, spot, at, a.input.Assumptions)
}

func (a *archive) OptionBars(ctx context.Context, asset string, at time.Time) (map[string]options.IndicativeBar, error) {
	if asset != a.input.AssetID {
		return nil, options.ErrChain
	}
	return options.ReadBars(ctx, a, a.input.Underlying, at)
}
