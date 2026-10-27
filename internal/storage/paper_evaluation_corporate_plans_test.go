package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type corporateSelectionBackend interface {
	chainBackend
	PaperEvaluationCorporateSelectionStore
}

func TestMemoryPredeclaredCorporateSelection(t *testing.T) {
	predeclaredCorporateSelection(t, NewMemory())
}
func TestPostgresContractsPredeclaredCorporateSelection(t *testing.T) {
	predeclaredCorporateSelection(t, timerPostgres(t, "predeclaredcorporate"))
}
func predeclaredCorporateSelection(t *testing.T, s corporateSelectionBackend) {
	ctx := context.Background()
	c, at := declarationFixture(t, s)
	root, acquisition := chainSource(c.ReceiverID, "planned-root", "", "", "event", at.Add(3*time.Second))
	c.Plan.CorporateSources = []PaperEvaluationCorporateSourcePlan{{Symbol: "SPY", JournalID: acquisition.JournalID, RootCollectionID: acquisition.CollectionID, AccessRef: root.Recording.AccessRef, Start: root.Recording.Start, End: root.Recording.End}}
	badJournal := clone(c)
	badJournal.Plan.CorporateSources[0].JournalID = "unresolvable-journal-name"
	if _, err := s.DeclarePaperEvaluation(ctx, badJournal, func() time.Time { t.Fatal("invalid new journal intent sampled clock"); return time.Time{} }); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("new intent cannot match a configured acquisition", err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := s.DeclarePaperEvaluation(ctx, c, func() time.Time { return at.Add(time.Second) }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	d, err := s.DeclarePaperEvaluation(ctx, c, func() time.Time { t.Fatal("retry read clock"); return time.Time{} })
	if err != nil || !d.Duplicate || d.Record.Schema != "mantik-owned-paper-evaluation-declaration-v3" {
		t.Fatal(d, err)
	}
	f := &corporateChainFixture{s: s, declaration: d.Record, at: at, step: 2, de: recoveryFixtureDecisionEvaluator, oe: recoveryFixtureOutcomeEvaluator}
	old := f.assess(t, "before-planned-root")
	early, err := s.PaperEvaluationCorporateSelection(ctx, old.Command.ID, f.de, f.oe)
	if err != nil || len(early.Rows) != 1 || early.Rows[0].Status != "predeclared_root_unavailable_at_cut" {
		t.Fatal(early, err)
	}
	f.step = 6 // request/completion happened after declaration and before retention
	f.retain(t, root, acquisition)
	newer := f.assess(t, "with-planned-root")
	got, err := s.PaperEvaluationCorporateSelection(ctx, newer.Command.ID, f.de, f.oe)
	if err != nil || len(got.Rows) != 1 || !got.Rows[0].PredeclaredChainResolved || got.Rows[0].Chain == nil || got.Rows[0].PlanSHA256 != workerplan.Hash(c.Plan.CorporateSources[0]) || got.Rows[0].Chain.RootSourceSHA256 == "" || got.CoverageComplete || got.EconomicApplicationReady || got.AdmissionAvailable || got.ExecutionAvailable || got.QualificationCredit {
		t.Fatal(got, err)
	}
	again, err := s.PaperEvaluationCorporateSelection(ctx, old.Command.ID, f.de, f.oe)
	if err != nil || !reflect.DeepEqual(again, early) {
		t.Fatal("later source entered old selection", again, err)
	}
	bad := clone(c)
	bad.Plan.CorporateSources[0].RootCollectionID = "replacement"
	if _, err := s.DeclarePaperEvaluation(ctx, bad, nil); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("plan rewritten", err)
	}
	if pg, ok := s.(*Postgres); ok {
		corporatePlanMigration(t, pg)
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		r, err := reopened.PaperEvaluationCorporateSelection(ctx, newer.Command.ID, f.de, f.oe)
		if err != nil || !reflect.DeepEqual(r, got) {
			t.Fatal("restart changed selection", r, err)
		}
	}
	got.Rows[0].Plan.Symbol = "MUTATED"
	got.Rows[0].Chain.Pages[0].Source.Symbols[0] = "MUTATED"
	r, err := s.PaperEvaluationCorporateSelection(ctx, newer.Command.ID, f.de, f.oe)
	if err != nil || r.Rows[0].Plan.Symbol != "SPY" || r.Rows[0].Chain.Pages[0].Source.Symbols[0] != "SPY" {
		t.Fatal("mutable readback", r, err)
	}
}

func TestMemoryHistoricalCorporateJournalIntent(t *testing.T) {
	historicalCorporateJournalIntent(t, NewMemory())
}
func TestPostgresContractsHistoricalCorporateJournalIntent(t *testing.T) {
	historicalCorporateJournalIntent(t, timerPostgres(t, "historicaljournalintent"))
}
func historicalCorporateJournalIntent(t *testing.T, s corporateSelectionBackend) {
	t.Helper()
	ctx := context.Background()
	c, at := declarationFixture(t, s)
	c.Plan.CorporateSources = []PaperEvaluationCorporateSourcePlan{{Symbol: "SPY", JournalID: "unresolvable-journal-name", RootCollectionID: "root", AccessRef: "access", Start: "2026-10-14", End: "2026-10-14"}}
	strategies, err := s.PaperStrategies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := s.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Manufacture an originally valid v3 record using its preserved replay
	// constructor. This is compatibility evidence, not an acquisition observation.
	old, err := paperEvaluationDeclarationVersion(c, strategies, policies, at.Add(time.Second), "mantik-owned-paper-evaluation-declaration-v3")
	if err != nil {
		t.Fatal(err)
	}
	switch owner := s.(type) {
	case *Memory:
		owner.paperEvaluationPlans[c.ID] = clone(old)
	case *Postgres:
		raw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.pool.Exec(ctx, "INSERT INTO mantik_paper_evaluation_declarations(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown fixture owner")
	}
	for range 2 {
		r, err := s.DeclarePaperEvaluation(ctx, c, func() time.Time { t.Fatal("historical retry sampled clock"); return time.Time{} })
		if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Record, old) {
			t.Fatal("historical intent changed", r, err)
		}
	}
	f := &corporateChainFixture{s: s, declaration: old, at: at, step: 2, de: recoveryFixtureDecisionEvaluator, oe: recoveryFixtureOutcomeEvaluator}
	a := f.assess(t, "historical-intent-assessment")
	r, err := s.PaperEvaluationCorporateSelection(ctx, a.Command.ID, f.de, f.oe)
	if err != nil || len(r.Rows) != 1 || r.Rows[0].Status != "predeclared_root_unavailable_at_cut" || r.Rows[0].PredeclaredChainResolved || r.EconomicApplicationReady || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("historical intent became a source grant", r, err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		retry, err := reopened.DeclarePaperEvaluation(ctx, c, nil)
		if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, old) {
			t.Fatal("restart lost historical retry", retry, err)
		}
	}
}

func corporatePlanMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	const q = "SELECT jsonb_agg(payload ORDER BY id)::text FROM mantik_paper_evaluation_declarations"
	var before, after, comment, hash string
	if err := p.pool.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version=74; COMMENT ON TABLE mantik_paper_evaluation_declarations IS 'predeclaration rollback marker'; CREATE FUNCTION reject_plan_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=74 THEN RAISE EXCEPTION 'manufactured plan migration failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_plan_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_plan_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_declarations'::regclass)").Scan(&comment); err != nil || comment != "predeclaration rollback marker" {
		t.Fatal("migration escaped rollback", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_plan_migration ON mantik_schema; DROP FUNCTION reject_plan_migration()"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.pool.QueryRow(ctx, q).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote declarations", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=74").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=74"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration checksum ignored")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=74", hash); err != nil {
		t.Fatal(err)
	}
}

func TestCorporateSourcePlanValidationAndClockBounds(t *testing.T) {
	for _, kind := range []string{"valid", "scope", "before_declaration", "after_deadline", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			s := NewMemory()
			c, at := declarationFixture(t, s)
			requested := at.Add(3 * time.Second)
			if kind == "before_declaration" {
				requested = at.Add(-time.Hour)
			}
			if kind == "after_deadline" {
				requested = c.Plan.OutcomeDeadline
			}
			root, a := chainSource(c.ReceiverID, "planned-root", "", "", "event", requested)
			if kind != "legacy" {
				c.Plan.CorporateSources = []PaperEvaluationCorporateSourcePlan{{Symbol: "SPY", JournalID: a.JournalID, RootCollectionID: a.CollectionID, AccessRef: root.Recording.AccessRef, Start: root.Recording.Start, End: root.Recording.End}}
			}
			if kind == "scope" {
				c.Plan.CorporateSources[0].AccessRef = "different-access"
			}
			d, err := s.DeclarePaperEvaluation(context.Background(), c, func() time.Time { return at.Add(time.Second) })
			if err != nil {
				t.Fatal(err)
			}
			f := &corporateChainFixture{s: s, declaration: d.Record, at: at, step: 6, de: recoveryFixtureDecisionEvaluator, oe: recoveryFixtureOutcomeEvaluator}
			if kind == "after_deadline" {
				f.at = c.Plan.OutcomeDeadline
			}
			f.retain(t, root, a)
			assessment := f.assess(t, "selection")
			r, err := s.PaperEvaluationCorporateSelection(context.Background(), assessment.Command.ID, f.de, f.oe)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "legacy" {
				if r.Status != "corporate_source_plan_not_predeclared" || len(r.Rows) != 0 || d.Record.Schema != "mantik-owned-paper-evaluation-declaration-v2" {
					t.Fatal(r)
				}
				return
			}
			want := map[string]string{"valid": "page_chain_terminated", "scope": "predeclared_query_scope_mismatch", "before_declaration": "acquisition_outside_predeclared_clock_bounds", "after_deadline": "acquisition_outside_predeclared_clock_bounds"}[kind]
			if r.Rows[0].Status != want || r.Rows[0].PredeclaredChainResolved != (kind == "valid") {
				t.Fatal(r)
			}
		})
	}
	s := NewMemory()
	c, at := declarationFixture(t, s)
	valid := PaperEvaluationCorporateSourcePlan{Symbol: "SPY", JournalID: "journal", RootCollectionID: "root", AccessRef: "access", Start: "2026-10-14", End: "2026-10-14"}
	for _, plans := range [][]PaperEvaluationCorporateSourcePlan{{}, {valid, valid}, {{Symbol: "WRONG", JournalID: "journal", RootCollectionID: "root", AccessRef: "access", Start: valid.Start, End: valid.End}}, {{Symbol: "SPY", JournalID: "bad/id", RootCollectionID: "root", AccessRef: "access", Start: valid.Start, End: valid.End}}} {
		bad := clone(c)
		bad.Plan.CorporateSources = plans
		if _, err := s.DeclarePaperEvaluation(context.Background(), bad, func() time.Time { return at.Add(time.Second) }); err == nil {
			t.Fatal("invalid plan accepted", plans)
		}
	}
}
