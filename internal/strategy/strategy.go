// Package strategy defines the one interface every trading strategy uses. A
// strategy reads a point-in-time view at its decision time and returns target
// weights. It never places orders, reads the clock or uses the network: code
// outside the strategy turns targets into orders.
package strategy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

// KindDailyBars is daily bars adjusted for splits and dividends, read as of
// the decision time. It is the only data kind so far; quotes, option chains,
// fundamentals and Kalshi books will add their own.
const KindDailyBars = "daily_bars_adjusted"

const (
	// MaxStateBytes bounds the state a strategy carries between decisions.
	MaxStateBytes = 64 << 10
	// MaxAssets bounds one view.
	MaxAssets = 1000
)

var (
	ErrInvalidSpec     = errors.New("invalid strategy spec")
	ErrFutureData      = errors.New("view data is later than the decision time")
	ErrInvalidDecision = errors.New("invalid strategy decision")

	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	paramPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	assetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)
	weightText   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)
	kinds        = []string{KindDailyBars}
)

// Strategy is one version of a strategy. Decide must depend only on the view:
// the same view gives the same decision.
type Strategy interface {
	Spec() Spec
	Decide(View) (Decision, error)
}

// Spec declares what a strategy is and needs. Version is the strategy code's
// version; bump it whenever its behaviour changes. Params are the strategy's
// parameters as text. Data lists the data kinds it reads, and Warmup the
// number of bars it needs before its first decision.
type Spec struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Params  map[string]string `json:"params"`
	Data    []string          `json:"data"`
	Warmup  int               `json:"warmup"`
}

// Validate reports whether the spec is well formed.
func (s Spec) Validate() error {
	if !namePattern.MatchString(s.Name) || s.Version == "" || len(s.Version) > 64 || s.Warmup < 0 || len(s.Data) == 0 || len(s.Params) > 64 {
		return ErrInvalidSpec
	}
	for k, v := range s.Params {
		if !paramPattern.MatchString(k) || len(v) > 256 {
			return fmt.Errorf("%w: parameter %q", ErrInvalidSpec, k)
		}
	}
	for _, d := range s.Data {
		if !slices.Contains(kinds, d) {
			return fmt.Errorf("%w: unknown data kind %q", ErrInvalidSpec, d)
		}
	}
	return nil
}

// ID is the content hash of the strategy version: its name, code version,
// parameters, data kinds and warm-up. Map order and data-kind order or
// repetition do not change it.
func (s Spec) ID() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	data := slices.Clone(s.Data)
	slices.Sort(data)
	data = slices.Compact(data)
	params := s.Params
	if params == nil {
		params = map[string]string{}
	}
	// encoding/json writes map keys in sorted order.
	raw, err := json.Marshal(struct {
		Name    string            `json:"name"`
		Version string            `json:"version"`
		Params  map[string]string `json:"params"`
		Data    []string          `json:"data"`
		Warmup  int               `json:"warmup"`
	}{s.Name, s.Version, params, data, s.Warmup})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "strategy-" + hex.EncodeToString(sum[:]), nil
}

// Position is a holding in the strategy's sleeve, in shares as exact decimal
// text. Negative is short.
type Position struct {
	Asset    string `json:"asset"`
	Quantity string `json:"quantity"`
}

// Fill is one of the strategy's executed fills.
type Fill struct {
	Asset    string    `json:"asset"`
	Time     time.Time `json:"time"`
	Quantity string    `json:"quantity"`
	Price    string    `json:"price"`
}

// Book is the strategy's own record at the decision time, kept by the engine.
type Book struct {
	Positions []Position
	Fills     []Fill
	State     []byte
}

// View is everything a strategy may see at its decision time. Its fields are
// unexported and its methods return copies, so a strategy cannot change it.
type View struct {
	at        time.Time
	assets    []string
	bars      map[string][]marketdata.AdjustedBar
	positions []Position
	fills     []Fill
	state     []byte
}

// At is the decision time. It is the time the view was built for, not the
// wall clock.
func (v View) At() time.Time { return v.at }

// Assets lists the assets in the view, sorted.
func (v View) Assets() []string { return slices.Clone(v.assets) }

// Bars returns an asset's adjusted daily bars as of the decision time, oldest
// first. It is empty unless the spec declares KindDailyBars.
func (v View) Bars(asset string) []marketdata.AdjustedBar { return slices.Clone(v.bars[asset]) }

func (v View) Positions() []Position { return slices.Clone(v.positions) }
func (v View) Fills() []Fill         { return slices.Clone(v.fills) }

// State is the state the strategy returned from its previous decision.
func (v View) State() []byte { return slices.Clone(v.state) }

// BarSource reads an asset's adjusted bars as of a clock, such as a closure
// over marketdata.Read with that asset's corporate actions.
type BarSource interface {
	AdjustedBars(ctx context.Context, assetID string, asOf time.Time) (marketdata.Series, error)
}

