package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategyidentity"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type forwardBackend interface {
	forward.Store
	strategygate.Store
	strategytrial.Store
	CappedBookStore
}

func forwardFixture(t *testing.T, s forwardBackend) (forward.Declaration, time.Time) {
	t.Helper()
	ctx := context.Background()
	gateStorageContracts(t, s)
	g, e := s.StrategyGate(ctx, "retained-gate")
	if e != nil {
		t.Fatal(e)
	}
	var spec strategyidentity.Spec
	if e = json.Unmarshal(g.Declaration.Spec, &spec); e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(time.Minute)
	if _, e = s.CreateCappedBook(ctx, account.DefaultCappedConfig("forward-book", "forward-paper", account.Cash, at.Add(-time.Second))); e != nil {
		t.Fatal(e)
	}
	bench := strategyidentity.Spec{Name: "cash", Version: "1", Data: []string{"daily_bars_adjusted"}}
	bid, e := bench.ID()
	if e != nil {
		t.Fatal(e)
	}
	d := forward.Declaration{ID: "forward-one", Family: g.Declaration.Family, GateID: g.Declaration.ID, BookID: "forward-book", SleeveID: "model", Spec: spec, StrategyID: g.Declaration.StrategyID, Benchmark: forward.Benchmark{Spec: bench, StrategyID: bid}, WindowStart: at.Add(time.Hour), WindowEnd: at.Add(28 * 24 * time.Hour), OutcomeDeadline: at.Add(56 * 24 * time.Hour), MaxHoldingSeconds: 28 * 86400, EligibilityTTLSeconds: 28 * 86400, MinSessions: 20, MinIndependentDecisions: 10, MinimumNetExcess: "0.01", MaximumDrawdown: "0.15", MaxSearchTrials: 4}
	return d, at
}
func forwardTrial(t *testing.T, s forwardBackend, d forward.Declaration, id string) {
	t.Helper()
	raw, e := json.Marshal(d.Spec)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.StartStrategyTrial(context.Background(), strategytrial.Start{ID: id, Family: d.Family, StrategyID: d.StrategyID, Spec: raw, Config: json.RawMessage(`{"contract_fixture":true}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: time.Now().UTC()})
	if e != nil {
		t.Fatal(e)
	}
}
func forwardContracts(t *testing.T, s forwardBackend) {
	t.Helper()
	ctx := context.Background()
	d, at := forwardFixture(t, s)
	forwardTrial(t, s, d, "failed")
	forwardTrial(t, s, d, "unfinished")
	if e := s.FinishStrategyTrial(ctx, "failed", strategytrial.Outcome{Status: "failed", FinishedAt: at}); e != nil {
		t.Fatal(e)
	}
	r, e := s.DeclareForward(ctx, d, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	if r.SearchTotal != 2 || r.SearchStarts[0].ID != "failed" || r.SearchStarts[1].ID != "unfinished" || r.QualificationCredit {
		t.Fatal(r)
	}
	retry, e := s.DeclareForward(ctx, d, func() time.Time { panic("exact retry must not read clock") })
	if e != nil || !reflect.DeepEqual(r, retry) {
		t.Fatal("retry changed", retry, e)
	}
	for name, change := range map[string]func(*forward.Declaration){"benchmark": func(v *forward.Declaration) {
		v.Benchmark.Spec.Version = "2"
		v.Benchmark.StrategyID, _ = v.Benchmark.Spec.ID()
	}, "minimum": func(v *forward.Declaration) { v.MinSessions++ }, "search": func(v *forward.Declaration) { v.MaxSearchTrials++ }, "holding": func(v *forward.Declaration) { v.MaxHoldingSeconds-- }, "window": func(v *forward.Declaration) { v.WindowStart = v.WindowStart.Add(time.Minute) }} {
		t.Run(name, func(t *testing.T) {
			v := clone(d)
			change(&v)
			if _, e := s.DeclareForward(ctx, v, func() time.Time { return at }); !errors.Is(e, forward.ErrConflict) {
				t.Fatal("changed retry", e)
			}
		})
	}
	for name, delta := range map[string]time.Duration{"equal": 0, "after": time.Nanosecond} {
		v := clone(d)
		v.ID = "late-" + name
		if _, e := s.DeclareForward(ctx, v, func() time.Time { return d.WindowStart.Add(delta) }); !errors.Is(e, forward.ErrInvalid) {
			t.Fatal("late declaration", e)
		}
	}
	invalid := clone(d)
	invalid.ID = "wrong-source"
	invalid.Spec.Version = "other"
	invalid.StrategyID, _ = invalid.Spec.ID()
	if _, e = s.DeclareForward(ctx, invalid, func() time.Time { return at }); !errors.Is(e, forward.ErrConflict) {
		t.Fatal("wrong canonical source", e)
	}
	for _, when := range []time.Time{d.WindowStart, d.WindowStart.Add(21 * 24 * time.Hour)} {
		v, e := s.ForwardEligibility(ctx, d.ID, when)
		if e != nil || !v.EntryEligible || v.SearchTotal != 2 || v.QualificationCredit {
			t.Fatal("multiweek eligibility", v, e)
		}
	}
	forwardTrial(t, s, d, "third")
	forwardTrial(t, s, d, "fourth")
	v, e := s.ForwardEligibility(ctx, d.ID, d.WindowStart)
	if e != nil || !v.EntryEligible || v.SearchTotal != 4 {
		t.Fatal(v, e)
	}
	forwardTrial(t, s, d, "fifth")
	v, e = s.ForwardEligibility(ctx, d.ID, d.WindowStart)
	if e != nil || v.EntryEligible || v.OutcomeOpen || v.SearchTotal != 5 {
		t.Fatal("bound fence", v, e)
	}
	if _, e = s.DeclareForward(ctx, forward.Declaration{}, nil); !errors.Is(e, forward.ErrInvalid) {
		t.Fatal(e)
	}
	if got, e := s.ForwardDeclaration(ctx, d.ID); e != nil || !reflect.DeepEqual(got, r) {
		t.Fatal("new starts rewrote declaration", got, e)
	}
	if _, e = s.DeclareForward(ctx, d, nil); e != nil {
		t.Fatal("expired search retry", e)
	}
}
func TestMemoryForwardDeclarations(t *testing.T) { forwardContracts(t, NewMemory()) }
func TestPostgresContractsForwardDeclarations(t *testing.T) {
	p := timerPostgres(t, "forward_declarations")
	forwardContracts(t, p)
	ctx := context.Background()
	before, e := p.ForwardDeclaration(ctx, "forward-one")
	if e != nil {
		t.Fatal(e)
	}
	// Keep a legacy threshold record through the new migration and repeated restart.
	c, at := declarationFixture(t, p)
	legacy, e := p.DeclarePaperEvaluation(ctx, c, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e = p.pool.Exec(ctx, migrations.ForwardDeclarations); e != nil {
			t.Fatal(e)
		}
		if e = p.migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.ForwardDeclaration(ctx, "forward-one")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed", after, e)
	}
	replay, e := reopened.DeclarePaperEvaluation(ctx, c, func() time.Time { panic("legacy retry clock") })
	if e != nil || !reflect.DeepEqual(legacy.Record, replay.Record) {
		t.Fatal("legacy changed", e)
	}
	var maximum int
	if e = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); e != nil || maximum != 125 {
		t.Fatal(maximum, e)
	}
	for _, sql := range []string{"UPDATE mantik_forward_declarations SET family=family", "DELETE FROM mantik_forward_declarations", "TRUNCATE mantik_forward_declarations"} {
		if _, e = p.pool.Exec(ctx, sql); e == nil {
			t.Fatal("mutable", sql)
		}
	}
}
func forwardConcurrent(t *testing.T, s forwardBackend) {
	t.Helper()
	ctx := context.Background()
	d, at := forwardFixture(t, s)
	d.MaxSearchTrials = 8
	// All starts race with the snapshot; each unfinished start must still count.
	start := make(chan struct{})
	errs := make(chan error, 9)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			raw, e := json.Marshal(d.Spec)
			if e != nil {
				errs <- e
				return
			}
			_, e = s.StartStrategyTrial(ctx, strategytrial.Start{ID: fmt.Sprintf("race-%d", i), Family: d.Family, StrategyID: d.StrategyID, Spec: raw, Config: json.RawMessage(`{"race":true}`), DataSHA256: strings.Repeat("a", 64), EvidenceClass: "synthetic_fixture", StartedAt: at})
			errs <- e
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, e := s.DeclareForward(ctx, d, func() time.Time { return at })
		errs <- e
	}()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	r, e := s.ForwardDeclaration(ctx, d.ID)
	if e != nil || r.SearchTotal > 8 || int64(len(r.SearchStarts)) != r.SearchTotal {
		t.Fatal(r, e)
	}
	v, e := s.ForwardEligibility(ctx, d.ID, d.WindowStart)
	if e != nil || v.SearchTotal != 8 || !v.EntryEligible {
		t.Fatal(v, e)
	}
	forwardTrial(t, s, d, "over-bound")
	v, e = s.ForwardEligibility(ctx, d.ID, d.WindowStart)
	if e != nil || v.EntryEligible || v.SearchTotal != 9 {
		t.Fatal(v, e)
	}
}
func TestMemoryForwardConcurrentStarts(t *testing.T) { forwardConcurrent(t, NewMemory()) }
func TestPostgresContractsForwardConcurrentStarts(t *testing.T) {
	forwardConcurrent(t, timerPostgres(t, "forward_concurrent"))
}

func forwardBindingContracts(t *testing.T, s forwardBackend) {
	t.Helper()
	ctx := context.Background()
	d, at := forwardFixture(t, s)
	d.WindowStart = at.Add(time.Minute)
	d.WindowEnd = d.WindowStart.Add(14 * 24 * time.Hour)
	d.OutcomeDeadline = d.WindowEnd.Add(28 * 24 * time.Hour)
	record, e := s.DeclareForward(ctx, d, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	execution := d.WindowStart.Add(time.Second)
	state, e := s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	plan := account.IncubationPlan{ForwardDeclarations: map[string]string{d.SleeveID: d.ID}, InputSHA256: strings.Repeat("c", 64), DecidedAt: d.WindowStart, Costs: map[string]executioncost.Model{d.SleeveID: executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: d.SleeveID, StrategyID: d.StrategyID}}, Rebalance: allocation.RebalanceRequest{ID: "forward-bind-allocation", At: execution, Sleeves: []allocation.Sleeve{{ID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{KeepPositions: true}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", SourceSHA256: strings.Repeat("d", 64), EvidenceClass: "prospective_market_unqualified", OccurredAt: execution, ReceivedAt: execution}}}
	state, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: "bind-forward", Kind: "incubation_plan", At: execution, Incubation: &plan})
	if e != nil {
		t.Fatal(e)
	}
	// A direct distributor call inherits the retained binding even without an
	// Incubation payload. A nonzero target at the exact window end is refused.
	rebalance := plan.Rebalance
	rebalance.ID = "late-raw"
	rebalance.At = d.WindowEnd
	rebalance.Targets = []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}
	if _, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: rebalance.ID, Kind: "rebalance", At: rebalance.At, Rebalance: &rebalance, Marks: map[string]string{"SPY": "100"}}); e == nil || !strings.Contains(e.Error(), "forward clocks") {
		t.Fatal("raw write bypassed expiry", e)
	}
	// Explicit empty targets remain possible at/after the outcome deadline.
	rebalance.ID = "flat-after-tail"
	rebalance.At = d.OutcomeDeadline
	rebalance.Targets[0].Decision = allocation.TargetDecision{}
	if _, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: rebalance.ID, Kind: "rebalance", At: rebalance.At, Rebalance: &rebalance, Marks: map[string]string{"SPY": "100"}}); e != nil {
		t.Fatal("flat blocked", e)
	}
	if r, e := s.ForwardDeclaration(ctx, d.ID); e != nil || r.SHA256 != record.SHA256 {
		t.Fatal(r, e)
	}
	// A later plan contains only its requested roster. The old binding remains
	// in history and must still fence an omitted sleeve when it returns.
	gate, e := s.StrategyGate(ctx, d.GateID)
	if e != nil {
		t.Fatal(e)
	}
	nextGate := gate.Declaration
	nextGate.ID = "alternate-gate"
	spec := d.Spec
	spec.Version = "alternate"
	nextGate.StrategyID, e = spec.ID()
	if e != nil {
		t.Fatal(e)
	}
	nextGate.Spec, e = json.Marshal(spec)
	if e != nil {
		t.Fatal(e)
	}
	retainedGate, e := s.DeclareStrategyGate(ctx, nextGate)
	if e != nil {
		t.Fatal(e)
	}
	decision := *gate.Decision
	decision.ID = nextGate.ID
	decision.StrategyID = nextGate.StrategyID
	decision.At = retainedGate.DeclaredAt
	decision.DeclarationSHA256, e = strategygate.Hash(nextGate)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordStrategyGate(ctx, decision); e != nil {
		t.Fatal(e)
	}
	nextPlan := clone(plan)
	nextPlan.ForwardDeclarations = nil
	nextPlan.DecidedAt = d.OutcomeDeadline.Add(time.Minute)
	nextPlan.Rebalance.ID = "alternate-allocation"
	nextPlan.Rebalance.At = nextPlan.DecidedAt.Add(time.Second)
	nextPlan.Rebalance.Sleeves[0].ID = "alternate"
	nextPlan.Rebalance.Sleeves[0].StrategyID = nextGate.StrategyID
	nextPlan.Rebalance.Sleeves[0].GateID = nextGate.ID
	nextPlan.Rebalance.Targets = []allocation.SleeveTarget{{SleeveID: "alternate", Decision: allocation.TargetDecision{}}}
	nextPlan.Decisions = []account.IncubationDecision{{SleeveID: "alternate", StrategyID: nextGate.StrategyID}}
	nextPlan.Costs = map[string]executioncost.Model{"alternate": executioncost.Default()}
	quote := nextPlan.Quotes["SPY"]
	quote.OccurredAt = nextPlan.Rebalance.At
	quote.ReceivedAt = quote.OccurredAt
	nextPlan.Quotes["SPY"] = quote
	state, e = s.CappedBook(ctx, d.BookID)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []account.BookCommand{
		{ID: "alternate-plan", Kind: "incubation_plan", At: nextPlan.Rebalance.At, Incubation: &nextPlan},
		{ID: nextPlan.Rebalance.ID, Kind: "rebalance", At: nextPlan.Rebalance.At, Rebalance: &nextPlan.Rebalance, Marks: map[string]string{"SPY": "100"}},
	} {
		state, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, c)
		if e != nil {
			t.Fatal("inactive roster replacement", e)
		}
	}
	if account.ForwardBindings(state)[d.SleeveID] != d.ID {
		t.Fatal("omission detached historical binding")
	}
	returning := clone(plan.Rebalance)
	returning.ID = "return-expired-sleeve"
	returning.At = nextPlan.Rebalance.At.Add(time.Minute)
	returning.Targets[0].Decision = allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}
	if _, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: returning.ID, Kind: "rebalance", At: returning.At, Rebalance: &returning, Marks: map[string]string{"SPY": "100"}}); e == nil || !strings.Contains(e.Error(), "forward clocks") {
		t.Fatal("omitted sleeve returned without its binding", e)
	}
}
func TestMemoryForwardBinding(t *testing.T) { forwardBindingContracts(t, NewMemory()) }
func TestPostgresContractsForwardBinding(t *testing.T) {
	p := timerPostgres(t, "forward_binding")
	forwardBindingContracts(t, p)
	reopened, e := OpenPostgres(context.Background(), p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	state, e := reopened.CappedBook(context.Background(), "forward-book")
	if e != nil {
		t.Fatal(e)
	}
	if account.ForwardBindings(state)["model"] != "forward-one" {
		t.Fatal("restart lost omitted binding")
	}
}

func TestMemoryForwardMissingAndIneligibleSources(t *testing.T) {
	m := NewMemory()
	d, at := forwardFixture(t, m)
	ctx := context.Background()
	missing := d
	missing.GateID = "absent"
	if _, e := m.DeclareForward(ctx, missing, func() time.Time { return at }); !errors.Is(e, strategygate.ErrNotFound) {
		t.Fatal(e)
	}
	missing = d
	missing.BookID = "absent"
	if _, e := m.DeclareForward(ctx, missing, func() time.Time { return at }); !errors.Is(e, account.ErrBookNotFound) {
		t.Fatal(e)
	}
	g := m.strategyGates[d.GateID]
	g.Decision.EvidenceClass = "synthetic_fixture"
	m.strategyGates[d.GateID] = g
	if _, e := m.DeclareForward(ctx, d, func() time.Time { return at }); !errors.Is(e, forward.ErrConflict) {
		t.Fatal("synthetic gate used as forward source", e)
	}
}

func TestPostgresContractsForwardUpgradeFrom123(t *testing.T) {
	p := timerPostgres(t, "forward_upgrade")
	ctx := context.Background()
	// Seed real legacy declaration/assessment contracts under the pre-124 schema.
	if _, e := p.pool.Exec(ctx, "DROP TABLE mantik_forward_declarations; DELETE FROM mantik_schema WHERE version>=124"); e != nil {
		t.Fatal(e)
	}
	evaluationAssessmentContracts(t, p)
	before, e := p.PaperEvaluationDeclarations(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var oldAssessments []byte
	if e = p.pool.QueryRow(ctx, "SELECT coalesce(json_agg(t)::text,'[]') FROM (SELECT * FROM mantik_paper_evaluation_assessments ORDER BY id) t").Scan(&oldAssessments); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = p.migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.PaperEvaluationDeclarations(ctx)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("threshold declaration replay changed", e)
	}
	var newAssessments []byte
	if e = p.pool.QueryRow(ctx, "SELECT coalesce(json_agg(t)::text,'[]') FROM (SELECT * FROM mantik_paper_evaluation_assessments ORDER BY id) t").Scan(&newAssessments); e != nil || string(oldAssessments) != string(newAssessments) {
		t.Fatal("legacy assessment bytes changed", e)
	}
}

func TestForwardDeclarationSafetyBounds(t *testing.T) {
	d, _ := forwardFixture(t, NewMemory())
	for name, mutate := range map[string]func(*forward.Declaration){
		"strategy_identity":         func(v *forward.Declaration) { v.Spec.Version = "changed" },
		"benchmark_identity":        func(v *forward.Declaration) { v.Benchmark.Spec.Version = "changed" },
		"zero_minimum":              func(v *forward.Declaration) { v.MinIndependentDecisions = 0 },
		"unusable_sleeve":           func(v *forward.Declaration) { v.SleeveID = strings.Repeat("a", 65) },
		"zero_sessions":             func(v *forward.Declaration) { v.MinSessions = 0 },
		"zero_search_bound":         func(v *forward.Declaration) { v.MaxSearchTrials = 0 },
		"zero_holding":              func(v *forward.Declaration) { v.MaxHoldingSeconds = 0 },
		"holding_over_90_days":      func(v *forward.Declaration) { v.MaxHoldingSeconds = forward.MaxSeconds + 1 },
		"eligibility_over_90_days":  func(v *forward.Declaration) { v.EligibilityTTLSeconds = forward.MaxSeconds + 1 },
		"outcome_tail_too_short":    func(v *forward.Declaration) { v.OutcomeDeadline = v.WindowEnd.Add(14 * 24 * time.Hour) },
		"outcome_tail_over_90_days": func(v *forward.Declaration) { v.OutcomeDeadline = v.WindowEnd.Add(90*24*time.Hour + time.Nanosecond) },
		"fraction_is_not_decimal":   func(v *forward.Declaration) { v.MaximumDrawdown = "1/4" },
		"negative_drawdown":         func(v *forward.Declaration) { v.MaximumDrawdown = "-0.1" },
	} {
		t.Run(name, func(t *testing.T) {
			v := clone(d)
			mutate(&v)
			if v.Validate() == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
	// All three independent clocks can legitimately span four weeks.
	if e := d.Validate(); e != nil {
		t.Fatal(e)
	}
}
