package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type evaluationDeclarationBackend interface {
	allocationBackend
	PaperStrategyStore
	PaperEvaluationDeclarationStore
}

func declarationFixture(t *testing.T, s evaluationDeclarationBackend, evidenceClass ...string) (PaperEvaluationDeclarationCommand, time.Time) {
	return declarationVariantsFixture(t, s, true, evidenceClass...)
}

func declarationVariantsFixture(t *testing.T, s evaluationDeclarationBackend, distinct bool, evidenceClass ...string) (PaperEvaluationDeclarationCommand, time.Time) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	account := policyAccount(t, s, "evaluation-account")
	variants := []PaperEvaluationVariant{}
	strategies := []paperstrategy.Record{}
	for _, id := range []string{"baseline-v1", "variant-v1"} {
		d := strategyDefinition(id)
		if distinct && id == "variant-v1" {
			d.Safety.StopCents--
		}
		if len(evidenceClass) > 0 {
			d.InputEvidenceClass = evidenceClass[0]
		}
		r, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: account.ReceiverID, Definition: d}, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		variants = append(variants, PaperEvaluationVariant{id, r.Record.SHA256})
		strategies = append(strategies, r.Record)
	}
	c := policyCommand(account, "evaluation-policy")
	c.Policy.Symbols[0].ID = "SPY"
	c.Policy.Strategies[0].ArtifactSHA256 = strategies[0].DefinitionSHA256
	other := c.Policy.Strategies[0]
	other.ID = "variant-v1"
	other.ArtifactSHA256 = strategies[1].DefinitionSHA256
	c.Policy.Strategies = append(c.Policy.Strategies, other)
	c.ExecutionModel = &paperfill.Model{Method: paperfill.Method, FeeBPS: 10, CommissionPerFillCents: 1, LiquidityBPS: 5000, LatencyMillis: 100, MaxQuoteAgeSeconds: 60}
	policy, err := s.RegisterAllocationPolicy(ctx, c, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	stress := *c.ExecutionModel
	stress.FeeBPS = 20
	stress.SlippageBPS = 50
	return PaperEvaluationDeclarationCommand{ID: "evaluation-one", ReceiverID: account.ReceiverID, Recipe: PaperEvaluationRecipe, PolicyID: c.Policy.ID, PolicySHA256: policy.Record.SHA256, Plan: PaperEvaluationPlan{FamilyID: "family-one", Variants: variants, WindowStart: at.Add(time.Hour), WindowEnd: at.Add(2 * time.Hour), OutcomeDeadline: at.Add(4 * time.Hour), MinIndependentDecisions: 30, MinimumNetExcessCents: 0, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 3600, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress}}, at
}
func TestMemoryPaperEvaluationDeclarations(t *testing.T) {
	evaluationDeclarationContracts(t, NewMemory())
}
func TestPostgresContractsPaperEvaluationDeclarations(t *testing.T) {
	evaluationDeclarationContracts(t, timerPostgres(t, "paperevaluationdeclarations"))
}
func evaluationDeclarationContracts(t *testing.T, s evaluationDeclarationBackend) {
	t.Helper()
	ctx := context.Background()
	c, at := declarationFixture(t, s)
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	clock := func() time.Time { calls.Add(1); return at.Add(time.Second) }
	noClock := func() time.Time { panic("historical declaration read clock") }
	if pg, ok := s.(*Postgres); ok {
		// The statement succeeds; only COMMIT rejects the staged declaration.
		if _, err := pg.pool.Exec(ctx, `CREATE FUNCTION reject_declaration_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test declaration commit rollback'; END $$;
 CREATE CONSTRAINT TRIGGER reject_declaration_commit AFTER INSERT ON mantik_paper_evaluation_declarations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_declaration_commit()`); err != nil {
			t.Fatal(err)
		}
		failed, err := s.DeclarePaperEvaluation(ctx, c, clock)
		if err == nil || !reflect.DeepEqual(failed, PaperEvaluationDeclarationReceipt{}) {
			t.Fatal("commit failure returned staged declaration", failed, err)
		}
		empty, err := s.PaperEvaluationDeclarations(ctx)
		if err != nil || len(empty.Records) != 0 {
			t.Fatal("commit failure retained declaration", empty, err)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_declaration_commit ON mantik_paper_evaluation_declarations; DROP FUNCTION reject_declaration_commit()"); err != nil {
			t.Fatal(err)
		}
		_, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_evaluation_declaration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_evaluation_declaration BEFORE INSERT ON mantik_paper_evaluation_declarations FOR EACH ROW EXECUTE FUNCTION reject_evaluation_declaration()`)
		if err != nil {
			t.Fatal(err)
		}
		failed, err = s.DeclarePaperEvaluation(ctx, c, clock)
		if err == nil || !reflect.DeepEqual(failed, PaperEvaluationDeclarationReceipt{}) {
			t.Fatal("failed insert returned staged declaration", failed, err)
		}
		empty, err = s.PaperEvaluationDeclarations(ctx)
		if err != nil || len(empty.Records) != 0 {
			t.Fatal(empty, err)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_evaluation_declaration ON mantik_paper_evaluation_declarations; DROP FUNCTION reject_evaluation_declaration()"); err != nil {
			t.Fatal(err)
		}
		calls.Store(0)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.DeclarePaperEvaluation(ctx, c, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate clock reads", calls.Load())
	}
	all, err := s.PaperEvaluationDeclarations(ctx)
	if err != nil || len(all.Records) != 1 {
		t.Fatal(all, err)
	}
	record := all.Records[0]
	if record.Status != "declared_not_evaluated" || record.Authority != "operator_predeclared_evaluation_plan_only" || record.PlannedVariants != 2 || len(record.PlannedChecks) != 8 || record.AdmissionAvailable || record.ExecutionAvailable || record.EvaluationAvailable || record.QualificationCredit {
		t.Fatal(record)
	}
	unsigned := clone(record)
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != record.SHA256 {
		t.Fatal("record hash")
	}
	duplicate, err := s.DeclarePaperEvaluation(ctx, c, noClock)
	if err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, record) {
		t.Fatal(duplicate, err)
	}
	duplicate.Record.Command.Plan.Variants[0].ID = "changed"
	duplicate.Record.PlannedChecks[0] = "changed"
	again, _ := s.PaperEvaluationDeclarations(ctx)
	if !reflect.DeepEqual(again, all) {
		t.Fatal("aliased receipt")
	}
	bad := clone(c)
	bad.ID = "family-reset"
	if _, err = s.DeclarePaperEvaluation(ctx, bad, noClock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("family reset", err)
	}
	bad = clone(c)
	bad.Plan.MinIndependentDecisions--
	if _, err = s.DeclarePaperEvaluation(ctx, bad, noClock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("changed declaration", err)
	}
	for _, mutate := range []func(*PaperEvaluationDeclarationCommand){
		func(c *PaperEvaluationDeclarationCommand) { c.Plan.WindowStart = at },
		func(c *PaperEvaluationDeclarationCommand) { c.Plan.Variants[0].SHA256 = strings.Repeat("f", 64) },
		func(c *PaperEvaluationDeclarationCommand) { c.PolicySHA256 = strings.Repeat("f", 64) },
		func(c *PaperEvaluationDeclarationCommand) { c.Plan.StressModel.FeeBPS = 0 },
		func(c *PaperEvaluationDeclarationCommand) { c.Plan.OutcomeDeadline = c.Plan.WindowEnd.Add(time.Second) },
		func(c *PaperEvaluationDeclarationCommand) { c.Plan.Benchmarks = []string{"cash_no_action"} },
	} {
		bad = clone(c)
		bad.ID = "invalid-plan"
		bad.Plan.FamilyID = "invalid-family"
		mutate(&bad)
		if _, err = s.DeclarePaperEvaluation(ctx, bad, func() time.Time { return at.Add(time.Second) }); err == nil {
			t.Fatal("invalid plan accepted", bad)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.DeclarePaperEvaluation(cancelled, c, noClock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Once the window has started, only an exact historical retry is valid.
	bad = clone(c)
	bad.ID = "late-plan"
	bad.Plan.FamilyID = "late-family"
	if _, err = s.DeclarePaperEvaluation(ctx, bad, func() time.Time { return c.Plan.WindowStart }); err == nil {
		t.Fatal("look-back declaration accepted")
	}
	if _, err = s.DeclarePaperEvaluation(ctx, c, func() time.Time { return c.Plan.WindowEnd.Add(time.Hour) }); err != nil {
		t.Fatal("expired historical retry failed", err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("declaration mutated operational state")
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.PaperEvaluationDeclarations(ctx)
		reopened.Close()
		if err != nil || !reflect.DeepEqual(got, all) {
			t.Fatal("reconnect changed declarations", err)
		}
	}
	bad = clone(c)
	bad.ID = "cancelled-declaration"
	bad.Plan.FamilyID = "cancelled-family"
	cancelledWrite, cancelWrite := context.WithCancel(ctx)
	if _, err = s.DeclarePaperEvaluation(cancelledWrite, bad, func() time.Time { cancelWrite(); return at.Add(time.Second) }); err == nil {
		t.Fatal("cancelled declaration committed")
	}
	unchanged, err := s.PaperEvaluationDeclarations(ctx)
	if err != nil || !reflect.DeepEqual(unchanged, all) {
		t.Fatal("cancel left partial declaration", unchanged, err)
	}
	bad = clone(c)
	bad.ID = "wrong-owner"
	bad.ReceiverID = "receiver-aaaaaaaaaaaaaaaa"
	if _, err = s.DeclarePaperEvaluation(ctx, bad, noClock); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	for i := 1; i < MaxPaperEvaluationDeclarations; i++ {
		next := clone(c)
		next.ID = fmt.Sprintf("plan-%02d", i)
		next.Plan.FamilyID = fmt.Sprintf("family-%02d", i)
		if _, err = s.DeclarePaperEvaluation(ctx, next, func() time.Time { return at.Add(time.Second) }); err != nil {
			t.Fatal(i, err)
		}
	}
	bad = clone(c)
	bad.ID = "overflow"
	bad.Plan.FamilyID = "overflow"
	if _, err = s.DeclarePaperEvaluation(ctx, bad, noClock); !errors.Is(err, ErrPaperEvaluationDeclarationCapacity) {
		t.Fatal(err)
	}
	if _, err = s.DeclarePaperEvaluation(ctx, c, noClock); err != nil {
		t.Fatal("full exact retry failed", err)
	}

	if pg, ok := s.(*Postgres); ok {
		damaged := clone(record)
		damaged.AdmissionAvailable = true
		damaged.SHA256 = ""
		damaged.SHA256 = workerplan.Hash(damaged)
		raw, _ := json.Marshal(damaged)
		if _, err = pg.pool.Exec(ctx, "UPDATE mantik_paper_evaluation_declarations SET payload=$1 WHERE id=$2", raw, c.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.PaperEvaluationDeclarations(ctx)
		if err == nil || !reflect.DeepEqual(got, PaperEvaluationDeclarationCatalog{}) {
			t.Fatal("corrupt catalogue returned a partial result", got, err)
		}
		failed, err := s.DeclarePaperEvaluation(ctx, c, noClock)
		if err == nil || !reflect.DeepEqual(failed, PaperEvaluationDeclarationReceipt{}) {
			t.Fatal("exact retry hid persisted corruption", failed, err)
		}
	}
}
func TestMemoryPaperEvaluationDeclarationRejectsRehashedCorruption(t *testing.T) {
	for name, mutate := range map[string]func(*PaperEvaluationDeclaration){"admission": func(r *PaperEvaluationDeclaration) { r.AdmissionAvailable = true }, "qualification": func(r *PaperEvaluationDeclaration) { r.QualificationCredit = true }, "checks": func(r *PaperEvaluationDeclaration) { r.PlannedChecks = r.PlannedChecks[:1] }, "source": func(r *PaperEvaluationDeclaration) { r.SourceSHA256 = strings.Repeat("f", 64) }, "backdate": func(r *PaperEvaluationDeclaration) { r.Command.Plan.WindowStart = r.DeclaredAt }} {
		t.Run(name, func(t *testing.T) {
			m := NewMemory()
			c, at := declarationFixture(t, m)
			r, err := m.DeclarePaperEvaluation(context.Background(), c, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			mutate(&r.Record)
			r.Record.SHA256 = ""
			r.Record.SHA256 = workerplan.Hash(r.Record)
			m.paperEvaluationPlans[c.ID] = r.Record
			if _, err = m.PaperEvaluationDeclarations(context.Background()); err == nil {
				t.Fatal("rehashed damage accepted")
			}
			if _, err = m.DeclarePaperEvaluation(context.Background(), c, func() time.Time { panic("corrupt retry clock") }); err == nil {
				t.Fatal("retry hid corruption")
			}
		})
	}
}

func TestPostgresContractsPaperEvaluationDeclarationUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "paperevaluationupgrade")
	c, at := declarationFixture(t, s)
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=46"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal("idempotent schema47 reopen", err)
	}
	var maximum int
	if err = s.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 106 {
		t.Fatal(maximum, err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade altered operational state", err)
	}
	if _, err = s.DeclarePaperEvaluation(ctx, c, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
}
