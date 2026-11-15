package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

func paperFixture(t *testing.T, handler http.HandlerFunc) (*PaperTrader, string) {
	t.Helper()
	s := httptest.NewServer(loopbacktest.GoClientOnly(handler))
	t.Cleanup(s.Close)
	root := filepath.Join(t.TempDir(), "paper")
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	p, err := NewPaperTrader(PaperCredentials{OAuthToken: "test-only"}, "account-1", j)
	if err != nil {
		t.Fatal(err)
	}
	p.origin = s.URL
	return p, root
}

func testStockOrder() StockOrder {
	return StockOrder{ClientOrderID: "mantik-test", Symbol: "SPY", Qty: "1", Side: "buy", Type: "limit", TimeInForce: "day", LimitPrice: "1"}
}

const testPaperResponse = `{"id":"order-1","client_order_id":"mantik-test","symbol":"SPY","qty":"1.0000","side":"buy","type":"limit","time_in_force":"day","limit_price":"1.00","filled_qty":"0","filled_avg_price":null,"status":"new"}`

func TestPaperAmbiguityAndRestartNeverResubmit(t *testing.T) {
	for _, failure := range []string{"server_error", "transport", "not_found", "malformed_2xx", "mismatched_2xx"} {
		t.Run(failure, func(t *testing.T) {
			posts, lookups := 0, 0
			var root string
			p, dir := paperFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-only" || r.Header.Get("APCA-API-KEY-ID") != "" {
					t.Error("wrong credential mode")
				}
				switch r.URL.Path {
				case "/v2/account":
					_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE"}`))
				case "/v2/assets/SPY":
					_, _ = w.Write([]byte(`{"symbol":"SPY","class":"us_equity","tradable":true}`))
				case "/v2/orders":
					posts++
					if failure == "malformed_2xx" {
						w.WriteHeader(201)
						_, _ = w.Write([]byte("not-json"))
						return
					}
					if failure == "mismatched_2xx" {
						w.WriteHeader(201)
						_, _ = w.Write([]byte(strings.Replace(testPaperResponse, `"qty":"1.0000"`, `"qty":"2"`, 1)))
						return
					}
					if _, err := os.Stat(filepath.Join(root, "paper-intent-mantik-test.json")); err != nil {
						t.Error("intent not durable before POST", err)
					}
					if failure == "transport" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"message":"uncertain"}`))
				case "/v2/orders:by_client_order_id":
					lookups++
					if r.URL.Query().Get("client_order_id") != "mantik-test" {
						t.Error("wrong recovery ID")
					}
					if failure == "not_found" {
						w.WriteHeader(404)
						return
					}
					_, _ = w.Write([]byte(testPaperResponse))
				default:
					t.Error(r.URL.Path)
					w.WriteHeader(404)
				}
			})
			root = dir
			_, err := p.Submit(context.Background(), testStockOrder())
			if failure == "not_found" {
				if !errors.Is(err, ErrSubmissionUnknown) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err = p.journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			p.journal = j
			_, err = p.Submit(context.Background(), testStockOrder())
			if failure == "not_found" {
				if !errors.Is(err, ErrSubmissionUnknown) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if posts != 1 || lookups != 2 {
				t.Fatal(posts, lookups)
			}
			changed := testStockOrder()
			changed.Qty = "2"
			if _, err = p.Submit(context.Background(), changed); !errors.Is(err, ErrJournalConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestPaperRetainsBeforeDecodeAndFencesAccount(t *testing.T) {
	p, root := paperFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`not-json`)) })
	if _, err := p.Submit(context.Background(), testStockOrder()); err == nil {
		t.Fatal("malformed account accepted")
	}
	files, err := filepath.Glob(filepath.Join(root, "paper-response-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Raw    []byte
		SHA256 string
	}
	if json.Unmarshal(raw, &record) != nil || string(record.Raw) != "not-json" || record.SHA256 != Digest("not-json") {
		t.Fatal("raw response not retained")
	}
}

func TestPaperReconciliationAndFillPagination(t *testing.T) {
	p, _ := paperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/account":
			_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE"}`))
		case "/v2/positions":
			_, _ = w.Write([]byte(`[{"asset_id":"spy-id","qty":"1.250000000"},{"asset_id":"short-id","qty":"-2"}]`))
		case "/v2/account/activities/FILL":
			_, _ = w.Write([]byte(`[{"id":"fill-1","qty":"0.25","price":"601.15","order_id":"order-1"}]`))
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	})
	diffs, err := p.Reconcile(context.Background(), map[string]string{"spy-id": "1.25", "missing-id": "1"})
	if err != nil || len(diffs) != 2 || diffs[0].AssetID != "missing-id" || diffs[1].Actual != "-2" {
		t.Fatal(diffs, err)
	}
	fills, err := p.Fills(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(fills) != 1 || !strings.Contains(string(fills[0]), "601.15") {
		t.Fatal(fills, err)
	}
}

