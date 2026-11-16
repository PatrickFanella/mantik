package mlegpaper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

// This transport has no network fallback. Every request is answered in memory.
type atomicNetVenue struct {
	t               *testing.T
	orders          map[string]alpaca.OptionPaperOrder
	posts           int
	expensiveOpen   bool
	expensiveClose  bool
	beforePositions func()
}

func (v *atomicNetVenue) RoundTrip(r *http.Request) (*http.Response, error) {
	var body any
	switch {
	case r.Method == "GET" && r.URL.Path == "/v2/account":
		body = map[string]any{"id": "account-1", "status": "ACTIVE", "options_trading_level": 3}
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v2/options/contracts/"):
		body = map[string]any{"symbol": strings.TrimPrefix(r.URL.Path, "/v2/options/contracts/"), "underlying_symbol": "TST", "size": "100", "status": "active", "tradable": true}
	case r.Method == "GET" && r.URL.Path == "/v2/orders":
		body = []any{}
	case r.Method == "GET" && r.URL.Path == "/v2/positions":
		if v.beforePositions != nil {
			v.beforePositions()
		}
		quantities := map[string]int{}
		for _, o := range v.orders {
			for _, l := range o.Legs {
				n, _ := strconv.Atoi(l.FilledQty)
				if l.Side == "sell" {
					n = -n
				}
				quantities[l.Symbol] += n
			}
		}
		positions := []map[string]string{}
		for s, n := range quantities {
			if n != 0 {
				positions = append(positions, map[string]string{"symbol": s, "qty": strconv.Itoa(n)})
			}
		}
		body = positions
	case r.Method == "GET" && r.URL.Path == "/v2/orders:by_client_order_id":
		o, ok := v.orders[r.URL.Query().Get("client_order_id")]
		if !ok {
			return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		body = o
	case r.Method == "POST" && r.URL.Path == "/v2/orders":
		var req alpaca.MLegOrder
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		v.posts++
		o := alpaca.OptionPaperOrder{PaperOrder: alpaca.PaperOrder{ID: "venue-" + req.ClientOrderID, ClientOrderID: req.ClientOrderID, Qty: req.Qty, Type: req.Type, TimeInForce: req.TimeInForce, LimitPrice: req.LimitPrice, FilledQty: req.Qty, FilledAvgPrice: &req.LimitPrice, Status: "filled"}, OrderClass: "mleg"}
		opening := !strings.HasPrefix(req.ClientOrderID, "mleg-close-")
		if opening {
			avg := "-0.02"
			o.FilledAvgPrice = &avg
		}
		for i, l := range req.Legs {
			price := "0.01"
			if opening && l.Side == "sell" || !opening && l.Side == "buy" {
				price = "0.02"
			}
			if opening && v.expensiveOpen {
				if l.Side == "buy" {
					price = "15"
				} else {
					price = "15.01"
				}
			}
			if !opening && v.expensiveClose {
				if l.Side == "buy" {
					price = "15"
				} else {
					price = "14.99"
				}
			}
			o.Legs = append(o.Legs, alpaca.OptionPaperOrder{PaperOrder: alpaca.PaperOrder{ID: fmt.Sprintf("%s-leg%d", o.ID, i), Symbol: l.Symbol, Side: l.Side, Qty: "1", FilledQty: "1", FilledAvgPrice: &price, Status: "filled"}, PositionIntent: l.PositionIntent})
		}
		v.orders[req.ClientOrderID] = o
		body = o
	default:
		return nil, fmt.Errorf("unexpected fake request %s %s", r.Method, r.URL.Path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}
func atomicNetInput(t *testing.T) alpaca.MLegOpen {
	o := admissionFixture(t, "iron_condor", true)
	// Move all source clocks together to the actual invocation time.
	now := time.Now().UTC().Add(-time.Minute)
	shift := now.Sub(o.Chain.AsOf)
	o.Chain.AsOf = now
	o.Chain.Spot.At = o.Chain.Spot.At.Add(shift)
	o.Chain.Spot.ObservedAt = now
	o.Chain.Spot.AvailableAt = now
	z, _ := time.LoadLocation("America/New_York")
	o.Chain.Spot.Date = o.Chain.Spot.At.In(z).Format(time.DateOnly)
	o.Chain.Spot.Price = 100
	for i := range o.Chain.Entries {
		e := &o.Chain.Entries[i]
		e.ReferenceObservedAt = now
		e.ReferenceAvailableAt = now
		e.PriceOccurredAt = o.Chain.Spot.At
		e.PriceObservedAt = now
		e.PriceAvailableAt = now
		old := e.Symbol
		e.Strike += 99
		e.Symbol = old[:len(old)-8] + fmt.Sprintf("%08d", int(e.Strike*1000+.5))
		e.ContractID = e.Symbol
		e.Greeks.Gamma = .000001
		e.Greeks.Delta /= 10
		for j := range o.Structure.Legs {
			if o.Structure.Legs[j].Symbol == old {
				o.Structure.Legs[j].Symbol = e.Symbol
				o.Order.Legs[j].Symbol = e.Symbol
			}
		}
	}
	// Build a genuinely scheduled synthetic test ledger at the current cutoff.
	var err error
	o.Funding, err = account.NewCappedState(account.DefaultCappedConfig("dedicated", "account-1", account.Margin, now.AddDate(0, 0, -14)))
	if err != nil {
		t.Fatal(err)
	}
	due, err := o.Funding.Config.DueDeposits(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		o.Funding, err = account.ApplyCappedCommand(o.Funding, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := admit(context.Background(), o, now); err != nil {
		t.Fatal("invalid admission fixture", err)
	}
	return o
}
func TestAtomicNetHighGrossExecutionUsesRealAdmission(t *testing.T) {
	for _, stage := range []string{"opening", "whole_close"} {
		t.Run(stage, func(t *testing.T) {
			v := &atomicNetVenue{t: t, orders: map[string]alpaca.OptionPaperOrder{}, expensiveOpen: stage == "opening", expensiveClose: stage == "whole_close"}
			old := http.DefaultTransport
			http.DefaultTransport = v
			t.Cleanup(func() { http.DefaultTransport = old })
			j, err := alpaca.OpenJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p, err := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
			if err != nil {
				t.Fatal(err)
			}
			o := atomicNetInput(t)
			cash, err := o.LocalFunding("account-1")
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(context.Background(), p, o)
			if err != nil {
				t.Fatal(err)
			}
			result := opened
			state := "opened"
			if stage == "whole_close" {
				closed, e := p.CloseMLeg(context.Background(), alpaca.MLegClose{OpenClientOrderID: o.Order.ClientOrderID, LimitPrice: "0.02", LegLimits: []string{"0.01", "0.02", "0.02", "0.01"}})
				if e != nil {
					t.Fatal(e)
				}
				result = closed.Whole
				state = closed.State
			}
			buys, sales := 0.0, 0.0
			for _, l := range result.Legs {
				price, _ := strconv.ParseFloat(*l.FilledAvgPrice, 64)
				if l.Side == "buy" {
					buys += 100 * price
				} else {
					sales += 100 * price
				}
			}
			t.Logf("public Open -> %s: dedicated cash=%s buy debits=%.2f sale proceeds=%.2f net debit=%.2f posts=%d", state, cash, buys, sales, buys-sales, v.posts)
			available, _ := strconv.ParseFloat(cash, 64)
			if available != 2500 || buys != 3000 {
				t.Fatal("lost high gross P1 fixture", cash, buys)
			}
			wantSales := 3002.0
			if stage == "whole_close" {
				wantSales = 2998
			}
			if sales != wantSales {
				t.Fatal(sales, wantSales)
			}
			// Four contracts, fixed three-round modeled fee pool. Opening credit
			// consumes no premium allowance; whole close consumes its $2 limit.
			allowance := 7.8
			if stage == "whole_close" {
				allowance += 2
			}
			if allowance > available {
				t.Fatal("unfunded package", allowance)
			}
			if o.ExecutionPolicy != alpaca.PaperAtomicNetV2 {
				t.Fatal("implicit policy")
			}
			if stage == "whole_close" && state != "closed" {
				t.Fatal(state)
			}
		})
	}
}

func TestAtomicNetFreshnessAfterBlockingPreparation(t *testing.T) {
	v := &atomicNetVenue{t: t, orders: map[string]alpaca.OptionPaperOrder{}}
	old := http.DefaultTransport
	http.DefaultTransport = v
	t.Cleanup(func() { http.DefaultTransport = old })
	root := filepath.Join(t.TempDir(), "journal")
	j, err := alpaca.OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	p, err := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
	if err != nil {
		t.Fatal(err)
	}
	o := atomicNetInput(t)
	now := time.Now().UTC()
	o.Chain.AsOf = now
	o.Chain.Assumptions.MaxAge = 200 * time.Millisecond
	o.Chain.Spot.At = now
	o.Chain.Spot.ObservedAt = now
	o.Chain.Spot.AvailableAt = now
	z, _ := time.LoadLocation("America/New_York")
	o.Chain.Spot.Date = now.In(z).Format(time.DateOnly)
	for i := range o.Chain.Entries {
		e := &o.Chain.Entries[i]
		e.PriceOccurredAt = now
		e.PriceObservedAt = now
		e.PriceAvailableAt = now
		e.ReferenceObservedAt = now
		e.ReferenceAvailableAt = now
	}
	if err := admit(context.Background(), o, now); err != nil {
		t.Fatal("fixture not initially fresh", err)
	}
	v.beforePositions = func() { time.Sleep(250 * time.Millisecond) }
	_, err = Open(context.Background(), p, o)
	if err == nil || v.posts != 0 {
		t.Fatalf("stale evidence reached POST: posts=%d error=%v", v.posts, err)
	}
	t.Logf("blocked preparation refused before POST: %v; posts=%d", err, v.posts)
	v.beforePositions = nil
	_, err = Open(context.Background(), p, o)
	if err != alpaca.ErrOptionRefused || v.posts != 0 {
		t.Fatalf("retry escaped fence: posts=%d error=%v", v.posts, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j2, err := alpaca.OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	p2, err := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fake-only"}, "account-1", j2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), p2, o)
	if err != alpaca.ErrOptionRefused || v.posts != 0 {
		t.Fatalf("restart escaped fence: posts=%d error=%v", v.posts, err)
	}
	t.Logf("exact retry and reopened journal remained GET-only: %v; posts=%d", err, v.posts)
}

func TestAtomicNetPolicyKeepsRealAdmissionCaps(t *testing.T) {
	for name, change := range map[string]func(*alpaca.MLegOpen){
		"risk_option_delta": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				o.Chain.Entries[i].Greeks.Delta = .99
				if o.Chain.Entries[i].Type == "put" {
					o.Chain.Entries[i].Greeks.Delta = -.99
				}
			}
		},
		"risk_option_gamma": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				o.Chain.Entries[i].Greeks.Gamma = 1
			}
		},
		"risk_option_vega": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				o.Chain.Entries[i].Greeks.Vega = 200000
			}
		},
		"risk_option_theta": func(o *alpaca.MLegOpen) {
			for i := range o.Chain.Entries {
				o.Chain.Entries[i].Greeks.Theta = -1000
			}
		},
		"risk_cash_reserve": func(o *alpaca.MLegOpen) {
			for i := range o.Structure.Legs {
				o.Structure.Legs[i].Limit = 10
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := &atomicNetVenue{t: t, orders: map[string]alpaca.OptionPaperOrder{}}
			old := http.DefaultTransport
			http.DefaultTransport = v
			t.Cleanup(func() { http.DefaultTransport = old })
			j, err := alpaca.OpenJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p, err := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fake-only"}, "account-1", j)
			if err != nil {
				t.Fatal(err)
			}
			o := atomicNetInput(t)
			change(&o)
			_, err = Open(context.Background(), p, o)
			if err == nil || !strings.Contains(err.Error(), name) || v.posts != 0 {
				t.Fatal("net policy bypassed admission", err, v.posts)
			}
		})
	}
}
