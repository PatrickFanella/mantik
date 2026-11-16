package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type forwardAssessmentBackend interface {
	forwardBackend
	forward.AssessmentStore
	QualificationCollector
}

func benchmarkFixture(t *testing.T, d forward.Declaration, at time.Time) forward.BenchmarkObservation {
	t.Helper()
	raw, e := json.Marshal(struct {
		StrategyID, Index, EvidenceClass string
		ObservedAt, ReceivedAt           time.Time
	}{d.Benchmark.StrategyID, "100", "prospective_market_unqualified", at, at})
	if e != nil {
		t.Fatal(e)
	}
	return forward.BenchmarkObservation{StrategyID: d.Benchmark.StrategyID, Index: "100", EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at, Raw: raw, SourceSHA256: forward.SourceDigest(raw)}
}

// This is synthetic contract evidence. Source class strings exercise validation;
// they make no claim about real observations, paper qualification or profit.
func assessmentFixture(t *testing.T, s forwardAssessmentBackend, days int) (forward.Declaration, forward.AssessmentPlanRecord, account.CappedState) {
	t.Helper()
	ctx := context.Background()
	installAssessmentFixtureEvaluator(s)
	d, at := forwardFixture(t, s)
	d.MaxHoldingSeconds = 60
	d.MinSessions = days
	d.MinIndependentDecisions = days
	d.MinimumNetExcess = "0.01"
	w, e := s.DeclareForward(ctx, d, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	p := forward.AssessmentPlan{ID: "assessment-one", DeclarationID: d.ID, Policy: forward.AssessmentPolicy, TrainEnd: d.WindowStart.Add(time.Minute), HoldoutStart: d.WindowStart.Add(3 * time.Minute), PurgeSeconds: 60, EmbargoSeconds: 60, AlphaBasisPoints: 500}
	pr, e := s.DeclareForwardAssessment(ctx, p, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	if pr.DeclarationSHA256 != w.SHA256 {
		t.Fatal(pr)
	}
	retry, e := s.DeclareForwardAssessment(ctx, p, func() time.Time { panic("retry read clock") })
	if e != nil || !reflect.DeepEqual(retry, pr) {
		t.Fatal(retry, e)
	}
	bad := p
	bad.AlphaBasisPoints++
	if _, e = s.DeclareForwardAssessment(ctx, bad, nil); e == nil {
		t.Fatal("changed policy retry")
	}
	b, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	apply := func(c account.BookCommand) {
		t.Helper()
		var e error
		if c.Kind == "qualification_plan" {
			b, e = s.CollectQualification(ctx, d.BookID, b.Version, c)
		} else {
			b, e = s.ApplyCappedBook(ctx, d.BookID, b.Version, c)
		}
		if e != nil {
			t.Fatalf("%s: %v", c.ID, e)
		}
	}
	for day := range days {
		for leg := range 2 {
			when := p.HoldoutStart.Add(time.Duration(day)*24*time.Hour + time.Duration(leg*20)*time.Second)
			prefix := fmt.Sprintf("q%d-%d", day, leg)
			bid, ask, limit := "99.99", "100.01", "101"
			target := allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}
			if leg == 1 {
				bid, ask, limit = "109.99", "110.01", "109"
				target = allocation.TargetDecision{}
			}
			plan := account.IncubationPlan{Qualification: &forward.Collection{PlanID: p.ID, DecisionSources: []json.RawMessage{json.RawMessage(`{"contract_fixture":true}`)}, Benchmark: benchmarkFixture(t, d, when.Add(time.Second))}, ForwardDeclarations: map[string]string{d.SleeveID: d.ID}, InputSHA256: strings.Repeat("c", 64), DecidedAt: when, Costs: map[string]executioncost.Model{d.SleeveID: executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: d.SleeveID, StrategyID: d.StrategyID}}, Rebalance: allocation.RebalanceRequest{ID: prefix + "-allocation", At: when.Add(time.Second), Sleeves: []allocation.Sleeve{{ID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: target}}, Limits: map[string]string{"SPY": limit}, Fees: map[string]string{"SPY": "1"}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: bid, Ask: ask, BidQuantity: "100", AskQuantity: "100", SourceSHA256: workerplan.Hash(prefix), EvidenceClass: "prospective_market_unqualified", OccurredAt: when.Add(time.Second), ReceivedAt: when.Add(time.Second)}}}
			marks := map[string]string{"SPY": "100"}
			if leg == 1 {
				marks["SPY"] = "110"
			}
			apply(account.BookCommand{ID: prefix + "-plan", Kind: "qualification_plan", At: plan.Rebalance.At, Incubation: &plan})
			apply(account.BookCommand{ID: plan.Rebalance.ID, Kind: "rebalance", At: plan.Rebalance.At, Rebalance: &plan.Rebalance, Marks: marks})
			snap, e := account.SnapshotCapped(b, marks, plan.Rebalance.At)
			if e != nil || len(snap.Distribution.Orders) != 1 {
				t.Fatal(snap, e)
			}
			o := snap.Distribution.Orders[0]
			order := account.BookOrder{ID: o.ID, Symbol: o.Symbol, Quantity: o.Quantity, Limit: o.Limit, Side: o.Side, Fee: o.Fee, At: o.At, AllocationID: o.AllocationID, SleeveID: o.SleeveID, StrategyID: o.StrategyID, ReduceOnly: o.ReduceOnly}
			apply(account.BookCommand{ID: prefix + "-reserve", Kind: "portfolio_reserve", At: o.At, Order: &order, Marks: marks})
			apply(account.BookCommand{ID: prefix + "-bind", Kind: "bind", At: o.At, OrderID: o.ID, VenueOrderID: prefix + "-venue"})
			f := account.BookFill{ID: prefix + "-fill", OrderID: o.ID, VenueOrderID: prefix + "-venue", Symbol: o.Symbol, Quantity: o.Quantity, Price: marks["SPY"], Fee: "0.001", Side: o.Side, SourceSHA256: workerplan.Hash(prefix + "-raw-fill"), At: o.At.Add(time.Second)}
			model, e := account.ModelIncubationFill(b, f)
			if e != nil {
				t.Fatal(e)
			}
			apply(account.BookCommand{ID: f.ID, Kind: "fill", At: f.At, Fill: &f, ModelFill: model, Marks: marks})
		}
	}
	return d, pr, b
}
func assessmentContracts(t *testing.T, s forwardAssessmentBackend) {
	t.Helper()
	ctx := context.Background()
	d, p, b := assessmentFixture(t, s, 6)
	// Failed and unfinished starts are both actual searches. Neither the cap (4)
	// nor the backtest decision's old count (0) is used as the correction (2).
	forwardTrial(t, s, d, "failed")
	forwardTrial(t, s, d, "unfinished")
	if e := s.FinishStrategyTrial(ctx, "failed", strategytrial.Outcome{Status: "failed", FinishedAt: d.OutcomeDeadline}); e != nil {
		t.Fatal(e)
	}
	a, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil || a.Status != "passed" || !a.QualificationCredit || a.SearchTotal != 2 || a.PValue != "1/64" || a.AdjustedPValue != "1/32" || a.JournalVersion != b.Version {
		t.Fatal(a, e)
	}
	after, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	roster, e := account.PortfolioRoster(after)
	if e != nil || roster[d.SleeveID].Stage != "qualified" {
		t.Fatal(roster, e)
	}
	retry, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { panic("assessment retry read clock") })
	if e != nil || !reflect.DeepEqual(a, retry) {
		t.Fatal(retry, e)
	}
	forged := a
	forged.PlanID = "forged"
	if _, e = s.ApplyCappedBook(ctx, d.BookID, after.Version, account.BookCommand{ID: "forged", Kind: "qualification", At: d.OutcomeDeadline, Qualification: &forged}); e == nil {
		t.Fatal("imported qualification accepted")
	}
	req := allocation.RebalanceRequest{ID: "actually-qualified", At: d.OutcomeDeadline.Add(time.Second), Sleeves: []allocation.Sleeve{roster[d.SleeveID]}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
	after, e = s.ApplyCappedBook(ctx, d.BookID, after.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}})
	if e != nil {
		t.Fatal("real qualified allocation refused", e)
	}
	snap, e := account.SnapshotCapped(after, map[string]string{"SPY": "100"}, req.At)
	if e != nil || snap.Distribution.Sleeves[0].Budget == "0.000000000" {
		t.Fatal("promotion did not change allocation", snap, e)
	}
	forwardTrial(t, s, d, "later-start")
	req.ID = "family-changed"
	req.At = req.At.Add(time.Second)
	if _, e = s.ApplyCappedBook(ctx, d.BookID, after.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}}); e == nil {
		t.Fatal("stale promoted family admitted")
	}
}

