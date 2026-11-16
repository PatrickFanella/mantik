package service

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

func qualificationPostgres(t *testing.T) *storage.Postgres {
	t.Helper()
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("requires fresh owned PostgreSQL test container")
	}
	ctx := context.Background()
	u, e := url.Parse(base)
	if e != nil {
		t.Fatal(e)
	}
	db := u.Path[1:] + "_compiled_qualification"
	conn, e := pgx.Connect(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()); e != nil {
			t.Error(e)
		}
		if e := conn.Close(ctx); e != nil {
			t.Error(e)
		}
	})
	u.Path = "/" + db
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	p, e := storage.OpenPostgresWithQualificationEvaluator(ctx, u.String(), EvaluateQualificationCollection)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p
}

func TestQualificationCollectionRecomputesAtBoundary(t *testing.T) {
	ctx := context.Background()
	r, req, _, _, d, p := qualificationRunnerFixture(t)
	req = nextForwardCycle(req, "boundary-proof", p.HoldoutStart)
	req.Qualification = &forward.Collection{PlanID: p.ID, Benchmark: qualificationBenchmark(t, d, req.ExecuteAt)}
	req = compiledQualificationRequest(t, req, true)
	r.now = func() time.Time { return req.ExecuteAt }
	healthyIncubationWorkers(t, r, req.ExecuteAt)
	out, e := r.Run(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		t.Fatal(e)
	}
	c := account.BookCommand{ID: req.ID, Kind: "qualification_plan", At: req.ExecuteAt, Incubation: &out.Plan}
	collector := r.Store.(storage.QualificationCollector)
	for _, tc := range []struct {
		name string
		edit func(*account.BookCommand)
	}{
		{"null-source", func(c *account.BookCommand) {
			c.Incubation.Qualification.DecisionSources = []json.RawMessage{json.RawMessage(`null`)}
		}},
		{"forged-target", func(c *account.BookCommand) { c.Incubation.Rebalance.Targets[0].Decision = allocation.TargetDecision{} }},
		{"forged-state", func(c *account.BookCommand) { c.Incubation.Decisions[0].State = []byte("invented") }},
		{"forged-control", func(c *account.BookCommand) {
			c.Incubation.Qualification.DecisionSources = []json.RawMessage{json.RawMessage(`{"CodeControl":{"State":""}}`)}
			c.Incubation.Rebalance.Targets[0].Decision = allocation.TargetDecision{}
		}},
		{"caller-search-total", func(c *account.BookCommand) { n := int64(999); c.Incubation.Qualification.SearchTotal = &n }},
		{"wrong-kind", func(c *account.BookCommand) { c.Kind = "incubation_plan" }},
		{"future-source-self-consistent-hash", func(c *account.BookCommand) {
			var input IncubationInput
			if e := json.Unmarshal(c.Incubation.Qualification.Input, &input); e != nil {
				t.Fatal(e)
			}
			bars := input.Strategies[0].Bars["SPY"]
			bars[0].ObservedAt = input.DecidedAt.Add(time.Hour)
			input.Strategies[0].Bars["SPY"] = bars
			raw, e := json.Marshal(input)
			if e != nil {
				t.Fatal(e)
			}
			c.Incubation.Qualification.Input = raw
			c.Incubation.InputSHA256, e = IncubationInputHash(input)
			if e != nil {
				t.Fatal(e)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, e := json.Marshal(c)
			if e != nil {
				t.Fatal(e)
			}
			var forged account.BookCommand
			if e = json.Unmarshal(raw, &forged); e != nil {
				t.Fatal(e)
			}
			tc.edit(&forged)
			if _, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, forged); e == nil {
				t.Fatal("ordinary command gained collection authority")
			}
			rejected, err := collector.CollectQualification(ctx, r.BookID, state.Version, forged)
			if err == nil {
				t.Fatal("forgery passed compiled evaluator")
			}
			for i := range rejected.Events {
				rejected.Events[i].Kind = "caller-edited-error-history"
			}

			after, e := r.Store.CappedBook(ctx, r.BookID)
			if e != nil || !reflect.DeepEqual(state, after) {
				t.Fatal("rejected collection changed journal", e)
			}
		})
	}
	if _, e = collector.CollectQualification(ctx, r.BookID, state.Version, c); e != nil {
		t.Fatal("exact collection retry", e)
	}
	// A plain storage instance has no compiled collection authority.
	m := storage.NewMemory()
	if _, e = m.CollectQualification(ctx, r.BookID, 0, c); e == nil {
		t.Fatal("unconfigured store admitted collection")
	}
}
func TestPostgresContractsQualificationCollectionRecomputesAtBoundary(t *testing.T) {
	TestQualificationCollectionRecomputesAtBoundary(t)
}
func qualificationBenchmark(t *testing.T, d forward.Declaration, at time.Time) forward.BenchmarkObservation {
	t.Helper()
	point := forward.BenchmarkObservation{StrategyID: d.Benchmark.StrategyID, Index: "100", EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at}
	var e error
	point.Raw, e = json.Marshal(struct {
		StrategyID, Index, EvidenceClass string
		ObservedAt, ReceivedAt           time.Time
	}{point.StrategyID, point.Index, point.EvidenceClass, at, at})
	if e != nil {
		t.Fatal(e)
	}
	point.SourceSHA256 = forward.SourceDigest(point.Raw)
	return point
}

