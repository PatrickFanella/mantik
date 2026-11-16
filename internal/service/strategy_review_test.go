package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

func reviewFixture(t *testing.T, pending bool) (IncubationRunner, IncubationRequest, account.ReviewInput) {
	t.Helper()
	return reviewFixtureHistory(t, pending, 20)
}

func reviewFixtureHistory(t *testing.T, pending bool, backtestN int, venuePrices ...string) (IncubationRunner, IncubationRequest, account.ReviewInput) {
	t.Helper()
	ctx := context.Background()
	r, req, _, broker := incubationFixture(t, false)
	out, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	order := out.Distribution.Orders[0]
	venuePrice := "100"
	if len(venuePrices) > 0 {
		venuePrice = venuePrices[0]
	}
	if pending {
		broker.fill(t, order.ID, "partial", "0.1", "100", "0", req.ExecuteAt.Add(time.Second), false)
	} else {
		total, _ := new(big.Rat).SetString(order.Quantity)
		for i := 0; i < 5; i++ {
			qty := "0.1"
			if i == 4 {
				qty = new(big.Rat).Sub(total, big.NewRat(4, 10)).FloatString(9)
			}
			broker.fill(t, order.ID, fmt.Sprint("fill-", i), qty, venuePrice, "0", req.ExecuteAt.Add(time.Duration(i+1)*time.Second), i == 4)
		}
	}
	at := req.ExecuteAt.Add(8 * time.Second)
	if _, e = r.Sync(ctx, at, req.Quotes); e != nil {
		t.Fatal(e)
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	d := account.ReviewDeclaration{ID: "oversight", SleeveID: "trend", StrategyID: out.Plan.Rebalance.Sleeves[0].StrategyID, GateID: req.Strategies[0].GateID, Policy: account.ReviewPolicy, Benchmark: "cash", DrawdownBudget: "0.05", BacktestSourceSHA256: strings.Repeat("a", 64), BacktestEnd: req.DecidedAt.Add(-24 * time.Hour)}
	if len(venuePrices) > 1 {
		d.DrawdownBudget = venuePrices[1]
	}
	for i := backtestN; i > 0; i-- {
		d.Backtest = append(d.Backtest, strategygate.ReferenceReturn{Date: d.BacktestEnd.AddDate(0, 0, -i).Format(time.DateOnly), Return: "0.001"})
	}
	if backtestN == 0 {
		d.BacktestUnavailable = "forward_comparison_unavailable"
	}
	_, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "review-declare-oversight", Kind: "review_declare", At: at, ReviewDeclaration: &d})
	if e != nil {
		t.Fatal(e)
	}
	in := reviewObservation(at.Add(time.Second), "100", time.Time{})
	if _, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "baseline", in); e != nil {
		t.Fatal(e)
	}
	return r, req, in
}

