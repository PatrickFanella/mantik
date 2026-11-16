package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

func TestIncubationStaleGateAllowsOnlyRetainedOwnerFlatExit(t *testing.T) {
	for _, name := range []string{"flat", "zero-target", "keep", "target", "new-sleeve"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r, req, s, b := incubationFixture(t, false)
			out, e := r.Run(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			b.fill(t, out.Orders[0].ClientOrderID, "complete", out.Orders[0].Qty, "100", "0", req.ExecuteAt.Add(time.Second), true)
			syncAt := req.ExecuteAt.Add(2 * time.Second)
			if _, e = r.Sync(ctx, syncAt, req.Quotes); e != nil {
				t.Fatal(e)
			}
			s.decide = func(v strategy.View) (strategy.Decision, error) {
				if len(v.Positions()) != 1 {
					t.Fatal("retained owner lost position", v.Positions())
				}
				switch name {
				case "keep":
					return strategy.Decision{KeepPositions: true}, nil
				case "target":
					return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: "0.5"}}}, nil
				case "zero-target":
					return strategy.Decision{Targets: []strategy.Target{{Asset: "SPY", Weight: "0"}}}, nil
				default:
					return strategy.Decision{}, nil
				}
			}
			spec := s.Spec()
			raw, e := json.Marshal(spec)
			if e != nil {
				t.Fatal(e)
			}
			sid, e := spec.ID()
			if e != nil {
				t.Fatal(e)
			}
			_, e = r.Store.(*storage.Memory).StartStrategyTrial(ctx, strategytrial.Start{ID: "later-search", Family: "incubation-fixture", StrategyID: sid, Spec: raw, Config: json.RawMessage(`{"variant":1}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
			if e != nil {
				t.Fatal(e)
			}
			req.ID, req.DecidedAt, req.ExecuteAt, req.InputSHA256 = "next-cycle", syncAt, syncAt.Add(time.Second), strings.Repeat("e", 64)
			q := req.Quotes["SPY"]
			q.OccurredAt, q.ReceivedAt, q.SourceSHA256 = req.ExecuteAt, req.ExecuteAt, strings.Repeat("e", 64)
			req.Quotes["SPY"] = q
			req.Limits["SPY"] = "99"
			// Flat exits restore the retained sleeve, including the owner risk input.
			req.Strategies[0].Risk = "0.03"
			if name == "new-sleeve" {
				req.Strategies[0].SleeveID = "new-owner"
			}
			r.now = func() time.Time { return req.ExecuteAt }
			before, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil {
				t.Fatal(e)
			}
			exit, e := r.Run(ctx, req)
			if name == "flat" || name == "zero-target" {
				if e != nil || s.calls != 2 || b.posts != 2 || len(exit.Distribution.Orders) != 1 {
					t.Fatal(exit, e, s.calls, b.posts)
				}
				o := exit.Distribution.Orders[0]
				if o.Side != "sell" || !o.ReduceOnly || o.Quantity != out.Distribution.Orders[0].Quantity {
					t.Fatal("exit changed exposure", o)
				}
				b.fill(t, o.ID, "exit-complete", o.Quantity, "100", "0", req.ExecuteAt.Add(time.Second), true)
				if _, e = r.Sync(ctx, req.ExecuteAt.Add(2*time.Second), req.Quotes); e != nil {
					t.Fatal(e)
				}
				after, e := r.Store.CappedBook(ctx, r.BookID)
				if e != nil {
					t.Fatal(e)
				}
				snap, e := account.SnapshotCapped(after, incubationMarks(req.Quotes), req.ExecuteAt.Add(2*time.Second))
				if e != nil || len(snap.Pending) != 0 {
					t.Fatal("exit did not flatten", snap, e)
				}
				for _, quantity := range snap.Positions {
					q, ok := new(big.Rat).SetString(quantity)
					if !ok || q.Sign() != 0 {
						t.Fatal("exit retained exposure", snap)
					}
				}
			} else {
				if e == nil || !strings.Contains(e.Error(), "gate_search_history_changed") || b.posts != 1 {
					t.Fatal("stale exposure accepted", e, b.posts)
				}
				after, readErr := r.Store.CappedBook(ctx, r.BookID)
				if readErr != nil || after.Version != before.Version {
					t.Fatal("stale decision retained", after, readErr)
				}
				wantCalls := 2
				if name == "new-sleeve" {
					wantCalls = 1
				}
				if s.calls != wantCalls {
					t.Fatal("unexpected stale strategy evaluation", s.calls)
				}
			}
		})
	}
}

type incubationTransport func(*http.Request) (*http.Response, error)

func (f incubationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIncubationActualAdapterChecksClockAfterPreparation(t *testing.T) {
	for _, name := range []string{"expires", "valid", "ambiguous", "worker-death", "owner-pause"} {
		t.Run(name, func(t *testing.T) {
			r, req, s, _ := incubationFixture(t, false)
			at := req.ExecuteAt.Add(53 * time.Second)
			if name == "expires" {
				at = req.ExecuteAt.Add(57 * time.Second)
			}
			r.now = func() time.Time { return at }
			posts, gets := 0, 0
			recoverOrder := name != "ambiguous"
			var sent alpaca.StockOrder
			old := http.DefaultTransport
			http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
				raw, status := "", 200
				if h.Method == http.MethodGet {
					gets++
					if h.URL.Path != "/v2/account/activities/FILL" {
						at = at.Add(2 * time.Second)
					}
				}
				switch h.URL.Path {
				case "/v2/account/activities/FILL":
					raw = `[]`
				case "/v2/account":
					raw = `{"id":"paper-fixture","status":"ACTIVE"}`
				case "/v2/assets/SPY":
					if name == "worker-death" {
						if e := r.Store.(storage.OperationalStore).ReportWorker(context.Background(), domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: "protective_monitoring", WorkerID: "fixture-worker", At: req.ExecuteAt, IntervalMillis: 60000, Stopped: true}}); e != nil {
							t.Fatal(e)
						}
					}
					if name == "owner-pause" {
						state, e := r.Store.CappedBook(context.Background(), r.BookID)
						if e != nil {
							t.Fatal(e)
						}
						if _, e = r.Store.ApplyCappedBook(context.Background(), r.BookID, state.Version, account.BookCommand{ID: "owner-pause-at-preparation", Kind: "risk_control", At: req.ExecuteAt, RiskControl: &risk.Control{Action: "reduce_only"}}); e != nil {
							t.Fatal(e)
						}
					}
					raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
				case "/v2/orders":
					if h.Method != http.MethodPost {
						t.Fatal("unexpected order method", h.Method)
					}
					posts++
					if at.Sub(req.ExecuteAt) > time.Minute {
						t.Fatal("expired quote reached POST", at)
					}
					if e := json.NewDecoder(h.Body).Decode(&sent); e != nil {
						t.Fatal(e)
					}
					if name == "ambiguous" {
						return nil, errors.New("fixture transport interruption")
					}
				case "/v2/orders:by_client_order_id":
					if !recoverOrder {
						status = 404
					}
				default:
					t.Fatalf("unexpected intercepted request: %s", h.URL.Path)
				}
				if sent.ClientOrderID != "" && status == 200 && (h.URL.Path == "/v2/orders" || h.URL.Path == "/v2/orders:by_client_order_id") {
					b, e := json.Marshal(alpaca.PaperOrder{ID: "venue-fixture", ClientOrderID: sent.ClientOrderID, Symbol: sent.Symbol, Qty: sent.Qty, Side: sent.Side, Type: sent.Type, TimeInForce: sent.TimeInForce, LimitPrice: sent.LimitPrice, Status: "new", FilledQty: "0"})
					if e != nil {
						t.Fatal(e)
					}
					raw = string(b)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = old })
			journal, e := alpaca.OpenJournal(filepath.Join(t.TempDir(), "paper"))
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				if e := journal.Close(); e != nil {
					t.Error(e)
				}
			})
			p, e := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fixture-only"}, "paper-fixture", journal)
			if e != nil {
				t.Fatal(e)
			}
			r.Broker = p
			out, e := r.Run(context.Background(), req)
			if name == "expires" || name == "worker-death" || name == "owner-pause" {
				if e == nil || posts != 0 || gets != 4 {
					t.Fatal("preparation expiry missed", e, posts, gets)
				}
				state, readErr := r.Store.CappedBook(context.Background(), r.BookID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				snap, readErr := account.SnapshotCapped(state, incubationMarks(req.Quotes), req.ExecuteAt)
				if readErr != nil || len(snap.Pending) != 0 {
					t.Fatal("known pre-POST refusal trapped reserve", snap, readErr)
				}
			} else if name == "ambiguous" {
				if !errors.Is(e, alpaca.ErrSubmissionUnknown) || posts != 1 {
					t.Fatal(out, e, posts)
				}
			} else if e != nil || posts != 1 {
				t.Fatal(out, e, posts)
			}
			// An expired retry recovers prior work by GET, without re-evaluation or POST.
			at = req.ExecuteAt.Add(24 * time.Hour)
			recoverOrder = true
			if _, e = r.Run(context.Background(), req); e != nil || s.calls != 1 {
				t.Fatal("expired retry failed", e, s.calls)
			}
			wantPosts := 1
			if name == "expires" || name == "worker-death" || name == "owner-pause" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatal("retry posted again", posts)
			}
		})
	}
}

func TestForwardActualAdapterChecksWindowAfterPreparation(t *testing.T) {
	r, req, strategy, _, record := forwardRunnerFixture(t)
	req = nextForwardCycle(req, "forward-adapter-expiry", record.Declaration.WindowEnd.Add(-4*time.Second))
	at := req.ExecuteAt
	healthyIncubationWorkers(t, r, at)
	r.now = func() time.Time { return at }
	gets, posts := 0, 0
	old := http.DefaultTransport
	http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
		if h.Method == http.MethodGet {
			gets++
			if h.URL.Path != "/v2/account/activities/FILL" {
				at = at.Add(2 * time.Second)
			}
		}
		raw := ""
		switch h.URL.Path {
		case "/v2/account/activities/FILL":
			raw = `[]`
		case "/v2/account":
			raw = `{"id":"paper-fixture","status":"ACTIVE"}`
		case "/v2/assets/SPY":
			raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
		case "/v2/orders":
			posts++
			return nil, errors.New("unexpected POST after forward expiry")
		default:
			t.Fatalf("unexpected intercepted path: %s", h.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	journal, err := alpaca.OpenJournal(filepath.Join(t.TempDir(), "forward-paper"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	r.Broker, err = alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fixture-only"}, "paper-fixture", journal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "forward eligibility expired before submission") || posts != 0 || gets != 4 || strategy.calls != 1 {
		t.Fatal("preparatory requests extended the forward window", err, posts, gets, strategy.calls)
	}
	state, err := r.Store.CappedBook(context.Background(), r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := account.SnapshotCapped(state, incubationMarks(req.Quotes), at)
	if err != nil || len(snapshot.Pending) != 0 {
		t.Fatal("known forward refusal trapped the reservation", snapshot, err)
	}
}
