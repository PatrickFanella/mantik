package alpaca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrBackpressure = errors.New("alpaca request already in flight")

// Query is restricted to market reads. No account/order path can be requested.
type Query struct {
	PageToken  string
	Capability string
	Symbols    []string
	Start      string
	End        string
}
type Response struct {
	Original   string
	SHA256     string
	ReceivedAt time.Time
	Attempts   int
}

type Client struct {
	key, secret, feed          string
	http                       *http.Client
	gate                       chan struct{}
	last                       time.Time
	interval                   time.Duration
	dataOrigin, calendarOrigin string
}

// NewClient does not perform network access. Calendar reads use the paper
// endpoint; quote reads use the separate Market Data origin with explicit feed.
func NewClient(key, secret, feed string) (*Client, error) {
	valid := func(s string) bool { return s != "" && len(s) <= 512 && !strings.ContainsFunc(s, unicode.IsControl) }
	if !valid(key) || !valid(secret) || !slices.Contains([]string{"iex", "sip", "delayed_sip"}, feed) {
		return nil, errors.New("alpaca reads require credentials and explicit supported feed")
	}
	return &Client{key: key, secret: secret, feed: feed, http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, gate: make(chan struct{}, 1), interval: time.Second, dataOrigin: "https://data.alpaca.markets", calendarOrigin: "https://paper-api.alpaca.markets"}, nil
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) target(q Query) (string, error) {
	switch q.Capability {
	case "latest_quotes":
		if len(q.Symbols) < 1 || len(q.Symbols) > 10 || q.Start != "" || q.End != "" || q.PageToken != "" {
			return "", errors.New("quote request requires 1–10 symbols")
		}
		symbols := slices.Clone(q.Symbols)
		slices.Sort(symbols)
		for n, s := range symbols {
			if !symbolPattern.MatchString(s) || n > 0 && symbols[n-1] == s {
				return "", errors.New("invalid quote symbols")
			}
		}
		v := url.Values{"symbols": {strings.Join(symbols, ",")}, "feed": {c.feed}, "currency": {"USD"}}
		return c.dataOrigin + "/v2/stocks/quotes/latest?" + v.Encode(), nil
	case "corporate_actions":
		if !validCorporateActionPageToken(q.PageToken) {
			return "", errors.New("invalid corporate-action page token")
		}
		if _, _, err := calendarRange(q.Start, q.End); err != nil {
			return "", err
		}
		symbols := slices.Clone(q.Symbols)
		slices.Sort(symbols)
		if len(symbols) < 1 || len(symbols) > 10 {
			return "", errors.New("corporate actions require 1–10 symbols")
		}
		for n, symbol := range symbols {
			if !symbolPattern.MatchString(symbol) || n > 0 && symbols[n-1] == symbol {
				return "", errors.New("invalid corporate-action symbols")
			}
		}
		v := url.Values{"symbols": {strings.Join(symbols, ",")}, "start": {q.Start}, "end": {q.End}, "region": {"us"}, "data_quality": {"all"}, "sort": {"asc"}, "limit": {"1000"}}
		if q.PageToken != "" {
			v.Set("page_token", q.PageToken)
		}
		return c.dataOrigin + "/v1/corporate-actions?" + v.Encode(), nil
	case "market_calendar":
		if len(q.Symbols) != 0 || q.PageToken != "" {
			return "", errors.New("calendar query does not accept symbols")
		}
		if _, _, err := calendarRange(q.Start, q.End); err != nil {
			return "", err
		}
		v := url.Values{"start": {q.Start}, "end": {q.End}, "date_type": {"TRADING"}}
		return c.calendarOrigin + "/v2/calendar?" + v.Encode(), nil
	default:
		return "", errors.New("unsupported Alpaca read capability")
	}
}

// Fetch admits one in-flight operation, at least one second between attempts,
// at most three attempts, and at most 1 MiB per response. The caller must retain
// response bytes and attribution before normalization or any service submission.
func (c *Client) Fetch(ctx context.Context, q Query) (Response, error) {
	return c.fetch(ctx, q, nil, time.Now)
}

// The private observer durably records each attempt before a request and its
// bounded response before retrying or returning success. Observer failure stops
// acquisition immediately; it is never treated as a transport retry.
func (c *Client) fetch(ctx context.Context, q Query, observe func(ReadAttempt) error, now func() time.Time) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	target, err := c.target(q)
	if err != nil {
		return Response{}, err
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	default:
		return Response{}, ErrBackpressure
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := wait(ctx, time.Until(c.last.Add(c.interval))); err != nil {
			return Response{}, err
		}
		if observe != nil {
			if err := observe(ReadAttempt{Number: attempt, State: "started", At: now().UTC()}); err != nil {
				return Response{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return Response{}, errors.New("invalid Alpaca read request")
		}
		req.Header.Set("APCA-API-KEY-ID", c.key)
		req.Header.Set("APCA-API-SECRET-KEY", c.secret)
		c.last = time.Now()
		res, err := c.http.Do(req)
		if err != nil {
			if observe != nil {
				if e := observe(ReadAttempt{Number: attempt, State: "transport_failed", At: now().UTC()}); e != nil {
					return Response{}, e
				}
			}
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			if attempt == 3 {
				return Response{}, errors.New("alpaca transport failed after bounded retries")
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
		_ = res.Body.Close()
		receivedAt := now().UTC()
		if observe != nil {
			bounded := raw
			if len(bounded) > MaxBytes {
				bounded = bounded[:MaxBytes]
			}
			if err := observe(ReadAttempt{Number: attempt, State: "response", At: receivedAt, HTTPStatus: res.StatusCode, Raw: bounded, Partial: readErr != nil || len(raw) > MaxBytes}); err != nil {
				return Response{}, err
			}
		}
		if len(raw) > MaxBytes {
			return Response{}, errors.New("alpaca response exceeds 1 MiB")
		}
		if res.StatusCode == 200 && readErr == nil {
			return Response{Original: string(raw), SHA256: Digest(string(raw)), ReceivedAt: receivedAt, Attempts: attempt}, nil
		}
		retry := res.StatusCode == 429 || res.StatusCode >= 500 && res.StatusCode <= 599 || res.StatusCode == 200 && readErr != nil
		if !retry || attempt == 3 {
			return Response{}, fmt.Errorf("alpaca read failed (HTTP %d; attempts %d)", res.StatusCode, attempt)
		}
		if res.StatusCode == 429 {
			delay := c.interval
			if value := res.Header.Get("Retry-After"); value != "" {
				seconds, e := strconv.Atoi(value)
				if e == nil && seconds >= 0 && seconds <= 30 {
					delay = time.Duration(seconds) * time.Second
				} else if at, e := http.ParseTime(value); e == nil {
					delay = time.Until(at)
				} else {
					return Response{}, errors.New("alpaca rate limit has invalid or excessive retry delay")
				}
				if delay > 30*time.Second {
					return Response{}, errors.New("alpaca rate limit exceeds bounded retry window")
				}
			}
			if delay < c.interval {
				delay = c.interval
			}
			if err := wait(ctx, delay); err != nil {
				return Response{}, err
			}
		}
	}
	return Response{}, errors.New("alpaca retry limit exhausted")
}
