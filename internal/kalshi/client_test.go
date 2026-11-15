package kalshi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type memoryStore struct{ *storage.Memory }

func TestPublicReads(t *testing.T) {
	s := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("sent authorization")
		}
		switch r.URL.Path {
		case "/series/KX":
			_, _ = w.Write([]byte(`{"series":{"ticker":"KX","settlement_sources":[{"name":"Weather","url":"https://weather.test"}]}}`))
		case "/markets":
			if r.URL.Query().Get("status") != "open" {
				t.Fatal(r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"markets":[{"ticker":"KXHIGH-26JAN01-B54.5","event_ticker":"KX-26JAN01","title":"High","status":"active","close_time":"2026-01-02T00:00:00Z","rules_primary":"rule"}],"cursor":"next"}`))
		case "/markets/KXHIGH-26JAN01-B54.5/orderbook":
			_, _ = w.Write([]byte(`{"orderbook_fp":{"yes_dollars":[["0.60","10"]],"no_dollars":[["0.40","10"]]}}`))
		default:
			http.NotFound(w, r)
		}
	})))
	defer s.Close()
	c, err := OpenClient(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.origin = s.URL
	m, err := c.FetchOpenMarkets(context.Background(), "KX", "")
	if err != nil || len(m.Markets) != 1 || m.Cursor != "next" || m.SHA256 == "" {
		t.Fatal(m, err)
	}
	b, err := c.FetchOrderBook(context.Background(), "KXHIGH-26JAN01-B54.5")
	if err != nil || string(b.Book) != `{"yes_dollars":[["0.60","10"]],"no_dollars":[["0.40","10"]]}` {
		t.Fatal(b, err)
	}
	if _, err := c.FetchOrderBook(context.Background(), "bad/path"); err == nil {
		t.Fatal("invalid ticker admitted")
	}
	if _, _, err := c.Collect(context.Background(), memoryStore{storage.NewMemory()}, "KX", ""); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureBeforeMalformedHTTPDecode(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("not-json"))
			})))
			defer server.Close()
			root := filepath.Join(t.TempDir(), "journal")
			c, err := OpenClient(root)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.origin = server.URL
			if _, err = c.FetchOpenMarkets(context.Background(), "KXHIGH-26JAN01-B54.5CHI", ""); err == nil {
				t.Fatal("invalid read accepted")
			}
			files, err := filepath.Glob(filepath.Join(root, "capture-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal(files, err)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Raw    []byte
				Status int
				SHA256 string
			}
			if json.Unmarshal(raw, &record) != nil || string(record.Raw) != "not-json" || record.Status != status || record.SHA256 != digest(record.Raw) {
				t.Fatal("lost original")
			}
		})
	}
}

func TestCollectionKeepsMarketsWhenLaterBookFails(t *testing.T) {
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/series/"):
			_, _ = w.Write([]byte(`{"series":{"ticker":"KX","settlement_sources":[{"name":"Weather source","url":"https://example.com"}]}}`))
		case r.URL.Path == "/markets":
			_, _ = w.Write([]byte(`{"markets":[{"ticker":"KXHIGH-26JAN01-B54.5","event_ticker":"KX-26JAN01","title":"High","status":"active","close_time":"2026-01-02T00:00:00Z","rules_primary":"rule"}],"cursor":""}`))
		default:
			w.WriteHeader(503)
			_, _ = w.Write([]byte("unavailable"))
		}
	})))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "journal")
	c, err := OpenClient(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.origin = server.URL
	store := storage.NewMemory()
	receipt, _, err := c.Collect(context.Background(), store, "KX", "")
	if err == nil || receipt.Markets != 1 {
		t.Fatal(receipt, err)
	}
	markets, err := store.KalshiMarketsAsOf(context.Background(), time.Now())
	if err != nil || len(markets) != 1 {
		t.Fatal(markets, err)
	}
}
