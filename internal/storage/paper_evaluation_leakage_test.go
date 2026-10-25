package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestKnownSourceLeakageRecipeVersions(t *testing.T) {
	m := NewMemory()
	// Actual owner issuance/replay supplies the base. The altered evidence below
	// is a negative recipe fixture, not a forged service input or qualification.
	evaluationAssessmentSelectionContracts(t, m, true)
	var base PaperEvaluationAssessment
	for _, r := range m.evaluationAssessments {
		if r.Schema == "mantik-owned-paper-evaluation-assessment-v4" && len(r.Evidence.Rows) > 0 {
			base = clone(r)
			break
		}
	}
	if base.WarmUp == nil {
		t.Fatal("missing actual owner base")
	}
	for _, status := range []string{"source_unavailable_at_decision", "selected_research_evidence", "abstention", "decision_before_window", "decision_after_window", "quote_before_window", "quote_after_window", "synthetic_not_selected"} {
		t.Run(status, func(t *testing.T) {
			e := clone(base.Evidence)
			e.Rows[0].Status = status
			e.SHA256 = ""
			e.SHA256 = workerplan.Hash(e)
			old, err := paperEvaluationAssessmentRecipe(base.Command, e, base.WarmUp, "mantik-owned-paper-evaluation-assessment-v3")
			if err != nil {
				t.Fatal(err)
			}
			got, err := paperEvaluationAssessmentRecipe(base.Command, e, base.WarmUp, "mantik-owned-paper-evaluation-assessment-v4")
			if err != nil || got.EvaluationComplete || got.AdmissionAvailable || got.ExecutionAvailable || got.QualificationCredit || !reflect.DeepEqual(got.Evidence, e) {
				t.Fatal("recipe changed evidence or granted authority", got, err)
			}
			for i, check := range got.Checks {
				if check.Name == "leakage" {
					if old.Checks[i].Status != "unavailable" {
						t.Fatal("legacy leakage semantics changed", old)
					}
					if status == "source_unavailable_at_decision" && check.StrategyID == e.Rows[0].Decision.Command.StrategyID {
						if check.Status != "failed" || check.ReasonCode != "retained_candidate_source_unavailable_at_finalization" || got.Status != "blocked" {
							t.Fatal("known failure erased by exclusion", got)
						}
					} else if check.Status != "unavailable" {
						t.Fatal("absence of a known failure claimed a pass", check)
					}
				} else if !reflect.DeepEqual(check, old.Checks[i]) {
					t.Fatal("unrelated check changed", check)
				}
			}
			unsigned := clone(got)
			unsigned.SHA256 = ""
			if workerplan.Hash(unsigned) != got.SHA256 {
				t.Fatal("unbound failure")
			}
		})
	}
}

func TestKnownSourceLeakageVariantIsolation(t *testing.T) {
	r := PaperEvaluationAssessment{Status: "pending", Checks: []PaperEvaluationCheck{{StrategyID: "one", Name: "leakage", Status: "unavailable"}, {StrategyID: "two", Name: "leakage", Status: "unavailable"}}, Evidence: PaperEvaluationEvidence{Rows: []PaperEvaluationEvidenceRow{{Status: "source_unavailable_at_decision", Decision: PaperDecisionRecord{Command: PaperDecisionCommand{StrategyID: "one"}}}}}}
	applyKnownSourceLeakage(&r)
	if r.Status != "blocked" || r.Checks[0].Status != "failed" || r.Checks[1].Status != "unavailable" {
		t.Fatal(r)
	}
}

func knownLeakageMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY id),'[]'::jsonb)::text FROM mantik_paper_evaluation_assessments"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=61; COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'schema60 variant fixture'; CREATE FUNCTION reject_leakage_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=61 THEN RAISE EXCEPTION 'leakage migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_leakage_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_leakage_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_assessments'::regclass)").Scan(&marker); err != nil || marker != "schema60 variant fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_leakage_migration ON mantik_schema; DROP FUNCTION reject_leakage_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote assessment payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=61").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=61"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("leakage migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=61", hash); err != nil {
		t.Fatal(err)
	}
}

// Upgrade replay must not attach the later failure rule to an actual retained
// historical v3 assessment, including across a PostgreSQL reopen.
func legacyLeakageReplay(t *testing.T, s assessmentBackend, current PaperEvaluationAssessmentReceipt, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) {
	t.Helper()
	old, err := paperEvaluationAssessmentRecipe(current.Record.Command, current.Record.Evidence, current.Record.WarmUp, "mantik-owned-paper-evaluation-assessment-v3")
	if err != nil {
		t.Fatal(err)
	}
	set := func(r PaperEvaluationAssessment) {
		switch b := s.(type) {
		case *Memory:
			b.evaluationAssessments[r.Command.ID] = clone(r)
		case *Postgres:
			raw, e := json.Marshal(r)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = b.pool.Exec(context.Background(), "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, r.Command.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	set(old)
	defer set(current.Record)
	check := func(backend PaperEvaluationAssessmentStore) {
		got, e := backend.AssessPaperEvaluation(context.Background(), old.Command, func() time.Time { t.Fatal("legacy v3 retry consulted clock"); return time.Time{} }, de, oe)
		if e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, old) {
			t.Fatal("legacy v3 replay changed", got, e)
		}
	}
	check(s)
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(context.Background(), pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		check(reopened)
	}
}
