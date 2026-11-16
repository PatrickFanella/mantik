package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func briefTestBook(t *testing.T) account.CappedState {
	t.Helper()
	b, e := account.NewCappedState(account.DefaultCappedConfig("brief", "brief-paper", account.Cash, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func briefTestMark(id string, at time.Time, prices map[string]string) account.BookCommand {
	return account.BookCommand{ID: id, At: at, Review: &account.StrategyReview{Input: account.ReviewInput{At: at, Mark: account.ReviewMark{Prices: prices, SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at}}}}
}
func briefTestFill(id, asset, qty, price string, at time.Time) account.BookCommand {
	return account.BookCommand{ID: id, Kind: "fill", At: at, Fill: &account.BookFill{Symbol: asset, Quantity: qty, Price: price, Fee: "0", Side: "buy", At: at}}
}

// This isolates exact economic arithmetic from the account command validator;
// full finalized command/HTTP fixtures are exercised separately below.
func TestDailyBriefTimeWeightedDeposits(t *testing.T) {
	b := briefTestBook(t)
	a := b.Config.OpenedAt
	flow1 := a.Add(time.Hour)
	flow2 := a.Add(2 * time.Hour)
	end := a.Add(3 * time.Hour)
	b.Events = []account.BookCommand{briefTestFill("buy", "OLD", "10", "100", a), {ID: "deposit-one", Kind: "deposit", Amount: "500", At: flow1, EffectiveAt: flow1}, briefTestMark("boundary-one", flow1, map[string]string{"OLD": "110"}), {ID: "deposit-two", Kind: "deposit", Amount: "500", At: flow2, EffectiveAt: flow2}, briefTestMark("boundary-two", flow2, map[string]string{"OLD": "120"}), briefTestMark("end", end, map[string]string{"OLD": "130"})}
	var out DailyBrief
	if e := briefFinancial(b, end, &out); e != nil {
		t.Fatal(e)
	}
	// 2100/2000 * 2700/2600 * 3300/3200 - 1 = .124489182692307692
	if out.Equity.Value != "3300.000000000" || out.Deposits.Value != "1000.000000000" || out.BookTWR.Value != "0.124459134615384615" {
		t.Fatal(out)
	}
	if reflect.DeepEqual(out.BookTWR, number("0.650000000000000000")) {
		t.Fatal("deposits counted as return")
	}
	for _, test := range []struct {
		name   string
		mutate func(*account.CappedState)
	}{
		{"missing boundary", func(b *account.CappedState) { b.Events[2].Review.Input.Mark.Prices = nil }},
		{"almost boundary", func(b *account.CappedState) { b.Events[2].Review.Input.Mark.ObservedAt = flow1.Add(-time.Nanosecond) }},
		{"missing final asset", func(b *account.CappedState) { b.Events[5].Review.Input.Mark.Prices = map[string]string{"SPY": "130"} }},
		{"stale final", func(b *account.CappedState) { b.Events[5].Review.Input.Mark.ObservedAt = end.Add(-2 * time.Minute) }},
		{"future final", func(b *account.CappedState) {
			b.Events[5].Review.Input.Mark.ObservedAt = end.Add(time.Second)
			b.Events[5].Review.Input.Mark.ReceivedAt = end.Add(time.Second)
		}},
		{"missing source", func(b *account.CappedState) { b.Events[5].Review.Input.Mark.SourceSHA256 = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(b)
			var changed account.CappedState
			if e := json.Unmarshal(raw, &changed); e != nil {
				t.Fatal(e)
			}
			test.mutate(&changed)
			var got DailyBrief
			if e := briefFinancial(changed, end, &got); e != nil {
				t.Fatal(e)
			}
			if got.BookTWR.Value != "" || got.BookTWR.Unavailable == "" {
				t.Fatal(got)
			}
		})
	}
}
func TestDailyBriefSharedAssetsAndDelayedFills(t *testing.T) {
	b := briefTestBook(t)
	a := b.Config.OpenedAt
	end := a.Add(time.Hour)
	// Two sleeves share one asset. The book marks aggregate quantity exactly once.
	b.Events = []account.BookCommand{briefTestFill("one", "OLD", "1", "100", a), briefTestFill("two", "OLD", "2", "100", a), briefTestMark("end", end, map[string]string{"OLD": "110"})}
	var got DailyBrief
	if e := briefFinancial(b, end, &got); e != nil {
		t.Fatal(e)
	}
	if got.Equity.Value != "2030.000000000" || got.BookTWR.Value != "0.015000000000000000" {
		t.Fatal(got)
	}
	// Receipt order cannot move a pre-deposit fill past its economic boundary.
	b.Events[0].At = end
	b.Events = append([]account.BookCommand{{ID: "dep", Kind: "deposit", At: end, EffectiveAt: a.Add(30 * time.Minute), Amount: "500"}}, b.Events...)
	if e := briefFinancial(b, end, &got); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(got.BookTWR.Unavailable, "flow_boundary") {
		t.Fatal(got)
	}
}
func TestDailyBriefAllCashAndBenchmarkEvidence(t *testing.T) {
	b := briefTestBook(t)
	end := b.Config.OpenedAt.Add(time.Hour)
	src := BriefSource{Book: b, AsOf: end}
	out, e := BuildDailyBrief(src, nil)
	if e != nil {
		t.Fatal(e)
	}
	if out.BookTWR.Value != "0.000000000000000000" || out.Equity.Value != "2000.000000000" || out.Deposits.Value != "0.000000000" || out.SPYTWR.Value != "" || out.HealthUnavailable == "" {
		t.Fatal(out)
	}
	benchmark := BriefBenchmark{Symbol: "SPY", Currency: "USD", Method: "total_return_index", EvidenceClass: "prospective_market_unqualified", StartAt: b.Config.OpenedAt, EndAt: end, ReceivedAt: end, StartIndex: "100", EndIndex: "102", SourceID: "provider-total-return", SourceSHA256: strings.Repeat("b", 64)}
	out, e = BuildDailyBrief(src, &benchmark)
	if e != nil || out.SPYTWR.Value != "0.020000000000000000" {
		t.Fatal(out, e)
	}
	for _, test := range []struct {
		name   string
		change func(*BriefBenchmark)
	}{{"price only", func(v *BriefBenchmark) { v.Method = "close_price" }}, {"missing source", func(v *BriefBenchmark) { v.SourceSHA256 = "" }}, {"wrong start", func(v *BriefBenchmark) { v.StartAt = v.StartAt.Add(time.Second) }}, {"late receipt", func(v *BriefBenchmark) { v.ReceivedAt = v.ReceivedAt.Add(time.Second) }}} {
		t.Run(test.name, func(t *testing.T) {
			v := benchmark
			test.change(&v)
			out, e := BuildDailyBrief(src, &v)
			if e != nil || out.SPYTWR.Value != "" || out.SPYTWR.Unavailable == "" {
				t.Fatal(out, e)
			}
		})
	}
	// A retained source can arrive after the valuation end without moving the
	// return interval, provided the service had it by its capture clock.
	later := benchmark
	later.ReceivedAt = end.Add(time.Minute)
	captured := src
	captured.CapturedAt = end.Add(2 * time.Minute)
	delayed, e := BuildDailyBrief(captured, &later)
	if e != nil || delayed.SPYTWR.Value != "0.020000000000000000" {
		t.Fatal(delayed, e)
	}
	lateSource := later
	lateSource.ReceivedAt = captured.CapturedAt.Add(time.Second)
	if got, e := BuildDailyBrief(captured, &lateSource); e != nil || got.SPYTWR.Value != "" {
		t.Fatal(got, e)
	}
	// A real scheduled recorded deposit still produces known zero all-cash return.
	due, _ := b.Config.DueDeposits(b.Config.OpenedAt.AddDate(0, 0, 5))
	if len(due) != 1 {
		t.Fatal(due)
	}
	b, e = account.ApplyCappedCommand(b, due[0])
	if e != nil {
		t.Fatal(e)
	}
	out, e = BuildDailyBrief(BriefSource{Book: b, AsOf: due[0].At}, nil)
	if e != nil || out.Equity.Value != "2500.000000000" || out.Deposits.Value != "500.000000000" || out.BookTWR.Value != "0.000000000000000000" {
		t.Fatal(out, e)
	}
}
func TestDailyBriefRetainedLifecycleCopyAndHealth(t *testing.T) {
	ctx := context.Background()
	r, req, _, _ := incubationFixture(t, false)
	result, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	b, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	b, e = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "halt-for-brief", Kind: "halt", At: req.ExecuteAt.Add(time.Second)})
	if e != nil {
		t.Fatal(e)
	}
	b, e = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "pause-for-brief", Kind: "risk_control", At: req.ExecuteAt.Add(2 * time.Second), RiskControl: &risk.Control{Action: "pause_strategy", StrategyID: result.Plan.Rebalance.Sleeves[0].StrategyID}})
	if e != nil {
		t.Fatal(e)
	}
	source := BriefSource{Book: b, AsOf: req.ExecuteAt.Add(3 * time.Second)}
	before, _ := json.Marshal(source)
	got, e := BuildDailyBrief(source, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !got.Halted || len(got.Strategies) != 1 || got.Strategies[0].State != "owner_paused" || len(got.Strategies[0].PendingOrders) == 0 || !strings.Contains(strings.Join(got.Refusals, " "), "risk_hard_halt") || got.Health == nil {
		t.Fatal(got)
	}
	if got.HealthUnavailable == "" {
		t.Fatal("retained health without bounded expiry presented as current", got.Health)
	}
	got.Health.Checks["new"] = got.Health.Checks["data_freshness"]
	got.Strategies[0].Reasons = append(got.Strategies[0].Reasons, "mutated")
	after, _ := json.Marshal(source)
	if !bytes.Equal(before, after) {
		t.Fatal("brief aliases source")
	}
	reread, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil || reread.Version != b.Version {
		t.Fatal("brief wrote a book command", e)
	}
	var text bytes.Buffer
	if e = RenderDailyBrief(&text, got); e != nil || !strings.Contains(text.String(), "unavailable") {
		t.Fatal(text.String(), e)
	}
}
func TestDailyBriefHTTPAuthAndConcurrentPrefix(t *testing.T) {
	store := storage.NewMemory()
	b := briefTestBook(t)
	if _, e := store.CreateCappedBook(context.Background(), b.Config); e != nil {
		t.Fatal(e)
	}
	end := b.Config.OpenedAt.Add(time.Hour)
	handler := HandlerWithClock(store, "brief-token", briefClock{end})
	call := func(auth string, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/capped-books/brief/daily-brief-source", strings.NewReader(payload))
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call("", "{}"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	unconfigured := httptest.NewRecorder()
	HandlerWithClock(store, "", briefClock{end}).ServeHTTP(unconfigured, httptest.NewRequest("POST", "/v1/capped-books/brief/daily-brief-source", strings.NewReader("{}")))
	if unconfigured.Code != 503 {
		t.Fatal(unconfigured.Code)
	}
	if w := call("Bearer brief-token", `{"unknown":true}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 12; i++ {
			for {
				state, e := store.CappedBook(context.Background(), "brief")
				if e != nil {
					t.Error(e)
					return
				}
				_, e = store.ApplyCappedBook(context.Background(), "brief", state.Version, account.BookCommand{ID: fmt.Sprint("control-", i), Kind: "risk_control", At: b.Config.OpenedAt.Add(time.Duration(i) * time.Second), RiskControl: &risk.Control{Action: "pause_strategy", StrategyID: "test"}})
				if e == nil {
					break
				}
				if e != account.ErrBookConflict {
					t.Error(e)
					return
				}
			}
		}
	})
	for i := 0; i < 12; i++ {
		w := call("Bearer brief-token", "{}")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var src BriefSource
		if e := json.Unmarshal(w.Body.Bytes(), &src); e != nil {
			t.Fatal(e)
		}
		if src.Book.Version != int64(len(src.Book.Events)) {
			t.Fatal(src)
		}
		if _, e := BuildDailyBrief(src, nil); e != nil {
			t.Fatal(e)
		}
	}
	wg.Wait()
	// All new listening HTTP fakes filter unrelated local probes.
	server := httptest.NewServer(loopbacktest.GoClientOnly(handler))
	defer server.Close()
	response, e := http.Get(server.URL + "/v1/capped-books/brief/daily-brief-source")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
}

type briefClock struct{ at time.Time }

func (c briefClock) Now() time.Time { return c.at }

func (c briefClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestDailyBriefPendingFamilyIsNotAZeroOrPermission(t *testing.T) {
	ctx := context.Background()
	r, req, _, _ := incubationFixture(t, false)
	store := r.Store.(*storage.Memory)
	result, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	gate, e := store.StrategyGate(ctx, req.Strategies[0].GateID)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.BeginStrategyTrialBatch(ctx, "brief-pending-family", gate.Declaration.Family); e != nil {
		t.Fatal(e)
	}
	before, e := store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	handler := HandlerWithClock(store, "owner", briefClock{req.ExecuteAt.Add(3 * time.Second)})
	w := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/capped-books/"+r.BookID+"/daily-brief-source", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer owner")
	handler.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var source BriefSource
	if e = json.Unmarshal(w.Body.Bytes(), &source); e != nil {
		t.Fatal(e)
	}
	sleeve := result.Plan.Rebalance.Sleeves[0].ID
	if !source.Families[sleeve].Pending || source.Families[sleeve].SearchTotal != nil {
		t.Fatal("pending family acquired a finite count", source.Families)
	}
	got, e := BuildDailyBrief(source, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got.Strategies[0].FamilyStatus != "pending_family" || !strings.Contains(strings.Join(got.Refusals, " "), "pending_family") || !strings.Contains(strings.Join(got.IdleCashReasons, " "), "pending_family") {
		t.Fatal(got)
	}
	after, e := store.CappedBook(ctx, r.BookID)
	if e != nil || after.Version != before.Version {
		t.Fatal("read appended events", e)
	}
	// The same separately clocked observation cannot claim historical readiness.
	source.AsOf = req.ExecuteAt
	historical, e := BuildDailyBrief(source, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(historical.Strategies[0].FamilyStatus, "unavailable:") {
		t.Fatal(historical)
	}
}

func TestDailyBriefLatestReviewActionDoesNotResizeHistory(t *testing.T) {
	ctx := context.Background()
	r, _, in := reviewFixture(t, true)
	b, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	source := BriefSource{Book: b, AsOf: in.At}
	got, e := BuildDailyBrief(source, nil)
	if e != nil {
		t.Fatal(e)
	}
	var retained *account.StrategyReview
	for _, event := range b.Events {
		if event.Review != nil {
			retained = event.Review
		}
	}
	if retained == nil || got.Strategies[0].Action != retained.Action || got.Strategies[0].BudgetCeiling.Value != retained.BudgetCeiling || got.Strategies[0].Scale.Value != retained.Scale {
		t.Fatal(got)
	}
	// Retained sizing is independently reported, including outstanding orders.
	if len(got.Strategies[0].PendingOrders) == 0 || got.Strategies[0].Budget.Value == "" {
		t.Fatal(got)
	}
	next, e := BuildDailyBrief(source, nil)
	if e != nil || !reflect.DeepEqual(got, next) {
		t.Fatal("unstable read", e)
	}
}
