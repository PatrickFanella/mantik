package service

import (
	"context"
	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type review83CheckedBroker struct {
	*incubationTestBroker
	before func()
}

func (b review83CheckedBroker) SubmitChecked(ctx context.Context, o alpaca.StockOrder, check func() error) (alpaca.PaperOrder, error) {
	b.before()
	if e := check(); e != nil {
		return alpaca.PaperOrder{}, e
	}
	return b.incubationTestBroker.Submit(ctx, o)
}
func TestStrategyReviewRegressionOwnerRetiresAtFinalAdapterBoundary(t *testing.T) {
	ctx := context.Background()
	r, req, _, broker := incubationFixture(t, false)
	sid, _ := req.Strategies[0].Strategy.Spec().ID()
	r.Broker = review83CheckedBroker{broker, func() {
		b, e := r.Store.CappedBook(ctx, r.BookID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "retire-before-post", Kind: "risk_control", At: req.ExecuteAt, RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: sid}})
		if e != nil {
			t.Fatal(e)
		}
	}}
	_, e := r.Run(ctx, req)
	t.Logf("Run err=%v broker POSTs after committed retirement=%d", e, broker.posts)
	if broker.posts > 0 {
		t.Errorf("opening POST escaped permanent owner retirement")
	}
}
func TestStrategyReviewRegressionCeilingAfterStalePlan(t *testing.T) {
	ctx := context.Background()
	r, req, base := reviewFixture(t, false)
	previous := base.At
	for day := 1; day <= 20; day++ {
		at := base.At.Add(time.Duration(day) * 24 * time.Hour)
		v, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, fmt.Sprint("growth-", day), reviewObservation(at, fmt.Sprintf("%.2f", 100+float64(day)*0.2), previous))
		if e != nil {
			t.Fatal(e)
		}
		if day == 20 && v.Action != "grow" {
			t.Fatal(v)
		}
		previous = at
	}
	next := nextForwardCycle(req, "frozen-before-hold", previous.Add(24*time.Hour+time.Second))
	r.now = func() time.Time { return next.ExecuteAt }
	healthyIncubationWorkers(t, r, next.ExecuteAt)
	original := r.Store
	originalSafety := r.Safety
	var frozen account.StrategyReview
	r.Store = &reviewAfterPlanStore{IncubationStore: original, after: func(ctx context.Context, _ account.CappedState) error {
		in := reviewObservation(next.ExecuteAt, "104", previous)
		in.Benchmark = nil
		var e error
		frozen, e = (StrategyReviewService{original}).Review(ctx, r.BookID, "freeze-dollar-budget", in)
		return e
	}}
	r.Safety = startIncubationTestSafety(t, r, next.DecidedAt)
	out, e := r.Run(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	in := reviewObservation(next.ExecuteAt.Add(24*time.Hour), "104", next.ExecuteAt)
	in.Benchmark = nil
	later, e := (StrategyReviewService{original}).Review(ctx, r.BookID, "still-no-comparison", in)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := new(big.Rat).SetString(frozen.BudgetCeiling)
	b, _ := new(big.Rat).SetString(later.BudgetCeiling)
	t.Logf("first %s ceiling=%s; pre-review plan budget=%s; later %s ceiling=%s", frozen.Action, frozen.BudgetCeiling, out.Distribution.Sleeves[0].Budget, later.Action, later.BudgetCeiling)
	r.Store = original
	r.Safety = originalSafety
	fresh := nextForwardCycle(req, "after-missing-review", in.At.Add(time.Second))
	r.now = func() time.Time { return fresh.ExecuteAt }
	healthyIncubationWorkers(t, r, fresh.ExecuteAt)
	result, e := r.Run(ctx, fresh)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("next fresh rebalance after missing comparison: budget=%s", result.Distribution.Sleeves[0].Budget)
	budget, _ := new(big.Rat).SetString(result.Distribution.Sleeves[0].Budget)
	if b.Cmp(a) > 0 || budget.Cmp(a) > 0 {
		t.Errorf("hold/investigate increased absolute ceiling or actual budget without complete grow review")
	}
}

func TestStrategyReviewRegressionActualAdapterRetirementDuringPreparation(t *testing.T) {
	ctx := context.Background()
	r, req, _, _ := incubationFixture(t, false)
	sid, _ := req.Strategies[0].Strategy.Spec().ID()
	posts := 0
	retired := false
	var sent alpaca.StockOrder
	old := http.DefaultTransport
	http.DefaultTransport = incubationTransport(func(h *http.Request) (*http.Response, error) {
		raw := ""
		switch h.URL.Path {
		case "/v2/account/activities/FILL":
			raw = `[]`
		case "/v2/account":
			raw = `{"id":"paper-fixture","status":"ACTIVE"}`
		case "/v2/assets/SPY":
			b, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil {
				t.Fatal(e)
			}
			_, e = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "owner-retire-during-preparation", Kind: "risk_control", At: req.ExecuteAt, RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: sid}})
			if e != nil {
				t.Fatal(e)
			}
			retired = true
			raw = `{"symbol":"SPY","class":"us_equity","tradable":true}`
		case "/v2/orders":
			if h.Method != "POST" {
				t.Fatal(h.Method)
			}
			posts++
			if !retired {
				t.Fatal("retirement not committed before POST")
			}
			if e := json.NewDecoder(h.Body).Decode(&sent); e != nil {
				t.Fatal(e)
			}
			body, _ := json.Marshal(alpaca.PaperOrder{ID: "venue-fixture", ClientOrderID: sent.ClientOrderID, Symbol: sent.Symbol, Qty: sent.Qty, Side: sent.Side, Type: sent.Type, TimeInForce: sent.TimeInForce, LimitPrice: sent.LimitPrice, Status: "new", FilledQty: "0"})
			raw = string(body)
		default:
			t.Fatalf("unexpected intercepted request %s", h.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	journal, e := alpaca.OpenJournal(filepath.Join(t.TempDir(), "paper"))
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	p, e := alpaca.NewPaperTrader(alpaca.PaperCredentials{OAuthToken: "fixture-only"}, "paper-fixture", journal)
	if e != nil {
		t.Fatal(e)
	}
	r.Broker = p
	_, e = r.Run(ctx, req)
	t.Logf("actual PaperTrader + intercepted HTTP: Run err=%v, committed retirement=%v, POSTs=%d", e, retired, posts)
	if posts > 0 {
		t.Error("actual adapter POSTed opening risk after owner retirement")
	}
}
