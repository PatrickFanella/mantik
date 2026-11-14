package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

const (
	// BarsFeed is fixed: the free plan serves SIP history once a request's end
	// is at least RecentSIPDelay old, whatever feed the client uses for quotes.
	BarsFeed = "sip"
	// RecentSIPDelay is the free plan's embargo on recent SIP data. A bars
	// request ending later than this is refused before it is sent.
	RecentSIPDelay = 15 * time.Minute
	// MaxBarSymbols bounds one request's symbol list and URL length.
	MaxBarSymbols = 100
	// MaxBarPages bounds one FetchDailyBars call.
	MaxBarPages = 1000
	// barPageLimit keeps a page well under MaxBytes. The limit is shared across
	// the symbols in a request.
	barPageLimit = 4000
	// barInterval paces bar requests, retries included, at 187.5 per minute,
	// under the free plan's 200 per minute.
	barInterval = 320 * time.Millisecond
)

// decimalPattern accepts plain non-negative decimals. Bar prices are often
// sub-cent, so they stay as exact text and never pass through floating point.
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)

// Bar is one daily bar exactly as Alpaca reported it: raw (unadjusted) prices
// as decimal text, the bar's start time, volume, trade count and VWAP.
type Bar struct {
	Time       time.Time
	Open       string
	High       string
	Low        string
	Close      string
	Volume     int64
	TradeCount int64
	VWAP       string
}

// DailyBars is the result of one FetchDailyBars call. Pages holds every
// successful response's original bytes and digest, in request order, for the
// archive. Bars holds each symbol's bars in strictly increasing time order.
// End is the end actually requested, after any clamp.
type DailyBars struct {
	Symbols []string
	Start   time.Time
	End     time.Time
	Clamped bool
	Pages   []Response
	Bars    map[string][]Bar
}

// FetchDailyBars reads raw daily SIP bars for 1–MaxBarSymbols symbols from
// start to end, following page tokens to the last page. An end later than
// RecentSIPDelay ago is clamped to that point and reported in Clamped.
func (c *Client) FetchDailyBars(ctx context.Context, symbols []string, start, end time.Time) (DailyBars, error) {
	return c.fetchDailyBars(ctx, symbols, start, end, time.Now)
}

func (c *Client) fetchDailyBars(ctx context.Context, symbols []string, start, end time.Time, now func() time.Time) (DailyBars, error) {
	symbols, err := barSymbols(symbols)
	if err != nil {
		return DailyBars{}, err
	}
	start = start.UTC().Truncate(time.Second)
	end = end.UTC().Truncate(time.Second)
	out := DailyBars{Symbols: symbols, Start: start, End: end, Bars: map[string][]Bar{}}
	if latest := now().UTC().Add(-RecentSIPDelay).Truncate(time.Second); end.After(latest) {
		out.End, out.Clamped = latest, true
	}
	if start.IsZero() || out.End.Before(start) {
		return DailyBars{}, errors.New("daily bars require a start at or before the clamped end")
	}
	q := Query{Capability: "daily_bars", Symbols: symbols, Start: start.Format(time.RFC3339), End: out.End.Format(time.RFC3339)}
	seen := map[string]bool{}
	for page := 1; ; page++ {
		if page > MaxBarPages {
			return DailyBars{}, errors.New("daily bars exceeded the page bound")
		}
		res, err := c.fetch(ctx, q, nil, now)
		if err != nil {
			return DailyBars{}, err
		}
		next, err := out.add(res.Original)
		if err != nil {
			return DailyBars{}, fmt.Errorf("daily bars page %d: %w", page, err)
		}
		out.Pages = append(out.Pages, res)
		if next == "" {
			return out, nil
		}
		if seen[next] || !alpacarecord.ValidPageToken(next) {
			return DailyBars{}, errors.New("daily bars returned a repeated or invalid page token")
		}
		seen[next] = true
		q.PageToken = next
	}
}

func barSymbols(symbols []string) ([]string, error) {
	symbols = slices.Clone(symbols)
	slices.Sort(symbols)
	if len(symbols) < 1 || len(symbols) > MaxBarSymbols {
		return nil, fmt.Errorf("daily bars require 1–%d symbols", MaxBarSymbols)
	}
	for n, s := range symbols {
		if !symbolPattern.MatchString(s) || n > 0 && symbols[n-1] == s {
			return nil, errors.New("invalid daily bar symbols")
		}
	}
	return symbols, nil
}

