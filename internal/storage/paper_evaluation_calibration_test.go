package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Retain the exact v2 recipe in the real owner store. Its historical warm-up
// result does not acquire the later calibration applicability result.
func legacyCalibrationFixture(t *testing.T, s assessmentBackend, receipt PaperEvaluationAssessmentReceipt, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) PaperEvaluationAssessmentReceipt {
	t.Helper()
	old, err := paperEvaluationAssessmentRecipe(receipt.Record.Command, receipt.Record.Evidence, receipt.Record.WarmUp, "mantik-owned-paper-evaluation-assessment-v2")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(old)
	if old.ForecastCalibration != nil || strings.Contains(string(raw), `"forecast_calibration":`) {
		t.Fatal("legacy calibration bytes changed")
	}
	for _, c := range old.Checks {
		if c.Name == "forecast_calibration" && c.Status != "unavailable" {
			t.Fatal(c)
		}
	}
	switch b := s.(type) {
	case *Memory:
		b.evaluationAssessments[old.Command.ID] = clone(old)
	case *Postgres:
		if _, err = b.pool.Exec(context.Background(), "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, old.Command.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.AssessPaperEvaluation(context.Background(), old.Command, func() time.Time { t.Fatal("legacy calibration retry read clock"); return time.Time{} }, de, oe)
	if err != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, old) {
		t.Fatal("legacy v2 retry changed", got, err)
	}
	receipt.Record = old
	return receipt
}
func assertNativeCalibration(t *testing.T, r PaperEvaluationAssessment, plan PaperEvaluationDeclarationCommand) {
	t.Helper()
	c := r.ForecastCalibration
	if c == nil || c.Method != "native-threshold-no-probability-forecast-v1" || c.OutputKind != "boolean_threshold_signal" || c.Status != "not_applicable" || c.ReasonCode != "compiled_threshold_predicate_has_no_probability_forecast" || !reflect.DeepEqual(c.Strategies, plan.Plan.Variants) || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("invalid native calibration applicability", r)
	}
	unsigned := clone(*c)
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != c.SHA256 {
		t.Fatal("unhashed calibration applicability")
	}
	count := 0
	for _, check := range r.Checks {
		if check.Name == "forecast_calibration" {
			count++
			if check.Status != c.Status || check.ReasonCode != c.ReasonCode {
				t.Fatal(check)
			}
		}
	}
	if count != len(plan.Plan.Variants) {
		t.Fatal("missing per-variant calibration check", r.Checks)
	}
}
func nativeCalibrationMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=55; CREATE FUNCTION reject_calibration_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=55 THEN RAISE EXCEPTION 'calibration migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_calibration_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_calibration_migration()`); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("migration failure ignored")
	}
	var max int
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&max); err != nil || max != 54 {
		t.Fatal("partial calibration migration", max, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_calibration_migration ON mantik_schema; DROP FUNCTION reject_calibration_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	var hash string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=55").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=55"); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("calibration checksum corruption accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=55", hash); err != nil {
		t.Fatal(err)
	}
}