// Build reads the view for one decision. It loads only the data kinds the
// spec declares, and fails with ErrFutureData if the source returns a bar
// available after at, or the book holds a fill after at.
func Build(ctx context.Context, spec Spec, src BarSource, at time.Time, assets []string, book Book) (View, error) {
	if err := spec.Validate(); err != nil {
		return View{}, err
	}
	if at.IsZero() {
		return View{}, errors.New("view requires a decision time")
	}
	assets = slices.Clone(assets)
	slices.Sort(assets)
	if len(assets) > MaxAssets || slices.ContainsFunc(assets, func(a string) bool { return !assetPattern.MatchString(a) }) || len(slices.Compact(slices.Clone(assets))) != len(assets) {
		return View{}, errors.New("view requires distinct valid assets")
	}
	if len(book.State) > MaxStateBytes {
		return View{}, errors.New("strategy state is too large")
	}
	v := View{at: at.UTC(), assets: assets, bars: map[string][]marketdata.AdjustedBar{}, state: slices.Clone(book.State)}
	for _, f := range book.Fills {
		if f.Time.After(at) {
			return View{}, fmt.Errorf("%w: fill at %s", ErrFutureData, f.Time.Format(time.RFC3339Nano))
		}
	}
	v.fills = slices.Clone(book.Fills)
	v.positions = slices.Clone(book.Positions)
	if slices.Contains(spec.Data, KindDailyBars) {
		for _, a := range assets {
			s, err := src.AdjustedBars(ctx, a, at)
			if err != nil {
				return View{}, fmt.Errorf("bars for %s: %w", a, err)
			}
			if s.AssetID != a || !s.AsOf.Equal(at) {
				return View{}, fmt.Errorf("bars for %s were not read as of the decision time", a)
			}
			for _, b := range s.Bars {
				if b.Raw.AvailableAt.After(at) {
					return View{}, fmt.Errorf("%w: %s bar %s", ErrFutureData, a, b.Date)
				}
			}
			for _, c := range s.Applied {
				if c.KnownAt.After(at) {
					return View{}, fmt.Errorf("%w: %s action %s", ErrFutureData, a, c.SourceID)
				}
			}
			// The source may reuse its slice, so the view keeps its own copy.
			v.bars[a] = slices.Clone(s.Bars)
		}
	}
	return v, nil
}

// Target is the weight of the strategy's sleeve to hold in one asset, as exact
// decimal text from -1 to 1. Negative is short. The capital distributor turns
// weights into quantities and the engine turns those into orders.
type Target struct {
	Asset  string `json:"asset"`
	Weight string `json:"weight"`
}

// Decision is a strategy's output: the complete set of targets, where an
// asset left out is held at zero, and the state for its next decision.
type Decision struct {
	Targets []Target `json:"targets"`
	State   []byte   `json:"state,omitempty"`
}

// Decide asks the strategy for a decision and checks it: every target names a
// view asset at most once, and the absolute weights sum to at most 1. Targets
// come back sorted by asset. A panic in the strategy becomes an error.
func Decide(s Strategy, v View) (d Decision, err error) {
	defer func() {
		if r := recover(); r != nil {
			d, err = Decision{}, fmt.Errorf("%w: strategy panicked: %v", ErrInvalidDecision, r)
		}
	}()
	d, err = s.Decide(v)
	if err != nil {
		return Decision{}, err
	}
	if len(d.State) > MaxStateBytes {
		return Decision{}, fmt.Errorf("%w: state is too large", ErrInvalidDecision)
	}
	d.Targets = slices.Clone(d.Targets)
	slices.SortFunc(d.Targets, func(a, b Target) int { return cmp.Compare(a.Asset, b.Asset) })
	gross := new(big.Rat)
	for i, t := range d.Targets {
		w, ok := new(big.Rat).SetString(t.Weight)
		if !ok || !weightText.MatchString(t.Weight) || w.Cmp(big.NewRat(-1, 1)) < 0 || w.Cmp(big.NewRat(1, 1)) > 0 {
			return Decision{}, fmt.Errorf("%w: weight %q for %s", ErrInvalidDecision, t.Weight, t.Asset)
		}
		if _, ok := slices.BinarySearch(v.assets, t.Asset); !ok || i > 0 && d.Targets[i-1].Asset == t.Asset {
			return Decision{}, fmt.Errorf("%w: target %s is not a distinct view asset", ErrInvalidDecision, t.Asset)
		}
		gross.Add(gross, w.Abs(w))
	}
	if gross.Cmp(big.NewRat(1, 1)) > 0 {
		return Decision{}, fmt.Errorf("%w: absolute weights sum above 1", ErrInvalidDecision)
	}
	return d, nil
}
