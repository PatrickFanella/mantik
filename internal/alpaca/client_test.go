package alpaca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	c, err := NewClient("manufactured-key", "manufactured-secret", "iex")
	if err != nil {
		t.Fatal(err)
	}
	c.dataOrigin, c.calendarOrigin = server.URL, server.URL
	c.interval = time.Millisecond
	return c
}
func TestClientReadBoundariesAndRateRetry(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v2/stocks/quotes/latest" || r.URL.Query().Get("feed") != "iex" || r.URL.Query().Get("currency") != "USD" || r.Header.Get("APCA-API-SECRET-KEY") != "manufactured-secret" {
			t.Error("request escaped read-only contract")
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		_, _ = fmt.Fprint(w, goodQuotes)
	})
	out, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}})
	if err != nil || out.Attempts != 2 || out.SHA256 != Digest(goodQuotes) || out.ReceivedAt.IsZero() {
		t.Fatal("bounded retry lost acquisition receipt", err)
	}
	if _, err := c.Fetch(context.Background(), Query{Capability: "orders"}); err == nil || calls.Load() != 2 {
		t.Fatal("unsupported path reached provider")
	}
	if _, err := c.Fetch(context.Background(), Query{Capability: "market_calendar", Start: "2026-01-01", End: "2026-03-01"}); err == nil {
		t.Fatal("unbounded calendar query admitted")
	}
}
func TestClientFailuresAreBoundedAndSanitized(t *testing.T) {
	for _, status := range []int{401, 403, 302, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://example.invalid/secret")
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, "manufactured-secret")
			})
			_, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}})
			want := int32(1)
			if status == 500 {
				want = 3
			}
			if err == nil || calls.Load() != want || strings.Contains(err.Error(), "manufactured-secret") {
				t.Fatal("failure retried without bound or exposed body", calls.Load(), err)
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, strings.Repeat("a", MaxBytes+1)) })
	if _, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}}); err == nil {
		t.Fatal("oversized response admitted")
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	})
	if _, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}}); err == nil {
		t.Fatal("excessive retry delay admitted")
	}
}
func TestClientBackpressureAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = fmt.Fprint(w, goodQuotes)
	})
	done := make(chan error, 1)
	go func() {
		_, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}})
		done <- err
	}()
	<-entered
	if _, err := c.Fetch(context.Background(), Query{Capability: "latest_quotes", Symbols: []string{"SPY"}}); !errors.Is(err, ErrBackpressure) {
		t.Fatal("concurrent read queued without backpressure")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.interval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx, Query{Capability: "latest_quotes", Symbols: []string{"SPY"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancelled pacing wait reached provider")
	}
}
func TestCalendarClientOnlyUsesPaperReadEndpoint(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/calendar" || r.URL.Query().Get("date_type") != "TRADING" {
			t.Error("wrong calendar contract")
		}
		_, _ = fmt.Fprint(w, `[]`)
	})
	if _, err := c.Fetch(context.Background(), Query{Capability: "market_calendar", Start: "2026-10-14", End: "2026-10-15"}); err != nil {
		t.Fatal(err)
	}
	real, err := NewClient("key", "secret", "sip")
	if err != nil || real.dataOrigin != "https://data.alpaca.markets" || real.calendarOrigin != "https://paper-api.alpaca.markets" || real.interval != time.Second {
		t.Fatal("production client endpoints or rate changed")
	}
}
