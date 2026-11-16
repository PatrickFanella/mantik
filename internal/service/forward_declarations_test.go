package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

func forwardRunnerFixture(t *testing.T) (IncubationRunner, IncubationRequest, *incubationTestStrategy, *incubationTestBroker, forward.Record) {
	t.Helper()
	r, req, s, b := incubationFixture(t, false)
	bench := strategy.Spec{Name: "cash", Version: "1", Data: []string{strategy.KindDailyBars}}
	bid, e := bench.ID()
	if e != nil {
		t.Fatal(e)
	}
	sid, e := s.Spec().ID()
	if e != nil {
		t.Fatal(e)
	}
	d := forward.Declaration{ID: "forward-runner", Family: "incubation-fixture", GateID: req.Strategies[0].GateID, BookID: r.BookID, SleeveID: req.Strategies[0].SleeveID, StrategyID: sid, Spec: s.Spec(), Benchmark: forward.Benchmark{StrategyID: bid, Spec: bench}, WindowStart: req.DecidedAt, WindowEnd: req.DecidedAt.Add(14 * 24 * time.Hour), OutcomeDeadline: req.DecidedAt.Add(42 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 5, MinIndependentDecisions: 5, MinimumNetExcess: "0", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	record, e := r.Store.(forward.Store).DeclareForward(context.Background(), d, func() time.Time { return req.DecidedAt.Add(-time.Second / 2) })
	if e != nil {
		t.Fatal(e)
	}
	req.Strategies[0].ForwardDeclarationID = d.ID
	return r, req, s, b, record
}

func nextForwardCycle(req IncubationRequest, id string, at time.Time) IncubationRequest {
	req.Calendar = incubationTestCalendar(at)
	req.ID = id
	req.InputSHA256 = incubationDigest(id)
	req.DecidedAt = at
	req.ExecuteAt = at.Add(time.Second)
	req.Quotes = map[string]account.IncubationQuote{}
	req.Quotes["SPY"] = account.IncubationQuote{Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: incubationDigest(id), EvidenceClass: "prospective_market_unqualified", OccurredAt: req.ExecuteAt, ReceivedAt: req.ExecuteAt}
	return req
}
func incubationDigest(id string) string { h, _ := IncubationInputHash(id); return h }

func TestForwardRunnerMultiweekHoldAndOutcomeTail(t *testing.T) {
	ctx := context.Background()
	r, req, s, b, record := forwardRunnerFixture(t)
	out, e := r.Run(ctx, req)
	if e != nil || len(out.Distribution.Orders) != 1 {
		t.Fatal(out, e)
	}
	order := out.Distribution.Orders[0]
	filled := req.ExecuteAt.Add(time.Second)
	b.fill(t, order.ID, "forward-entry", order.Quantity, "100", "0", filled, true)
	if _, e = r.Sync(ctx, filled.Add(time.Second), req.Quotes); e != nil {
		t.Fatal(e)
	}
	// Omitting the binding cannot detach the sleeve. During the entry window a
	// strategy can keep its position; later tail holds do not need a model call.
	req.Strategies[0].ForwardDeclarationID = ""
	s.decide = func(strategy.View) (strategy.Decision, error) { return strategy.Decision{KeepPositions: true}, nil }
	week := nextForwardCycle(req, "week-one", req.DecidedAt.Add(7*24*time.Hour))
	r.now = func() time.Time { return week.ExecuteAt }
	healthyIncubationWorkers(t, r, week.ExecuteAt)
	out, e = r.Run(ctx, week)
	if e != nil || s.calls != 2 {
		t.Fatal(out, e, s.calls)
	}
	// Legacy KeepPositions can resize after a deposit. Fill any such order, and
	// verify that it does not reset the original holding clock.
	for i, o := range out.Distribution.Orders {
		b.fill(t, o.ID, fmt.Sprintf("week-fill-%d", i), o.Quantity, "100", "0", week.ExecuteAt.Add(time.Second), true)
	}
	if _, e = r.Sync(ctx, week.ExecuteAt.Add(2*time.Second), week.Quotes); e != nil {
		t.Fatal(e)
	}
	s.decide = func(strategy.View) (strategy.Decision, error) {
		return strategy.Decision{}, errors.New("model unavailable")
	}
	tail := nextForwardCycle(req, "week-three", req.DecidedAt.Add(21*24*time.Hour))
	r.now = func() time.Time { return tail.ExecuteAt }
	out, e = r.Run(ctx, tail)
	if e != nil || len(out.Distribution.Orders) != 0 || s.calls != 2 || !out.Plan.Rebalance.Targets[0].Decision.HoldExposure || out.QualificationCredit {
		t.Fatal(out, e, s.calls)
	}
	eligibility, e := r.Store.(forward.Store).ForwardEligibility(ctx, record.Declaration.ID, tail.ExecuteAt)
	if e != nil || eligibility.EntryEligible || !eligibility.OutcomeOpen || eligibility.HoldExpired || eligibility.QualificationCredit {
		t.Fatal(eligibility, e)
	}
	// Execution crosses the exact holding expiry even though decision is just
	// before it. The bound flattens without calling the unavailable strategy.
	expiry := filled.Add(28 * 24 * time.Hour)
	closeReq := nextForwardCycle(req, "hold-expired", expiry.Add(-time.Second))
	closeReq.Limits = map[string]string{"SPY": "99"}
	r.now = func() time.Time { return closeReq.ExecuteAt }
	out, e = r.Run(ctx, closeReq)
	if e != nil || s.calls != 2 || len(out.Distribution.Orders) != 1 || !out.Distribution.Orders[0].ReduceOnly {
		t.Fatal(out, e, s.calls)
	}
	if out.Distribution.Orders[0].Side != "sell" || out.Plan.ForwardDeclarations["trend"] != record.Declaration.ID {
		t.Fatal(out)
	}
	// Restart reuses the plan and original pending order, without another POST.
	posts := b.posts
	r.now = func() time.Time { return expiry.Add(time.Hour) }
	if _, e = r.Run(ctx, closeReq); e != nil || b.posts != posts || s.calls != 2 {
		t.Fatal("restart", e, b.posts, s.calls)
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := account.ForwardHoldingOpened(state, "trend", closeReq.ExecuteAt)
	if e != nil || !opened.Equal(filled) {
		t.Fatal("oldest exposure clock reset", opened, e)
	}
}

func TestForwardRoutes(t *testing.T) {
	ctx := context.Background()
	r, req, _, _, record := forwardRunnerFixture(t)
	clock := referenceClock(req.DecidedAt.Add(-time.Second / 2))
	server := httptest.NewServer(loopbacktest.GoClientOnly(HandlerWithClock(r.Store.(*storage.Memory), "forward-token", clock)))
	defer server.Close()
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		request, e := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer forward-token")
		response, e := server.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		value, e := io.ReadAll(response.Body)
		if e != nil || response.StatusCode != want {
			t.Fatal(response.StatusCode, string(value), e)
		}
		return value
	}
	raw := call("POST", "/v1/forward-declarations", record.Declaration, 200)
	var got forward.Record
	if e := json.Unmarshal(raw, &got); e != nil || got.SHA256 != record.SHA256 {
		t.Fatal(got, e)
	}
	call("GET", "/v1/forward-declarations/forward-runner", nil, 200)
	call("GET", "/v1/forward-declarations/forward-runner/eligibility", nil, 200)
	changed := record.Declaration
	changed.MinSessions++
	call("POST", "/v1/forward-declarations", changed, 409)
	bad := map[string]any{"qualification_credit": true}
	call("POST", "/v1/forward-declarations", bad, 422)
	call("GET", "/v1/forward-declarations/absent", nil, 404)
	call("GET", "/v1/forward-declarations/forward-runner?at=2030", nil, 422)
}

func TestForwardEntryExpiresAtActualSubmissionClock(t *testing.T) {
	r, req, s, b, record := forwardRunnerFixture(t)
	req = nextForwardCycle(req, "last-window-entry", record.Declaration.WindowEnd.Add(-10*time.Second))
	healthyIncubationWorkers(t, r, req.ExecuteAt)
	calls := 0
	r.now = func() time.Time {
		calls++
		if calls == 1 {
			return req.ExecuteAt
		}
		return record.Declaration.WindowEnd
	}
	if _, e := r.Run(context.Background(), req); e == nil || !strings.Contains(e.Error(), "forward eligibility expired before submission") || b.posts != 0 || s.calls != 1 {
		t.Fatal("slow reservation extended window", e, b.posts, s.calls)
	}
}

func TestForwardLatePartialFillUsesOldestExecutionClock(t *testing.T) {
	ctx := context.Background()
	r, req, _, b, _ := forwardRunnerFixture(t)
	out, err := r.Run(ctx, req)
	if err != nil || len(out.Distribution.Orders) != 1 {
		t.Fatal(out, err)
	}
	order := out.Distribution.Orders[0]
	later := req.ExecuteAt.Add(20 * time.Second)
	earlier := req.ExecuteAt.Add(10 * time.Second)
	b.fill(t, order.ID, "seen-later-first", "0.01", "100", "0", later, false)
	if _, err = r.Sync(ctx, later.Add(time.Second), req.Quotes); err != nil {
		t.Fatal(err)
	}
	// A second activity poll exposes an earlier partial fill for the same order.
	b.fill(t, order.ID, "late-discovery-earlier", "0.01", "100", "0", earlier, false)
	at := req.ExecuteAt.Add(30 * time.Second)
	if _, err = r.Sync(ctx, at, req.Quotes); err != nil {
		t.Fatal(err)
	}
	state, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := account.ForwardHoldingOpened(state, req.Strategies[0].SleeveID, at)
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Equal(earlier) {
		t.Fatalf("holding clock extends by %s: got %s, earliest owned fill is %s", opened.Sub(earlier), opened.Format(time.RFC3339Nano), earlier.Format(time.RFC3339Nano))
	}
}

type alternateForwardStrategy struct{}

func (alternateForwardStrategy) Spec() strategy.Spec {
	return strategy.Spec{Name: "alternate-forward-fixture", Version: "1", Data: []string{strategy.KindDailyBars}}
}
func (alternateForwardStrategy) Decide(strategy.View) (strategy.Decision, error) {
	return strategy.Decision{}, nil
}

func alternateForwardCycle(t *testing.T, r IncubationRunner, req IncubationRequest) IncubationRequest {
	t.Helper()
	ctx := context.Background()
	m := r.Store.(*storage.Memory)
	old, e := m.StrategyGate(ctx, req.Strategies[0].GateID)
	if e != nil {
		t.Fatal(e)
	}
	alt := alternateForwardStrategy{}
	d := old.Declaration
	d.ID = "alternate-forward-gate"
	d.StrategyID, e = alt.Spec().ID()
	if e != nil {
		t.Fatal(e)
	}
	d.Spec, e = json.Marshal(alt.Spec())
	if e != nil {
		t.Fatal(e)
	}
	gate, e := m.DeclareStrategyGate(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	decision := *old.Decision
	decision.ID = d.ID
	decision.StrategyID = d.StrategyID
	decision.At = gate.DeclaredAt
	decision.DeclarationSHA256, e = IncubationInputHash(d)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.RecordStrategyGate(ctx, decision); e != nil {
		t.Fatal(e)
	}
	next := nextForwardCycle(req, "alternate-roster", req.DecidedAt.Add(time.Minute))
	next.Strategies = []IncubatingStrategy{{SleeveID: "alternate", GateID: d.ID, Risk: "0.02", Strategy: alt, Source: incubationTestSource{}, Assets: []string{"SPY"}}}
	return next
}

func TestForwardInactiveBindingLeavesRosterAndReturns(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			ctx := context.Background()
			r, req, s, _, record := forwardRunnerFixture(t)
			if !bound {
				req.Strategies[0].ForwardDeclarationID = ""
			}
			s.decide = func(strategy.View) (strategy.Decision, error) { return strategy.Decision{}, nil }
			if _, e := r.Run(ctx, req); e != nil {
				t.Fatal(e)
			}
			next := alternateForwardCycle(t, r, req)
			r.now = func() time.Time { return next.ExecuteAt }
			out, e := r.Run(ctx, next)
			if e != nil || len(out.Plan.ForwardDeclarations) != 0 || out.QualificationCredit {
				t.Fatal("inactive binding leaked into current roster", out, e)
			}
			// The lifetime binding survives omission. Returning the same sleeve after
			// expiry must inherit its declaration and skip the unavailable strategy.
			if bound {
				returning := nextForwardCycle(req, "return-old-sleeve", record.Declaration.WindowEnd.Add(time.Minute))
				returning.Strategies[0].ForwardDeclarationID = ""
				calls := s.calls
				s.decide = func(strategy.View) (strategy.Decision, error) {
					return strategy.Decision{}, errors.New("unavailable strategy")
				}
				r.now = func() time.Time { return returning.ExecuteAt }
				out, e = r.Run(ctx, returning)
				if e != nil || out.Plan.ForwardDeclarations[req.Strategies[0].SleeveID] != record.Declaration.ID || s.calls != calls {
					t.Fatal("omission detached historical binding", out, e, s.calls)
				}
				returning = nextForwardCycle(returning, "changed-binding", returning.ExecuteAt.Add(time.Minute))
				returning.Strategies[0].ForwardDeclarationID = "other-declaration"
				r.now = func() time.Time { return returning.ExecuteAt }
				if _, e = r.Run(ctx, returning); !errors.Is(e, forward.ErrConflict) {
					t.Fatal("historical binding replaced", e)
				}
			}
		})
	}
}

