// Package strategy defines the one interface every trading strategy uses. A
// strategy reads a point-in-time view at its decision time and returns target
// weights. It never places orders, reads the clock or uses the network: code
// outside the strategy turns targets into orders.
package strategy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
	"git.subcult.tv/subculture-collective/mantik/internal/strategyidentity"
)

// KindDailyBars is daily bars adjusted for splits and dividends, read as of
// the decision time.
const KindDailyBars = "daily_bars_adjusted"

// KindOptionChains supplies indicative option chains for the view's underlyings.
const KindOptionChains = "option_chains_indicative"

// KindStockUniverse supplies screened membership known at the decision time.
const KindStockUniverse = "stock_universe"

const (
	// MaxStateBytes bounds the state a strategy carries between decisions.
	MaxStateBytes = 64 << 10
	// MaxAssets bounds one view.
	MaxAssets = 1000
)

var (
	ErrInvalidSpec     = strategyidentity.ErrInvalidSpec
	ErrFutureData      = errors.New("view data is later than the decision time")
	ErrInvalidDecision = errors.New("invalid strategy decision")

	assetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)
	weightText   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)
)

// Strategy is one version of a strategy. Decide must depend only on the view:
// the same view gives the same decision.
type Strategy interface {
	Spec() Spec
	Decide(View) (Decision, error)
}

// Spec is the canonical strategy version, shared with declaration storage.
type Spec = strategyidentity.Spec

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
	chains    map[string]options.Chain
	universe  []string
	positions []Position
	fills     []Fill
	state     []byte
}

// At is the decision time. It is the time the view was built for, not the
// wall clock.
func (v View) At() time.Time { return v.at }

// Assets lists the assets in the view, sorted.
func (v View) Assets() []string { return slices.Clone(v.assets) }

// Universe lists eligible configured asset IDs from the point-in-time source.
// It is empty unless the strategy declares KindStockUniverse.
func (v View) Universe() []string { return slices.Clone(v.universe) }

// Bars returns an asset's adjusted daily bars as of the decision time, oldest
// first. It is empty unless the spec declares KindDailyBars.
func (v View) Bars(asset string) []marketdata.AdjustedBar { return slices.Clone(v.bars[asset]) }

// RecentBars returns at most count latest adjusted bars without copying the
// entire retained history. A nonpositive count returns no bars.
func (v View) RecentBars(asset string, count int) []marketdata.AdjustedBar {
	if count <= 0 {
		return nil
	}
	bars := v.bars[asset]
	return slices.Clone(bars[max(0, len(bars)-count):])
}

// OptionChain returns the underlying asset's indicative chain and whether it
// was declared and loaded. Its entries are copied; estimates expose their status.
func (v View) OptionChain(asset string) (options.Chain, bool) {
	c, ok := v.chains[asset]
	c.Entries = slices.Clone(c.Entries)
	return c, ok
}

func (v View) Positions() []Position { return slices.Clone(v.positions) }
func (v View) Fills() []Fill         { return slices.Clone(v.fills) }

// State is the state the strategy returned from its previous decision.
func (v View) State() []byte { return slices.Clone(v.state) }

// BarSource reads an asset's adjusted bars as of a clock, such as a closure
// over marketdata.Read with that asset's corporate actions.
type BarSource interface {
	AdjustedBars(ctx context.Context, assetID string, asOf time.Time) (marketdata.Series, error)
}

// ChainSource is the additional capability a source must implement when a spec
// declares KindOptionChains. The source binds stable asset IDs to underlying
// symbols and calls options.Read with explicit same-session spot and assumptions.
type ChainSource interface {
	OptionChain(ctx context.Context, assetID string, asOf time.Time) (options.Chain, error)
}

// UniverseSource supplies the screened universe at the decision clock. Sources
// must use known reference observations; backdated current listings are refused.
type UniverseSource interface {
	StockUniverse(ctx context.Context, asOf time.Time) (marketdata.UniverseReport, error)
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
	v := View{at: at.UTC(), assets: assets, bars: map[string][]marketdata.AdjustedBar{}, chains: map[string]options.Chain{}, state: slices.Clone(book.State)}
	for _, f := range book.Fills {
		if f.Time.After(at) {
			return View{}, fmt.Errorf("%w: fill at %s", ErrFutureData, f.Time.Format(time.RFC3339Nano))
		}
	}
	v.fills = slices.Clone(book.Fills)
	v.positions = slices.Clone(book.Positions)
	if slices.Contains(spec.Data, KindStockUniverse) {
		u, ok := src.(UniverseSource)
		if !ok {
			return View{}, errors.New("stock universe requires a universe-capable source")
		}
		r, err := u.StockUniverse(ctx, at)
		if err != nil {
			return View{}, fmt.Errorf("stock universe: %w", err)
		}
		if !r.AsOf.Equal(at) || r.ReferenceObservedAt.IsZero() || r.ReferenceObservedAt.After(at) {
			return View{}, fmt.Errorf("%w: universe reference/clock", ErrFutureData)
		}
		seen := map[string]bool{}
		for _, m := range r.Members {
			if !assetPattern.MatchString(m.AssetID) || seen[m.AssetID] || m.LastSession >= at.UTC().Format(time.DateOnly) {
				return View{}, errors.New("invalid universe member")
			}
			if _, err := time.Parse(time.DateOnly, m.LastSession); err != nil {
				return View{}, err
			}
			seen[m.AssetID] = true
			if _, ok := slices.BinarySearch(assets, m.AssetID); ok {
				v.universe = append(v.universe, m.AssetID)
			}
		}
		slices.Sort(v.universe)
	}
	if slices.Contains(spec.Data, KindDailyBars) {
		if src == nil {
			return View{}, errors.New("daily bars require a source")
		}
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
	if slices.Contains(spec.Data, KindOptionChains) {
		chains, ok := src.(ChainSource)
		if !ok {
			return View{}, errors.New("option chains require a chain-capable source")
		}
		for _, asset := range assets {
			c, err := chains.OptionChain(ctx, asset, at)
			if err != nil {
				return View{}, fmt.Errorf("chain for %s: %w", asset, err)
			}
			if err := c.Validate(at, asset); err != nil {
				return View{}, fmt.Errorf("%w: chain for %s: %v", ErrFutureData, asset, err)
			}
			c.Entries = slices.Clone(c.Entries)
			v.chains[asset] = c
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
	// KeepPositions issues no replacement targets on this decision. It avoids
	// daily rebalancing between a strategy's scheduled rebalances.
	KeepPositions bool `json:"keep_positions,omitempty"`
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
	if d.KeepPositions && len(d.Targets) > 0 {
		return Decision{}, fmt.Errorf("%w: hold cannot include targets", ErrInvalidDecision)
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