func TestMemoryForwardAssessments(t *testing.T) { assessmentContracts(t, NewMemory()) }
func TestPostgresContractsForwardAssessments(t *testing.T) {
	p := timerPostgres(t, "forward_assessments")
	assessmentContracts(t, p)
	ctx := context.Background()
	before, e := p.ForwardAssessmentPlan(ctx, "assessment-one")
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e = p.pool.Exec(ctx, migrations.ForwardAssessmentPlans); e != nil {
			t.Fatal(e)
		}
	}
	for _, sql := range []string{"UPDATE mantik_forward_assessment_plans SET id=id", "DELETE FROM mantik_forward_assessment_plans", "TRUNCATE mantik_forward_assessment_plans"} {
		if _, e = p.pool.Exec(ctx, sql); e == nil {
			t.Fatal("mutable", sql)
		}
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	installAssessmentFixtureEvaluator(reopened)
	after, e := reopened.ForwardAssessmentPlan(ctx, "assessment-one")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed plan", e)
	}
	b, e := reopened.CappedBook(ctx, "forward-book")
	if e != nil {
		t.Fatal(e)
	}
	roster, e := account.PortfolioRoster(b)
	if e != nil || roster["model"].Stage != "qualified" {
		t.Fatal("restart lost promotion", e)
	}
}
func TestForwardAssessmentFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		edit           func(*account.CappedState, *forward.AssessmentPlanRecord, *forward.Record)
		pending        bool
		status, reason string
	}{
		{"pending", nil, true, "unavailable", "family_accounting_pending"},
		{"incubation", func(b *account.CappedState, _ *forward.AssessmentPlanRecord, _ *forward.Record) {
			b.Events[0].Kind = "incubation_plan"
			b.Events[0].Incubation.Qualification = nil
		}, false, "unavailable", "incubation_or_unclassified_fill_in_window_owner"},
		{"synthetic", func(b *account.CappedState, _ *forward.AssessmentPlanRecord, _ *forward.Record) {
			b.Events[0].Incubation.Quotes["SPY"] = account.IncubationQuote{EvidenceClass: "synthetic_fixture"}
		}, false, "unavailable", "synthetic_or_unclassified_quote"},
		{"missing-cost", func(b *account.CappedState, _ *forward.AssessmentPlanRecord, _ *forward.Record) {
			b.Events[4].ModelFill = nil
		}, false, "unavailable", "fill_cost_or_original_clock_unavailable"},
		{"sign", nil, false, "failed", "actual_family_corrected_sign_test_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemory()
			d, p, b := assessmentFixture(t, s, 5)
			w, _ := s.ForwardDeclaration(context.Background(), d.ID)
			if tc.edit != nil {
				tc.edit(&b, &p, &w)
			}
			starts := []strategytrial.Start{registryStart("failed"), registryStart("unfinished")}
			a := evaluateForward(p, w, b, starts, tc.pending, d.OutcomeDeadline)
			if a.Status != tc.status || a.Reason != tc.reason || a.QualificationCredit {
				t.Fatal(a)
			}
		})
	}
}

