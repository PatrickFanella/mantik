package alpaca

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var barsNow = time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return barsNow }

const barsPage1 = `{"bars":{"QQQ":[{"t":"2026-11-10T05:00:00Z","o":510.1,"h":512.25,"l":508.0035,"c":511,"v":30100000,"n":410000,"vw":510.774412}],"SPY":[{"t":"2026-11-10T05:00:00Z","o":601.115,"h":603,"l":600.5,"c":602.4475,"v":61000000,"n":820000,"vw":602.003321},{"t":"2026-11-11T05:00:00Z","o":602.4,"h":604.9,"l":601.2,"c":604.1,"v":59000000,"n":790000,"vw":603.4}]},"next_page_token":"U1BZfDIwMjYtMTEtMTE="}`
const barsPage2 = `{"bars":{"SPY":[{"t":"2026-11-12T05:00:00Z","o":604.1,"h":605,"l":603.0001,"c":604.5,"v":52000000,"n":700000,"vw":604.2}]},"next_page_token":null}`

func TestDailyBarsFollowsPagesInTimeOrderAndKeepsRawBytes(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v2/stocks/bars" || q.Get("symbols") != "QQQ,SPY" || q.Get("timeframe") != "1Day" || q.Get("feed") != "sip" || q.Get("adjustment") != "raw" || q.Get("sort") != "asc" || q.Get("limit") != "4000" || q.Get("start") != "2026-11-10T00:00:00Z" || q.Get("end") != "2026-11-12T23:59:59Z" {
			t.Errorf("unexpected bars request %s", r.URL.RawQuery)
		}
		switch calls.Add(1) {
		case 1:
			if q.Has("page_token") {
				t.Error("first page sent a page token")
			}
			_, _ = fmt.Fprint(w, barsPage1)
		default:
			if q.Get("page_token") != "U1BZfDIwMjYtMTEtMTE=" {
				t.Error("second page lost the page token")
			}
			_, _ = fmt.Fprint(w, barsPage2)
		}
	})
	out, err := c.fetchDailyBars(context.Background(), []string{"SPY", "QQQ"}, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 23, 59, 59, 0, time.UTC), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(out.Pages) != 2 || out.Pages[0].Original != barsPage1 || out.Pages[1].SHA256 != Digest(barsPage2) || out.Clamped {
		t.Fatal("pages were not followed or raw bytes were lost", calls.Load(), len(out.Pages))
	}
	spy := out.Bars["SPY"]
	if len(spy) != 3 || len(out.Bars["QQQ"]) != 1 {
		t.Fatal("bars missing", len(spy), len(out.Bars["QQQ"]))
	}
	for n := 1; n < len(spy); n++ {
		if !spy[n].Time.After(spy[n-1].Time) {
			t.Fatal("bars not in time order")
		}
	}
	// Sub-cent prices survive as exact text; integers keep their JSON form.
	if spy[0].Open != "601.115" || spy[0].Close != "602.4475" || spy[0].High != "603" || spy[0].VWAP != "602.003321" || spy[0].Volume != 61000000 || spy[0].TradeCount != 820000 {
		t.Fatalf("bar fields changed: %+v", spy[0])
	}
}

func TestDailyBarsClampsRecentEndBeforeSending(t *testing.T) {
	var ends []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		ends = append(ends, r.URL.Query().Get("end"))
		_, _ = fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	})
	out, err := c.fetchDailyBars(context.Background(), []string{"SPY"}, barsNow.Add(-48*time.Hour), barsNow, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	want := barsNow.Add(-RecentSIPDelay)
	if !out.Clamped || !out.End.Equal(want) || len(ends) != 1 || ends[0] != want.Format(time.RFC3339) {
		t.Fatal("recent end was not clamped", out.End, ends)
	}
	// A range entirely inside the embargo is refused without a request.
	if _, err := c.fetchDailyBars(context.Background(), []string{"SPY"}, barsNow.Add(-5*time.Minute), barsNow, fixedNow); err == nil || len(ends) != 1 {
		t.Fatal("embargoed range reached the provider")
	}
	// A direct query with a recent end is refused before it is sent.
	q := Query{Capability: "daily_bars", Symbols: []string{"SPY"}, Start: barsNow.Add(-time.Hour).Format(time.RFC3339), End: barsNow.Add(-14 * time.Minute).Format(time.RFC3339)}
	if _, err := c.fetch(context.Background(), q, nil, fixedNow); err == nil || len(ends) != 1 {
		t.Fatal("recent query end reached the provider")
	}
}

