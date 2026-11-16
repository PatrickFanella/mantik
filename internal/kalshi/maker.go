package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// MakerPolicy declares a fee-aware weather recipe. Quarter-Kelly is a hard
// ceiling, not a configurable multiplier. Limits cover ALL pending maker quotes
// and worst-case $1-per-contract inventory across the separate notional book.
type MakerPolicy struct {
	MinimumBucketSettled                                    int
	MinimumNetEdge, MinimumProbability, MaximumBookFraction float64
	MaximumSpreadMicros                                     int64
	MaximumAge, Lifetime                                    time.Duration
}

// DefaultMakerPolicy avoids longshots, requires an occupied calibration bucket,
// caps aggregate risk at 5% of cash and never infers a fill from resting depth.
func DefaultMakerPolicy() MakerPolicy {
	return MakerPolicy{20, .02, .1, .05, 100000, 15 * time.Minute, 5 * time.Minute}
}
func (p MakerPolicy) valid() bool {
	return p.MinimumBucketSettled >= 4 && p.MinimumBucketSettled <= 100000 && finite(p.MinimumNetEdge) && p.MinimumNetEdge > 0 && p.MinimumNetEdge < 1 && finite(p.MinimumProbability) && p.MinimumProbability >= .1 && p.MinimumProbability < .5 && finite(p.MaximumBookFraction) && p.MaximumBookFraction > 0 && p.MaximumBookFraction <= .2 && p.MaximumSpreadMicros >= 10000 && p.MaximumSpreadMicros <= 500000 && p.MaximumAge > 0 && p.MaximumAge <= 24*time.Hour && p.Lifetime > 0 && p.Lifetime <= time.Hour
}

// MakerIntent is a placed, post-only simulated limit quote. It is pending, not
// a fill: snapshots alone provide neither queue position nor resting executions.
// ReservedMicros includes a conservative taker-fee bound even for maker pricing.
type MakerIntent struct {
	MarketSHA256, EventSHA256                               string
	CalibrationSHA256                                       string
	Order                                                   SimOrder
	PostOnly                                                bool
	Active                                                  bool
	ExpiresAt                                               time.Time
	PredictionID, ModelVersion, BookSHA256, FeeSourceSHA256 string
	CalibrationAt                                           time.Time
	CalibrationSamples                                      int
	CalibratedProbability, QuarterKelly                     float64
	ReservedMicros, FeeBoundMicros                          int64
	Policy                                                  MakerPolicy
}

// MakerDecision carries a fixed abstention reason or a separately booked intent.
type MakerDecision struct {
	Reason string
	Intent *MakerIntent
}

// WeatherTradingStore grants reads only; this strategy has no venue client.
type WeatherTradingStore interface {
	SimulationStore
	WeatherHistory
}

func (b *NotionalBook) pendingMakerMicros() int64 {
	sum := int64(0)
	for _, q := range b.makers {
		if q.Active {
			sum += q.ReservedMicros
		}
	}
	return sum
}

// ExpireMakers cancels elapsed quotes without manufacturing maker fills. Call it
// before new decisions; cash reserved by a live quote cannot fund taker orders.
func (b *NotionalBook) ExpireMakers(at time.Time) error {
	if at.IsZero() || at.Before(b.last) {
		return errors.New("maker expiry clock moved backward")
	}
	for id, q := range b.makers {
		if q.Active && !q.ExpiresAt.After(at) {
			q.Active = false
			b.makers[id] = q
		}
	}
	b.last = at
	return nil
}