func qualificationDemotionContract(t *testing.T, s forwardAssessmentBackend) {
	t.Helper()
	ctx := context.Background()
	d, p, b := assessmentFixture(t, s, 6)
	forwardTrial(t, s, d, "one-search")
	a, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil || a.Status != "passed" {
		t.Fatal(a.Reason, e)
	}
	b, e = s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	apply := func(c account.BookCommand) {
		t.Helper()
		var e error
		if c.Kind == "qualification_plan" {
			b, e = s.CollectQualification(ctx, d.BookID, b.Version, c)
		} else {
			b, e = s.ApplyCappedBook(ctx, d.BookID, b.Version, c)
		}
		if e != nil {
			t.Fatalf("%s: %v", c.ID, e)
		}
	}
	roster, e := account.PortfolioRoster(b)
	if e != nil {
		t.Fatal(e)
	}
	owner := roster[d.SleeveID]
	at := d.OutcomeDeadline.Add(time.Second)
	plan := account.IncubationPlan{ForwardDeclarations: map[string]string{d.SleeveID: d.ID}, InputSHA256: strings.Repeat("a", 64), DecidedAt: at, Costs: map[string]executioncost.Model{d.SleeveID: executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: d.SleeveID, StrategyID: d.StrategyID}}, Rebalance: allocation.RebalanceRequest{ID: "qualified-allocation", At: at.Add(time.Second), Sleeves: []allocation.Sleeve{owner}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: strings.Repeat("d", 64), EvidenceClass: "prospective_market_unqualified", OccurredAt: at.Add(time.Second), ReceivedAt: at.Add(time.Second)}}}
	apply(account.BookCommand{ID: "qualified-plan", Kind: "incubation_plan", At: plan.Rebalance.At, Incubation: &plan})
	apply(account.BookCommand{ID: plan.Rebalance.ID, Kind: "rebalance", At: plan.Rebalance.At, Rebalance: &plan.Rebalance, Marks: map[string]string{"SPY": "100"}})
	snap, e := account.SnapshotCapped(b, map[string]string{"SPY": "100"}, plan.Rebalance.At)
	if e != nil {
		t.Fatal(e)
	}
	o := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: o.ID, Symbol: o.Symbol, Quantity: o.Quantity, Limit: o.Limit, Side: o.Side, Fee: o.Fee, At: o.At, AllocationID: o.AllocationID, SleeveID: o.SleeveID, StrategyID: o.StrategyID, ReduceOnly: o.ReduceOnly}
	apply(account.BookCommand{ID: "qualified-reserve", Kind: "portfolio_reserve", At: o.At, Order: &order, Marks: map[string]string{"SPY": "100"}})
	apply(account.BookCommand{ID: "qualified-bind", Kind: "bind", At: o.At, OrderID: o.ID, VenueOrderID: "qualified-venue"})
	declaration := account.ReviewDeclaration{ID: "qualified-overseer", SleeveID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Policy: account.ReviewPolicy, Benchmark: "cash", DrawdownBudget: "0.01", BacktestUnavailable: "forward_only_no_historical_credit"}
	apply(account.BookCommand{ID: "review-declare-" + declaration.ID, Kind: "review_declare", At: at.Add(2 * time.Second), ReviewDeclaration: &declaration})
	reviewAt := at.Add(3 * time.Second)
	review := func(id, price string, when time.Time) account.StrategyReview {
		t.Helper()
		v, e := account.BuildStrategyReview(b, account.ReviewInput{DeclarationID: declaration.ID, At: when, Mark: account.ReviewMark{Prices: map[string]string{"SPY": price}, SourceSHA256: strings.Repeat("c", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: when, ReceivedAt: when}})
		if e != nil {
			t.Fatal(e)
		}
		apply(account.BookCommand{ID: id, Kind: "strategy_review", At: when, Review: &v})
		return v
	}
	review("baseline", "100", reviewAt)
	f := account.BookFill{ID: "qualified-fill", OrderID: o.ID, VenueOrderID: "qualified-venue", Symbol: o.Symbol, Quantity: o.Quantity, Price: "100", Fee: "0.001", Side: o.Side, SourceSHA256: strings.Repeat("e", 64), At: reviewAt.Add(time.Second)}
	model, e := account.ModelIncubationFill(b, f)
	if e != nil {
		t.Fatal(e)
	}
	apply(account.BookCommand{ID: f.ID, Kind: "fill", At: f.At, Fill: &f, ModelFill: model, Marks: map[string]string{"SPY": "100"}})
	nextAt := reviewAt.Add(24 * time.Hour)
	demotion := review("demotion", "98.3", nextAt)
	if demotion.Action != "demote" || demotion.JournalSHA256 == "" || account.Qualification(b, d.SleeveID) != nil {
		t.Fatal("evidence backed demotion missing", demotion.Action, demotion.Reasons)
	}
	flat := plan.Rebalance
	flat.ID = "demoted-allocation"
	flat.At = nextAt.Add(time.Second)
	flat.Targets = []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{}}}
	flat.Limits = map[string]string{"SPY": "98"}
	apply(account.BookCommand{ID: flat.ID, Kind: "rebalance", At: flat.At, Rebalance: &flat, Marks: map[string]string{"SPY": "98.3"}})
	snap, e = account.SnapshotCapped(b, map[string]string{"SPY": "98.3"}, flat.At)
	if e != nil || len(snap.Distribution.Orders) != 1 || !snap.Distribution.Orders[0].ReduceOnly || snap.Distribution.Sleeves[0].Budget != "0.000000000" {
		t.Fatal("demotion did not affect real allocation", snap, e)
	}
	apply(account.BookCommand{ID: "owner-pause-qualified", Kind: "risk_control", At: flat.At.Add(time.Second), RiskControl: &risk.Control{Action: "pause_strategy", StrategyID: d.StrategyID}})
	if _, e = s.AssessForward(ctx, p.Plan.ID, nil); e != nil {
		t.Fatal(e)
	}
	current, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	snap, e = account.SnapshotCapped(current, map[string]string{"SPY": "98.3"}, flat.At.Add(time.Second))
	if e != nil || !snap.Risk.Breakers[d.StrategyID] || account.Qualification(current, d.SleeveID) != nil {
		t.Fatal("assessment replay cleared owner control/demotion", e)
	}
}
func TestMemoryQualificationDemotion(t *testing.T) { qualificationDemotionContract(t, NewMemory()) }
func TestPostgresContractsQualificationDemotion(t *testing.T) {
	qualificationDemotionContract(t, timerPostgres(t, "qualification_demotion"))
}