func reviewObservation(at time.Time, price string, previous time.Time) account.ReviewInput {
	in := account.ReviewInput{DeclarationID: "oversight", At: at, Mark: account.ReviewMark{Prices: map[string]string{"SPY": price}, SourceSHA256: strings.Repeat("d", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at}}
	if !previous.IsZero() {
		in.Benchmark = &account.ReviewBenchmark{Return: "0", SourceSHA256: strings.Repeat("e", 64), EvidenceClass: "prospective_market_unqualified", StartAt: previous, EndAt: at, ReceivedAt: at}
	}
	return in
}

func TestStrategyReviewActionsBindEvidenceAndNextRebalance(t *testing.T) {
	for _, tc := range []struct{ price, action, scale string }{{"93.5", "cut", "0.5"}, {"91.5", "demote", "0.25"}, {"88", "retire", "0"}} {
		t.Run(tc.action, func(t *testing.T) {
			ctx := context.Background()
			r, req, base := reviewFixture(t, false)
			before, _ := r.Store.CappedBook(ctx, r.BookID)
			in := reviewObservation(base.At.Add(24*time.Hour), tc.price, base.At)
			review, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "daily", in)
			if e != nil || review.Action != tc.action || review.Scale != tc.scale || review.JournalVersion != before.Version || len(review.Reasons) == 0 || len(review.JournalSHA256) != 64 || len(review.DeclarationSHA256) != 64 || review.QualificationCredit {
				t.Fatal(review, e)
			}
			// An exact restart retry returns the original event and cannot mutate input.
			again, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "daily", in)
			if e != nil || !reflect.DeepEqual(again, review) {
				t.Fatal(again, e)
			}
			changed := in
			changed.Mark = reviewObservation(in.At, "99", base.At).Mark
			if _, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "daily", changed); !errors.Is(e, account.ErrBookConflict) {
				t.Fatal("changed retry", e)
			}
			if _, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "different-id", in); !errors.Is(e, account.ErrBookConflict) {
				t.Fatal("duplicate day", e)
			}
			after, _ := r.Store.CappedBook(ctx, r.BookID)
			if !reflect.DeepEqual(after.Events[:len(before.Events)], before.Events) {
				t.Fatal("historical events changed")
			}
			forged := review
			forged.Action = "grow"
			forged.Input.At = in.At.Add(24 * time.Hour)
			if _, e = r.Store.ApplyCappedBook(ctx, r.BookID, after.Version, account.BookCommand{ID: "forged", Kind: "strategy_review", At: forged.Input.At, Review: &forged}); e == nil {
				t.Fatal("forged action accepted")
			}
			// New runner uses a later decision. Retirement does not invoke Decide.
			next := nextForwardCycle(req, "review-next", in.At.Add(time.Second))
			next.Limits["SPY"] = "99"
			r.now = func() time.Time { return next.ExecuteAt }
			out, e := r.Run(ctx, next)
			if e != nil {
				t.Fatal(e)
			}
			budget, _ := new(big.Rat).SetString(out.Distribution.Sleeves[0].Budget)
			max := big.NewRat(50, 1)
			if tc.action == "demote" {
				max = big.NewRat(25, 1)
			}
			if tc.action == "retire" {
				max = new(big.Rat)
			}
			if budget.Cmp(max) > 0 || len(out.Distribution.Orders) != 1 || !out.Distribution.Orders[0].ReduceOnly || out.Distribution.Orders[0].Side != "sell" {
				t.Fatal("review did not resize real order", out)
			}
			if tc.action == "retire" && !account.ReviewExitRequired(after, "trend", review.StrategyID, next.DecidedAt) {
				t.Fatal("retirement not retained")
			}
			roster, e := account.PortfolioRoster(after)
			if e != nil || roster["trend"].Stage != "incubation" {
				t.Fatal("review changed admission", roster, e)
			}
		})
	}
}

func TestStrategyReviewCompleteGrowthAndUnavailableEvidence(t *testing.T) {
	for _, mode := range []string{"complete", "missing_benchmark", "missing_marks", "short_backtest", "unavailable_backtest", "venue_gap", "benchmark_lag", "edge_decay", "negative_excess", "owner_pause", "owner_kill"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backtestN := 20
			if mode == "short_backtest" {
				backtestN = 1
			}
			if mode == "unavailable_backtest" {
				backtestN = 0
			}
			venuePrice := "100"
			if mode == "venue_gap" {
				venuePrice = "100.5"
			}
			r, _, base := reviewFixtureHistory(t, false, backtestN, venuePrice)
			var review account.StrategyReview
			previous := base.At
			for day := 1; day <= 20; day++ {
				price := 100 + float64(day)*0.2
				if mode == "negative_excess" {
					price = 100 - float64(day)*0.01
				}
				if mode == "edge_decay" && day > 10 {
					price = 102 - float64(day-10)*0.01
				}
				in := reviewObservation(base.At.Add(time.Duration(day)*24*time.Hour), fmt.Sprintf("%.2f", price), previous)
				if mode == "missing_benchmark" {
					in.Benchmark = nil
				}
				if mode == "missing_marks" {
					in.Mark.Prices = nil
				}
				if mode == "benchmark_lag" {
					in.Benchmark.Return = "0.003"
				}
				if day == 20 && (mode == "owner_pause" || mode == "owner_kill") {
					b, _ := r.Store.CappedBook(ctx, r.BookID)
					roster, _ := account.PortfolioRoster(b)
					action := "pause_strategy"
					if mode == "owner_kill" {
						action = "kill"
					}
					_, e := r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "prevent-growth", Kind: "risk_control", At: in.At, RiskControl: &risk.Control{Action: action, StrategyID: roster["trend"].StrategyID}})
					if e != nil {
						t.Fatal(e)
					}
				}
				var e error
				review, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, fmt.Sprint("day-", day), in)
				if e != nil {
					t.Fatal(e)
				}
				if day < 20 && review.Action == "grow" {
					t.Fatal("short forward sample grew risk", review)
				}
				previous = in.At
			}
			expected := "grow"
			switch mode {
			case "missing_benchmark", "missing_marks":
				expected = "investigate"
			case "negative_excess":
				expected = "demote"
			case "benchmark_lag":
				expected = "cut"
			case "short_backtest", "unavailable_backtest", "venue_gap":
				expected = "investigate"
			case "edge_decay":
				expected = "investigate"
			case "owner_pause", "owner_kill":
				expected = "hold"
			}
			if review.Action != expected || review.QualificationCredit || review.Samples > 20 {
				t.Fatal(mode, review)
			}
			if mode == "complete" && (review.Samples != 20 || review.Fills != 5 || review.Scale != "1.00" || review.EdgeDecay == "") {
				t.Fatal(review)
			}
		})
	}
}

