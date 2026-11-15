package kalshi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// SimulationVersion names whole-contract, recorded-depth, taker-only simulation.
const SimulationVersion = "kalshi-notional-depth-fees-v2"
const dollarMicros int64 = 1000000

// SimulationStore reads recorded markets and books without venue order access.
type SimulationStore interface {
	KalshiMarketsAsOf(context.Context, time.Time) ([]storage.KalshiMarket, error)
	KalshiBooksAsOf(context.Context, string, time.Time) ([]storage.KalshiOrderBook, error)
}

// SimOrder is a complete immutable simulated limit order, not a venue request.
// At is the decision clock. Execution uses a strictly later observed book.
type SimOrder struct {
	ID, MarketTicker, Side, Action, Limit string
	Contracts                             int64
	At                                    time.Time
}

// SimFill reports dollars in integer microdollars and whole contracts. Fee is
// the conservative rounded-per-level taker fee; no resting maker fill is inferred.
type SimFill struct {
	OrderID, MarketTicker, Side, Action string
	Contracts, PriceMicros, FeeMicros   int64
	At                                  time.Time
	SourceSHA256, FeeSourceSHA256       string
}

// NotionalReport deliberately has no engine.Result/portfolio return field.
// Cash excludes inventory value. Payout is official settlement cash only.
type NotionalReport struct {
	Kind                                 string
	IncludedInSPY                        bool
	Version                              string
	CashMicros, FeesMicros, PayoutMicros int64
	Positions                            map[string]int64
	Fills                                []SimFill
}

type executed struct {
	order SimOrder
	at    time.Time
	fills []SimFill
}

// NotionalBook is a sequential offline simulator. It owns no broker credentials,
// SQL write authority or Alpaca book. Create a separate instance per replay.
type NotionalBook struct {
	cash, fees, payout int64
	last               time.Time
	eventAt            time.Time // most recent applied fill or settlement cash event
	positions          map[string]int64
	consumed           map[string]int64
	orders             map[string]executed
	settlements        map[string]bool
	fills              []SimFill
}