func TestPostgresContractsForwardAssessmentUpgradeFrom125(t *testing.T) {
	p := timerPostgres(t, "assessment_upgrade125")
	ctx := context.Background()
	d, at := forwardFixture(t, p)
	original, e := p.DeclareForward(ctx, d, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	evaluationAssessmentContracts(t, p)
	var before []byte
	if e = p.pool.QueryRow(ctx, "SELECT coalesce(json_agg(t)::text,'[]') FROM (SELECT * FROM mantik_paper_evaluation_assessments ORDER BY id) t").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e = p.pool.Exec(ctx, "DROP TABLE mantik_forward_assessment_plans, mantik_capped_book_heads, mantik_capped_book_history; DELETE FROM mantik_schema WHERE version>=126"); e != nil {
		t.Fatal(e)
	}
	var version int
	if e = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&version); e != nil || version != 125 {
		t.Fatal("upgrade did not start at schema125", version, e)
	}
	if e = p.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&version); e != nil || version != 127 {
		t.Fatal("combined journal/assessment upgrade incomplete", version, e)
	}
	current, e := p.ForwardDeclaration(ctx, d.ID)
	if e != nil || !reflect.DeepEqual(original, current) {
		t.Fatal("127 changed forward v1 bytes", e)
	}
	var after []byte
	if e = p.pool.QueryRow(ctx, "SELECT coalesce(json_agg(t)::text,'[]') FROM (SELECT * FROM mantik_paper_evaluation_assessments ORDER BY id) t").Scan(&after); e != nil || string(before) != string(after) {
		t.Fatal("127 changed legacy assessments", e)
	}
	if _, e = p.pool.Exec(ctx, migrations.ForwardAssessmentPlans); e != nil {
		t.Fatal(e)
	}
}