func TestStrategyReviewPendingAndOwnerOverrides(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, control := range []string{"pause_strategy", "retire_strategy"} {
			for _, before := range []bool{false, true} {
				t.Run(fmt.Sprint(pending, control, before), func(t *testing.T) {
					ctx := context.Background()
					r, req, base := reviewFixture(t, pending)
					state, _ := r.Store.CappedBook(ctx, r.BookID)
					roster, _ := account.PortfolioRoster(state)
					sid := roster["trend"].StrategyID
					at := base.At.Add(24 * time.Hour)
					apply := func() {
						t.Helper()
						state, e := r.Store.CappedBook(ctx, r.BookID)
						if e != nil {
							t.Fatal(e)
						}
						_, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "owner", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: control, StrategyID: sid}})
						if e != nil {
							t.Fatal(e)
						}
					}
					if before {
						apply()
					}
					in := reviewObservation(at, "91.5", base.At)
					if _, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "daily", in); e != nil {
						t.Fatal(e)
					}
					if !before {
						apply()
					}
					if control == "retire_strategy" {
						state, _ := r.Store.CappedBook(ctx, r.BookID)
						if _, e := r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "resume", Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: "resume_strategy", StrategyID: sid}}); e != nil {
							t.Fatal(e)
						}
					}
					next := nextForwardCycle(req, "owner-next", at.Add(time.Second))
					next.Limits["SPY"] = "99"
					r.now = func() time.Time { return next.ExecuteAt }
					out, e := r.Run(ctx, next)
					if e != nil {
						t.Fatal(e)
					}
					if pending {
						if out.Distribution.Sleeves[0].Reason != "pending_orders" || len(out.Distribution.Orders) != 0 {
							t.Fatal(out)
						}
					} else if control == "pause_strategy" {
						if len(out.Distribution.Orders) != 0 {
							t.Fatal("pause lost", out)
						}
					} else {
						if len(out.Distribution.Orders) != 1 || !out.Distribution.Orders[0].ReduceOnly || out.Distribution.Sleeves[0].Budget != "0.000000000" {
							t.Fatal("retirement resumed", out)
						}
					}
				})
			}
		}
	}
}

func TestStrategyReviewAuthenticatedRoutesAndConcurrentOwner(t *testing.T) {
	ctx := context.Background()
	r, _, base := reviewFixture(t, false)
	at := base.At.Add(24 * time.Hour)
	clock := newFakeClock(at)
	h := HandlerWithClock(r.Store.(*storage.Memory), "owner-token", clock)
	in := reviewObservation(at, "104", base.At)
	raw, _ := json.Marshal(in)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"wrong", 401}, {"owner-token", 200}, {"owner-token", 200}} {
		req := httptest.NewRequest("POST", "/v1/capped-books/"+r.BookID+"/reviews/daily", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	unauth := HandlerWithClock(r.Store.(*storage.Memory), "", clock)
	req := httptest.NewRequest("POST", "/v1/capped-books/"+r.BookID+"/reviews/noauth", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	unauth.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	// Competing writes share the book's version fence. If control wins the race,
	// review retries from the owner state; if review wins, control remains final.
	state, _ := r.Store.CappedBook(ctx, r.BookID)
	roster, _ := account.PortfolioRoster(state)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "race-review", reviewObservation(at.Add(24*time.Hour), "105", at))
		errs <- e
	})
	wg.Go(func() {
		for i := 0; i < 4; i++ {
			b, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil {
				errs <- e
				return
			}
			_, e = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: "race-retire", Kind: "risk_control", At: at.Add(24 * time.Hour), RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: roster["trend"].StrategyID}})
			if errors.Is(e, account.ErrBookConflict) {
				continue
			}
			errs <- e
			return
		}
		errs <- account.ErrBookConflict
	})
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	state, _ = r.Store.CappedBook(ctx, r.BookID)
	snap, e := account.SnapshotCapped(state, map[string]string{"SPY": "105"}, at.Add(24*time.Hour))
	if e != nil || !snap.Risk.Retired[roster["trend"].StrategyID] {
		t.Fatal(snap, e)
	}
}