// PlaceWeatherMaker derives calibration from earlier retained outcomes for the
// prediction's exact model version. A Beta(1,1)-smoothed empirical bucket rate
// replaces the raw model probability. It selects the better fee-net YES/NO edge
// and places a noncrossing cent-tick quote with cash, inventory and pending risk
// reserved atomically. No minimum evidence means an explicit no-trade decision.
func (b *NotionalBook) PlaceWeatherMaker(ctx context.Context, store WeatherTradingStore, id, ticker, predictionID string, at time.Time, event EventObservation, policy MakerPolicy) (MakerDecision, error) {
	abstain := func(reason string) (MakerDecision, error) { return MakerDecision{Reason: reason}, nil }
	if old, ok := b.makers[id]; ok {
		if old.Order.MarketTicker != ticker || old.PredictionID != predictionID || !old.Order.At.Equal(at) || old.Policy != policy || old.EventSHA256 != event.SHA256 {
			return MakerDecision{}, errors.New("maker identity conflicts")
		}
		copy := old
		return MakerDecision{Intent: &copy}, nil
	}
	if store == nil || id == "" || len(id) > 128 || at.IsZero() || at.Before(b.last) || !policy.valid() || len(b.makers) >= 512 {
		return MakerDecision{}, errors.New("invalid maker decision")
	}
	if _, done := b.settlements[ticker]; done {
		return abstain("market_settled")
	}
	predictions, e := store.KalshiWeatherPredictions(ctx, at)
	if e != nil {
		return MakerDecision{}, e
	}
	var prediction *storage.KalshiWeatherPrediction
	for i := range predictions {
		p := &predictions[i]
		if p.RecordedAt.After(at) || p.AsOf.After(at) {
			return MakerDecision{}, errors.New("future prediction leaked")
		}
		if p.ID == predictionID {
			if prediction != nil {
				return MakerDecision{}, errors.New("duplicate prediction")
			}
			prediction = p
		}
	}
	if prediction == nil || prediction.MarketTicker != ticker || prediction.RecordedAt.IsZero() || prediction.AsOf.After(prediction.RecordedAt) || !prediction.CloseTime.After(at) || at.Sub(prediction.AsOf) > policy.MaximumAge || simHash(string(prediction.Payload)) != prediction.ID {
		return abstain("known_prediction_unavailable")
	}
	var payload WeatherPrediction
	if json.Unmarshal(prediction.Payload, &payload) != nil || payload.ModelVersion != prediction.ModelVersion || payload.MarketTicker != ticker || payload.Probability != prediction.Probability || !payload.AsOf.Equal(prediction.AsOf) || !payload.CloseTime.Equal(prediction.CloseTime) || len(payload.Evidence) == 0 || !finite(payload.Probability) || payload.Probability < 0 || payload.Probability > 1 {
		return MakerDecision{}, errors.New("prediction payload differs from admitted metadata")
	}
	for _, v := range payload.Evidence {
		if simHash(v.Original) != v.SHA256 || v.ObservedAt.IsZero() || v.ObservedAt.After(payload.AsOf) {
			return MakerDecision{}, errors.New("prediction evidence invalid")
		}
	}
	settlements, e := store.KalshiWeatherSettlements(ctx, at)
	if e != nil {
		return MakerDecision{}, e
	}
	if len(predictions) > 20000 || len(settlements) > 20000 {
		return MakerDecision{}, errors.New("calibration history exceeds bound")
	}
	selected := []storage.KalshiWeatherPrediction{}
	tickers := map[string]bool{}
	for _, p := range predictions {
		if p.ModelVersion != prediction.ModelVersion {
			continue
		}
		if _, err := verifiedPrediction(p); err != nil {
			return MakerDecision{}, err
		}
		selected = append(selected, p)
		tickers[p.MarketTicker] = true
	}
	outcomes := []storage.KalshiWeatherSettlement{}
	seenResults := map[string]bool{}
	for _, r := range settlements {
		if !tickers[r.MarketTicker] {
			continue
		}
		if r.ObservedAt.After(at) || seenResults[r.MarketTicker] {
			return MakerDecision{}, errors.New("future or duplicate calibration result")
		}
		if err := validateOfficialSettlement(r); err != nil {
			return MakerDecision{}, err
		}
		seenResults[r.MarketTicker] = true
		outcomes = append(outcomes, r)
	}
	history := weatherSnapshot{selected, outcomes}
	calibrationRaw, e := json.Marshal(history)
	if e != nil {
		return MakerDecision{}, e
	}
	calibration, e := CalibrateWeather(ctx, history, prediction.ModelVersion, at)
	if e != nil {
		return MakerDecision{}, e
	}
	bucket := calibration.Buckets[min(int(payload.Probability*10), 9)]
	if bucket.Count < policy.MinimumBucketSettled || bucket.ObservedFrequency == nil {
		return abstain("calibration_unavailable")
	}
	probability := (*bucket.ObservedFrequency*float64(bucket.Count) + 1) / float64(bucket.Count+2)
	markets, e := store.KalshiMarketsAsOf(ctx, at)
	if e != nil {
		return MakerDecision{}, e
	}
	var market *storage.KalshiMarket
	for i := range markets {
		m := &markets[i]
		if m.ObservedAt.After(at) {
			return MakerDecision{}, errors.New("future market leaked")
		}
		if m.Ticker == ticker {
			if market != nil {
				return MakerDecision{}, errors.New("duplicate market")
			}
			market = m
		}
	}
	if market == nil || market.ObservedAt.IsZero() || at.Sub(market.ObservedAt) > policy.MaximumAge || simHash(market.Original) != market.SHA256 || simHash(market.SeriesOriginal) != market.SeriesSHA256 || !slices.Contains([]string{"open", "active"}, market.Status) {
		return abstain("known_open_market_unavailable")
	}
	var page struct {
		Markets []Market `json:"markets"`
	}
	if json.Unmarshal([]byte(market.Original), &page) != nil {
		return MakerDecision{}, errors.New("invalid market source")
	}
	count := 0
	for _, m := range page.Markets {
		if m.Ticker == ticker {
			if m.EventTicker != market.EventTicker || m.Status != market.Status || m.CloseTime != market.CloseTime {
				return MakerDecision{}, errors.New("market metadata differs from source")
			}
			count++
		}
	}
	if count != 1 {
		return MakerDecision{}, errors.New("market must occur once in source")
	}
	multiplier, e := simulationFee(*market, event, at)
	if e != nil {
		return MakerDecision{}, e
	}
	books, e := store.KalshiBooksAsOf(ctx, ticker, at)
	if e != nil {
		return MakerDecision{}, e
	}
	var book *storage.KalshiOrderBook
	for i := range books {
		v := &books[i]
		if v.MarketTicker != ticker || v.ObservedAt.After(at) {
			return MakerDecision{}, errors.New("future or unrelated book")
		}
		if book == nil || v.ObservedAt.After(book.ObservedAt) {
			book = v
		}
	}
	if book == nil || book.ObservedAt.IsZero() || at.Sub(book.ObservedAt) > policy.MaximumAge || simHash(book.Original) != book.SHA256 {
		return abstain("fresh_depth_unavailable")
	}
	if market.ObservedAt.After(book.ObservedAt) || event.ObservedAt.After(book.ObservedAt) {
		return abstain("fee_or_market_newer_than_depth")
	}
	var raw struct {
		Book json.RawMessage `json:"orderbook_fp"`
	}
	// Explicit tags below accept the fixed-point API names only.
	var levels struct {
		Yes [][]string `json:"yes_dollars"`
		No  [][]string `json:"no_dollars"`
	}
	if json.Unmarshal(book.Book, &levels) != nil || json.Unmarshal([]byte(book.Original), &raw) != nil {
		return MakerDecision{}, errors.New("invalid depth")
	}
	a, _ := json.Marshal(levels)
	var originalLevels struct {
		Yes [][]string `json:"yes_dollars"`
		No  [][]string `json:"no_dollars"`
	}
	if json.Unmarshal(raw.Book, &originalLevels) != nil {
		return MakerDecision{}, errors.New("invalid original depth")
	}
	z, _ := json.Marshal(originalLevels)
	if string(a) != string(z) {
		return MakerDecision{}, errors.New("depth differs from source")
	}
	best := func(rows [][]string) (int64, error) {
		bid := int64(0)
		if len(rows) > 10000 {
			return 0, errors.New("excessive depth")
		}
		seen := map[int64]bool{}
		for _, row := range rows {
			if len(row) != 2 {
				return 0, errors.New("invalid depth level")
			}
			p, e := micros(row[0])
			q, qe := numberRat(row[1])
			if e != nil || qe != nil || p <= 0 || p >= dollarMicros || q.Cmp(big.NewRat(1000000, 1)) > 0 || seen[p] {
				return 0, errors.New("invalid depth value")
			}
			seen[p] = true
			if q.Cmp(big.NewRat(1, 1)) >= 0 {
				bid = max(bid, p)
			}
		}
		return bid, nil
	}
	yes, e := best(levels.Yes)
	if e != nil {
		return MakerDecision{}, e
	}
	no, e := best(levels.No)
	if e != nil {
		return MakerDecision{}, e
	}
	if yes == 0 || no == 0 || yes+no >= dollarMicros || dollarMicros-yes-no > policy.MaximumSpreadMicros {
		return abstain("spread_or_liquidity")
	}
	var chosen *MakerIntent
	bestEdge := policy.MinimumNetEdge
	for _, side := range []string{"yes", "no"} {
		p := probability
		bid, ask := yes, dollarMicros-no
		if side == "no" {
			p = 1 - p
			bid, ask = no, dollarMicros-yes
		}
		if p < policy.MinimumProbability || p > 1-policy.MinimumProbability {
			continue
		}
		price := ((bid / 10000) + 1) * 10000
		if price >= ask {
			price = (bid / 10000) * 10000
		}
		if price < 100000 || price > 900000 || price <= 0 || price >= ask {
			continue
		}
		fee, e := takerFee(price, 1, multiplier)
		if e != nil {
			return MakerDecision{}, e
		}
		fee = (fee + 99) / 100 * 100
		cost := price + fee
		if cost >= dollarMicros {
			continue
		}
		edge := p - float64(cost)/float64(dollarMicros)
		if edge <= bestEdge {
			continue
		}
		kelly := (p - float64(cost)/float64(dollarMicros)) / (1 - float64(cost)/float64(dollarMicros)) / 4
		pending := b.pendingMakerMicros()
		inventory := int64(0)
		for _, q := range b.positions {
			inventory += q * dollarMicros
		}
		budget := int64(math.Floor(float64(b.cash)*min(kelly, policy.MaximumBookFraction))) - pending - inventory
		budget = min(budget, b.cash-pending)
		contracts := min(int64(1000000), budget/cost)
		if contracts < 1 {
			continue
		}
		quote := MakerIntent{Order: SimOrder{ID: id, MarketTicker: ticker, Side: side, Action: "buy", Limit: big.NewRat(price, dollarMicros).FloatString(6), Contracts: contracts, At: at}, PostOnly: true, Active: true, ExpiresAt: at.Add(policy.Lifetime), PredictionID: predictionID, ModelVersion: prediction.ModelVersion, BookSHA256: book.SHA256, FeeSourceSHA256: market.SeriesSHA256 + "/" + event.SHA256, MarketSHA256: market.SHA256, EventSHA256: event.SHA256, CalibrationAt: at, CalibrationSamples: bucket.Count, CalibratedProbability: p, QuarterKelly: kelly, ReservedMicros: contracts * cost, FeeBoundMicros: contracts * fee, Policy: policy, CalibrationSHA256: simHash(string(calibrationRaw))}
		if !quote.ExpiresAt.Before(prediction.CloseTime) {
			quote.ExpiresAt = prediction.CloseTime
		}
		chosen = &quote
		bestEdge = edge
	}
	if chosen == nil {
		return abstain("insufficient_net_edge_or_capacity")
	}
	if b.makers == nil {
		b.makers = map[string]MakerIntent{}
	}
	b.makers[id] = *chosen
	b.last = at
	copy := *chosen
	return MakerDecision{Intent: &copy}, nil
}

