package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type batchBackend interface {
	forwardBackend
	strategytrial.BatchStore
	strategytrial.FamilyReader
}

func batchConsumerContracts(t *testing.T, s batchBackend) {
	t.Helper()
	ctx := context.Background()
	d, at := forwardFixture(t, s)
	old, e := s.DeclareForward(ctx, d, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	g, e := s.StrategyGate(ctx, d.GateID)
	if e != nil {
		t.Fatal(e)
	}
	next := g.Declaration
	next.ID = "pending-gate"
	if _, e = s.DeclareStrategyGate(ctx, next); e != nil {
		t.Fatal(e)
	}
	if e = s.BeginStrategyTrialBatch(ctx, "pending", d.Family); e != nil {
		t.Fatal(e)
	}
	assertPending := func() {
		t.Helper()
		if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("count escaped fence", n, e)
		}
		if _, e := s.StrategyTrials(ctx, d.Family); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("snapshot escaped fence", e)
		}

		decision := *g.Decision
		decision.ID = next.ID
		decision.DeclarationSHA256, _ = strategygate.Hash(next)
		decision.At = at
		if _, e := s.RecordStrategyGate(ctx, decision); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("direct gate write escaped fence", e)
		}
		if _, e := s.ForwardEligibility(ctx, d.ID, d.WindowStart); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("forward eligibility escaped fence", e)
		}
		another := d
		another.ID = "pending-forward"
		if _, e := s.DeclareForward(ctx, another, func() time.Time { return at }); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("declaration escaped fence", e)
		}
		state, e := s.CappedBook(ctx, d.BookID)
		if e != nil {
			t.Fatal(e)
		}
		req := allocation.RebalanceRequest{ID: "pending-rebalance", At: d.WindowStart.Add(time.Second), Sleeves: []allocation.Sleeve{{ID: d.SleeveID, StrategyID: d.StrategyID, GateID: d.GateID, Stage: "forward_only", ModelDriven: true, Risk: "1", Minimum: "1"}}, Targets: []allocation.SleeveTarget{{SleeveID: d.SleeveID, Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "1"}}}}}, Limits: map[string]string{"SPY": "101"}, Fees: map[string]string{"SPY": "1"}}
		if _, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: map[string]string{"SPY": "100"}}); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("admission escaped fence", e)
		}
		for _, bound := range []bool{false, true} {
			plan := account.IncubationPlan{InputSHA256: strings.Repeat("c", 64), DecidedAt: d.WindowStart, Rebalance: req, Costs: map[string]executioncost.Model{d.SleeveID: executioncost.Default()}, Decisions: []account.IncubationDecision{{SleeveID: d.SleeveID, StrategyID: d.StrategyID}}, Quotes: map[string]account.IncubationQuote{"SPY": {Symbol: "SPY", Bid: "99.99", Ask: "100.01", BidQuantity: "100", AskQuantity: "100", OccurredAt: req.At, ReceivedAt: req.At, EvidenceClass: "prospective_market_unqualified", SourceSHA256: strings.Repeat("d", 64)}}}
			if bound {
				plan.ForwardDeclarations = map[string]string{d.SleeveID: d.ID}
			}
			if _, e = s.ApplyCappedBook(ctx, d.BookID, state.Version, account.BookCommand{ID: "pending-incubation", Kind: "incubation_plan", At: req.At, Incubation: &plan}); !errors.Is(e, strategytrial.ErrFamilyPending) {
				t.Fatal("incubation/binding escaped fence", bound, e)
			}
		}

		after, e := s.ForwardDeclaration(ctx, d.ID)
		if e != nil || !reflect.DeepEqual(after, old) {
			t.Fatal("immutable declaration changed", e)
		}
	}
	assertPending()
	b := strategytrial.Batch{ID: "pending", Family: d.Family, At: at, Reply: json.RawMessage(`"private reply"`), TrialIDs: []string{"first", "second", "third"}}
	if e = s.ReserveStrategyTrialBatch(ctx, b); e != nil {
		t.Fatal(e)
	}
	for _, id := range b.TrialIDs {
		if e = s.ResolveStrategyTrialBatch(ctx, b.ID); !errors.Is(e, strategytrial.ErrFamilyPending) {
			t.Fatal("early resolution", e)
		}
		assertPending()
		forwardTrial(t, s, d, id)
	}
	// Even a fully written batch stays fenced until reconciliation acknowledges it.
	assertPending()
	if e = s.ResolveStrategyTrialBatch(ctx, b.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ResolveStrategyTrialBatch(ctx, b.ID); e != nil {
		t.Fatal("idempotent resolution", e)
	}
	if n, e := s.StrategyTrialSearchTotal(ctx, d.Family); e != nil || n != 3 {
		t.Fatal(n, e)
	}

	if v, e := s.ForwardEligibility(ctx, d.ID, d.WindowStart); e != nil || v.SearchTotal != 3 {
		t.Fatal(v, e)
	}
	b.Reply = json.RawMessage(`"changed"`)
	if e = s.ReserveStrategyTrialBatch(ctx, b); !errors.Is(e, strategytrial.ErrConflict) {
		t.Fatal("changed reply", e)
	}
}
func TestMemoryStrategyBatchConsumers(t *testing.T) { batchConsumerContracts(t, NewMemory()) }
func TestPostgresContractsStrategyBatchConsumers(t *testing.T) {
	p := timerPostgres(t, "batch_consumers")
	batchConsumerContracts(t, p)
	ctx := context.Background()
	before, e := p.StrategyTrialBatch(ctx, "pending")
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e = p.pool.Exec(ctx, migrations.StrategyTrialBatches); e != nil {
			t.Fatal(e)
		}
		if e = p.migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var digest string
	if e = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=125").Scan(&digest); e != nil || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(migrations.StrategyTrialBatches))) {
		t.Fatal(digest, e)
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.StrategyTrialBatch(ctx, "pending")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed intent", e)
	}
	for _, table := range []string{"mantik_strategy_trial_batch_intents", "mantik_strategy_trial_batches", "mantik_strategy_trial_batch_resolutions"} {
		for _, sql := range []string{"UPDATE " + table + " SET id=id", "DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			if _, e = p.pool.Exec(ctx, sql); e == nil {
				t.Fatal("mutable accounting", sql)
			}
		}
	}
	// A schema124 pending reply must fence readers before Run gets to recover it.
	if _, e = p.pool.Exec(ctx, `INSERT INTO mantik_research_desk_events VALUES ('legacy-pending',0,'start',now(),'{"family":"legacy"}'),('legacy-pending',1,'call',now(),'{}'),('legacy-pending',2,'reply',now(),'"{\"tool\":\"evolve_specs\",\"variants\":[{}]}"')`); e != nil {
		t.Fatal(e)
	}
	if _, e = p.StrategyTrialSearchTotal(ctx, "legacy"); !errors.Is(e, strategytrial.ErrFamilyPending) {
		t.Fatal("legacy pending count", e)
	}
	if _, e = p.StrategyTrials(ctx, "legacy"); !errors.Is(e, strategytrial.ErrFamilyPending) {
		t.Fatal("legacy pending snapshot", e)
	}
}

