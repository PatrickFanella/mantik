package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

var kalshiTicker = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var ErrInvalidKalshiRecord = errors.New("invalid Kalshi record")

type KalshiMarket struct {
	Ticker, SeriesTicker, SeriesOriginal, SeriesSHA256, EventTicker, ResolutionSource, ResolutionURL, Title, Status, CloseTime, ExpirationTime, RulesPrimary, Original, SHA256 string
	ObservedAt                                                                                                                                                                 time.Time
}
type KalshiOrderBook struct {
	MarketTicker, Original, SHA256 string
	Book                           json.RawMessage
	ObservedAt                     time.Time
}
type KalshiReceipt struct{ Markets, Books, Unchanged int }
type kalshiStore struct {
	markets map[string][]KalshiMarket
	books   map[string][]KalshiOrderBook
}

func validKalshiTime(t time.Time) bool { return !t.IsZero() }
func validateMarket(v KalshiMarket) (KalshiMarket, error) {
	v.ObservedAt = v.ObservedAt.UTC().Truncate(time.Microsecond)
	if !kalshiTicker.MatchString(v.Ticker) || !kalshiTicker.MatchString(v.SeriesTicker) || !kalshiTicker.MatchString(v.EventTicker) || v.ResolutionSource == "" || v.ResolutionURL == "" || v.Status == "" || v.Original == "" || !validKalshiHash(v.Original, v.SHA256) || !validKalshiHash(v.SeriesOriginal, v.SeriesSHA256) || !validKalshiTime(v.ObservedAt) {
		return v, ErrInvalidKalshiRecord
	}
	return v, nil
}
func validateBook(v KalshiOrderBook) (KalshiOrderBook, error) {
	v.ObservedAt = v.ObservedAt.UTC().Truncate(time.Microsecond)
	if !kalshiTicker.MatchString(v.MarketTicker) || v.Original == "" || !validKalshiHash(v.Original, v.SHA256) || !json.Valid(v.Book) || !validKalshiTime(v.ObservedAt) {
		return v, ErrInvalidKalshiRecord
	}
	normalized, err := canonicalKalshiJSON(v.Book)
	if err != nil {
		return v, ErrInvalidKalshiRecord
	}
	v.Book = normalized
	return v, nil
}
func validKalshiHash(original, expected string) bool {
	sum := sha256.Sum256([]byte(original))
	return len(expected) == 64 && hex.EncodeToString(sum[:]) == expected
}
func cloneBook(v KalshiOrderBook) KalshiOrderBook { v.Book = bytes.Clone(v.Book); return v }
func sameMarket(a, b KalshiMarket) bool           { return a == b }
func sameBook(a, b KalshiOrderBook) bool {
	return a.MarketTicker == b.MarketTicker && a.Original == b.Original && a.SHA256 == b.SHA256 && a.ObservedAt.Equal(b.ObservedAt) && sameKalshiJSON(a.Book, b.Book)
}
func validateKalshiBatch(markets []KalshiMarket, books []KalshiOrderBook) ([]KalshiMarket, []KalshiOrderBook, error) {
	ms := make([]KalshiMarket, len(markets))
	bs := make([]KalshiOrderBook, len(books))
	marketKeys := map[string]KalshiMarket{}
	bookKeys := map[string]KalshiOrderBook{}
	for i, v := range markets {
		var err error
		ms[i], err = validateMarket(v)
		if err != nil {
			return nil, nil, err
		}
		key := ms[i].Ticker + "\x00" + ms[i].ObservedAt.Format(time.RFC3339Nano)
		if old, ok := marketKeys[key]; ok && !sameMarket(old, ms[i]) {
			return nil, nil, fmt.Errorf("%w: conflicting market identity", ErrInvalidKalshiRecord)
		}
		marketKeys[key] = ms[i]
	}
	for i, v := range books {
		var err error
		bs[i], err = validateBook(v)
		if err != nil {
			return nil, nil, err
		}
		bs[i] = cloneBook(bs[i])
		key := bs[i].MarketTicker + "\x00" + bs[i].ObservedAt.Format(time.RFC3339Nano)
		if old, ok := bookKeys[key]; ok && !sameBook(old, bs[i]) {
			return nil, nil, fmt.Errorf("%w: conflicting book identity", ErrInvalidKalshiRecord)
		}
		bookKeys[key] = bs[i]
	}
	return ms, bs, nil
}

