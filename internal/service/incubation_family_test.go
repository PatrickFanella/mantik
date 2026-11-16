package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIncubationPendingFamilyRetainedOwnerExit(t *testing.T) {
	for _, mode := range []string{"explicit-flat", "holding-expiry", "unbound-flat"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r, req, s, b, record := forwardRunnerFixture(t)
			if mode == "unbound-flat" {
				req.Strategies[0].ForwardDeclarationID = ""
			}
			entry, err := r.Run(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			order := entry.Distribution.Orders[0]
			filled := req.ExecuteAt.Add(time.Second)
			b.fill(t, order.ID, "entry-fill", order.Quantity, "100", "0", filled, true)
			if _, err = r.Sync(ctx, filled.Add(time.Second), req.Quotes); err != nil {
				t.Fatal(err)
			}
			if err = r.Store.(*storage.Memory).BeginStrategyTrialBatch(ctx, "pending-research", record.Declaration.Family); err != nil {
				t.Fatal(err)
			}
			if _, err = r.Sync(ctx, filled.Add(2*time.Second), req.Quotes); err != nil {
				t.Fatal("pending family blocked reconciliation", err)
			}
			at := filled.Add(3 * time.Second)
			s.decide = func(strategy.View) (strategy.Decision, error) { return strategy.Decision{}, nil }
			if mode == "holding-expiry" {
				at = filled.Add(time.Duration(record.Declaration.MaxHoldingSeconds) * time.Second)
				s.decide = func(strategy.View) (strategy.Decision, error) {
					return strategy.Decision{}, fmt.Errorf("must not invoke strategy at expiry")
				}
			}
			closeReq := nextForwardCycle(req, "close-review", at)
			closeReq.Limits = map[string]string{"SPY": "99"}
			r.now = func() time.Time { return closeReq.ExecuteAt }
			out, err := r.Run(ctx, closeReq)
			if err != nil || len(out.Distribution.Orders) != 1 || !out.Distribution.Orders[0].ReduceOnly || b.posts != 2 {
				t.Fatalf("safe exit blocked: error=%v orders=%d posts=%d strategy_calls=%d", err, len(out.Distribution.Orders), b.posts, s.calls)
			}
		})
	}
}

func TestIncubationPendingFamilyAtActualPost(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			r, req, _, _ := incubationFixture(t, false)
			if bound {
				r, req, _, _, _ = forwardRunnerFixture(t)
			}
			posts := 0
			old := http.DefaultTransport
			http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
				raw := ""
				switch h.URL.Path {
				case "/v2/account/activities/FILL":
					raw = `[]`
				case "/v2/account":
					raw = `{"id":"paper-fixture","status":"ACTIVE"}`
				case "/v2/assets/SPY":
					raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
					if e := r.Store.(*storage.Memory).BeginStrategyTrialBatch(context.Background(), "concurrent-research", "incubation-fixture"); e != nil {
						t.Fatal(e)
					}
				case "/v2/orders":
					posts++
					var o alpaca.StockOrder
					if e := json.NewDecoder(h.Body).Decode(&o); e != nil {
						t.Fatal(e)
					}
					body, e := json.Marshal(alpaca.PaperOrder{ID: "venue-fixture", ClientOrderID: o.ClientOrderID, Symbol: o.Symbol, Qty: o.Qty, Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce, LimitPrice: o.LimitPrice, Status: "new", FilledQty: "0"})
					if e != nil {
						t.Fatal(e)
					}
					raw = string(body)
				default:
					t.Fatalf("unexpected intercepted request %s", h.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = old })
			j, err := alpaca.OpenJournal(filepath.Join(t.TempDir(), "paper"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { j.Close() })
			p, err := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fixture-only"}, "paper-fixture", j)
			if err != nil {
				t.Fatal(err)
			}
			r.Broker = p
			_, err = r.Run(context.Background(), req)
			if (err == nil || !errors.Is(err, strategytrial.ErrFamilyPending) && !(bound && strings.Contains(err.Error(), "book_safety_forward_search_unavailable"))) || posts != 0 {
				t.Fatalf("pending family reached POST: err=%v posts=%d", err, posts)
			}
			state, err := r.Store.CappedBook(context.Background(), r.BookID)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := account.SnapshotCapped(state, incubationMarks(req.Quotes), req.ExecuteAt)
			if err != nil || len(snap.Pending) != 0 {
				t.Fatal("known pre-POST refusal retained reservation", snap, err)
			}
			if _, err = r.Run(context.Background(), req); err != nil || posts != 0 {
				t.Fatal("refused retry renewed entry authority", err, posts)
			}
		})
	}
}

func TestIncubationPendingFamilyRefusesNewOwnerBeforeDecision(t *testing.T) {
	ctx := context.Background()
	r, req, s, b := incubationFixture(t, false)
	if err := r.Store.(*storage.Memory).BeginStrategyTrialBatch(ctx, "pending-before-admission", "incubation-fixture"); err != nil {
		t.Fatal(err)
	}
	_, err := r.Run(ctx, req)
	if !errors.Is(err, strategytrial.ErrFamilyPending) || s.calls != 0 || b.posts != 0 {
		t.Fatal("pending family admitted a new owner", err, s.calls, b.posts)
	}
	if _, err = r.Store.StrategyTrialSearchTotal(ctx, "incubation-fixture"); !errors.Is(err, strategytrial.ErrFamilyPending) {
		t.Fatal("pending family exposed a finite search count", err)
	}
}