type reviewAfterPlanStore struct {
	IncubationStore
	after func(context.Context, account.CappedState) error
}

func (s *reviewAfterPlanStore) ApplyCappedBook(ctx context.Context, id string, v int64, c account.BookCommand) (account.CappedState, error) {
	out, e := s.IncubationStore.ApplyCappedBook(ctx, id, v, c)
	if e == nil && c.Kind == "incubation_plan" && s.after != nil {
		callback := s.after
		s.after = nil
		e = callback(ctx, out)
	}
	return out, e
}

func TestStrategyReviewBetweenPlanAndSizingNeverRewritesDecision(t *testing.T) {
	ctx := context.Background()
	r, req, base := reviewFixture(t, false)
	next := nextForwardCycle(req, "before-review", base.At.Add(24*time.Hour))
	r.now = func() time.Time { return next.ExecuteAt }
	original := r.Store
	r.Store = &reviewAfterPlanStore{IncubationStore: original, after: func(ctx context.Context, _ account.CappedState) error {
		in := reviewObservation(next.ExecuteAt, "93.5", base.At)
		review, e := (StrategyReviewService{original}).Review(ctx, r.BookID, "interleaved-review", in)
		if e == nil && review.Action != "cut" {
			return fmt.Errorf("expected cut: %v", review)
		}
		return e
	}}
	out, e := r.Run(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	budget, _ := new(big.Rat).SetString(out.Distribution.Sleeves[0].Budget)
	if budget.Cmp(big.NewRat(90, 1)) < 0 {
		t.Fatal("review rewrote an already retained decision", out)
	}
	state, _ := original.CappedBook(ctx, r.BookID)
	oldPlan := out.Plan
	oldDistribution := out.Distribution
	r.now = func() time.Time { return next.ExecuteAt.Add(time.Second) }
	retry, e := r.Run(ctx, next)
	if e != nil || !reflect.DeepEqual(oldPlan, retry.Plan) || !reflect.DeepEqual(oldDistribution, retry.Distribution) {
		t.Fatal("changed retry after review", retry, e)
	}
	after, _ := original.CappedBook(ctx, r.BookID)
	if state.Version != after.Version {
		t.Fatal("retry added history")
	}
}

func TestStrategyReviewSleeveBudgetsAndOwnerHTTP(t *testing.T) {
	for _, budget := range []string{"0.03", "0.1"} {
		t.Run(budget, func(t *testing.T) {
			ctx := context.Background()
			r, _, base := reviewFixtureHistory(t, false, 20, "100", budget)
			in := reviewObservation(base.At.Add(24*time.Hour), "95", base.At)
			out, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "budget-check", in)
			if e != nil {
				t.Fatal(e)
			}
			if budget == "0.03" && out.Action != "demote" || budget == "0.1" && out.Action != "hold" {
				t.Fatal("ignored sleeve budget", out)
			}
		})
	}
	ctx := context.Background()
	r, _, base := reviewFixture(t, true)
	state, _ := r.Store.CappedBook(ctx, r.BookID)
	roster, _ := account.PortfolioRoster(state)
	sid := roster["trend"].StrategyID
	clock := newFakeClock(base.At.Add(time.Second))
	h := HandlerWithClock(r.Store.(*storage.Memory), "owner-token", clock)
	post := func(path string, body any, token string) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	var declaration *account.ReviewDeclaration
	for _, event := range state.Events {
		if event.ReviewDeclaration != nil {
			declaration = event.ReviewDeclaration
		}
	}
	if code := post("/v1/capped-books/"+r.BookID+"/review-declaration", declaration, "owner-token"); code != 200 {
		t.Fatal("declaration retry", code)
	}
	modified := *declaration
	modified.BacktestSourceSHA256 = strings.Repeat("f", 64)
	if code := post("/v1/capped-books/"+r.BookID+"/review-declaration", modified, "owner-token"); code != 422 {
		t.Fatal("source substitution", code)
	}
	body := map[string]any{"Control": risk.Control{Action: "retire_strategy", StrategyID: sid}}
	path := "/v1/capped-books/" + r.BookID + "/owner-controls/retire-http"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"owner-token", 200}, {"owner-token", 200}} {
		if code := post(path, body, tc.token); code != tc.status {
			t.Fatal(code, tc.status)
		}
	}
	state, _ = r.Store.CappedBook(ctx, r.BookID)
	for _, event := range state.Events {
		if event.Kind == "portfolio_reserve" {
			e := account.CheckPortfolioSubmission(state, *event.Order, map[string]string{"SPY": "100"})
			if e == nil || !strings.Contains(e.Error(), "risk_strategy_retired") {
				t.Fatal("retained opening order escaped retirement", e)
			}
		}
	}
	stale := reviewObservation(clock.Now().Add(-2*time.Minute), "100", base.At)
	if code := post("/v1/capped-books/"+r.BookID+"/reviews/stale", stale, "owner-token"); code != 422 {
		t.Fatal("stale new review", code)
	}
}

