// Package marketdata reads stored daily bars adjusted for splits and dividends.
// Stored bars stay raw; adjustment happens only when they are read, using the
// corporate actions known at the read clock.
package marketdata

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Scale is the number of decimal places adjusted values are rounded to. They
// are computed exactly and rounded only when rendered.
const Scale = 12

// MaxActions bounds the actions one read considers.
const MaxActions = 4096

var (
	ErrInvalidAction     = errors.New("invalid corporate action")
	ErrUnsupportedAction = errors.New("unsupported corporate action")

	newYork      = mustZone("America/New_York")
	plainDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
)

func mustZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return zone
}

// CorporateAction is one split or cash dividend for one asset. KnownAt is the
// earliest clock at which a reader may use it. Amount is USD per share held
// before the ex-date. A split turns From shares into To shares.
type CorporateAction struct {
	SourceID string    `json:"source_id"`
	Kind     string    `json:"kind"` // split or cash_dividend
	ExDate   string    `json:"ex_date"`
	PayDate  string    `json:"pay_date,omitempty"`
	Amount   string    `json:"amount,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	KnownAt  time.Time `json:"known_at"`
}

// AdjustedBar is one stored bar with prices adjusted back to the share basis
// of the read clock. Prices and VWAP carry split and dividend adjustment;
// Volume carries split adjustment only. TotalReturn is the value at this
// bar's close of one share bought at the first bar's close, with dividends
// reinvested at the close of their pay date.
type AdjustedBar struct {
	Date        string           `json:"date"`
	Open        string           `json:"open"`
	High        string           `json:"high"`
	Low         string           `json:"low"`
	Close       string           `json:"close"`
	VWAP        string           `json:"vwap"`
	Volume      string           `json:"volume"`
	TotalReturn string           `json:"total_return"`
	Raw         storage.DailyBar `json:"raw"`
}

// Series is one asset's bars as of a clock. Applied lists, in ex-date order,
// the actions known at the clock whose ex-date had arrived. An action with an
// ex-date on or before the first bar is listed but changes nothing.
type Series struct {
	AssetID string            `json:"asset_id"`
	AsOf    time.Time         `json:"as_of"`
	Bars    []AdjustedBar     `json:"bars"`
	Applied []CorporateAction `json:"applied"`
}

// BarReader is the daily bar store's point-in-time read.
type BarReader interface {
	DailyBarsAsOf(ctx context.Context, assetID string, asOf time.Time) ([]storage.DailyBar, error)
}

// Read returns the asset's stored bars as of asOf, adjusted with the actions
// known at asOf. The caller supplies the actions for this asset; tickers are
// reused, so matching actions to an asset is the caller's job.
func Read(ctx context.Context, store BarReader, assetID string, actions []CorporateAction, asOf time.Time) (Series, error) {
	bars, err := store.DailyBarsAsOf(ctx, assetID, asOf)
	if err != nil {
		return Series{}, err
	}
	return Adjust(assetID, bars, actions, asOf)
}

type action struct {
	CorporateAction
	ex, pay       time.Time
	amount, ratio *big.Rat // ratio is To/From
}

// Adjust adjusts raw bars in date order with the actions known at asOf whose
// ex-date has arrived by asOf's New York date. It never changes its inputs.
func Adjust(assetID string, raw []storage.DailyBar, actions []CorporateAction, asOf time.Time) (Series, error) {
	if asOf.IsZero() {
		return Series{}, errors.New("adjusted read requires an as-of clock")
	}
	applied, err := known(actions, asOf)
	if err != nil {
		return Series{}, err
	}
	out := Series{AssetID: assetID, AsOf: asOf.UTC(), Bars: []AdjustedBar{}, Applied: []CorporateAction{}}
	for _, a := range applied {
		out.Applied = append(out.Applied, a.CorporateAction)
	}
	if len(raw) == 0 {
		return out, nil
	}
	type parsed struct {
		day                          time.Time
		open, high, low, close, vwap *big.Rat
		volume                       *big.Rat
	}
	bars := make([]parsed, len(raw))
	for i, b := range raw {
		day, err := time.Parse(time.DateOnly, b.Date)
		if err != nil || b.AssetID != assetID || i > 0 && !day.After(bars[i-1].day) {
			return Series{}, fmt.Errorf("adjusted read needs one asset's bars in date order: bar %d", i)
		}
		p := parsed{day: day, volume: new(big.Rat).SetInt64(b.Volume)}
		for _, f := range []struct {
			dst **big.Rat
			src string
		}{{&p.open, b.Open}, {&p.high, b.High}, {&p.low, b.Low}, {&p.close, b.Close}, {&p.vwap, b.VWAP}} {
			v, ok := new(big.Rat).SetString(f.src)
			if !ok || !plainDecimal.MatchString(f.src) {
				return Series{}, fmt.Errorf("adjusted read: bar %s has an invalid price", b.Date)
			}
			*f.dst = v
		}
		if p.close.Sign() <= 0 {
			return Series{}, fmt.Errorf("adjusted read: bar %s has a zero close", b.Date)
		}
		bars[i] = p
	}

	// Back-adjustment: a bar before an ex-date is scaled by every later action.
	// A split scales prices by From/To and volume by To/From. A dividend scales
	// prices by 1 - amount/close, using the raw close of the last bar before
	// its ex-date put in the dividend's share basis: scaled by any split that
	// went ex after that bar and before the dividend.
	price := make([]*big.Rat, len(bars))
	volume := make([]*big.Rat, len(bars))
	pf, vf := big.NewRat(1, 1), big.NewRat(1, 1)
	next := len(applied) - 1
	for i := len(bars) - 1; i >= 0; i-- {
		for next >= 0 && applied[next].ex.After(bars[i].day) {
			a := applied[next]
			switch a.Kind {
			case "split":
				pf.Quo(pf, a.ratio)
				vf.Mul(vf, a.ratio)
			case "cash_dividend":
				// bars[i] is the last bar before this ex-date.
				basis := new(big.Rat).Set(bars[i].close)
				for _, s := range applied[:next] {
					if s.Kind == "split" && s.ex.After(bars[i].day) && s.ex.Before(a.ex) {
						basis.Quo(basis, s.ratio)
					}
				}
				f := new(big.Rat).Quo(a.amount, basis)
				if f.Cmp(big.NewRat(1, 1)) >= 0 {
					return Series{}, fmt.Errorf("%w: dividend %s is not below the prior close", ErrInvalidAction, a.SourceID)
				}
				pf.Mul(pf, f.Sub(big.NewRat(1, 1), f))
			}
			next--
		}
		price[i], volume[i] = new(big.Rat).Set(pf), new(big.Rat).Set(vf)
	}

	// Total return: hold one share from the first close. On an ex-date the
	// shares held before it earn the dividend; the cash arrives on the pay
	// date and is reinvested at that bar's close. A dividend whose pay date
	// has no bar yet is not counted.
	type pending struct {
		pay  time.Time
		cash *big.Rat
	}
	shares := big.NewRat(1, 1)
	owed := []pending{}
	first := 0
	for first < len(applied) && !applied[first].ex.After(bars[0].day) {
		first++
	}
	for i, b := range bars {
		start := first
		for first < len(applied) && !applied[first].ex.After(b.day) {
			first++
		}
		// Actions since the previous bar, in ex-date order. On one ex-date
		// dividends sort before splits: their amount is per share held before
		// the ex-date, so a split on the same date must not scale them.
		for _, a := range applied[start:first] {
			switch a.Kind {
			case "cash_dividend":
				owed = append(owed, pending{a.pay, new(big.Rat).Mul(shares, a.amount)})
			case "split":
				shares.Mul(shares, a.ratio)
			}
		}
		kept := owed[:0]
		for _, o := range owed {
			if o.pay.After(b.day) {
				kept = append(kept, o)
				continue
			}
			shares.Add(shares, o.cash.Quo(o.cash, b.close))
		}
		owed = kept
		r := raw[i]
		total := new(big.Rat).Mul(shares, b.close)
		out.Bars = append(out.Bars, AdjustedBar{
			Date:        r.Date,
			Open:        render(new(big.Rat).Mul(b.open, price[i])),
			High:        render(new(big.Rat).Mul(b.high, price[i])),
			Low:         render(new(big.Rat).Mul(b.low, price[i])),
			Close:       render(new(big.Rat).Mul(b.close, price[i])),
			VWAP:        render(new(big.Rat).Mul(b.vwap, price[i])),
			Volume:      render(new(big.Rat).Mul(b.volume, volume[i])),
			TotalReturn: render(total.Quo(total, bars[0].close)),
			Raw:         r,
		})
	}
	return out, nil
}

// known validates the actions and returns, in ex-date order, those usable at
// asOf. For a repeated source ID it keeps the latest version known at asOf.
func known(actions []CorporateAction, asOf time.Time) ([]action, error) {
	if len(actions) > MaxActions {
		return nil, fmt.Errorf("%w: a read takes at most %d actions", ErrInvalidAction, MaxActions)
	}
	n := asOf.In(newYork)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	latest := map[string]action{}
	for i, c := range actions {
		a, err := parse(c)
		if err != nil {
			return nil, fmt.Errorf("action %d: %w", i, err)
		}
		if a.KnownAt.After(asOf) {
			continue
		}
		if prior, ok := latest[a.SourceID]; ok {
			if prior.KnownAt.Equal(a.KnownAt) && prior.CorporateAction != a.CorporateAction {
				return nil, fmt.Errorf("%w: %s has two versions known at the same time", ErrInvalidAction, a.SourceID)
			}
			if !a.KnownAt.After(prior.KnownAt) {
				continue
			}
		}
		latest[a.SourceID] = a
	}
	// The ex-date is checked on the selected version, so a known correction
	// that postpones an action also withdraws its earlier date.
	out := make([]action, 0, len(latest))
	for _, a := range latest {
		if !a.ex.After(today) {
			out = append(out, a)
		}
	}
	dividendFirst := func(kind string) int {
		if kind == "cash_dividend" {
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b action) int {
		return cmp.Or(a.ex.Compare(b.ex), cmp.Compare(dividendFirst(a.Kind), dividendFirst(b.Kind)), cmp.Compare(a.SourceID, b.SourceID))
	})
	return out, nil
}

func parse(c CorporateAction) (action, error) {
	a := action{CorporateAction: c}
	a.KnownAt = c.KnownAt.UTC()
	ex, err := time.Parse(time.DateOnly, c.ExDate)
	if err != nil || c.SourceID == "" || len(c.SourceID) > 128 || c.KnownAt.IsZero() {
		return action{}, ErrInvalidAction
	}
	a.ex = ex
	switch c.Kind {
	case "cash_dividend":
		amount, ok := positive(c.Amount)
		pay, err := time.Parse(time.DateOnly, c.PayDate)
		if !ok || err != nil || pay.Before(ex) || c.From != "" || c.To != "" {
			return action{}, ErrInvalidAction
		}
		a.amount, a.pay = amount, pay
	case "split":
		from, ok1 := positive(c.From)
		to, ok2 := positive(c.To)
		if !ok1 || !ok2 || c.Amount != "" || c.PayDate != "" {
			return action{}, ErrInvalidAction
		}
		a.ratio = to.Quo(to, from)
	default:
		return action{}, fmt.Errorf("%w: %q", ErrUnsupportedAction, c.Kind)
	}
	return a, nil
}

func positive(s string) (*big.Rat, bool) {
	if !plainDecimal.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok && r.Sign() > 0
}

// render rounds to Scale places, half away from zero, without trailing zeros.
func render(r *big.Rat) string {
	s := r.FloatString(Scale)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}
