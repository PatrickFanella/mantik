package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type decisionBackend interface {
	strategyBackend
	PaperDecisionStore
	FeedCollectorStore
}

func TestMemoryOwnedPaperDecisions(t *testing.T) { ownedDecisionContracts(t, NewMemory()) }
func TestPostgresContractsOwnedPaperDecisions(t *testing.T) {
	ownedDecisionContracts(t, timerPostgres(t, "owneddecisions"))
}
func ownedDecisionContracts(t *testing.T, s decisionBackend) {
	ctx := context.Background()
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	at := time.Now().UTC()
	d := strategyDefinition("baseline")
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	registration := feedcollector.Registration{ID: "spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "test-journal", AccessRef: "manufactured-fixture", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: "spy", Registration: &registration}, {RequestID: "claim", Operation: "claim", RegistrationID: "spy", OwnerID: "owner"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
	}
	c := PaperDecisionCommand{ID: "first", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: "spy"}
	eval := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "storage_contract_only"}, nil
	}
	before, _ := s.Snapshot(ctx)
	var clocks atomic.Int32
	clock := func() time.Time { clocks.Add(1); return at.Add(time.Second) }
	noClock := func() time.Time { panic("historical retry read clock") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.FinalizePaperDecision(ctx, c, clock, eval); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if clocks.Load() != 1 {
		t.Fatal(clocks.Load())
	}
	all, err := s.PaperDecisions(ctx, eval)
	if err != nil || len(all.Records) != 1 {
		t.Fatal(all, err)
	}
	first := all.Records[0]
	if first.AdmissionAvailable || first.ExecutionAvailable || first.QualificationCredit {
		t.Fatal(first)
	}
	receipt, err := s.FinalizePaperDecision(ctx, c, noClock, eval)
	if err != nil || !receipt.Duplicate || !reflect.DeepEqual(receipt.Record, first) {
		t.Fatal(receipt, err)
	}
	receipt.Record.Evaluation.ReasonCode = "mutated"
	bad := c
	bad.StrategySHA256 = string(make([]byte, 64))
	if _, err := s.FinalizePaperDecision(ctx, bad, noClock, eval); err == nil {
		t.Fatal("invalid pin accepted")
	}
	bad = c
	bad.RegistrationID = "other"
	if _, err := s.FinalizePaperDecision(ctx, bad, noClock, eval); !errors.Is(err, ErrPaperDecisionConflict) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.FinalizePaperDecision(canceled, c, noClock, eval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Later lease changes do not rewrite or invalidate the captured as-of basis.
	if _, err := s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "stop", Operation: "stop", RegistrationID: "spy", OwnerID: "owner", Generation: 1}, func() time.Time { return at.Add(2 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxPaperDecisions; i++ {
		next := c
		next.ID = fmt.Sprintf("decision-%02d", i)
		if _, err := s.FinalizePaperDecision(ctx, next, func() time.Time { return at.Add(3 * time.Second) }, eval); err != nil {
			t.Fatal(i, err)
		}
	}
	bad = c
	bad.ID = "overflow"
	if _, err := s.FinalizePaperDecision(ctx, bad, noClock, eval); !errors.Is(err, ErrPaperDecisionCapacity) {
		t.Fatal(err)
	}
	if _, err := s.FinalizePaperDecision(ctx, c, noClock, eval); err != nil {
		t.Fatal(err)
	}
	all, err = s.PaperDecisions(ctx, eval)
	if err != nil || len(all.Records) != MaxPaperDecisions || !reflect.DeepEqual(all.Records[len(all.Records)-1], first) {
		t.Fatal(all, err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("research finalization changed operational state")
	}

	original := retainedPaperDecision{}
	set := func(v retainedPaperDecision) {
		if m, ok := s.(*Memory); ok {
			m.paperDecisions[c.ID] = v
		} else {
			raw, _ := json.Marshal(v)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_decisions SET payload=$1 WHERE id=$2", raw, c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if m, ok := s.(*Memory); ok {
		original = clone(m.paperDecisions[c.ID])
	} else {
		var raw []byte
		if err := s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_decisions WHERE id=$1", c.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
	}
	damaged := clone(original)
	damaged.Record.ExecutionAvailable = true
	damaged.Record.SHA256 = ""
	damaged.Record.SHA256 = workerplan.Hash(damaged.Record)
	set(damaged)
	if _, err := s.PaperDecisions(ctx, eval); err == nil {
		t.Fatal("self-hashed execution forgery accepted")
	}
	if _, err := s.FinalizePaperDecision(ctx, c, noClock, eval); err == nil {
		t.Fatal("retry hid corrupt prefix")
	}
	if _, err := s.FinalizePaperDecision(ctx, bad, noClock, eval); err == nil {
		t.Fatal("new write hid corrupt prefix")
	}
	set(original)
	if pg, ok := s.(*Postgres); ok {
		cfg := pg.pool.Config().ConnString()
		pg.Close()
		next, err := OpenPostgres(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		got, err := next.PaperDecisions(ctx, eval)
		if err != nil || !reflect.DeepEqual(got, all) {
			t.Fatal("restart changed decisions", err)
		}
	}
}

func TestPostgresContractsOwnedDecisionUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "decisionupgrade")
	owner, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner.ReceiverID, Definition: strategyDefinition("preserved")}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	policies, _ := s.AllocationPolicies(ctx)
	cfg := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DELETE FROM mantik_schema WHERE version>=38"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	after, _ := up.Snapshot(ctx)
	a, _ := up.PaperAccounts(ctx)
	p, _ := up.AllocationPolicies(ctx)
	strategies, err := up.PaperStrategies(ctx)
	if err != nil || len(strategies.Records) != 1 || !reflect.DeepEqual(strategies.Records[0], strategy.Record) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, a) || !reflect.DeepEqual(policies, p) {
		t.Fatal("upgrade changed retained state", err)
	}
	reg := feedcollector.Registration{ID: "spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "test-journal", AccessRef: "fixture", LeaseSeconds: 300}
	if _, err := up.FeedCollectorCommand(ctx, owner.ReceiverID, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: "spy", Registration: &reg}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "rollback_contract_only"}, nil
	}
	c := PaperDecisionCommand{ID: "new", ReceiverID: owner.ReceiverID, StrategyID: "preserved", StrategySHA256: strategy.Record.SHA256, RegistrationID: "spy"}
	if _, err := up.pool.Exec(ctx, `CREATE FUNCTION reject_paper_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_paper_decision BEFORE INSERT ON mantik_paper_decisions FOR EACH ROW EXECUTE FUNCTION reject_paper_decision()`); err != nil {
		t.Fatal(err)
	}
	if _, err := up.FinalizePaperDecision(ctx, c, func() time.Time { return at }, eval); err == nil {
		t.Fatal("failed insert accepted")
	}
	empty, err := up.PaperDecisions(ctx, eval)
	if err != nil || len(empty.Records) != 0 {
		t.Fatal("partial insert", err)
	}
	if _, err := up.pool.Exec(ctx, "DROP TRIGGER reject_paper_decision ON mantik_paper_decisions; DROP FUNCTION reject_paper_decision()"); err != nil {
		t.Fatal(err)
	}
	if _, err := up.FinalizePaperDecision(ctx, c, func() time.Time { return at }, eval); err != nil {
		t.Fatal(err)
	}
	if _, err := up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=38"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, cfg); err == nil {
		broken.Close()
		t.Fatal("changed migration accepted")
	}
}