func TestDailyBarsPacesUnder200PerMinuteAndBacksOffOn429(t *testing.T) {
	real, err := NewClient("key", "secret", "iex")
	if err != nil || real.barInterval < time.Minute/200 || real.interval != time.Second {
		t.Fatal("bar pacing would exceed 200 requests per minute", real.barInterval)
	}
	var mu sync.Mutex
	var at []time.Time
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		switch calls.Add(1) {
		case 1:
			// No Retry-After; Market Data reports the window reset instead.
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10))
			w.WriteHeader(429)
		case 2:
			_, _ = fmt.Fprint(w, `{"bars":{"SPY":[{"t":"2026-11-10T05:00:00Z","o":1,"h":1,"l":1,"c":1,"v":1,"n":1,"vw":1}]},"next_page_token":"p2"}`)
		default:
			_, _ = fmt.Fprint(w, `{"bars":{"SPY":[{"t":"2026-11-11T05:00:00Z","o":1,"h":1,"l":1,"c":1,"v":1,"n":1,"vw":1}]},"next_page_token":null}`)
		}
	})
	c.barInterval = 50 * time.Millisecond
	out, err := c.FetchDailyBars(context.Background(), []string{"SPY"}, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || len(out.Bars["SPY"]) != 2 || out.Pages[0].Attempts != 2 {
		t.Fatal("429 was not retried", err)
	}
	if gap := at[1].Sub(at[0]); gap < 10*time.Millisecond {
		t.Fatal("429 retried without backing off", gap)
	}
	// The server sees each request after variable latency, so allow some
	// slack below the spacing the client enforces from its send times.
	if gap := at[2].Sub(at[1]); gap < c.barInterval*3/5 {
		t.Fatal("pages were not paced", gap)
	}
}

func TestDailyBarsRejectsMalformedPages(t *testing.T) {
	bar := func(t string, o string) string {
		return fmt.Sprintf(`{"t":%q,"o":%s,"h":2,"l":1,"c":2,"v":5,"n":3,"vw":1.5}`, t, o)
	}
	for name, body := range map[string]string{
		"unrequested symbol": `{"bars":{"QQQ":[` + bar("2026-11-10T05:00:00Z", "1") + `]},"next_page_token":null}`,
		"duplicate time":     `{"bars":{"SPY":[` + bar("2026-11-10T05:00:00Z", "1") + `,` + bar("2026-11-10T05:00:00Z", "1") + `]},"next_page_token":null}`,
		"out of order":       `{"bars":{"SPY":[` + bar("2026-11-11T05:00:00Z", "1") + `,` + bar("2026-11-10T05:00:00Z", "1") + `]},"next_page_token":null}`,
		"outside range":      `{"bars":{"SPY":[` + bar("2026-12-01T05:00:00Z", "1") + `]},"next_page_token":null}`,
		"float exponent":     `{"bars":{"SPY":[` + bar("2026-11-10T05:00:00Z", "1e2") + `]},"next_page_token":null}`,
		"zero price":         `{"bars":{"SPY":[` + bar("2026-11-10T05:00:00Z", "0") + `]},"next_page_token":null}`,
		"empty token":        `{"bars":{},"next_page_token":""}`,
		"trailing data":      `{"bars":{},"next_page_token":null}{}`,
		"repeated token":     `{"bars":{},"next_page_token":"same"}`,
		"null body":          `null`,
		"empty object":       `{}`,
		"unrelated object":   `{"message":"ok"}`,
		"null bars":          `{"bars":null,"next_page_token":null}`,
		"missing token":      `{"bars":{}}`,
		"bars array":         `{"bars":[],"next_page_token":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) })
			c.barInterval = time.Millisecond
			_, err := c.fetchDailyBars(context.Background(), []string{"SPY"}, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), fixedNow)
			if err == nil {
				t.Fatal("malformed page admitted")
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached provider") })
	for _, symbols := range [][]string{nil, {"spy"}, {"SPY", "SPY"}, make([]string, MaxBarSymbols+1)} {
		if _, err := c.fetchDailyBars(context.Background(), symbols, barsNow.Add(-48*time.Hour), barsNow.Add(-24*time.Hour), fixedNow); err == nil {
			t.Fatal("invalid symbols admitted", symbols)
		}
	}
	if _, err := c.fetchDailyBars(context.Background(), []string{"SPY"}, barsNow.Add(-24*time.Hour), barsNow.Add(-48*time.Hour), fixedNow); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatal("reversed range admitted")
	}
}