func TestMemoryForwardAssessmentFinalFailure(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	d, p, _ := assessmentFixture(t, s, 5)
	forwardTrial(t, s, d, "first")
	forwardTrial(t, s, d, "second")
	a, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil || a.Status != "failed" || a.Reason != "actual_family_corrected_sign_test_failed" || a.QualificationCredit {
		t.Fatal(a, e)
	}
	retry, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { panic("failed retry clock") })
	if e != nil || !reflect.DeepEqual(a, retry) {
		t.Fatal("failure retry changed", e)
	}
	b, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	roster, e := account.PortfolioRoster(b)
	if e != nil || roster[d.SleeveID].Stage == "qualified" {
		t.Fatal("failed trial promoted", e)
	}
}

func forwardPromotionRace(t *testing.T, s forwardAssessmentBackend) {
	t.Helper()
	ctx := context.Background()
	d, p, _ := assessmentFixture(t, s, 6)
	forwardTrial(t, s, d, "before")
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-ready
		raw, e := json.Marshal(d.Spec)
		if e == nil {
			_, e = s.StartStrategyTrial(ctx, strategytrial.Start{ID: "racing-start", Family: d.Family, StrategyID: d.StrategyID, Spec: raw, Config: json.RawMessage(`{"contract_fixture":true}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: d.OutcomeDeadline})
		}
		done <- e
	}()
	close(ready)
	a, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if a.Status != "passed" || a.SearchTotal < 1 || a.SearchTotal > 2 {
		t.Fatal(a.Status, a.Reason, a.SearchTotal)
	}
	b, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	roster, e := account.PortfolioRoster(b)
	if e != nil {
		t.Fatal(e)
	}
	req := allocation.RebalanceRequest{ID: "race-admission", At: d.OutcomeDeadline.Add(time.Second), Sleeves: []allocation.Sleeve{roster[d.SleeveID]}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
	_, e = s.ApplyCappedBook(ctx, d.BookID, b.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}})
	if (e == nil) != (a.SearchTotal == 2) {
		t.Fatal("promotion/start race admitted stale family", a.SearchTotal, e)
	}
}
func TestMemoryForwardPromotionRace(t *testing.T) { forwardPromotionRace(t, NewMemory()) }
func TestPostgresContractsForwardPromotionRace(t *testing.T) {
	forwardPromotionRace(t, timerPostgres(t, "promotion_race"))
}

func TestMemoryForwardAssessmentPendingOrder(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	d, p, b := assessmentFixture(t, s, 6)
	plan := clone(*b.Events[0].Incubation)
	plan.DecidedAt = d.WindowEnd.Add(-time.Hour)
	plan.Rebalance.At = plan.DecidedAt.Add(time.Second)
	plan.Rebalance.ID = "pending-window-allocation"
	plan.Qualification.SearchTotal = nil
	plan.Qualification.Benchmark = benchmarkFixture(t, d, plan.Rebalance.At)
	q := plan.Quotes["SPY"]
	q.OccurredAt = plan.Rebalance.At
	q.ReceivedAt = plan.Rebalance.At
	plan.Quotes["SPY"] = q
	apply := func(c account.BookCommand) {
		t.Helper()
		var e error
		if c.Kind == "qualification_plan" {
			b, e = s.CollectQualification(ctx, d.BookID, b.Version, c)
		} else {
			b, e = s.ApplyCappedBook(ctx, d.BookID, b.Version, c)
		}
		if e != nil {
			t.Fatal(c.ID, e)
		}
	}
	apply(account.BookCommand{ID: "pending-window-plan", Kind: "qualification_plan", At: plan.Rebalance.At, Incubation: &plan})
	apply(account.BookCommand{ID: plan.Rebalance.ID, Kind: "rebalance", At: plan.Rebalance.At, Rebalance: &plan.Rebalance, Marks: map[string]string{"SPY": "100"}})
	snap, e := account.SnapshotCapped(b, map[string]string{"SPY": "100"}, plan.Rebalance.At)
	if e != nil {
		t.Fatal(e)
	}
	o := snap.Distribution.Orders[0]
	order := account.BookOrder{ID: o.ID, Symbol: o.Symbol, Quantity: o.Quantity, Limit: o.Limit, Side: o.Side, Fee: o.Fee, At: o.At, AllocationID: o.AllocationID, SleeveID: o.SleeveID, StrategyID: o.StrategyID, ReduceOnly: o.ReduceOnly}
	apply(account.BookCommand{ID: "pending-window-reserve", Kind: "portfolio_reserve", At: o.At, Order: &order, Marks: map[string]string{"SPY": "100"}})
	forwardTrial(t, s, d, "one-search")
	result, e := s.AssessForward(ctx, p.Plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e != nil || result.Status != "unavailable" || result.Reason != "pending_window_orders" || result.QualificationCredit {
		t.Fatal(result.Status, result.Reason, e)
	}
}

func TestForwardAssessmentObservesRecoveredDrawdown(t *testing.T) {
	s := NewMemory()
	d, p, b := assessmentFixture(t, s, 6)
	w, e := s.ForwardDeclaration(context.Background(), d.ID)
	if e != nil {
		t.Fatal(e)
	}
	q := b.Events[0].Incubation.Quotes["SPY"]
	q.Bid = "80"
	b.Events[0].Incubation.Quotes["SPY"] = q
	// This observed 20% loss recovers before close. Positive close returns must
	// not hide the predeclared drawdown breach, or create an independent day.
	result := evaluateForward(p, w, b, []strategytrial.Start{registryStart("search")}, false, d.OutcomeDeadline)
	if result.Status != "failed" || result.Reason != "declared_drawdown_exceeded" || result.QualificationCredit || result.Sessions != 6 {
		t.Fatal(result.Status, result.Reason, result.MaximumDrawdown)
	}
}

// Synthetic statistics/journal tests deliberately install their own evaluator.
// Production uses service.EvaluateQualificationCollection, tested separately.
func installAssessmentFixtureEvaluator(s forwardAssessmentBackend) {
	eval := func(_ context.Context, _ account.CappedState, c account.BookCommand, _ forward.Record, _ strategygate.Record, _ int64) error {
		if len(c.Incubation.Qualification.DecisionSources) != 1 || string(c.Incubation.Qualification.DecisionSources[0]) != `{"contract_fixture":true}` {
			return forward.ErrInvalid
		}
		return nil
	}
	switch v := s.(type) {
	case *Memory:
		v.qualificationEvaluator = eval
	case *Postgres:
		v.qualificationEvaluator = eval
	}
}

// Adapted from the independent review reproduction: rejection is now required
// at the ordinary write, before assessment or operational allocation exists.
func TestReview92RejectForgedCollection(t *testing.T) {
	var backend forwardAssessmentBackend = NewMemory()
	if strings.Contains(t.Name(), "Postgres") {
		backend = timerPostgres(t, "forged_collection")
	}
	d, _, b := assessmentFixture(t, backend, 6)
	c := clone(b.Events[0])
	c.ID = "forged-collection"
	c.Incubation.Qualification.DecisionSources = []json.RawMessage{json.RawMessage(`null`)}
	if _, e := backend.ApplyCappedBook(context.Background(), d.BookID, b.Version, c); e == nil {
		t.Fatal("ordinary caller manufactured collection authority")
	}
	if _, e := backend.CollectQualification(context.Background(), d.BookID, b.Version, c); e == nil {
		t.Fatal("null source passed installed evaluator")
	}
	after, e := backend.CappedBook(context.Background(), d.BookID)
	if e != nil || !reflect.DeepEqual(b, after) {
		t.Fatal("forgery changed journal", e)
	}
}
func TestReview92RejectForgedCollectionPostgres(t *testing.T) { TestReview92RejectForgedCollection(t) }

// Reproduce history that the old public writer could retain. This helper is
// test-only: it bypasses the corrected durable collection boundary, while still
// using the original account reducer and append-only PostgreSQL journal.
type legacyCollectionWriter struct{ forwardAssessmentBackend }

func (s legacyCollectionWriter) CollectQualification(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	c = clone(c)
	c.Incubation.Qualification.DecisionSources = []json.RawMessage{json.RawMessage(`null`)}
	return s.ApplyCappedBook(ctx, id, version, c)
}

// Keep the entire reproduction in the legacy journal. Mixing these writes with
// modern history would seal the legacy table after the first ordinary command.
func (s legacyCollectionWriter) ApplyCappedBook(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	b, e := s.CappedBook(ctx, id)
	if e != nil {
		return b, e
	}
	if b.Version != version {
		return b, account.ErrBookConflict
	}
	next, e := account.ApplyCappedCommand(clone(b), c)
	if e != nil {
		return b, e
	}
	if next.Version == b.Version {
		return b, nil
	}
	// The old journal retains every original observation rather than a modern
	// risk checkpoint. Preserve those exact legacy prefixes for source hashes.
	next = clone(b)
	next.Events = append(next.Events, clone(c))
	next.Version++
	if e = account.ValidateCappedState(next); e != nil {
		return b, e
	}
	switch v := s.forwardAssessmentBackend.(type) {
	case *Memory:
		v.mu.Lock()
		v.cappedBooks[id] = next
		v.mu.Unlock()
	case *Postgres:
		raw, e := json.Marshal(c)
		if e != nil {
			return b, e
		}
		if _, e = v.pool.Exec(ctx, "INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,$2,$3,$4)", id, next.Version, c.ID, string(raw)); e != nil {
			return b, e
		}
	}
	return next, nil
}
func TestForwardAssessmentRejectsLegacyForgedSources(t *testing.T) {
	var s forwardAssessmentBackend = NewMemory()
	if strings.Contains(t.Name(), "Postgres") {
		s = timerPostgres(t, "legacy_forged_sources")
	}
	installAssessmentFixtureEvaluator(s)
	d, p, b := assessmentFixture(t, legacyCollectionWriter{s}, 6)
	if b.Version != int64(len(b.Events)) {
		t.Fatal("legacy fixture contains compacted observations")
	}
	if pg, ok := s.(*Postgres); ok {
		var originals, modern int64
		var head bool
		if e := pg.pool.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM mantik_capped_book_events WHERE book_id=$1),
 (SELECT count(*) FROM mantik_capped_book_history WHERE book_id=$1),
 EXISTS(SELECT 1 FROM mantik_capped_book_heads WHERE book_id=$1)`, d.BookID).Scan(&originals, &modern, &head); e != nil || originals != b.Version || modern != 0 || head {
			t.Fatal("legacy reproduction mixed modern custody", originals, modern, head, e)
		}
	}
	forwardTrial(t, s, d, "one-search")
	if _, e := s.AssessForward(context.Background(), p.Plan.ID, func() time.Time { return d.OutcomeDeadline }); !errors.Is(e, forward.ErrInvalid) {
		t.Fatal("legacy null source was not rejected for invalid evidence", e)
	}
	after, e := s.CappedBook(context.Background(), d.BookID)
	if e != nil || !reflect.DeepEqual(after, b) {
		t.Fatal("rejected legacy assessment mutated book", e)
	}
	// Also reproduce a promotion already issued by the old evaluator. Its valid
	// hashes cannot authorize allocation through the corrected admission boundary.
	var w forward.Record
	var starts []strategytrial.Start
	switch v := s.(type) {
	case *Memory:
		w = v.forwardDeclarations[d.ID]
		starts = sortedStarts(v.strategyTrials, d.Family)
	case *Postgres:
		w, e = v.loadForward(context.Background(), v.pool, d.ID)
		if e != nil {
			t.Fatal(e)
		}
		starts, e = loadQualificationStarts(context.Background(), v.pool, d.Family)
		if e != nil {
			t.Fatal(e)
		}
	}
	a := evaluateForward(p, w, b, starts, false, d.OutcomeDeadline)
	if a.Status != "passed" {
		t.Fatal("legacy reproduction failed", a.Reason)
	}
	command := account.BookCommand{ID: "qualification-" + a.PlanID, Kind: "qualification", At: a.At, Qualification: &a}
	promoted, e := account.ApplyCappedCommand(b, command)
	if e != nil {
		t.Fatal(e)
	}
	switch v := s.(type) {
	case *Memory:
		v.cappedBooks[d.BookID] = promoted
	case *Postgres:
		raw, e := json.Marshal(command)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = v.pool.Exec(context.Background(), "INSERT INTO mantik_capped_book_events(book_id,sequence,event_id,payload) VALUES($1,$2,$3,$4)", d.BookID, promoted.Version, command.ID, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	roster, e := account.PortfolioRoster(promoted)
	if e != nil {
		t.Fatal(e)
	}
	req := allocation.RebalanceRequest{ID: "legacy-forged-allocation", At: d.OutcomeDeadline.Add(time.Second), Sleeves: []allocation.Sleeve{roster[d.SleeveID]}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
	if _, e = s.ApplyCappedBook(context.Background(), d.BookID, promoted.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}}); !errors.Is(e, forward.ErrInvalid) {
		t.Fatal("old null-source promotion was not rejected for invalid evidence", e)
	}
}

func TestPostgresContractsForwardAssessmentRejectsLegacyForgedSources(t *testing.T) {
	TestForwardAssessmentRejectsLegacyForgedSources(t)
}
