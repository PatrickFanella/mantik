package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type optionRecoveryTransport struct{ handler http.HandlerFunc }

func (x optionRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	x.handler(w, r)
	return w.Result(), nil
}
func optionRecoveryVenue(t *testing.T) (*fakeMlegVenue, *PaperTrader, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "journal")
	j, e := OpenJournal(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = j.Close() })
	p, e := NewPaperTrader(PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
	if e != nil {
		t.Fatal(e)
	}
	v := &fakeMlegVenue{t: t, root: root, orders: map[string]OptionPaperOrder{}, requests: map[string]optionRequest{}, whole: "filled", short: "filled"}
	p.http = &http.Client{Transport: optionRecoveryTransport{v.serve}}
	return v, p, root
}
func TestMLegRecoveryStockLifecycleNameCollision(t *testing.T) {
	posts, deletes, gets := 0, 0, 0
	id := "mleg-lifecycle"
	response := strings.ReplaceAll(testPaperResponse, "mantik-test", id)
	j, e := OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	p, e := NewPaperTrader(PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
	if e != nil {
		t.Fatal(e)
	}
	p.http = &http.Client{Transport: optionRecoveryTransport{func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/account":
			fmt.Fprint(w, `{"id":"account-1","status":"ACTIVE"}`)
		case r.URL.Path == "/v2/assets/SPY":
			fmt.Fprint(w, `{"symbol":"SPY","class":"us_equity","tradable":true}`)
		case r.Method == "POST":
			posts++
			fmt.Fprint(w, response)
		case r.URL.Path == "/v2/orders:by_client_order_id":
			gets++
			fmt.Fprint(w, response)
		case r.Method == "DELETE":
			deletes++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}}}
	o := testStockOrder()
	o.ClientOrderID = id
	if _, e = p.Submit(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Submit(context.Background(), o); e != nil {
		t.Errorf("valid stock retry blocked: %v", e)
	}
	if e = p.Cancel(context.Background(), id); e != nil {
		t.Errorf("valid stock cancellation blocked: %v", e)
	}
	t.Logf("posts=%d lookups=%d deletes=%d", posts, gets, deletes)
}
func TestMLegRecoveryIndependentBudgetAlgebra(t *testing.T) {
	_, p, _ := optionRecoveryVenue(t)
	for _, op := range []string{"-10", "1.111111111"} {
		for _, cl := range []string{"-20", "2.222222222"} {
			o := mlegFixtureOpen()
			o.Order.LimitPrice = op
			opened := mlegResponse(optionRequest{MLegOrder: o.Order}, 1, "canceled")
			cash, _ := p.optionCloseCash(o, opened)
			expected := big.NewRat(198440, 100)
			if op[0] != '-' {
				x, _ := new(big.Rat).SetString(op)
				expected.Sub(expected, x.Mul(x, big.NewRat(100, 1)))
			}
			if cash.Cmp(expected) != 0 {
				t.Fatalf("opening actual %s want %s", cash, expected)
			}
			wr := optionRequest{MLegOrder: o.Order}
			wr.LimitPrice = cl
			whole := mlegResponse(wr, 1, "canceled")
			if cl[0] != '-' {
				x, _ := new(big.Rat).SetString(cl)
				expected.Sub(expected, x.Mul(x, big.NewRat(100, 1)))
			}
			buy := closingLeg(wr, "prior", o.Order.Legs[1], 2, "1.234567891")
			prior := mlegResponse(buy, 1, "canceled")
			improved := "0.1"
			prior.FilledAvgPrice = &improved
			expected.Sub(expected, big.NewRat(1234567891, 10000000))
			sale := closingLeg(wr, "sale", o.Order.Legs[0], 2, "999999")
			previous := map[int]OptionPaperOrder{1: prior, 0: mlegResponse(sale, 2, "filled")}
			next := closingLeg(wr, "next", o.Order.Legs[2], 1, new(big.Rat).Quo(expected, big.NewRat(100, 1)).FloatString(9))
			if e := p.optionFallbackFunding(o, opened, whole, previous, next); e != nil {
				t.Fatal("exact cash boundary", e)
			}
			x, _ := new(big.Rat).SetString(next.LimitPrice)
			next.LimitPrice = x.Add(x, big.NewRat(1, 1000000000)).FloatString(9)
			if e := p.optionFallbackFunding(o, opened, whole, previous, next); e == nil {
				t.Fatal("one decimal unit over boundary passed")
			}
		}
	}
}
func TestMLegRecoveryObservationBoundBeforeDelete(t *testing.T) {
	v, p, root := optionRecoveryVenue(t)
	o := mlegFixtureOpen()
	req := optionRequest{MLegOrder: o.Order}
	raw, _ := json.Marshal(o)
	if e := p.journal.writeOnce(optionLifecycle, raw); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(req)
	if e := p.journal.writeOnce(optionIntentName(req.ClientOrderID), raw); e != nil {
		t.Fatal(e)
	}
	var current OptionPaperOrder
	// Seed a valid changed history directly; avoid O(n^2) repeated reads.
	for i := 1; i <= 1024; i++ {
		status := "new"
		if i%2 == 0 {
			status = "accepted"
		}
		current = mlegResponse(req, 0, status)
		raw, _ = json.Marshal(optionObservation{req, current})
		if e := p.journal.writeOnce(optionObservationName(i), raw); e != nil {
			t.Fatal(e)
		}
	}
	v.orders[o.Order.ClientOrderID] = current
	v.requests[o.Order.ClientOrderID] = req
	out, e := p.CloseMLeg(context.Background(), mlegPlan())
	t.Logf("at full observation capacity: deletes=%d posts=%d state=%s err=%v", v.deletes, len(v.posts), out.State, e)
	if v.deletes != 0 || len(v.posts) != 0 {
		t.Error("authority allowed mutation with no capacity for its resulting observation")
	}
	if !errors.Is(e, ErrOptionQuarantined) {
		t.Errorf("expected capacity fence, got %v", e)
	}
	restartOptionJournal(t, p, root)
	before := v.deletes
	_, _ = p.CloseMLeg(context.Background(), mlegPlan())
	if v.deletes != before {
		t.Fatal("quarantine did not persist")
	}
}
func TestMLegRecoveryObservationBoundBeforePOST(t *testing.T) {
	v, p, _ := optionRecoveryVenue(t)
	o := mlegFixtureOpen()
	req := optionRequest{MLegOrder: o.Order}
	raw, _ := json.Marshal(o)
	if e := p.journal.writeOnce(optionLifecycle, raw); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(req)
	if e := p.journal.writeOnce(optionIntentName(req.ClientOrderID), raw); e != nil {
		t.Fatal(e)
	}
	var current OptionPaperOrder
	for i := 1; i <= 1024; i++ {
		status := "new"
		fill := 0
		if i%2 == 0 {
			status = "accepted"
		}
		if i == 1024 {
			status = "filled"
			fill = 2
		}
		current = mlegResponse(req, fill, status)
		raw, _ = json.Marshal(optionObservation{req, current})
		if e := p.journal.writeOnce(optionObservationName(i), raw); e != nil {
			t.Fatal(e)
		}
	}
	v.orders[o.Order.ClientOrderID] = current
	v.requests[o.Order.ClientOrderID] = req
	out, e := p.CloseMLeg(context.Background(), mlegPlan())
	t.Logf("full observation history: POSTs=%d DELETEs=%d state=%s err=%v", len(v.posts), v.deletes, out.State, e)
	if len(v.posts) != 0 {
		t.Error("new whole-close POST issued despite no observation capacity")
	}
}
func TestMLegRecoveryCustodyPriceIdentityAndRetry(t *testing.T) {
	for _, kind := range []string{"parent-price", "child-price", "parent-id", "child-id", "terminal-regression", "corrupt-intent"} {
		t.Run(kind, func(t *testing.T) {
			v, p, root := optionRecoveryVenue(t)
			o := mlegFixtureOpen()
			testOpenMleg(t, p, o)
			good := v.orders[o.Order.ClientOrderID]
			bad := mlegResponse(optionRequest{MLegOrder: o.Order}, 2, "filled")
			switch kind {
			case "parent-price":
				s := "-0.03"
				bad.FilledAvgPrice = &s
			case "child-price":
				s := "0.02"
				bad.Legs[0].FilledAvgPrice = &s
			case "parent-id":
				bad.ID = "changed"
			case "child-id":
				bad.Legs[0].ID = "changed"
			case "terminal-regression":
				bad.Status = "accepted"
			case "corrupt-intent":
				if e := os.WriteFile(filepath.Join(root, optionIntentName(o.Order.ClientOrderID)), []byte(`{}`), 0600); e != nil {
					t.Fatal(e)
				}
			}
			v.orders[o.Order.ClientOrderID] = bad
			_, e := p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(e, ErrOptionQuarantined) {
				t.Fatal(e)
			}
			v.orders[o.Order.ClientOrderID] = good
			restartOptionJournal(t, p, root)
			out, e := p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(e, ErrOptionQuarantined) || out.State == "closed" || len(v.posts) != 1 || v.deletes != 0 {
				t.Fatal(out, e, v.posts, v.deletes)
			}
		})
	}
}

// Emulate process death immediately after request() durably retains a successful
// POST response and before optionSubmitChecked calls optionObserve on that body.
func TestMLegRecoveryCrashAfterRawContradictoryPOST(t *testing.T) {
	testMLegInterruptedReply(t, true)
}

func TestMLegRecoveryCrashAfterRawValidPOST(t *testing.T) {
	testMLegInterruptedReply(t, false)
}

func testMLegInterruptedReply(t *testing.T, contradictory bool) {
	t.Helper()
	for _, rollover := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollover=%v", rollover), func(t *testing.T) {
			v, p, root := optionRecoveryVenue(t)
			o := mlegFixtureOpen()
			req := optionRequest{MLegOrder: o.Order}
			if e := p.bind(context.Background()); e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(o)
			if e := p.journal.writeOnce(optionLifecycle, raw); e != nil {
				t.Fatal(e)
			}
			raw, _ = json.Marshal(req)
			if e := p.journal.writeOnce(optionIntentName(req.ClientOrderID), raw); e != nil {
				t.Fatal(e)
			}
			if contradictory {
				p.http.Transport = contradictionTransport{base: p.http.Transport, stage: "opening"}
			}
			body, status, e := p.request(context.Background(), http.MethodPost, "/v2/orders", raw)
			if e != nil || status != 200 {
				t.Fatal(status, e)
			}
			if _, e := decodeOptionOrder(body, req); (e != nil) != contradictory {
				t.Fatal("unexpected fixture classification", e)
			}
			files, e := filepath.Glob(filepath.Join(root, "paper-response-*.json"))
			if e != nil || len(files) == 0 {
				t.Fatal(files, e)
			}
			// Persisted state matches death before optionObserve runs.
			if e := p.journal.Close(); e != nil {
				t.Fatal(e)
			}
			j, e := OpenJournal(root)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = j.Close() })
			p, e = NewPaperTrader(PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
			if e != nil {
				t.Fatal(e)
			}
			if rollover {
				if _, e := j.Rollover(context.Background(), t.TempDir()); e != nil {
					t.Fatal(e)
				}
			}
			p.http.Transport = optionRecoveryTransport{v.serve}
			out, e := p.CloseMLeg(context.Background(), mlegPlan())
			t.Logf("after retained unclassified POST + restart: posts=%d state=%s err=%v", len(v.posts), out.State, e)
			if !errors.Is(e, ErrOptionQuarantined) || out.State == "closed" || len(v.posts) != 1 {
				t.Error("later coherent GET restored authority after interrupted classification")
			}
		})
	}
}

