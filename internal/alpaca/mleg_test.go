package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/options"
)

func mlegFixtureOpen() MLegOpen {
	o := MLegOpen{ExecutionPolicy: PaperAtomicNetV2, Order: MLegOrder{ClientOrderID: "condor-open", Qty: "2", OrderClass: "mleg", Type: "limit", TimeInForce: "day", LimitPrice: "-0.02"}, Chain: options.Chain{AsOf: time.Now().UTC(), Underlying: "TST"}, Structure: options.Structure{Kind: "iron_condor"}}
	o.Funding, _ = account.NewCappedState(account.DefaultCappedConfig("dedicated-options", "account-1", account.Margin, o.Chain.AsOf.Add(-time.Hour)))
	for i, symbol := range []string{"TST261218P00000900", "TST261218P00000950", "TST261218C00001050", "TST261218C00001100"} {
		side, n, limit := "buy", 2, .01
		if i == 1 || i == 2 {
			side, n, limit = "sell", -2, .02
		}
		o.Order.Legs = append(o.Order.Legs, MLeg{Symbol: symbol, RatioQty: "1", Side: side, PositionIntent: side + "_to_open"})
		o.Structure.Legs = append(o.Structure.Legs, options.Leg{Symbol: symbol, Contracts: n, Multiplier: 100, Limit: limit})
	}
	return o
}
func mlegResponse(r optionRequest, fill int, status string) OptionPaperOrder {
	price := r.LimitPrice
	o := OptionPaperOrder{PaperOrder: PaperOrder{ID: "venue-" + r.ClientOrderID, ClientOrderID: r.ClientOrderID, Qty: r.Qty, Type: r.Type, TimeInForce: r.TimeInForce, LimitPrice: r.LimitPrice, Status: status, FilledQty: strconv.Itoa(fill)}, OrderClass: r.OrderClass, PositionIntent: r.PositionIntent}
	if fill > 0 {
		o.FilledAvgPrice = &price
	}
	if r.OrderClass == "mleg" {
		q, _ := optionInteger(r.Qty)
		prices := make([]string, len(r.Legs))
		net := new(big.Rat)
		for i, l := range r.Legs {
			prices[i] = "0.01"
			ratio, _ := optionInteger(l.RatioQty)
			if l.Side == "sell" {
				ratio = -ratio
			}
			net.Add(net, big.NewRat(int64(ratio), 100))
		}
		limit, _ := new(big.Rat).SetString(r.LimitPrice)
		delta := new(big.Rat).Sub(limit, net)
		for i, l := range r.Legs {
			if delta.Sign() >= 0 && l.Side == "buy" || delta.Sign() < 0 && l.Side == "sell" {
				ratio, _ := optionInteger(l.RatioQty)
				add := new(big.Rat).Quo(new(big.Rat).Abs(delta), big.NewRat(int64(ratio), 1))
				prices[i] = add.Add(add, big.NewRat(1, 100)).FloatString(9)
				break
			}
		}
		for i, l := range r.Legs {
			ratio, _ := optionInteger(l.RatioQty)
			childPrice := prices[i]
			o.Legs = append(o.Legs, OptionPaperOrder{PaperOrder: PaperOrder{ID: fmt.Sprintf("%s-leg%d", o.ID, i), Symbol: l.Symbol, Side: l.Side, Qty: strconv.Itoa(q * ratio), FilledQty: strconv.Itoa(fill * ratio), FilledAvgPrice: &childPrice, Status: status}, PositionIntent: l.PositionIntent})
		}
	} else {
		o.Symbol = r.Symbol
		o.Side = r.Side
	}
	return o
}

// fakeMlegVenue supplies only test transports. Positions come from the exact
// returned child fill quantities, so fallback safety checks see every partial.
type fakeMlegVenue struct {
	deletes            int
	incompletePending  bool
	cancelLegFills     []int
	closeLookupMissing bool
	closeHTTPFailure   bool
	refusalCode        int
	badPermission      bool
	badDeliverable     bool
	t                  *testing.T
	orders             map[string]OptionPaperOrder
	requests           map[string]optionRequest
	posts              []optionRequest
	gets               []string
	whole              string
	short              string
	cancelPending      bool
	cancelFill         int
	wrongInventory     bool
	extraPending       bool
	wrongLeg           bool
	lost               bool
	refused            bool
	root               string
}

