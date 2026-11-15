// Package kalshi reads public Kalshi market data. It never submits orders.
package kalshi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

const defaultOrigin = "https://api.elections.kalshi.com/trade-api/v2"

// Client reads public market and order-book endpoints.
type Client struct {
	origin  string
	http    *http.Client
	journal *os.Root
}

func NewClient() *Client {
	return &Client{origin: defaultOrigin, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// FetchOpenMarkets reads one public market page. Cursor is the response cursor from an earlier page.
func (c *Client) FetchOpenMarkets(ctx context.Context, series, cursor string) (MarketsResponse, error) {
	q := url.Values{"status": {"open"}, "limit": {"200"}}
	if !validTicker(series) || len(cursor) > 4096 {
		return MarketsResponse{}, errors.New("invalid Kalshi series or cursor")
	}
	if series != "" {
		q.Set("series_ticker", series)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return c.fetchMarkets(ctx, "/markets?"+q.Encode())
}

// FetchOrderBook reads the current public order book for one market ticker.
func (c *Client) FetchOrderBook(ctx context.Context, ticker string) (OrderBookResponse, error) {
	if !validTicker(ticker) {
		return OrderBookResponse{}, errors.New("invalid Kalshi market ticker")
	}
	var wire struct {
		OrderBook json.RawMessage `json:"orderbook_fp"`
	}
	raw, at, err := c.fetch(ctx, "/markets/"+url.PathEscape(ticker)+"/orderbook")
	if err != nil {
		return OrderBookResponse{}, err
	}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.OrderBook) == 0 || string(wire.OrderBook) == "null" {
		return OrderBookResponse{}, errors.New("invalid Kalshi order book response")
	}
	return OrderBookResponse{MarketTicker: ticker, ObservedAt: at, Original: string(raw), SHA256: digest(raw), Book: wire.OrderBook}, nil
}

type MarketsResponse struct {
	Markets          []Market
	Cursor           string
	ObservedAt       time.Time
	Original, SHA256 string
}
type OrderBookResponse struct {
	MarketTicker     string
	ObservedAt       time.Time
	Original, SHA256 string
	Book             json.RawMessage
}
type Market struct {
	Ticker         string `json:"ticker"`
	EventTicker    string `json:"event_ticker"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	CloseTime      string `json:"close_time"`
	ExpirationTime string `json:"expiration_time"`
	RulesPrimary   string `json:"rules_primary"`
}
type SettlementSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type SeriesResponse struct {
	Sources          []SettlementSource
	Original, SHA256 string
	ObservedAt       time.Time
}

func (c *Client) FetchSeries(ctx context.Context, ticker string) (SeriesResponse, error) {
	if !validTicker(ticker) {
		return SeriesResponse{}, errors.New("invalid Kalshi series ticker")
	}
	raw, at, err := c.fetch(ctx, "/series/"+url.PathEscape(ticker))
	if err != nil {
		return SeriesResponse{}, err
	}
	var wire struct {
		Series struct {
			Ticker  string             `json:"ticker"`
			Sources []SettlementSource `json:"settlement_sources"`
		} `json:"series"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil || wire.Series.Ticker != ticker || len(wire.Series.Sources) == 0 {
		return SeriesResponse{}, errors.New("invalid Kalshi series settlement sources")
	}
	for _, source := range wire.Series.Sources {
		u, e := url.Parse(source.URL)
		if e != nil || source.Name == "" || u.Host == "" || u.Scheme != "https" {
			return SeriesResponse{}, errors.New("invalid Kalshi settlement source")
		}
	}
	return SeriesResponse{wire.Series.Sources, string(raw), digest(raw), at}, nil
}

// Store is the narrow durable boundary used by a collection command.
type Store interface {
	RecordKalshiData(context.Context, []storage.KalshiMarket, []storage.KalshiOrderBook) (storage.KalshiReceipt, error)
}

// Collect reads one public market page and a snapshot for each returned market,
// then persists source responses and normalized values together.
func (c *Client) Collect(ctx context.Context, store Store, series, cursor string) (storage.KalshiReceipt, string, error) {
	if c.journal == nil {
		return storage.KalshiReceipt{}, "", errors.New("Kalshi collection requires a raw capture journal")
	}
	if !validTicker(series) {
		return storage.KalshiReceipt{}, "", errors.New("Kalshi collection requires an explicit series ticker")
	}
	sources, err := c.FetchSeries(ctx, series)
	if err != nil {
		return storage.KalshiReceipt{}, "", err
	}
	markets, err := c.FetchOpenMarkets(ctx, series, cursor)
	if err != nil {
		return storage.KalshiReceipt{}, "", err
	}
	// Commit the page's markets first; a later book failure cannot discard them.
	storedMarkets := make([]storage.KalshiMarket, 0, len(markets.Markets))
	for _, market := range markets.Markets {
		storedMarkets = append(storedMarkets, storage.KalshiMarket{Ticker: market.Ticker, EventTicker: market.EventTicker, SeriesTicker: series, SeriesOriginal: sources.Original, SeriesSHA256: sources.SHA256, ResolutionSource: sources.Sources[0].Name, ResolutionURL: sources.Sources[0].URL, Title: market.Title, Status: market.Status, CloseTime: market.CloseTime, ExpirationTime: market.ExpirationTime, RulesPrimary: market.RulesPrimary, Original: markets.Original, SHA256: markets.SHA256, ObservedAt: markets.ObservedAt})
	}
	receipt, err := store.RecordKalshiData(ctx, storedMarkets, nil)
	if err != nil {
		return receipt, markets.Cursor, err
	}
	for _, market := range markets.Markets {
		book, err := c.FetchOrderBook(ctx, market.Ticker)
		if err != nil {
			return receipt, markets.Cursor, err
		}
		r, err := store.RecordKalshiData(ctx, nil, []storage.KalshiOrderBook{{MarketTicker: book.MarketTicker, Original: book.Original, SHA256: book.SHA256, Book: book.Book, ObservedAt: book.ObservedAt}})
		if err != nil {
			return receipt, markets.Cursor, err
		}
		receipt.Books += r.Books
		receipt.Unchanged += r.Unchanged
	}
	return receipt, markets.Cursor, nil
}

func (c *Client) fetchMarkets(ctx context.Context, target string) (MarketsResponse, error) {
	raw, at, err := c.fetch(ctx, target)
	if err != nil {
		return MarketsResponse{}, err
	}
	var wire struct {
		Markets []Market `json:"markets"`
		Cursor  string   `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return MarketsResponse{}, errors.New("invalid Kalshi markets response")
	}
	for _, market := range wire.Markets {
		if _, err := time.Parse(time.RFC3339, market.CloseTime); err != nil {
			return MarketsResponse{}, errors.New("invalid market close time")
		}
		if !validTicker(market.Ticker) || !validTicker(market.EventTicker) || market.Status == "" || market.RulesPrimary == "" {
			return MarketsResponse{}, errors.New("Kalshi market is missing identity or status")
		}
	}
	return MarketsResponse{Markets: wire.Markets, Cursor: wire.Cursor, ObservedAt: at, Original: string(raw), SHA256: digest(raw)}, nil
}
func (c *Client) fetch(ctx context.Context, path string) ([]byte, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return c.fetchURL(ctx, strings.TrimRight(c.origin, "/")+path)
}

func (c *Client) fetchURL(ctx context.Context, target string) ([]byte, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/1.1 Mantik-weather-research/1.0 (https://github.com/subculture-collective/mantik)")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("Kalshi public read: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	at := time.Now().UTC().Truncate(time.Microsecond)
	partial := err != nil || len(raw) > 1<<20
	if len(raw) > 1<<20 {
		raw = raw[:1<<20]
	}
	if captureErr := c.capture(target, res.StatusCode, raw, at, partial); captureErr != nil {
		return nil, time.Time{}, captureErr
	}
	if partial {
		return nil, time.Time{}, errors.New("Kalshi response exceeds 1 MiB or cannot be read")
	}
	if res.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("Kalshi public read failed (HTTP %d)", res.StatusCode)
	}
	return raw, at, nil
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

var tickerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validTicker(value string) bool {
	return len(value) > 0 && len(value) <= 128 && tickerPattern.MatchString(value)
}