// barsTarget builds the request URL. It refuses an end later than
// RecentSIPDelay before now, so a recent request is never sent.
func (c *Client) barsTarget(q Query, now time.Time) (string, error) {
	if _, err := barSymbols(q.Symbols); err != nil {
		return "", err
	}
	start, err1 := time.Parse(time.RFC3339, q.Start)
	end, err2 := time.Parse(time.RFC3339, q.End)
	if err1 != nil || err2 != nil || end.Before(start) {
		return "", errors.New("daily bars require an RFC 3339 start at or before the end")
	}
	if end.After(now.Add(-RecentSIPDelay)) {
		return "", errors.New("daily bars end is less than 15 minutes old")
	}
	if q.PageToken != "" && !alpacarecord.ValidPageToken(q.PageToken) {
		return "", errors.New("invalid daily bars page token")
	}
	symbols := slices.Clone(q.Symbols)
	slices.Sort(symbols)
	v := url.Values{"symbols": {strings.Join(symbols, ",")}, "timeframe": {"1Day"}, "start": {q.Start}, "end": {q.End}, "feed": {BarsFeed}, "adjustment": {"raw"}, "sort": {"asc"}, "limit": {strconv.Itoa(barPageLimit)}}
	if q.PageToken != "" {
		v.Set("page_token", q.PageToken)
	}
	return c.dataOrigin + "/v2/stocks/bars?" + v.Encode(), nil
}

type barsPage struct {
	Bars          map[string][]rawBar `json:"bars"`
	NextPageToken *string             `json:"next_page_token"`
}

type rawBar struct {
	T  string      `json:"t"`
	O  json.Number `json:"o"`
	H  json.Number `json:"h"`
	L  json.Number `json:"l"`
	C  json.Number `json:"c"`
	V  json.Number `json:"v"`
	N  json.Number `json:"n"`
	VW json.Number `json:"vw"`
}

// add validates one page and appends its bars. It returns the next page token,
// or "" on the last page.
func (d *DailyBars) add(raw string) (string, error) {
	// An empty range is {"bars":{},"next_page_token":null}. Both keys must be
	// present, so null, {} or an unrelated object is never read as no data.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil || envelope == nil {
		return "", errors.New("response is not one bars object")
	}
	bars, token := envelope["bars"], envelope["next_page_token"]
	if !strings.HasPrefix(string(bars), "{") || token == nil {
		return "", errors.New("response lacks the bars envelope")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var page barsPage
	if err := decoder.Decode(&page); err != nil || decoder.More() {
		return "", errors.New("response is not one bars object")
	}
	for symbol, bars := range page.Bars {
		if _, ok := slices.BinarySearch(d.Symbols, symbol); !ok {
			return "", fmt.Errorf("unrequested symbol %q", symbol)
		}
		for _, r := range bars {
			bar, err := r.parse()
			if err != nil {
				return "", fmt.Errorf("%s: %w", symbol, err)
			}
			if bar.Time.Before(d.Start) || bar.Time.After(d.End) {
				return "", fmt.Errorf("%s: bar %s outside the requested range", symbol, r.T)
			}
			prior := d.Bars[symbol]
			if len(prior) > 0 && !bar.Time.After(prior[len(prior)-1].Time) {
				return "", fmt.Errorf("%s: bars out of time order at %s", symbol, r.T)
			}
			d.Bars[symbol] = append(prior, bar)
		}
	}
	if page.NextPageToken == nil {
		return "", nil
	}
	if *page.NextPageToken == "" {
		return "", errors.New("empty next page token")
	}
	return *page.NextPageToken, nil
}

func (r rawBar) parse() (Bar, error) {
	t, err := time.Parse(time.RFC3339, r.T)
	if err != nil {
		return Bar{}, errors.New("bar time is not RFC 3339")
	}
	b := Bar{Time: t.UTC(), Open: r.O.String(), High: r.H.String(), Low: r.L.String(), Close: r.C.String(), VWAP: r.VW.String()}
	for _, p := range []string{b.Open, b.High, b.Low, b.Close, b.VWAP} {
		if !decimalPattern.MatchString(p) {
			return Bar{}, fmt.Errorf("bar at %s has a price that is not a plain decimal", r.T)
		}
	}
	for _, p := range []string{b.Open, b.High, b.Low, b.Close} {
		if strings.Trim(p, "0.") == "" {
			return Bar{}, fmt.Errorf("bar at %s has a zero price", r.T)
		}
	}
	if b.Volume, err = strconv.ParseInt(r.V.String(), 10, 64); err != nil || b.Volume < 0 {
		return Bar{}, fmt.Errorf("bar at %s has an invalid volume", r.T)
	}
	if b.TradeCount, err = strconv.ParseInt(r.N.String(), 10, 64); err != nil || b.TradeCount < 0 {
		return Bar{}, fmt.Errorf("bar at %s has an invalid trade count", r.T)
	}
	return b, nil
}