// NewNotionalBook opens a separate simulated cash book, in microdollars.
func NewNotionalBook(initialMicros int64) (*NotionalBook, error) {
	if initialMicros <= 0 || initialMicros > 1000000000000 {
		return nil, errors.New("notional capital must be in (0, $1m]")
	}
	return &NotionalBook{cash: initialMicros, positions: map[string]int64{}, consumed: map[string]int64{}, orders: map[string]executed{}, settlements: map[string]bool{}}, nil
}
func simHash(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func numberRat(text string) (*big.Rat, error) {
	if len(text) == 0 || len(text) > 32 || strings.ContainsAny(text, "eE/+- ") {
		return nil, errors.New("invalid fixed decimal")
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || r.Sign() < 0 {
		return nil, errors.New("invalid fixed decimal")
	}
	return r, nil
}
func micros(text string) (int64, error) {
	r, e := numberRat(text)
	if e != nil {
		return 0, e
	}
	r.Mul(r, big.NewRat(dollarMicros, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, errors.New("price exceeds microdollar precision")
	}
	return r.Num().Int64(), nil
}
func ceilRat(r *big.Rat) int64 {
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// takerFee implements the declared direct-member precision: model fee rounded
// up to 1 microdollar, then buy/sell balance change aligned to $0.0001. Rounding
// each displayed level independently and retaining no rebate is conservative.
func takerFee(price, qty int64, multiplier *big.Rat) (int64, error) {
	fee := new(big.Rat).SetInt64(qty)
	fee.Mul(fee, big.NewRat(price*(dollarMicros-price), dollarMicros*dollarMicros))
	fee.Mul(fee, big.NewRat(7, 100))
	fee.Mul(fee, multiplier)
	fee.Mul(fee, big.NewRat(dollarMicros, 1))
	if fee.Cmp(big.NewRat(1000000000000, 1)) > 0 {
		return 0, errors.New("fee overflow")
	}
	return ceilRat(fee), nil
}

type simLevel struct {
	price, qty int64
	key        string
}

// Execute atomically fills the executable part against recorded displayed bids.
// Buying YES consumes complementary NO bids (and conversely). Selling requires
// owned contracts. Consumed depth is shared across orders and repeated snapshots;
// polling an unchanged book never manufactures replenished liquidity.
func (b *NotionalBook) Execute(ctx context.Context, store SimulationStore, o SimOrder, at time.Time, event EventObservation) ([]SimFill, error) {
	if old, ok := b.orders[o.ID]; ok {
		if old.order != o || !old.at.Equal(at) {
			return nil, errors.New("conflicting simulated order identity")
		}
		return slices.Clone(old.fills), nil
	}
	limit, e := micros(o.Limit)
	if e != nil || o.ID == "" || len(o.ID) > 128 || o.MarketTicker == "" || !slices.Contains([]string{"yes", "no"}, o.Side) || !slices.Contains([]string{"buy", "sell"}, o.Action) || o.Contracts < 1 || o.Contracts > 1000000 || limit <= 0 || limit >= dollarMicros || o.At.IsZero() || !at.After(o.At) || at.Before(b.last) {
		return nil, errors.New("invalid or out-of-order simulated order")
	}
	if _, done := b.settlements[o.MarketTicker]; done {
		return nil, errors.New("market is already settled")
	}
	markets, e := store.KalshiMarketsAsOf(ctx, at)
	if e != nil {
		return nil, e
	}
	var market *storage.KalshiMarket
	for i := range markets {
		if markets[i].ObservedAt.After(at) {
			return nil, errors.New("future market leaked by source")
		}
		if markets[i].Ticker == o.MarketTicker {
			if market != nil {
				return nil, errors.New("duplicate market state")
			}
			market = &markets[i]
		}
	}
	if market == nil || market.ObservedAt.IsZero() || simHash(market.Original) != market.SHA256 || simHash(market.SeriesOriginal) != market.SeriesSHA256 || !slices.Contains([]string{"active", "open"}, market.Status) {
		return nil, errors.New("no known open market")
	}
	var page struct {
		Markets []Market `json:"markets"`
	}
	if json.Unmarshal([]byte(market.Original), &page) != nil {
		return nil, errors.New("invalid market page")
	}
	matches := 0
	for _, raw := range page.Markets {
		if raw.Ticker == o.MarketTicker {
			matches++
			if raw.EventTicker != market.EventTicker || raw.Status != market.Status || raw.CloseTime != market.CloseTime {
				return nil, errors.New("normalized market differs from raw source")
			}
		}
	}
	if matches != 1 {
		return nil, errors.New("market must occur exactly once in retained page")
	}
	if event.Ticker != market.EventTicker || event.ObservedAt.IsZero() || event.ObservedAt.After(at) || simHash(event.Original) != event.SHA256 {
		return nil, errors.New("missing or future event fee evidence")
	}
	var ev struct {
		Event struct {
			Ticker     string       `json:"event_ticker"`
			Series     string       `json:"series_ticker"`
			FeeType    *string      `json:"fee_type_override"`
			Multiplier *json.Number `json:"fee_multiplier_override"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(event.Original), &ev) != nil || ev.Event.Ticker != market.EventTicker || ev.Event.Series != market.SeriesTicker {
		return nil, errors.New("event identity does not match market/series")
	}
	deadline, e := time.Parse(time.RFC3339, market.CloseTime)
	if e != nil || !deadline.After(at) {
		return nil, errors.New("market is closed")
	}
	var series struct {
		Series struct {
			Ticker     string      `json:"ticker"`
			FeeType    string      `json:"fee_type"`
			Multiplier json.Number `json:"fee_multiplier"`
		} `json:"series"`
	}
	if json.Unmarshal([]byte(market.SeriesOriginal), &series) != nil || series.Series.Ticker != market.SeriesTicker {
		return nil, errors.New("invalid series fee evidence")
	}
	if ev.Event.FeeType != nil {
		series.Series.FeeType = *ev.Event.FeeType
	}
	if ev.Event.Multiplier != nil {
		series.Series.Multiplier = *ev.Event.Multiplier
	}
	if !slices.Contains([]string{"quadratic", "quadratic_with_maker_fees", "quadratic_with_combo_maker_fees"}, series.Series.FeeType) {
		return nil, errors.New("unsupported or missing fee schedule")
	}
	multiplier, e := numberRat(series.Series.Multiplier.String())
	if e != nil || multiplier.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, errors.New("invalid fee multiplier")
	}
	books, e := store.KalshiBooksAsOf(ctx, o.MarketTicker, at)
	if e != nil {
		return nil, e
	}
	var book *storage.KalshiOrderBook
	for i := range books {
		r := &books[i]
		if r.MarketTicker != o.MarketTicker || r.ObservedAt.After(at) {
			return nil, errors.New("future or wrong book leaked by source")
		}
		if book == nil || r.ObservedAt.After(book.ObservedAt) {
			book = r
		}
	}
	if book == nil || !book.ObservedAt.After(o.At) || simHash(book.Original) != book.SHA256 {
		return nil, errors.New("no strictly later recorded book")
	}
	if book.ObservedAt.Before(b.eventAt) {
		return nil, errors.New("recorded book precedes an applied financial event")
	}
	if market.ObservedAt.After(book.ObservedAt) || event.ObservedAt.After(book.ObservedAt) {
		return nil, errors.New("market/fee evidence was not known at simulated fill time")
	}
	var levels struct {
		Yes [][]string `json:"yes_dollars"`
		No  [][]string `json:"no_dollars"`
	}
	if json.Unmarshal(book.Book, &levels) != nil {
		return nil, errors.New("invalid fixed-point orderbook")
	}
	var original struct {
		Book json.RawMessage `json:"orderbook_fp"`
	}
	var sourceLevels struct {
		Yes [][]string `json:"yes_dollars"`
		No  [][]string `json:"no_dollars"`
	}
	if json.Unmarshal([]byte(book.Original), &original) != nil || json.Unmarshal(original.Book, &sourceLevels) != nil {
		return nil, errors.New("invalid source orderbook")
	}
	a, _ := json.Marshal(levels)
	z, _ := json.Marshal(sourceLevels)
	if string(a) != string(z) {
		return nil, errors.New("normalized book differs from raw source")
	}
	side := o.Side
	if o.Action == "buy" {
		if side == "yes" {
			side = "no"
		} else {
			side = "yes"
		}
	}
	raw := levels.Yes
	if side == "no" {
		raw = levels.No
	}
	if len(raw) > 10000 {
		return nil, errors.New("book depth exceeds bound")
	}
	parsed := []simLevel{}
	seen := map[int64]bool{}
	for _, r := range raw {
		if len(r) != 2 {
			return nil, errors.New("invalid book level")
		}
		p, e := micros(r[0])
		q, qe := numberRat(r[1])
		if e != nil || qe != nil || p <= 0 || p >= dollarMicros || q.Cmp(big.NewRat(1000000, 1)) > 0 || seen[p] {
			return nil, errors.New("invalid price, depth or duplicate level")
		}
		seen[p] = true
		qty := new(big.Int).Quo(q.Num(), q.Denom()).Int64()
		key := fmt.Sprintf("%s/%s/%d", o.MarketTicker, side, p)
		if o.Action == "buy" {
			p = dollarMicros - p
		}
		parsed = append(parsed, simLevel{p, max(0, qty-b.consumed[key]), key})
	}
	slices.SortFunc(parsed, func(a, c simLevel) int {
		if o.Action == "buy" {
			return int(a.price - c.price)
		}
		return int(c.price - a.price)
	})
	posKey := o.MarketTicker + "/" + o.Side
	if o.Action == "sell" && b.positions[posKey] < o.Contracts {
		return nil, errors.New("cannot sell unowned contracts")
	}
	fills := []SimFill{}
	used := map[string]int64{}
	remaining := o.Contracts
	delta, totalFees, totalQty := int64(0), int64(0), int64(0)
	for _, l := range parsed {
		if remaining == 0 {
			break
		}
		if o.Action == "buy" && l.price > limit || o.Action == "sell" && l.price < limit {
			continue
		}
		qty := min(remaining, l.qty)
		if qty == 0 {
			continue
		}
		fee, e := takerFee(l.price, qty, multiplier)
		if e != nil {
			return nil, e
		}
		revenue := qty * l.price
		if o.Action == "buy" {
			revenue = -revenue
		}
		change := revenue - fee
		aligned := change / 100 * 100
		if change < 0 && change%100 != 0 {
			aligned -= 100
		}
		fee += change - aligned
		delta += aligned
		totalFees += fee
		totalQty += qty
		remaining -= qty
		used[l.key] += qty
		fills = append(fills, SimFill{o.ID, o.MarketTicker, o.Side, o.Action, qty, l.price, fee, book.ObservedAt, book.SHA256, market.SeriesSHA256 + "/" + event.SHA256})
	}
	if b.cash+delta < 0 || b.cash+delta > 1000000000000000 {
		return nil, errors.New("insufficient notional cash or balance bound")
	}
	b.cash += delta
	b.fees += totalFees
	if o.Action == "buy" {
		b.positions[posKey] += totalQty
	} else {
		b.positions[posKey] -= totalQty
	}
	for key, n := range used {
		b.consumed[key] += n
	}
	b.fills = append(b.fills, fills...)
	if totalQty > 0 {
		b.eventAt = book.ObservedAt
	}
	b.last = at
	b.orders[o.ID] = executed{o, at, slices.Clone(fills)}
	return fills, nil
}

// SettlementReader supplies immutable official outcomes retained by the existing
// weather collector/store, without granting the simulator source write authority.
type SettlementReader interface {
	KalshiWeatherSettlements(context.Context, time.Time) ([]storage.KalshiWeatherSettlement, error)
}

// Settle selects the retained official outcome at the cutoff, then pays $1 for
// the winner and $0 for the loser exactly once. It never accepts a prediction.
func (b *NotionalBook) Settle(ctx context.Context, store SettlementReader, ticker string, at time.Time) error {
	rows, err := store.KalshiWeatherSettlements(ctx, at)
	if err != nil {
		return err
	}
	var result *storage.KalshiWeatherSettlement
	for i := range rows {
		r := &rows[i]
		if r.ObservedAt.After(at) {
			return errors.New("future settlement leaked by source")
		}
		if r.MarketTicker == ticker {
			if result != nil {
				return errors.New("duplicate official result")
			}
			result = r
		}
	}
	if result == nil {
		return errors.New("no retained official result at cutoff")
	}
	return b.settle(*result, at)
}

func (b *NotionalBook) settle(r storage.KalshiWeatherSettlement, at time.Time) error {
	if at.Before(b.last) || r.ObservedAt.After(at) || r.SettledAt.After(r.ObservedAt) || r.SettledAt.IsZero() || simHash(r.Original) != r.SHA256 {
		return errors.New("invalid or future settlement")
	}
	var raw struct {
		Market struct {
			Ticker, Status, Result string
			Close                  time.Time `json:"close_time"`
			Settled                time.Time `json:"settlement_ts"`
		} `json:"market"`
	}
	if json.Unmarshal([]byte(r.Original), &raw) != nil || raw.Market.Ticker != r.MarketTicker || raw.Market.Status != "finalized" || !slices.Contains([]string{"yes", "no"}, raw.Market.Result) || (raw.Market.Result == "yes") != r.Yes || !raw.Market.Settled.Equal(r.SettledAt) || raw.Market.Close.IsZero() || r.SettledAt.Before(raw.Market.Close) {
		return errors.New("settlement must match official binary result")
	}
	if yes, done := b.settlements[r.MarketTicker]; done {
		if yes != r.Yes {
			return errors.New("conflicting official settlement")
		}
		return nil
	}
	winner := "yes"
	if !r.Yes {
		winner = "no"
	}
	quantity := b.positions[r.MarketTicker+"/"+winner]
	if quantity < 0 || quantity > 1000000000000000/dollarMicros || b.cash > 1000000000000000-quantity*dollarMicros {
		return errors.New("settlement exceeds notional balance bound")
	}
	payout := b.positions[r.MarketTicker+"/"+winner] * dollarMicros
	b.cash += payout
	b.payout += payout
	delete(b.positions, r.MarketTicker+"/yes")
	delete(b.positions, r.MarketTicker+"/no")
	b.settlements[r.MarketTicker] = r.Yes
	b.eventAt = at
	b.last = at
	return nil
}

// Report copies the separate simulation book. It is excluded from SPY by type
// and has no API to mutate the equity engine or its return/attribution record.
func (b *NotionalBook) Report() NotionalReport {
	positions := map[string]int64{}
	for k, v := range b.positions {
		positions[k] = v
	}
	return NotionalReport{"kalshi_notional", false, SimulationVersion, b.cash, b.fees, b.payout, positions, slices.Clone(b.fills)}
}