func TestForwardActiveSleeveCannotLeaveRoster(t *testing.T) {
	for _, filled := range []bool{false, true} {
		t.Run(fmt.Sprint(filled), func(t *testing.T) {
			ctx := context.Background()
			r, req, _, b, record := forwardRunnerFixture(t)
			initial, e := r.Run(ctx, req)
			if e != nil || len(initial.Distribution.Orders) != 1 {
				t.Fatal(initial, e)
			}
			if filled {
				order := initial.Distribution.Orders[0]
				at := req.ExecuteAt.Add(time.Second)
				b.fill(t, order.ID, "owned-active-fill", order.Quantity, "100", "0", at, true)
				if _, e = r.Sync(ctx, at.Add(time.Second), req.Quotes); e != nil {
					t.Fatal(e)
				}
			}
			next := alternateForwardCycle(t, r, req)
			r.now = func() time.Time { return next.ExecuteAt }
			posts := b.posts
			if _, e = r.Run(ctx, next); e == nil || !strings.Contains(e.Error(), "active_sleeve_omitted") || b.posts != posts {
				t.Fatal("active exposure omitted", e, b.posts, posts)
			}
			state, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil {
				t.Fatal(e)
			}
			if account.ForwardBindings(state)[req.Strategies[0].SleeveID] != record.Declaration.ID {
				t.Fatal("failed roster detached binding")
			}
		})
	}
}