func TestStrategyReviewGrowthRestoresOnlyAdmittedCapacity(t *testing.T) {
	ctx := context.Background()
	r, req, base := reviewFixture(t, false)
	cutAt := base.At.Add(24 * time.Hour)
	out, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, "initial-cut", reviewObservation(cutAt, "93.5", base.At))
	if e != nil || out.Action != "cut" {
		t.Fatal(out, e)
	}
	previous := cutAt
	for day := 1; day <= 20; day++ {
		in := reviewObservation(cutAt.Add(time.Duration(day)*24*time.Hour), fmt.Sprintf("%.2f", 93.5+float64(day)*0.5), previous)
		out, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, fmt.Sprint("recover-", day), in)
		if e != nil {
			t.Fatal(e)
		}
		previous = in.At
		if out.Action == "grow" {
			break
		}
	}
	if out.Action != "grow" || out.Scale != "0.75" || out.Drawdown != "0.000000000000" {
		t.Fatal("recovery did not restore bounded capacity", out)
	}
	next := nextForwardCycle(req, "after-growth", previous.Add(time.Second))
	state, _ := r.Store.CappedBook(ctx, r.BookID)
	due, e := state.Config.DueDeposits(next.ExecuteAt)
	if e != nil {
		t.Fatal(e)
	}
	next.Limits["SPY"] = "99"
	if len(due) >= 2 {
		next.Limits["SPY"] = "101"
	}
	r.now = func() time.Time { return next.ExecuteAt }
	healthyIncubationWorkers(t, r, next.ExecuteAt)
	result, e := r.Run(ctx, next)
	if e != nil {
		t.Fatal(result.Distribution, e)
	}
	budget, _ := new(big.Rat).SetString(result.Distribution.Sleeves[0].Budget)
	equity, _ := new(big.Rat).SetString(result.Distribution.Equity)
	cap := new(big.Rat).Mul(equity, big.NewRat(375, 10000))
	lower := new(big.Rat).Sub(cap, big.NewRat(2, 1))
	if budget.Cmp(lower) < 0 || budget.Cmp(cap) > 0 || len(result.Distribution.Orders) != 1 || result.Plan.Rebalance.Sleeves[0].Stage != "incubation" {
		t.Fatal("growth failed or escalated authority", result)
	}
}

func TestStrategyReviewMissingEvidenceCannotGrowFromDeposits(t *testing.T) {
	ctx := context.Background()
	r, req, base := reviewFixture(t, false)
	previous := base.At
	var review account.StrategyReview
	for day := 1; day <= 20; day++ {
		at := base.At.Add(time.Duration(day) * 24 * time.Hour)
		in := reviewObservation(at, "100", previous)
		in.Benchmark = nil
		var e error
		review, e = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, fmt.Sprint("no-comparison-", day), in)
		if e != nil {
			t.Fatal(e)
		}
		previous = at
	}
	if review.Action != "investigate" || review.BudgetCeiling != review.Budget {
		t.Fatal(review)
	}
	next := nextForwardCycle(req, "funded-but-unreviewed", previous.Add(time.Second))
	r.now = func() time.Time { return next.ExecuteAt }
	out, e := r.Run(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	actual, _ := new(big.Rat).SetString(out.Distribution.Sleeves[0].Budget)
	ceiling, _ := new(big.Rat).SetString(review.BudgetCeiling)
	if actual.Cmp(ceiling) > 0 || out.Distribution.Equity == "2000.000000000" || len(out.Distribution.Orders) > 0 {
		t.Fatal("missing comparison used deposits to grow risk", out)
	}
}
