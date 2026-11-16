package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	strategyspec "git.subcult.tv/subculture-collective/mantik/internal/strategy/spec"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// Compiled service fixture with an isolated in-memory venue; no provider calls.
func qualificationRunnerFixture(t *testing.T) (IncubationRunner, IncubationRequest, *incubationTestStrategy, *incubationTestBroker, forward.Declaration, forward.AssessmentPlan) {
	t.Helper()
	ctx := context.Background()
	r, req, s, b := incubationFixture(t, false)
	def := qualificationDefinition()
	compiled, e := strategyspec.Compile(def)
	if e != nil {
		t.Fatal(e)
	}
	old, e := r.Store.StrategyGate(ctx, req.Strategies[0].GateID)
	if e != nil {
		t.Fatal(e)
	}
	gd := old.Declaration
	gd.ID = "compiled-qualification"
	gd.StrategyID, _ = compiled.Spec().ID()
	gd.Spec, _ = json.Marshal(compiled.Spec())
	m := r.Store.(*storage.Memory)
	gr, e := m.DeclareStrategyGate(ctx, gd)
	if e != nil {
		t.Fatal(e)
	}
	decision := *old.Decision
	decision.ID = gd.ID
	decision.StrategyID = gd.StrategyID
	decision.DeclarationSHA256, _ = strategygate.Hash(gd)
	decision.At = gr.DeclaredAt
	if _, e = m.RecordStrategyGate(ctx, decision); e != nil {
		t.Fatal(e)
	}
	req.Strategies[0].GateID = gd.ID
	req.Strategies[0].Strategy = compiled
	if strings.HasPrefix(t.Name(), "TestPostgres") {
		pg := qualificationPostgres(t)
		pgGate, e := pg.DeclareStrategyGate(ctx, gd)
		if e != nil {
			t.Fatal(e)
		}
		decision.At = pgGate.DeclaredAt
		if _, e = pg.RecordStrategyGate(ctx, decision); e != nil {
			t.Fatal(e)
		}
		original, e := r.Store.CappedBook(ctx, r.BookID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pg.CreateCappedBook(ctx, original.Config); e != nil {
			t.Fatal(e)
		}
		r.Store = pg
		r.Safety = startIncubationTestSafety(t, r, req.ExecuteAt.Add(-time.Second))
	}

	bench := strategy.Spec{Name: "cash", Version: "1", Data: []string{strategy.KindDailyBars}}
	bid, _ := bench.ID()
	sid, _ := compiled.Spec().ID()
	d := forward.Declaration{ID: "qualified-runner", Family: "incubation-fixture", GateID: req.Strategies[0].GateID, BookID: r.BookID, SleeveID: req.Strategies[0].SleeveID, StrategyID: sid, Spec: compiled.Spec(), Benchmark: forward.Benchmark{StrategyID: bid, Spec: bench}, WindowStart: req.DecidedAt, WindowEnd: req.DecidedAt.Add(7 * 24 * time.Hour), OutcomeDeadline: req.DecidedAt.Add(8 * 24 * time.Hour), MaxHoldingSeconds: 60, EligibilityTTLSeconds: 7 * 86400, MinSessions: 6, MinIndependentDecisions: 6, MinimumNetExcess: "0.01", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	at := req.DecidedAt.Add(-time.Second / 2)
	if _, e := r.Store.(forward.Store).DeclareForward(ctx, d, func() time.Time { return at }); e != nil {
		t.Fatal(e)
	}
	fs := r.Store.(forward.AssessmentStore)
	p := forward.AssessmentPlan{ID: "runner-assessment", DeclarationID: d.ID, Policy: forward.AssessmentPolicy, TrainEnd: d.WindowStart, HoldoutStart: d.WindowStart.Add(2 * time.Minute), PurgeSeconds: 60, EmbargoSeconds: 60, AlphaBasisPoints: 500}
	if _, e := fs.DeclareForwardAssessment(ctx, p, func() time.Time { return at }); e != nil {
		t.Fatal(e)
	}
	req.Strategies[0].ForwardDeclarationID = d.ID
	return r, req, s, b, d, p
}
func collectQualificationRunnerEvidence(t *testing.T, r IncubationRunner, req IncubationRequest, b *incubationTestBroker, d forward.Declaration, p forward.AssessmentPlan, start time.Time) {
	t.Helper()
	ctx := context.Background()
	bid := d.Benchmark.StrategyID
	for day := range 6 {
		for leg := range 2 {
			next := nextForwardCycle(req, fmt.Sprintf("qual-%d-%d", day, leg), start.Add(time.Duration(day)*24*time.Hour+time.Duration(leg*15)*time.Second))
			point := forward.BenchmarkObservation{StrategyID: bid, Index: "100", EvidenceClass: "prospective_market_unqualified", ObservedAt: next.ExecuteAt, ReceivedAt: next.ExecuteAt}
			point.Raw, _ = json.Marshal(struct {
				StrategyID, Index, EvidenceClass string
				ObservedAt, ReceivedAt           time.Time
			}{point.StrategyID, point.Index, point.EvidenceClass, point.ObservedAt, point.ReceivedAt})
			point.SourceSHA256 = forward.SourceDigest(point.Raw)
			next.Qualification = &forward.Collection{PlanID: p.ID, Benchmark: point}
			price := "100"
			if leg == 1 {
				price = "110"
				q := next.Quotes["SPY"]
				q.Bid = "109.99"
				q.Ask = "110.01"
				next.Quotes["SPY"] = q
				next.Limits = map[string]string{"SPY": "109"}
			}
			next = compiledQualificationRequest(t, next, leg == 0)
			r.now = func() time.Time { return next.ExecuteAt }
			healthyIncubationWorkers(t, r, next.ExecuteAt)
			out, e := r.Run(ctx, next)
			if e != nil || len(out.Distribution.Orders) != 1 {
				t.Fatal("collection", day, leg, e, len(out.Distribution.Orders))
			}
			if out.Plan.Qualification == nil || len(out.Plan.Qualification.DecisionSources) != 1 || out.QualificationCredit {
				t.Fatal("missing compiled sources")
			}
			if day == 0 && leg == 1 {
				// Flat exits waive renewed entry admission, never the pinned fill costs.
				raw, e := json.Marshal(out.Plan)
				if e != nil {
					t.Fatal(e)
				}
				var forged account.IncubationPlan
				if e = json.Unmarshal(raw, &forged); e != nil {
					t.Fatal(e)
				}
				cost := forged.Costs[d.SleeveID]
				cost.SlippageBps = "0"
				forged.Costs[d.SleeveID] = cost
				state, e := r.Store.CappedBook(ctx, r.BookID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = r.Store.(storage.QualificationCollector).CollectQualification(ctx, r.BookID, state.Version, account.BookCommand{ID: next.ID, Kind: "qualification_plan", At: next.ExecuteAt, Incubation: &forged}); e == nil {
					t.Fatal("flat collection changed pinned gate costs")
				}
			}

			o := out.Distribution.Orders[0]
			b.fill(t, o.ID, fmt.Sprintf("qual-fill-%d-%d", day, leg), o.Quantity, price, "0", next.ExecuteAt.Add(time.Second), true)
			if _, e = r.Sync(ctx, next.ExecuteAt.Add(2*time.Second), next.Quotes); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestQualificationRunnerCollectsDistinctEvidence(t *testing.T) {
	ctx := context.Background()
	r, req, _, b, d, p := qualificationRunnerFixture(t)
	sid := d.StrategyID
	at := d.WindowStart.Add(-time.Second / 2)
	fs := r.Store.(forward.AssessmentStore)
	collectQualificationRunnerEvidence(t, r, req, b, d, p, p.HoldoutStart)
	// The start ledger is captured at assessment, not cached at declaration.
	raw, _ := json.Marshal(d.Spec)
	if _, e := r.Store.(strategytrial.Store).StartStrategyTrial(ctx, strategytrial.Start{ID: "actual-search", Family: d.Family, StrategyID: sid, Spec: raw, Config: json.RawMessage(`{"contract_fixture":true}`), DataSHA256: incubationDigest("trial-data"), EvidenceClass: "synthetic_fixture", StartedAt: at}); e != nil {
		t.Fatal(e)
	}
	result, e := fs.AssessForward(ctx, p.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil || result.Status != "passed" || result.Sessions != 6 {
		t.Fatal(result.Status, result.Reason, e)
	}
	retained, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil || retained.Version <= int64(len(retained.Events)) {
		t.Fatal("qualification did not exercise compacted journal history", e)
	}
	history := r.Store.(storage.CappedHistoryStore)
	page, e := history.CappedBookHistory(ctx, r.BookID, retained.Version-1, 1)
	if e != nil || len(page) != 1 || page[0].Command.Kind != "qualification" {
		t.Fatal("promotion missing from immutable custody", page, e)
	}
	rebuilt, e := storage.ReplayCappedBookHistory(ctx, history, retained.Config, retained.Version)
	if e != nil || !reflect.DeepEqual(retained, rebuilt) {
		t.Fatal("promotion checkpoint history did not reconstruct exactly", e)
	}

	next := nextForwardCycle(req, "promoted-real-cycle", d.OutcomeDeadline.Add(time.Second))
	next.Qualification = nil
	next = compiledQualificationRequest(t, next, true)
	r.now = func() time.Time { return next.ExecuteAt }
	healthyIncubationWorkers(t, r, next.ExecuteAt)
	if strings.Contains(t.Name(), "Fenced") {
		owned := r.Store.(qualificationTestStore)
		fenced := &qualificationFenceStore{qualificationTestStore: owned}
		fenced.before = func() {
			if strings.Contains(t.Name(), "Family") {
				_, e := owned.StartStrategyTrial(ctx, strategytrial.Start{ID: "late-family-start", Family: d.Family, StrategyID: sid, Spec: raw, Config: json.RawMessage(`{"contract_fixture":true}`), DataSHA256: incubationDigest("late-trial"), EvidenceClass: "synthetic_fixture", StartedAt: next.ExecuteAt})
				if e != nil {
					t.Fatal(e)
				}
			} else {
				current, e := owned.CappedBook(ctx, r.BookID)
				if e != nil {
					t.Fatal(e)
				}
				action := "pause_strategy"
				if strings.Contains(t.Name(), "Kill") {
					action = "kill"
				}
				if _, e = owned.ApplyCappedBook(ctx, r.BookID, current.Version, account.BookCommand{ID: "late-owner-control", Kind: "risk_control", At: next.ExecuteAt, RiskControl: &risk.Control{Action: action, StrategyID: sid}}); e != nil {
					t.Fatal(e)
				}
			}
		}
		r.Store = fenced
		r.Safety = startIncubationTestSafety(t, r, next.ExecuteAt.Add(-time.Second))
		_, e := r.Run(ctx, next)
		if e == nil || b.posts != 12 || !fenced.called {
			t.Fatal("qualified final storage fence missed current authority", e, b.posts, fenced.called)
		}
		return
	}
	out, e := r.Run(ctx, next)
	if e != nil || len(out.Distribution.Orders) != 1 || out.Plan.Rebalance.Sleeves[0].Stage != "qualified" {
		t.Fatal("promoted runner refused allocation", e)
	}
	budget, ok := new(big.Rat).SetString(out.Distribution.Sleeves[0].Budget)
	if !ok || budget.Cmp(big.NewRat(100, 1)) <= 0 {
		t.Fatal("qualified pool did not take effect", out.Distribution.Sleeves)
	}
	if b.posts != 13 {
		t.Fatal("post-window qualified order never reached checked adapter", b.posts)
	}
	// Fill the actual qualified order, then exercise ongoing review and owner controls.
	o := out.Distribution.Orders[0]
	b.fill(t, o.ID, "qualified-actual-fill", o.Quantity, "100", "0", next.ExecuteAt.Add(time.Second), true)
	if _, e = r.Sync(ctx, next.ExecuteAt.Add(2*time.Second), next.Quotes); e != nil {
		t.Fatal(e)
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	rd := account.ReviewDeclaration{ID: "qualified-overseer", SleeveID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Policy: account.ReviewPolicy, Benchmark: "cash", DrawdownBudget: "0.01", BacktestUnavailable: "forward_only_no_historical_credit"}
	reviewAt := next.ExecuteAt.Add(3 * time.Second)
	state, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "review-declare-" + rd.ID, Kind: "review_declare", At: reviewAt, ReviewDeclaration: &rd})
	if e != nil {
		t.Fatal(e)
	}
	review := func(id, price string, when time.Time) account.StrategyReview {
		t.Helper()
		v, e := (StrategyReviewService{r.Store}).Review(ctx, r.BookID, id, account.ReviewInput{DeclarationID: rd.ID, At: when, Mark: account.ReviewMark{Prices: map[string]string{"SPY": price}, SourceSHA256: strings.Repeat("f", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: when, ReceivedAt: when}})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	review("qualified-baseline", "100", reviewAt.Add(time.Second))
	demotedAt := reviewAt.Add(24 * time.Hour)
	demotion := review("qualified-demotion", "93.5", demotedAt)
	if demotion.Action != "demote" {
		t.Fatal("qualified live review did not demote", demotion)
	}
	state, e = r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	state, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: "owner-pause-qualified", Kind: "risk_control", At: demotedAt, RiskControl: &risk.Control{Action: "pause_strategy", StrategyID: d.StrategyID}})
	if e != nil {
		t.Fatal(e)
	}
	retry, e := fs.AssessForward(ctx, p.ID, nil)
	if e != nil || retry.SHA256 != result.SHA256 {
		t.Fatal("assessment retry", e)
	}
	state, e = r.Store.CappedBook(ctx, r.BookID)
	if e != nil || account.Qualification(state, d.SleeveID) != nil {
		t.Fatal("retry restored demoted grant", e)
	}
	snap, e := account.SnapshotCapped(state, map[string]string{"SPY": "93.5"}, demotedAt)
	if e != nil || !snap.Risk.Breakers[d.StrategyID] {
		t.Fatal("retry cleared owner pause", e)
	}
	exit := nextForwardCycle(req, "demoted-owner-exit", demotedAt.Add(time.Second))
	exit.Limits = map[string]string{"SPY": "93"}
	q := exit.Quotes["SPY"]
	q.Bid = "93.49"
	q.Ask = "93.51"
	exit.Quotes["SPY"] = q
	exit = compiledQualificationRequest(t, exit, true)
	r.now = func() time.Time { return exit.ExecuteAt }
	reduced, e := r.Run(ctx, exit)
	if e != nil || len(reduced.Distribution.Orders) != 1 || !reduced.Distribution.Orders[0].ReduceOnly {
		t.Fatal("demoted paused owner failed safe exit", e)
	}

	if pg, ok := r.Store.(*storage.Postgres); ok {
		// Join the worker (including its final health write) before closing its pool.
		// The registered cleanup uses the same idempotent stop.
		stopIncubationTestSafety(r.Safety)
		retained, e := pg.CappedBook(ctx, r.BookID)
		if e != nil {
			t.Fatal(e)
		}
		pg.Close()
		reopened, e := storage.OpenPostgresWithQualificationEvaluator(ctx, qualificationPostgresURL(t), EvaluateQualificationCollection)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		after, e := reopened.CappedBook(ctx, r.BookID)
		if e != nil || !reflect.DeepEqual(retained, after) {
			t.Fatal("restart changed compiled journal", e)
		}
		retry, e := reopened.AssessForward(ctx, p.ID, nil)
		if e != nil || retry.SHA256 != result.SHA256 {
			t.Fatal("restart changed compiled assessment", e)
		}
	}

}

func TestForwardAssessmentHTTPRejectsImportedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, token, auth, method, path, body string
		want                                  int
	}{
		{"unauthenticated", "configured", "", "POST", "/v1/forward-assessment-plans/missing/assess", "", 401},
		{"unconfigured", "", "Bearer configured", "POST", "/v1/forward-assessment-plans/missing/assess", "", 401},
		{"pass-boolean", "configured", "Bearer configured", "POST", "/v1/forward-assessment-plans/missing/assess", `{"passed":true}`, 422},
		{"imported-result", "configured", "Bearer configured", "POST", "/v1/forward-assessment-plans", `{"QualificationCredit":true}`, 422},
		{"query", "configured", "Bearer configured", "POST", "/v1/forward-assessment-plans/missing/assess?passed=true", "", 422},
		{"missing-plan", "configured", "Bearer configured", "POST", "/v1/forward-assessment-plans/missing/assess", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Handler(storage.NewMemory(), tc.token)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.auth)
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != tc.want {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
}

func TestQualificationRejectsMissingVenueFee(t *testing.T) {
	ctx := context.Background()
	r, req, _, b, d, p := qualificationRunnerFixture(t)
	next := nextForwardCycle(req, "missing-fee", p.HoldoutStart)
	point := forward.BenchmarkObservation{StrategyID: d.Benchmark.StrategyID, Index: "100", EvidenceClass: "prospective_market_unqualified", ObservedAt: next.ExecuteAt, ReceivedAt: next.ExecuteAt}
	point.Raw, _ = json.Marshal(struct {
		StrategyID, Index, EvidenceClass string
		ObservedAt, ReceivedAt           time.Time
	}{point.StrategyID, point.Index, point.EvidenceClass, point.ObservedAt, point.ReceivedAt})
	point.SourceSHA256 = forward.SourceDigest(point.Raw)
	next.Qualification = &forward.Collection{PlanID: p.ID, Benchmark: point}
	next = compiledQualificationRequest(t, next, true)
	r.now = func() time.Time { return next.ExecuteAt }
	healthyIncubationWorkers(t, r, next.ExecuteAt)
	out, e := r.Run(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	o := out.Distribution.Orders[0]
	b.fill(t, o.ID, "missing-fee-fill", o.Quantity, "100", "0", next.ExecuteAt.Add(time.Second), true)
	var raw map[string]any
	if e = json.Unmarshal(b.fills[0], &raw); e != nil {
		t.Fatal(e)
	}
	delete(raw, "commission")
	b.fills[0], e = json.Marshal(raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Sync(ctx, next.ExecuteAt.Add(2*time.Second), next.Quotes); e == nil || !strings.Contains(e.Error(), "no observed commission") {
		t.Fatal("missing fee became invented zero", e)
	}
}

func qualificationDefinition() strategyspec.Definition {
	return strategyspec.Definition{Name: "qualification-fixture", Score: &strategyspec.Expr{Op: "const", Value: 1}, Gross: 1, LongOnly: true, Universe: true}
}
func compiledQualificationRequest(t *testing.T, req IncubationRequest, member bool) IncubationRequest {
	t.Helper()
	def := qualificationDefinition()
	series, e := (incubationTestSource{}).AdjustedBars(context.Background(), "SPY", req.DecidedAt)
	if e != nil || len(series.Bars) == 0 {
		t.Fatal(e)
	}
	u := marketdata.UniverseReport{AsOf: req.DecidedAt, ReferenceObservedAt: req.DecidedAt.Add(-time.Hour)}
	if member {
		u.Members = []marketdata.UniverseMember{{AssetID: "SPY", LastSession: series.Bars[0].Date}}
	}
	in := IncubationInput{ID: req.ID, DecidedAt: req.DecidedAt, ExecuteAt: req.ExecuteAt, Qualification: req.Qualification, Quotes: req.Quotes, Limits: req.Limits, Fees: req.Fees, Calendar: req.Calendar, Strategies: []IncubationStrategyInput{{SleeveID: req.Strategies[0].SleeveID, GateID: req.Strategies[0].GateID, Risk: req.Strategies[0].Risk, ForwardDeclarationID: req.Strategies[0].ForwardDeclarationID, Expression: &def, Assets: []string{"SPY"}, Bars: map[string][]storage.DailyBar{"SPY": {series.Bars[0].Raw}}, Universe: &u}}}
	raw, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	out, e := DecodeIncubationInput(raw)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPostgresContractsQualificationRunnerCollectsDistinctEvidence(t *testing.T) {
	TestQualificationRunnerCollectsDistinctEvidence(t)
}

type review92UniverseSource struct {
	member         string
	referenceShift time.Duration
}

func (s review92UniverseSource) AdjustedBars(ctx context.Context, a string, at time.Time) (marketdata.Series, error) {
	return incubationTestSource{}.AdjustedBars(ctx, a, at)
}
func (s review92UniverseSource) StockUniverse(_ context.Context, at time.Time) (marketdata.UniverseReport, error) {
	return marketdata.UniverseReport{AsOf: at, ReferenceObservedAt: at.Add(-time.Hour + s.referenceShift), PossiblySurvivorBiased: true, Config: marketdata.DefaultUniverseConfig(), Members: []marketdata.UniverseMember{{AssetID: s.member, LastSession: at.Add(-24 * time.Hour).Format(time.DateOnly)}}}, nil
}
func TestReview92RetainUniverse(t *testing.T) {
	at := time.Date(2027, 1, 8, 16, 0, 0, 0, time.UTC)
	spec := strategy.Spec{Name: "universe-review", Version: "1", Data: []string{strategy.KindDailyBars, strategy.KindStockUniverse}}
	var saved [][]byte
	for _, member := range []string{"SPY", "QQQ"} {
		v, e := strategy.Build(context.Background(), spec, review92UniverseSource{member: member}, at, []string{"SPY", "QQQ"}, strategy.Book{})
		if e != nil {
			t.Fatal(e)
		}
		plan := account.IncubationPlan{Qualification: &forward.Collection{}, DecidedAt: at}
		if e = retainQualificationViews(&plan, []strategy.View{v}); e != nil {
			t.Fatal(e)
		}
		saved = append(saved, plan.Qualification.DecisionSources[0])
		t.Logf("consumed_universe=%v", v.Universe())
	}
	if string(saved[0]) == string(saved[1]) {
		t.Fatal("different consumed universes have byte-identical retained qualification sources")
	}
	v, e := strategy.Build(context.Background(), spec, review92UniverseSource{member: "SPY", referenceShift: -time.Hour}, at, []string{"SPY", "QQQ"}, strategy.Book{})
	if e != nil {
		t.Fatal(e)
	}
	plan := account.IncubationPlan{Qualification: &forward.Collection{}, DecidedAt: at}
	if e = retainQualificationViews(&plan, []strategy.View{v}); e != nil {
		t.Fatal(e)
	}
	if string(saved[0]) == string(plan.Qualification.DecisionSources[0]) {
		t.Fatal("original reference clock was not bound into evidence")
	}
	var retained struct {
		Universe       []string
		UniverseReport *marketdata.UniverseReport
	}
	if e = json.Unmarshal(saved[0], &retained); e != nil || len(retained.Universe) != 1 || retained.Universe[0] != "SPY" || retained.UniverseReport == nil || !retained.UniverseReport.ReferenceObservedAt.Equal(at.Add(-time.Hour)) || !retained.UniverseReport.PossiblySurvivorBiased || retained.UniverseReport.Config != marketdata.DefaultUniverseConfig() {
		t.Fatal("original source report not retained", e)
	}

}