func TestPaperValidation(t *testing.T) {
	for _, change := range []func(*StockOrder){func(o *StockOrder) { o.Qty = "1e3" }, func(o *StockOrder) { o.Symbol = "../SPY" }, func(o *StockOrder) { o.ClientOrderID = "../escape" }, func(o *StockOrder) { o.Side = "short" }} {
		o := testStockOrder()
		change(&o)
		if o.validate() == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

func TestPaperSubmissionFencesSurviveRollover(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "completed"
		if resume {
			name = "resume-on-open"
		}
		t.Run(name, func(t *testing.T) {
			posts, lookups, cancels := 0, 0, 0
			account := "account-1"
			p, root := paperFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/account":
					_ = json.NewEncoder(w).Encode(map[string]string{"id": account, "status": "ACTIVE"})
				case "/v2/assets/SPY":
					_, _ = w.Write([]byte(`{"symbol":"SPY","class":"us_equity","tradable":true}`))
				case "/v2/orders":
					posts++
					_, _ = w.Write([]byte(testPaperResponse))
				case "/v2/orders:by_client_order_id":
					lookups++
					_, _ = w.Write([]byte(testPaperResponse))
				case "/v2/orders/order-1":
					if r.Method != http.MethodDelete {
						t.Error(r.Method)
					}
					cancels++
					w.WriteHeader(204)
				default:
					t.Error(r.URL.Path)
					w.WriteHeader(404)
				}
			})
			ctx := context.Background()
			if _, err := p.Submit(ctx, testStockOrder()); err != nil {
				t.Fatal(err)
			}
			receipt, err := p.journal.Rollover(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if resume {
				// Crash after cleanup but before removing the durable pending marker.
				raw, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err = p.journal.writeOnce(rolloverPending, raw); err != nil {
					t.Fatal(err)
				}
			}
			if err = p.journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := OpenExistingJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p.journal = j
			if _, err = p.Submit(ctx, testStockOrder()); err != nil {
				t.Fatal(err)
			}
			if err = p.Cancel(ctx, testStockOrder().ClientOrderID); err != nil {
				t.Fatal(err)
			}
			if posts != 1 || lookups != 2 || cancels != 1 {
				t.Fatalf("posts=%d lookups=%d cancels=%d", posts, lookups, cancels)
			}
			changed := testStockOrder()
			changed.Qty = "2"
			if _, err = p.Submit(ctx, changed); !errors.Is(err, ErrJournalConflict) {
				t.Fatal("lost immutable intent", err)
			}
			account = "account-2"
			other, err := NewPaperTrader(PaperCredentials{OAuthToken: "test-only"}, account, j)
			if err != nil {
				t.Fatal(err)
			}
			other.origin = p.origin
			if _, err = other.Submit(ctx, testStockOrder()); !errors.Is(err, ErrJournalConflict) {
				t.Fatal("lost account binding", err)
			}
		})
	}
}