func TestMLegReplyFencePrecedesHTTPAndClearsOnClassifiedRecovery(t *testing.T) {
	for _, unavailable := range []string{"none", "transport", "http"} {
		t.Run(unavailable, func(t *testing.T) {
			v, p, root := optionRecoveryVenue(t)
			posts, gets := 0, 0
			assertPending := func(want int) {
				t.Helper()
				names, err := filepath.Glob(filepath.Join(root, optionReplyPendingPrefix+"*.json"))
				if err != nil || len(names) != want {
					t.Fatalf("pending classification markers: got %d want %d: %v", len(names), want, err)
				}
			}
			p.http.Transport = optionReplyTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost || r.URL.Path == "/v2/orders:by_client_order_id" {
					assertPending(1)
				}
				if r.Method == http.MethodPost {
					posts++
					if unavailable == "transport" {
						// The venue accepted the order before the transport failed.
						v.serve(httptest.NewRecorder(), r)
						return nil, errors.New("fixture transport failure")
					}
					v.lost = unavailable == "http"
				}
				if r.URL.Path == "/v2/orders:by_client_order_id" {
					gets++
					v.lost = false
				}
				return optionRecoveryTransport{v.serve}.RoundTrip(r)
			})
			testOpenMleg(t, p, mlegFixtureOpen())
			assertPending(0)
			if posts != 1 || (gets > 0) != (unavailable != "none") {
				t.Fatal("unexpected recovery requests", posts, gets)
			}
			// Reopening retains ordinary mutation authority after known failures
			// whose recovery replies were durably classified.
			restartOptionJournal(t, p, root)
			p.http.Transport = optionRecoveryTransport{v.serve}
			out, err := p.CloseMLeg(context.Background(), mlegPlan())
			if err != nil || out.State != "closed" || len(v.posts) != 2 {
				t.Fatal(out, err, v.posts)
			}
			assertPending(0)
		})
	}
}

type optionReplyTestTransport func(*http.Request) (*http.Response, error)

func (f optionReplyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestMLegUnclassifiedReplyDuringCloseBlocksClosedDeclaration(t *testing.T) {
	v, p, _ := optionRecoveryVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	p.http.Transport = optionReplyTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			// An older response whose retention/classification did not finish can
			// coexist with this valid reply. The new observation cannot clear it.
			if err := p.journal.writeOnce(optionReplyPendingPrefix+"interrupted.json", []byte(`{"state":"pending"}`)); err != nil {
				t.Fatal(err)
			}
		}
		return optionRecoveryTransport{v.serve}.RoundTrip(r)
	})
	out, err := p.CloseMLeg(context.Background(), mlegPlan())
	if !errors.Is(err, ErrOptionQuarantined) || out.State == "closed" || len(v.posts) != 2 {
		t.Fatal(out, err, v.posts)
	}
}