func TestPostgresContractsStrategyBatchUpgradeFrom124(t *testing.T) {
	p := timerPostgres(t, "batch_upgrade124")
	ctx := context.Background()
	start := registryStart("legacy-ordinary")
	if _, e := p.StartStrategyTrial(ctx, start); e != nil {
		t.Fatal(e)
	}
	// Remove only this disposable database's new schema to exercise a genuine
	// sequential installation over retained 124 history, rather than DDL replay.
	if _, e := p.pool.Exec(ctx, `DROP TABLE mantik_strategy_trial_batch_resolutions, mantik_strategy_trial_batches, mantik_strategy_trial_batch_intents; DELETE FROM mantik_schema WHERE version=125`); e != nil {
		t.Fatal(e)
	}
	if _, e := p.pool.Exec(ctx, `INSERT INTO mantik_research_desk_events VALUES ('legacy-call',0,'start',now(),'{"family":"unknown-legacy"}'),('legacy-call',1,'call',now(),'{}')`); e != nil {
		t.Fatal(e)
	}
	if e := p.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := p.StrategyTrialSearchTotal(ctx, "unknown-legacy"); !errors.Is(e, strategytrial.ErrFamilyPending) {
		t.Fatal("legacy unknown call became finite", e)
	}
	if n, e := p.StrategyTrialSearchTotal(ctx, start.Family); e != nil || n != 1 {
		t.Fatal("ordinary trial count changed", n, e)
	}
	r, e := p.StrategyTrial(ctx, start.ID)
	if e != nil || !sameTrial(r.Start, start) {
		t.Fatal("ordinary trial bytes changed", e)
	}
	var maximum int
	if e = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); e != nil || maximum != 125 {
		t.Fatal(maximum, e)
	}
}