func newFakeMlegVenue(t *testing.T) (*fakeMlegVenue, *PaperTrader, string) {
	v := &fakeMlegVenue{t: t, orders: map[string]OptionPaperOrder{}, requests: map[string]optionRequest{}, whole: "filled", short: "filled"}
	p, root := paperFixture(t, v.serve)
	v.root = root
	return v, p, root
}
func (v *fakeMlegVenue) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		v.gets = append(v.gets, r.URL.Path)
	}
	switch {
	case r.URL.Path == "/v2/account":
		if v.badPermission {
			_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE","options_trading_level":2}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE","options_trading_level":3,"buying_power":"100000000"}`))
	case strings.HasPrefix(r.URL.Path, "/v2/options/contracts/"):
		if v.badDeliverable {
			_, _ = w.Write([]byte(`{"size":"10"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"symbol": strings.TrimPrefix(r.URL.Path, "/v2/options/contracts/"), "underlying_symbol": "TST", "status": "active", "tradable": true, "size": "100"})
	case r.URL.Path == "/v2/orders" && r.Method == http.MethodPost:
		var req optionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			v.t.Error(err)
			w.WriteHeader(500)
			return
		}
		intent, err := os.ReadFile(filepath.Join(v.root, optionIntentName(req.ClientOrderID)))
		if err != nil {
			v.t.Error("POST before durable intent", err)
		}
		raw, _ := json.Marshal(req)
		if string(intent) != string(raw) {
			v.t.Error("wire intent differs")
		}
		v.posts = append(v.posts, req)
		v.requests[req.ClientOrderID] = req
		status, filled := "filled", 0
		q, _ := optionInteger(req.Qty)
		switch {
		case req.ClientOrderID == "condor-open":
			filled = q
		case strings.HasPrefix(req.ClientOrderID, "mleg-close-"):
			if v.refused {
				w.WriteHeader(422)
				code := v.refusalCode
				if code == 0 {
					code = 42210000
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "mutable text"})
				return
			}
			status = v.whole
			if status == "filled" {
				filled = q
			}
		default:
			if req.Side == "sell" {
				// No sale of protection is permitted with ANY remaining short liability.
				for symbol, n := range v.positions() {
					if n < 0 {
						v.t.Errorf("naked short %s=%d before protection sale", symbol, n)
					}
				}
				filled = q
			} else {
				status = v.short
				if status == "filled" {
					filled = q
				}
				if status == "partially_filled" {
					filled = 1
				}
			}
		}
		o := mlegResponse(req, filled, status)
		v.orders[req.ClientOrderID] = o
		if v.closeHTTPFailure && strings.HasPrefix(req.ClientOrderID, "mleg-close-") {
			w.WriteHeader(503)
			return
		}
		if v.lost && req.ClientOrderID == "condor-open" {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(o)
	case r.URL.Path == "/v2/orders:by_client_order_id":
		id := r.URL.Query().Get("client_order_id")
		if v.lost || v.closeLookupMissing && strings.HasPrefix(id, "mleg-close-") {
			w.WriteHeader(404)
			return
		}
		o, ok := v.orders[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if v.wrongLeg && len(o.Legs) > 0 {
			o.Legs = append([]OptionPaperOrder(nil), o.Legs...)
			o.Legs[0].ID = o.Legs[1].ID
		}
		_ = json.NewEncoder(w).Encode(o)
	case r.URL.Path == "/v2/orders":
		pending := []OptionPaperOrder{}
		if v.incompletePending {
			pending = append(pending, OptionPaperOrder{OrderClass: "mleg"})
		}
		for _, o := range v.orders {
			if !optionTerminal(o.Status) {
				pending = append(pending, o)
			}
		}
		if v.extraPending {
			pending = append(pending, OptionPaperOrder{PaperOrder: PaperOrder{Symbol: "TST261218P00000900"}})
		}
		_ = json.NewEncoder(w).Encode(pending)
	case r.URL.Path == "/v2/positions":
		pos := []map[string]string{}
		quantities := v.positions()
		if v.wrongInventory {
			quantities["TST261218P00000900"]++
		}
		for s, n := range quantities {
			if n != 0 {
				pos = append(pos, map[string]string{"symbol": s, "qty": strconv.Itoa(n)})
			}
		}
		_ = json.NewEncoder(w).Encode(pos)
	case strings.HasPrefix(r.URL.Path, "/v2/orders/") && r.Method == http.MethodDelete:
		v.deletes++
		id := strings.TrimPrefix(r.URL.Path, "/v2/orders/")
		for client, o := range v.orders {
			if o.ID == id && !v.cancelPending {
				o = mlegResponse(v.requests[client], v.cancelFill, "canceled")
				if len(v.cancelLegFills) == len(o.Legs) {
					for i, n := range v.cancelLegFills {
						o.Legs[i].FilledQty = strconv.Itoa(n)
						if n > 0 {
							price := "0.01"
							o.Legs[i].FilledAvgPrice = &price
						}
					}
				}

				v.orders[client] = o
			}
		}
		w.WriteHeader(204)
	default:
		v.t.Error("unexpected request", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}
func (v *fakeMlegVenue) positions() map[string]int {
	out := map[string]int{}
	for _, o := range v.orders {
		legs := o.Legs
		if o.OrderClass != "mleg" {
			legs = []OptionPaperOrder{o}
		}
		for _, l := range legs {
			n, _ := filledContracts(l)
			if l.Side == "sell" {
				n = -n
			}
			out[l.Symbol] += n
		}
	}
	return out
}
func testOpenMleg(t *testing.T, p *PaperTrader, o MLegOpen) {
	t.Helper()
	// Adapter tests supply an explicit fixture-only admission callback. Product
	// callers use mlegpaper.Open, whose real OptionBook caps are tested separately.
	if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func mlegPlan() MLegClose {
	return MLegClose{OpenClientOrderID: "condor-open", LimitPrice: "0.02", LegLimits: []string{"0.01", "0.02", "0.02", "0.01"}}
}

func TestMLegWireGrammar(t *testing.T) {
	for _, limit := range []string{"0.02", "-0.02"} {
		o := mlegFixtureOpen().Order
		o.LimitPrice = limit
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, change := range map[string]func(*MLegOrder){
		"zero": func(o *MLegOrder) { o.LimitPrice = "0" }, "minus zero": func(o *MLegOrder) { o.LimitPrice = "-0" }, "exponent": func(o *MLegOrder) { o.LimitPrice = "1e2" }, "not decimal": func(o *MLegOrder) { o.LimitPrice = "NaN" }, "fraction": func(o *MLegOrder) { o.Qty = "0.5" }, "not integer": func(o *MLegOrder) { o.Qty = "1.0" }, "five legs": func(o *MLegOrder) { o.Legs = append(o.Legs, o.Legs[0]) }, "one leg": func(o *MLegOrder) { o.Legs = o.Legs[:1] }, "ratio": func(o *MLegOrder) {
			for i := range o.Legs {
				o.Legs[i].RatioQty = "2"
			}
		}, "duplicate": func(o *MLegOrder) { o.Legs[1].Symbol = o.Legs[0].Symbol }, "stock": func(o *MLegOrder) { o.Legs[0].Symbol = "TST" }, "intent": func(o *MLegOrder) { o.Legs[0].PositionIntent = "sell_to_open" }, "mixed": func(o *MLegOrder) { o.Legs[0].PositionIntent = "buy_to_close" }, "overnight": func(o *MLegOrder) { o.TimeInForce = "ioc" },
	} {
		t.Run(name, func(t *testing.T) {
			o := mlegFixtureOpen().Order
			change(&o)
			if o.Validate() == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	raw, _ := json.Marshal(mlegFixtureOpen().Order)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	for _, key := range []string{"symbol", "side", "notional", "extended_hours"} {
		if _, ok := object[key]; ok {
			t.Fatal(key)
		}
	}
	stock := testStockOrder()
	stock.LimitPrice = "-1"
	if stock.Validate() == nil {
		t.Fatal("stock credit limit accepted")
	}
}

func TestMLegWholeCloseAndExactRetry(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	testOpenMleg(t, p, o)
	if _, err := p.OpenMLeg(context.Background(), o, func() error { t.Fatal("retry admission called"); return nil }); err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.Order.Qty = "3"
	if _, err := p.OpenMLeg(context.Background(), changed, nil); !errors.Is(err, ErrJournalConflict) {
		t.Fatal(err)
	}
	result, err := p.CloseMLeg(context.Background(), mlegPlan())
	if err != nil || result.State != "closed" {
		t.Fatal(result, err)
	}
	if len(v.posts) != 2 {
		t.Fatal(v.posts)
	}
	for i, l := range v.posts[1].Legs {
		if l.Side == o.Order.Legs[i].Side || l.PositionIntent != l.Side+"_to_close" {
			t.Fatal(l)
		}
	}
	result, err = p.CloseMLeg(context.Background(), mlegPlan())
	if err != nil || result.State != "closed" || len(v.posts) != 2 {
		t.Fatal(result, err, len(v.posts))
	}
	plan := mlegPlan()
	plan.LegLimits[0] = "0.03"
	if _, err = p.CloseMLeg(context.Background(), plan); !errors.Is(err, ErrJournalConflict) {
		t.Fatal(err)
	}
}

func TestMLegSafeFallbackCanceledPartialAndRefusal(t *testing.T) {
	for _, failure := range []string{"rejected", "canceled", "expired", "pending", "api_refusal"} {
		t.Run(failure, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			testOpenMleg(t, p, mlegFixtureOpen())
			v.whole = failure
			if failure == "pending" {
				v.whole = "new"
				v.cancelFill = 1
			}
			v.refused = failure == "api_refusal"
			result, err := p.CloseMLeg(context.Background(), mlegPlan())
			if err != nil || result.State != "closed" {
				t.Fatal(result, err)
			}
			if len(v.posts) != 6 {
				t.Fatal(len(v.posts))
			}
			for i, r := range v.posts[2:] {
				if (i < 2) != (r.Side == "buy") {
					t.Fatal("unsafe fallback sequence", v.posts)
				}
				want := "2"
				if failure == "pending" {
					want = "1"
				}
				if r.Qty != want {
					t.Fatal(r.Qty, want)
				}
			}
			result, err = p.CloseMLeg(context.Background(), mlegPlan())
			if err != nil || result.State != "closed" || len(v.posts) != 6 {
				t.Fatal(result, err, len(v.posts))
			}
		})
	}
}

func TestMLegFallbackStopsBeforeProtectionSale(t *testing.T) {
	for _, status := range []string{"new", "partially_filled", "rejected", "canceled"} {
		t.Run(status, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			testOpenMleg(t, p, mlegFixtureOpen())
			v.whole = "rejected"
			v.short = status
			_, err := p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(err, ErrOptionPending) {
				t.Fatal(err)
			}
			if len(v.posts) != 3 || v.posts[2].Side != "buy" {
				t.Fatal(v.posts)
			}
			_, err = p.CloseMLeg(context.Background(), mlegPlan())
			if !errors.Is(err, ErrOptionPending) {
				t.Fatal(err)
			}
			if len(v.posts) != 3 {
				t.Fatal("retry generated new authority", v.posts)
			}
			if status == "partially_filled" || status == "new" {
				id := v.posts[2].ClientOrderID
				v.orders[id] = mlegResponse(v.requests[id], 2, "filled")
				v.short = "filled"
				result, err := p.CloseMLeg(context.Background(), mlegPlan())
				if err != nil || result.State != "closed" {
					t.Fatal(result, err)
				}
			}
		})
	}
}

func TestMLegWholeCancelMustBeTerminal(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	v.whole = "new"
	v.cancelPending = true
	_, err := p.CloseMLeg(context.Background(), mlegPlan())
	if !errors.Is(err, ErrOptionPending) || len(v.posts) != 2 {
		t.Fatal(err, v.posts)
	}
	v.cancelPending = false
	v.cancelFill = 1
	result, err := p.CloseMLeg(context.Background(), mlegPlan())
	if err != nil || result.State != "closed" {
		t.Fatal(result, err)
	}
}

func TestMLegUnknownRestartAndRolloverNeverPOST(t *testing.T) {
	v, p, root := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	v.lost = true
	if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	if _, err := p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	if _, err := p.journal.Rollover(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := p.journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	p.journal = j
	if _, err = p.OpenMLeg(context.Background(), o, nil); !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	if len(v.posts) != 1 {
		t.Fatal(v.posts)
	}
	v.lost = false
	if _, err = p.OpenMLeg(context.Background(), o, nil); err != nil {
		t.Fatal(err)
	}
	v.whole = "rejected"
	v.short = "partially_filled"
	if _, err = p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrOptionPending) {
		t.Fatal(err)
	}
	if _, err = j.Rollover(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	p.journal = j
	if _, err = p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrOptionPending) {
		t.Fatal(err)
	}
	if len(v.posts) != 3 {
		t.Fatal(v.posts)
	}
}

func TestMLegCustodyAndFundingRefusals(t *testing.T) {
	for _, reason := range []string{"inventory", "pending", "owned_child", "funding"} {
		t.Run(reason, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			testOpenMleg(t, p, mlegFixtureOpen())
			plan := mlegPlan()
			switch reason {
			case "inventory":
				v.wrongInventory = true
			case "pending":
				v.extraPending = true
			case "owned_child":
				v.wrongLeg = true
			case "funding":
				plan.LegLimits[1] = "100"
			}
			if _, err := p.CloseMLeg(context.Background(), plan); err == nil {
				t.Fatal("unsafe close accepted")
			}
			if len(v.posts) != 1 {
				t.Fatal(v.posts)
			}
		})
	}
}

func TestMLegAdmissionAndCollisionFences(t *testing.T) {
	v, p, root := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	if _, err := p.OpenMLeg(context.Background(), o, func() error {
		want := []string{"/v2/account", "/v2/account"}
		for _, leg := range o.Order.Legs {
			want = append(want, "/v2/options/contracts/"+leg.Symbol)
		}
		want = append(want, "/v2/orders", "/v2/positions")
		if !slices.Equal(v.gets, want) {
			t.Error("admission must follow all preparatory GETs", v.gets)
		}
		for _, name := range []string{optionLifecycle, optionIntentName(o.Order.ClientOrderID)} {
			if _, err := os.Stat(filepath.Join(root, name)); err != nil {
				t.Error("admission preceded durable intent", name, err)
			}
		}
		return errors.New("risk_fixture_refusal")
	}); err == nil {
		t.Fatal("admission bypassed")
	}
	if len(v.posts) != 0 {
		t.Fatal(v.posts)
	}
	if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); !errors.Is(err, ErrOptionRefused) {
		t.Fatal(err)
	}
	if len(v.posts) != 0 {
		t.Fatal(v.posts)
	}
	other := o
	other.Order.ClientOrderID = "different"
	if _, err := p.OpenMLeg(context.Background(), other, func() error { return nil }); !errors.Is(err, ErrJournalConflict) {
		t.Fatal(err)
	}
}

func TestMLegDecodeRejectsUnownedOrExcessFills(t *testing.T) {
	req := optionRequest{MLegOrder: mlegFixtureOpen().Order}
	for name, change := range map[string]func(*OptionPaperOrder){
		"duplicate": func(o *OptionPaperOrder) { o.Legs[1].ID = o.Legs[0].ID }, "side": func(o *OptionPaperOrder) { o.Legs[1].Side = "buy" }, "symbol": func(o *OptionPaperOrder) { o.Legs[1].Symbol = "SPY" }, "fraction": func(o *OptionPaperOrder) { o.Legs[0].FilledQty = "0.5" }, "overfill": func(o *OptionPaperOrder) { o.Legs[0].FilledQty = "3" }, "price": func(o *OptionPaperOrder) { o.Legs[0].FilledAvgPrice = nil }, "missing": func(o *OptionPaperOrder) { o.Legs = o.Legs[:3] },
	} {
		t.Run(name, func(t *testing.T) {
			o := mlegResponse(req, 2, "filled")
			change(&o)
			raw, _ := json.Marshal(o)
			if _, err := decodeOptionOrder(raw, req); err == nil {
				t.Fatal("bad custody accepted")
			}
		})
	}
	o := mlegResponse(req, 2, "filled")
	slices.Reverse(o.Legs)
	credit := "-0.02"
	o.FilledAvgPrice = &credit
	raw, _ := json.Marshal(o)
	if _, err := decodeOptionOrder(raw, req); err != nil {
		t.Fatal(err)
	}
}

func TestMLegAmbiguousWholeCloseNeverFallsBack(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	v.whole = "new"
	// A transport failure on cancel and every subsequent lookup remains unknown.
	base := p.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	p.http.Transport = paperMlegTransport{base: base}
	_, err := p.CloseMLeg(context.Background(), mlegPlan())
	if !errors.Is(err, ErrOptionPending) {
		t.Fatal(err)
	}
	if len(v.posts) != 2 {
		t.Fatal("ambiguous cancel authorized fallback", v.posts)
	}
}

type paperMlegTransport struct{ base http.RoundTripper }

func (t paperMlegTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodDelete {
		return nil, errors.New("fixture timeout")
	}
	return t.base.RoundTrip(r)
}

func TestMLegOpeningCrashBeforePOSTIsGETOnly(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	raw, _ := json.Marshal(o)
	if err := p.journal.writeOnce(optionLifecycle, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenMLeg(context.Background(), o, func() error { return nil }); !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	if len(v.posts) != 0 {
		t.Fatal(v.posts)
	}
}

func TestMLegOpeningCannotReuseAbortedStockJournal(t *testing.T) {
	for _, id := range []string{"condor-open", "another-aborted-order"} {
		t.Run(id, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			ctx := context.Background()
			if err := p.AbortUnsubmitted(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := p.OpenMLeg(ctx, mlegFixtureOpen(), func() error { return nil }); err == nil {
				t.Fatal("stock abort fence accepted as an unused option journal")
			}
			if len(v.posts) != 0 {
				t.Fatal("aborted journal authorized an option POST", v.posts)
			}
			if _, err := p.journal.read(optionLifecycle, MaxBytes); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("aborted journal retained a new lifecycle", err)
			}
		})
	}
}

func TestMLegLocalFundingMustBeDedicatedAndScheduled(t *testing.T) {
	o := mlegFixtureOpen()
	if _, err := o.LocalFunding("account-2"); err == nil {
		t.Fatal("unowned account accepted")
	}
	o.Funding.Config.OpeningCash = "1000000"
	if _, err := o.LocalFunding("account-1"); err == nil {
		t.Fatal("broker-like funding accepted")
	}
	for _, kind := range []string{"reserve", "fill", "deposit"} {
		t.Run(kind, func(t *testing.T) {
			o := mlegFixtureOpen()
			o.Funding.Events = append(o.Funding.Events, account.BookCommand{Kind: kind, ID: "unowned", Amount: "500"})
			o.Funding.Version++
			if _, err := o.LocalFunding("account-1"); err == nil {
				t.Fatal("unscheduled or shared funding accepted")
			}
		})
	}
}

func TestMLegUnknownCloseAndCodesNeverAuthorizeFallback(t *testing.T) {
	for _, mode := range []string{"post_5xx", "unrecognized_code"} {
		t.Run(mode, func(t *testing.T) {
			v, p, root := newFakeMlegVenue(t)
			testOpenMleg(t, p, mlegFixtureOpen())
			v.closeLookupMissing = true
			if mode == "post_5xx" {
				v.closeHTTPFailure = true
			} else {
				v.refused = true
				v.refusalCode = 42299999
			}
			if _, err := p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrSubmissionUnknown) {
				t.Fatal(err)
			}
			if err := p.journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			p.journal = j
			if _, err := p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrSubmissionUnknown) {
				t.Fatal(err)
			}
			if len(v.posts) != 2 {
				t.Fatal("unknown close allowed fallback or rePOST", v.posts)
			}
		})
	}
}
func TestMLegPermissionAndDeliverableBlockBeforeIntent(t *testing.T) {
	for _, mode := range []string{"permission", "deliverable"} {
		t.Run(mode, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			v.badPermission = mode == "permission"
			v.badDeliverable = mode == "deliverable"
			if _, err := p.OpenMLeg(context.Background(), mlegFixtureOpen(), func() error { return nil }); err == nil {
				t.Fatal("preflight accepted")
			}
			if len(v.posts) != 0 {
				t.Fatal(v.posts)
			}
			if _, err := p.journal.read(optionLifecycle, MaxBytes); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed preflight retained authority", err)
			}
		})
	}
}
func TestMLegGatePreventsConcurrentLifecycleMutation(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	reached, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := p.OpenMLeg(context.Background(), o, func() error { close(reached); <-release; return nil })
		done <- err
	}()
	<-reached
	if _, err := p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrBackpressure) {
		t.Error(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(v.posts) != 1 {
		t.Fatal(v.posts)
	}
}

func TestMLegCanceledWholeNonproportionalFillQuarantines(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	v.whole = "new"
	v.cancelLegFills = []int{0, 1, 1, 0}
	result, err := p.CloseMLeg(context.Background(), mlegPlan())
	if !errors.Is(err, ErrOptionQuarantined) || result.State == "closed" || len(v.posts) != 2 {
		t.Fatal("nonproportional close authorized fallback", result, err, v.posts)
	}
}
func TestMLegNonProportionalOpenerStopsBeforeClose(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	o := v.orders["condor-open"]
	o.Status = "canceled"
	o.Legs[0].FilledQty = "1"
	o.Legs[0].Status = "canceled"
	v.orders["condor-open"] = o
	if _, err := p.CloseMLeg(context.Background(), mlegPlan()); err == nil {
		t.Fatal("non-proportional custody accepted")
	}
	if len(v.posts) != 1 {
		t.Fatal(v.posts)
	}
}

func TestMLegIncompletePendingInventoryStopsBeforePOST(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	v.incompletePending = true
	if _, err := p.OpenMLeg(context.Background(), mlegFixtureOpen(), func() error { return nil }); err == nil {
		t.Fatal("incomplete pending order allowed opening")
	}
	if len(v.posts) != 0 {
		t.Fatal(v.posts)
	}
}

func TestMLegTerminalParentWithPendingChildrenCannotFallback(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	testOpenMleg(t, p, mlegFixtureOpen())
	whole := optionRequest{MLegOrder: mlegFixtureOpen().Order}
	whole.ClientOrderID = optionCloseID("condor-open")
	whole.LimitPrice = mlegPlan().LimitPrice
	for i := range whole.Legs {
		if whole.Legs[i].Side == "buy" {
			whole.Legs[i].Side = "sell"
		} else {
			whole.Legs[i].Side = "buy"
		}
		whole.Legs[i].PositionIntent = whole.Legs[i].Side + "_to_close"
	}
	raw, _ := json.Marshal(whole)
	if err := p.journal.writeOnce(optionIntentName(whole.ClientOrderID), raw); err != nil {
		t.Fatal(err)
	}
	o := mlegResponse(whole, 0, "canceled")
	o.Legs[0].Status = "pending_cancel"
	v.orders[whole.ClientOrderID] = o
	if _, err := p.CloseMLeg(context.Background(), mlegPlan()); !errors.Is(err, ErrOptionPending) {
		t.Fatal(err)
	}
	if len(v.posts) != 1 {
		t.Fatal("parent status alone allowed fallback", v.posts)
	}
}