type weatherSnapshot struct {
	Predictions []storage.KalshiWeatherPrediction
	Settlements []storage.KalshiWeatherSettlement
}

func (s weatherSnapshot) KalshiWeatherPredictions(context.Context, time.Time) ([]storage.KalshiWeatherPrediction, error) {
	return s.Predictions, nil
}
func (s weatherSnapshot) KalshiWeatherSettlements(context.Context, time.Time) ([]storage.KalshiWeatherSettlement, error) {
	return s.Settlements, nil
}
func verifiedPrediction(p storage.KalshiWeatherPrediction) (WeatherPrediction, error) {
	var v WeatherPrediction
	if p.RecordedAt.IsZero() || p.AsOf.IsZero() || p.AsOf.After(p.RecordedAt) || !p.RecordedAt.Before(p.CloseTime) || p.ModelVersion == "" || simHash(string(p.Payload)) != p.ID || json.Unmarshal(p.Payload, &v) != nil || v.ModelVersion != p.ModelVersion || v.MarketTicker != p.MarketTicker || v.Probability != p.Probability || !finite(v.Probability) || v.Probability < 0 || v.Probability > 1 || !v.AsOf.Equal(p.AsOf) || !v.CloseTime.Equal(p.CloseTime) || len(v.Evidence) == 0 {
		return v, errors.New("calibration prediction identity invalid")
	}
	for _, e := range v.Evidence {
		if e.ObservedAt.IsZero() || e.ObservedAt.After(v.AsOf) || simHash(e.Original) != e.SHA256 {
			return v, errors.New("calibration forecast evidence invalid")
		}
	}
	return v, nil
}