// RecordKalshiData retains source responses before their normalized market or book fields.
func (m *Memory) RecordKalshiData(ctx context.Context, markets []KalshiMarket, books []KalshiOrderBook) (KalshiReceipt, error) {
	if err := ctx.Err(); err != nil {
		return KalshiReceipt{}, err
	}
	markets, books, err := validateKalshiBatch(markets, books)
	if err != nil {
		return KalshiReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kalshi.markets == nil {
		m.kalshi = kalshiStore{markets: map[string][]KalshiMarket{}, books: map[string][]KalshiOrderBook{}}
	}
	// Check all retained identities before committing any part of the batch.
	for _, v := range markets {
		for _, old := range m.kalshi.markets[v.Ticker] {
			if old.ObservedAt.Equal(v.ObservedAt) && !sameMarket(old, v) {
				return KalshiReceipt{}, ErrInvalidKalshiRecord
			}
		}
	}
	for _, v := range books {
		for _, old := range m.kalshi.books[v.MarketTicker] {
			if old.ObservedAt.Equal(v.ObservedAt) && !sameBook(old, v) {
				return KalshiReceipt{}, ErrInvalidKalshiRecord
			}
		}
	}
	r := KalshiReceipt{}
	for _, v := range markets {
		old := m.kalshi.markets[v.Ticker]
		for _, existing := range old {
			if existing.ObservedAt.Equal(v.ObservedAt) {
				if sameMarket(existing, v) {
					r.Unchanged++
					goto nextMarket
				}
				return KalshiReceipt{}, fmt.Errorf("%w: conflicting market identity", ErrInvalidKalshiRecord)
			}
		}
		m.kalshi.markets[v.Ticker] = append(old, v)
		r.Markets++
	nextMarket:
	}
	for _, v := range books {
		old := m.kalshi.books[v.MarketTicker]
		for _, existing := range old {
			if existing.ObservedAt.Equal(v.ObservedAt) {
				if sameBook(existing, v) {
					r.Unchanged++
					goto nextBook
				}
				return KalshiReceipt{}, fmt.Errorf("%w: conflicting book identity", ErrInvalidKalshiRecord)
			}
		}
		m.kalshi.books[v.MarketTicker] = append(old, v)
		r.Books++
	nextBook:
	}
	return r, nil
}

// KalshiMarketsAsOf returns each market's newest source observation visible at at.
func (m *Memory) KalshiMarketsAsOf(ctx context.Context, at time.Time) ([]KalshiMarket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []KalshiMarket{}
	for _, versions := range m.kalshi.markets {
		var best *KalshiMarket
		for i := range versions {
			if !versions[i].ObservedAt.After(at) && (best == nil || versions[i].ObservedAt.After(best.ObservedAt)) {
				best = &versions[i]
			}
		}
		if best != nil {
			out = append(out, *best)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticker < out[j].Ticker })
	return out, nil
}
func (m *Memory) KalshiBooksAsOf(ctx context.Context, ticker string, at time.Time) ([]KalshiOrderBook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !kalshiTicker.MatchString(ticker) || at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []KalshiOrderBook{}
	for _, v := range m.kalshi.books[ticker] {
		if !v.ObservedAt.After(at) {
			out = append(out, cloneBook(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObservedAt.Before(out[j].ObservedAt) })
	return out, nil
}
func (p *Postgres) RecordKalshiData(ctx context.Context, markets []KalshiMarket, books []KalshiOrderBook) (KalshiReceipt, error) {
	markets, books, err := validateKalshiBatch(markets, books)
	if err != nil {
		return KalshiReceipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return KalshiReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_kalshi_data',0))"); err != nil {
		return KalshiReceipt{}, err
	}
	r := KalshiReceipt{}
	for _, v := range markets {
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT payload FROM mantik_kalshi_markets WHERE ticker=$1 AND observed_at=$2", v.Ticker, v.ObservedAt).Scan(&raw)
		if err == nil {
			var old KalshiMarket
			if err = json.Unmarshal(raw, &old); err != nil {
				return KalshiReceipt{}, err
			}
			if sameMarket(old, v) {
				r.Unchanged++
				continue
			}
			return KalshiReceipt{}, fmt.Errorf("%w: conflicting market identity", ErrInvalidKalshiRecord)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return KalshiReceipt{}, err
		}
		payload, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return KalshiReceipt{}, marshalErr
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_kalshi_markets(ticker,observed_at,payload,original,sha256) VALUES($1,$2,$3,$4,$5)", v.Ticker, v.ObservedAt, payload, v.Original, v.SHA256)
		if err != nil {
			return KalshiReceipt{}, err
		}
		r.Markets++
	}
	for _, v := range books {
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT payload FROM mantik_kalshi_order_books WHERE market_ticker=$1 AND observed_at=$2", v.MarketTicker, v.ObservedAt).Scan(&raw)
		if err == nil {
			var old KalshiOrderBook
			if err = json.Unmarshal(raw, &old); err != nil {
				return KalshiReceipt{}, err
			}
			if sameBook(old, v) {
				r.Unchanged++
				continue
			}
			return KalshiReceipt{}, fmt.Errorf("%w: conflicting book identity", ErrInvalidKalshiRecord)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return KalshiReceipt{}, err
		}
		payload, marshalErr := json.Marshal(v)
		if marshalErr != nil {
			return KalshiReceipt{}, marshalErr
		}
		_, err = tx.Exec(ctx, "INSERT INTO mantik_kalshi_order_books(market_ticker,observed_at,payload,original,sha256) VALUES($1,$2,$3,$4,$5)", v.MarketTicker, v.ObservedAt, payload, v.Original, v.SHA256)
		if err != nil {
			return KalshiReceipt{}, err
		}
		r.Books++
	}
	return r, tx.Commit(ctx)
}

func (p *Postgres) KalshiMarketsAsOf(ctx context.Context, at time.Time) ([]KalshiMarket, error) {
	if at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	rows, err := p.pool.Query(ctx, "SELECT DISTINCT ON (ticker) payload FROM mantik_kalshi_markets WHERE observed_at<=$1 ORDER BY ticker,observed_at DESC", at.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KalshiMarket{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v KalshiMarket
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		v.ObservedAt = v.ObservedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *Postgres) KalshiBooksAsOf(ctx context.Context, ticker string, at time.Time) ([]KalshiOrderBook, error) {
	if !kalshiTicker.MatchString(ticker) || at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	rows, err := p.pool.Query(ctx, "SELECT payload FROM mantik_kalshi_order_books WHERE market_ticker=$1 AND observed_at<=$2 ORDER BY observed_at", ticker, at.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KalshiOrderBook{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v KalshiOrderBook
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		v.ObservedAt = v.ObservedAt.UTC()
		v.Book, err = canonicalKalshiJSON(v.Book)
		if err != nil {
			return nil, err
		}
		out = append(out, cloneBook(v))
	}
	return out, rows.Err()
}

func canonicalKalshiJSON(raw []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func sameKalshiJSON(a, b []byte) bool {
	x, e1 := canonicalKalshiJSON(a)
	y, e2 := canonicalKalshiJSON(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}