func TestQualificationReconciliationRecognizesCollectionButNotPromotion(t *testing.T) {
	rec := &incubationReconciliation{version: 0}
	state := account.CappedState{Version: 1, Events: []account.BookCommand{{Kind: "qualification_plan", Incubation: &account.IncubationPlan{Rebalance: allocation.RebalanceRequest{ID: "same"}}}}}
	if !reconciliationUnchanged(state, rec, "same") {
		t.Fatal("same allocation collection invalidated reconciliation")
	}
	if reconciliationUnchanged(state, rec, "other") {
		t.Fatal("foreign collection treated as harmless")
	}
	state.Events[0] = account.BookCommand{Kind: "qualification", Qualification: &forward.Assessment{Status: "passed"}}
	if reconciliationUnchanged(state, rec, "same") {
		t.Fatal("promotion changes roster and must invalidate reconciliation")
	}
}

type qualificationTestStore interface {
	IncubationStore
	forward.Store
	forward.AssessmentStore
	strategytrial.Store
	storage.QualificationCollector
}
type qualificationFenceStore struct {
	qualificationTestStore
	before func()
	called bool
}

func (s *qualificationFenceStore) CheckCappedSubmission(ctx context.Context, id string, o account.BookOrder, marks map[string]string) (account.CappedState, error) {
	if s.before != nil {
		// Populate committed custody immediately before the competing writer. The
		// final fence must reject current authority despite this warm cache.
		if _, e := s.qualificationTestStore.CappedBook(ctx, id); e != nil {
			return account.CappedState{}, e
		}
		f := s.before
		s.before = nil
		s.called = true
		f()
	}
	return s.qualificationTestStore.CheckCappedSubmission(ctx, id, o, marks)
}
func TestQualificationFencedFamily(t *testing.T) { TestQualificationRunnerCollectsDistinctEvidence(t) }
func TestQualificationFencedOwnerPause(t *testing.T) {
	TestQualificationRunnerCollectsDistinctEvidence(t)
}
func TestPostgresContractsQualificationFencedFamily(t *testing.T) {
	TestQualificationRunnerCollectsDistinctEvidence(t)
}
func TestPostgresContractsQualificationFencedOwnerPause(t *testing.T) {
	TestQualificationRunnerCollectsDistinctEvidence(t)
}

func qualificationPostgresURL(t *testing.T) string {
	t.Helper()
	u, e := url.Parse(os.Getenv("MANTIK_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	u.Path += "_compiled_qualification"
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestQualificationPendingFamilyStillExits(t *testing.T) {
	ctx := context.Background()
	r, req, _, broker, d, plan := qualificationRunnerFixture(t)
	entry := nextForwardCycle(req, "pending-family-entry", plan.HoldoutStart)
	entry.Qualification = &forward.Collection{PlanID: plan.ID, Benchmark: qualificationBenchmark(t, d, entry.ExecuteAt)}
	entry = compiledQualificationRequest(t, entry, true)
	r.now = func() time.Time { return entry.ExecuteAt }
	healthyIncubationWorkers(t, r, entry.ExecuteAt)
	out, e := r.Run(ctx, entry)
	if e != nil || len(out.Distribution.Orders) != 1 {
		t.Fatal("entry", e)
	}
	order := out.Distribution.Orders[0]
	broker.fill(t, order.ID, "pending-family-entry-fill", order.Quantity, "100", "0", entry.ExecuteAt.Add(time.Second), true)
	if _, e = r.Sync(ctx, entry.ExecuteAt.Add(2*time.Second), entry.Quotes); e != nil {
		t.Fatal(e)
	}
	batches := r.Store.(interface {
		BeginStrategyTrialBatch(context.Context, string, string) error
	})
	if e = batches.BeginStrategyTrialBatch(ctx, "pending-qualification-research", d.Family); e != nil {
		t.Fatal(e)
	}
	exit := nextForwardCycle(req, "pending-family-exit", entry.ExecuteAt.Add(3*time.Second))
	exit.Qualification = &forward.Collection{PlanID: plan.ID, Benchmark: qualificationBenchmark(t, d, exit.ExecuteAt)}
	exit.Limits = map[string]string{"SPY": "99"}
	// The recipe still wants exposure. Only a storage-confirmed control may flatten it.
	exit = compiledQualificationRequest(t, exit, true)
	r.now = func() time.Time { return exit.ExecuteAt }
	reduced, e := r.Run(ctx, exit)
	if e != nil || len(reduced.Distribution.Orders) != 1 || !reduced.Distribution.Orders[0].ReduceOnly || broker.posts != 2 {
		t.Fatal("pending qualification blocked protective exit", e, broker.posts)
	}
	q := reduced.Plan.Qualification
	if q == nil || q.SearchTotal == nil || *q.SearchTotal != forward.PendingSearchTotal {
		t.Fatal("pending family became a finite search count")
	}
	assessment, e := r.Store.(forward.AssessmentStore).AssessForward(ctx, plan.ID, func() time.Time { return d.OutcomeDeadline })
	if e == nil && assessment.QualificationCredit {
		t.Fatal("pending-family control earned credit")
	}
}
func TestPostgresContractsQualificationPendingFamilyStillExits(t *testing.T) {
	TestQualificationPendingFamilyStillExits(t)
}

func TestPostgresContractsQualificationFencedOwnerKill(t *testing.T) {
	TestQualificationRunnerCollectsDistinctEvidence(t)
}
