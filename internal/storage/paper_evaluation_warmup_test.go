package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Simulate a retained record written before the recipe upgrade. There is no
// public legacy-write or caller-supplied applicability endpoint.
func legacyAssessmentFixture(t *testing.T, s assessmentBackend, r PaperEvaluationAssessmentReceipt, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) PaperEvaluationAssessmentReceipt {
	t.Helper()
	old, err := newPaperEvaluationAssessment(r.Record.Command, r.Record.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(old)
	if strings.Contains(string(raw), `"warm_up":`) {
		t.Fatal("legacy bytes acquired warm-up result")
	}
	switch b := s.(type) {
	case *Memory:
		b.evaluationAssessments[old.Command.ID] = clone(old)
	case *Postgres:
		if _, err = b.pool.Exec(context.Background(), "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, old.Command.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.AssessPaperEvaluation(context.Background(), old.Command, func() time.Time { t.Fatal("legacy retry consulted clock"); return time.Time{} }, de, oe)
	if err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, old) {
		t.Fatal("legacy recipe retry changed", got, err)
	}
	r.Record = old
	return r
}
func assertNativeWarmUp(t *testing.T, r PaperEvaluationAssessment, plan PaperEvaluationDeclarationCommand) {
	t.Helper()
	if r.Schema != "mantik-owned-paper-evaluation-assessment-v4" || r.Recipe != "native-predeclared-necessary-checks-v4" || r.WarmUp == nil || r.WarmUp.Method != "native-threshold-stateless-warmup-v1" || r.WarmUp.RequiredLookbackObservations != 0 || r.WarmUp.Status != "not_required" || len(r.WarmUp.Strategies) != len(plan.Plan.Variants) || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("invalid native applicability", r)
	}
	for i, s := range r.WarmUp.Strategies {
		if s.Command.Definition.ID != plan.Plan.Variants[i].ID || s.SHA256 != plan.Plan.Variants[i].SHA256 || s.RegisteredAt.After(r.Evidence.Declaration.DeclaredAt) {
			t.Fatal("unbound native source", s)
		}
	}
	count := 0
	for _, c := range r.Checks {
		if c.Name == "warm_up" {
			count++
			if c.Status != "not_applicable" {
				t.Fatal(c)
			}
		}
	}
	if count != len(plan.Plan.Variants) {
		t.Fatal("missing per-variant warm-up check", r.Checks)
	}
}
func nativeWarmUpMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=54; CREATE FUNCTION reject_warmup_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=54 THEN RAISE EXCEPTION 'warmup migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_warmup_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_warmup_migration()`); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("migration failure ignored")
	}
	var max int
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&max); err != nil || max != 53 {
		t.Fatal("migration partially committed", max, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_warmup_migration ON mantik_schema; DROP FUNCTION reject_warmup_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	var digest string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=54").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=54"); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("warmup checksum corruption accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=54", digest); err != nil {
		t.Fatal(err)
	}
}

func TestNativeWarmUpEnvelopeCannotInventRequirementsOrMethods(t *testing.T) {
	s := NewMemory()
	plan, at := declarationFixture(t, s, "synthetic_unqualified")
	declared, err := s.DeclarePaperEvaluation(context.Background(), plan, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	// No decisions or outcomes exist; source callback bodies must not be needed.
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Fatal("no decision exists")
		return PaperDecisionEvaluation{}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Fatal("no outcome exists")
		return PaperOutcomeEvaluation{}, nil
	}
	c := PaperEvaluationAssessmentCommand{ID: "empty-applicability", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declared.Record.SHA256}
	receipt, err := s.AssessPaperEvaluation(context.Background(), c, func() time.Time { return plan.Plan.WindowStart }, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeWarmUp(t, receipt.Record, plan)
	assertNativeCalibration(t, receipt.Record, plan)
	for name, change := range map[string]func(*PaperEvaluationWarmUp){
		"lookback":       func(v *PaperEvaluationWarmUp) { v.RequiredLookbackObservations = 10 },
		"method":         func(v *PaperEvaluationWarmUp) { v.Method = "rolling_model_warmup" },
		"status":         func(v *PaperEvaluationWarmUp) { v.Status = "qualified" },
		"missing_source": func(v *PaperEvaluationWarmUp) { v.Strategies = nil },
		"source_method":  func(v *PaperEvaluationWarmUp) { v.Strategies[0].Command.Definition.Method = "invented-model" },
		"source_owner": func(v *PaperEvaluationWarmUp) {
			v.Strategies[0].Command.ReceiverID = "receiver-anotherabcdefghijklmnop"
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := clone(*receipt.Record.WarmUp)
			change(&v)
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(v)
			if got, err := paperEvaluationAssessmentRecipe(c, receipt.Record.Evidence, &v, receipt.Record.Schema); err == nil || !reflect.DeepEqual(got, PaperEvaluationAssessment{}) {
				t.Fatal("fabricated applicability accepted", got, err)
			}
		})
	}
	// Even a valid newly hashed native record cannot replace the plan's exact pin.
	v := clone(*receipt.Record.WarmUp)
	cmd := v.Strategies[0].Command
	cmd.Definition.EntryAtOrBelowCents--
	changed, err := paperstrategy.New(cmd, v.Strategies[0].RegisteredAt)
	if err != nil {
		t.Fatal(err)
	}
	v.Strategies[0] = changed
	v.SHA256 = ""
	v.SHA256 = workerplan.Hash(v)
	if _, err = paperEvaluationAssessmentRecipe(c, receipt.Record.Evidence, &v, receipt.Record.Schema); err == nil {
		t.Fatal("valid different strategy bypassed predeclaration")
	}
}
