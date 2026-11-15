package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestOptionsBackfillResumesIndicativePagesAndSkipsAdjusted(t *testing.T) {
	calls := 0
	interrupted := true
	symbol := "SPY240202C00490000"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v2/options/contracts":
			if q.Get("underlying_symbols") != "SPY" || q.Get("expiration_date_gte") != "2024-02-02" || q.Get("expiration_date_lte") != "2024-02-02" {
				t.Error(r.URL)
			}
			if q.Get("status") == "active" {
				fmt.Fprint(w, `{"option_contracts":[],"next_page_token":null}`)
				return
			}
			fmt.Fprintf(w, `{"option_contracts":[{"id":"standard","symbol":%q,"underlying_symbol":"SPY","expiration_date":"2024-02-02","strike_price":"490","type":"call"},{"id":"adjusted","symbol":"1SPY240202C00490000","underlying_symbol":"SPY","expiration_date":"2024-02-02","strike_price":"490","type":"call"}],"next_page_token":null}`, symbol)
		case "/v1beta1/options/bars":
			if q.Has("feed") || q.Get("timeframe") != "1Day" || q.Get("symbols") != symbol {
				t.Error(r.URL)
			}
			fmt.Fprintf(w, `{"bars":{%q:[{"t":"2024-02-01T05:00:00Z","o":2.1,"h":3,"l":1,"c":2,"v":12,"n":5,"vw":2.001}]},"next_page_token":null}`, symbol)
		case "/v1beta1/options/trades":
			if q.Has("feed") || strings.HasPrefix(q.Get("symbols"), "1") {
				t.Error(r.URL)
			}
			if q.Get("page_token") == "more" {
				fmt.Fprint(w, `{"trades":{},"next_page_token":null}`)
				return
			}
			if interrupted {
				w.WriteHeader(503)
				return
			}
			fmt.Fprintf(w, `{"trades":{%q:[{"t":"2024-02-01T15:00:00Z","p":2.123,"s":1,"x":"I","c":"I"},{"t":"2024-02-01T15:00:00Z","p":2.123,"s":1,"x":"I","c":"I"}]},"next_page_token":"more"}`, symbol)
		default:
			t.Error(r.URL)
			w.WriteHeader(404)
		}
	})
	c.barInterval = 0
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := OptionSpec{Underlyings: []string{"SPY"}, Start: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 2, 3, 0, 0, 0, 0, time.UTC), ExpiryStart: "2024-02-02", ExpiryEnd: "2024-02-02"}
	store := storage.NewMemory()
	ctx := context.Background()
	if _, err := j.CollectOptions(ctx, c, store, s, io.Discard); err == nil {
		t.Fatal("interruption succeeded")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	before := calls
	interrupted = false
	got, err := j.CollectOptions(ctx, c, store, s, io.Discard)
	if err != nil || got.Contracts != 2 || got.Bars != 1 || got.Trades != 2 || got.Skipped != 1 || got.Inserted != 2 || got.Unchanged != 3 || calls != before+2 {
		t.Fatalf("receipt %+v calls=%d before=%d: %v", got, calls, before, err)
	}
	before = calls
	got, err = j.CollectOptions(ctx, c, store, s, io.Discard)
	if err != nil || got.Inserted != 0 || got.Unchanged != 5 || calls != before {
		t.Fatal(got, calls, before, err)
	}
	history, err := store.OptionDataAsOf(ctx, symbol, s.End)
	if err != nil || len(history) != 0 {
		t.Fatal("backdated availability", history, err)
	}
	history, err = store.OptionDataAsOf(ctx, symbol, time.Now().UTC())
	if err != nil || len(history) != 4 {
		t.Fatal(history, err)
	}
	for _, r := range history {
		if r.Kind != "contract" && r.Feed != "indicative" {
			t.Fatal("OPRA mislabel", r)
		}
	}
}

func TestOptionsRejectUnsupportedRequestsBeforeNetwork(t *testing.T) {
	c, err := NewClient("key", "secret", "sip")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []Query{
		{Capability: "option_bars", Symbols: []string{"1SPY240202C00490000"}, Start: "2024-02-01T00:00:00Z", End: "2024-02-02T00:00:00Z"},
		{Capability: "option_trades", Symbols: []string{"SPY240202C00490000"}, Start: "2023-02-01T00:00:00Z", End: "2024-02-02T00:00:00Z"},
	} {
		if _, err := c.target(q, time.Now()); err == nil {
			t.Fatal("invalid request admitted", q)
		}
	}
}

func TestOptionsPreserveSubsecondHistoryBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, end string }{
		{"whole-second-end", "2024-02-01T15:00:01Z"},
		{"fractional-end", "2024-02-01T15:00:00.501Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, err := time.Parse(time.RFC3339Nano, "2024-02-01T15:00:00.1Z")
			if err != nil {
				t.Fatal(err)
			}
			end, err := time.Parse(time.RFC3339Nano, tc.end)
			if err != nil {
				t.Fatal(err)
			}
			events := []time.Time{start.Add(-time.Nanosecond), start, start.Add(100 * time.Millisecond), end.Add(-time.Nanosecond), end}
			symbol := "SPY240202C00490000"
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				switch r.URL.Path {
				case "/v2/options/contracts":
					if q.Get("status") == "active" {
						fmt.Fprint(w, `{"option_contracts":[],"next_page_token":null}`)
						return
					}
					fmt.Fprintf(w, `{"option_contracts":[{"id":"standard","symbol":%q,"underlying_symbol":"SPY","expiration_date":"2024-02-02","strike_price":"490","type":"call"}],"next_page_token":null}`, symbol)
				case "/v1beta1/options/bars":
					fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
				case "/v1beta1/options/trades":
					from, e1 := time.Parse(time.RFC3339Nano, q.Get("start"))
					to, e2 := time.Parse(time.RFC3339Nano, q.Get("end"))
					if e1 != nil || e2 != nil {
						t.Error("invalid request boundary")
						w.WriteHeader(400)
						return
					}
					trades := []map[string]any{}
					for _, at := range events {
						if !at.Before(from) && !at.After(to) {
							trades = append(trades, map[string]any{"t": at.Format(time.RFC3339Nano), "p": 1, "s": 1, "c": "I", "x": "I"})
						}
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"trades": map[string]any{symbol: trades}, "next_page_token": nil}); err != nil {
						t.Error(err)
					}
				default:
					t.Error(r.URL)
					w.WriteHeader(404)
				}
			})
			c.barInterval = 0
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			store := storage.NewMemory()
			spec := OptionSpec{Underlyings: []string{"SPY"}, Start: start, End: end, ExpiryStart: "2024-02-02", ExpiryEnd: "2024-02-02"}
			got, err := j.CollectOptions(context.Background(), c, store, spec, io.Discard)
			if err != nil || got.Trades != 3 {
				t.Fatalf("exclusive range [%s,%s): trades=%d want=3 error=%v", start, end, got.Trades, err)
			}
		})
	}
}
