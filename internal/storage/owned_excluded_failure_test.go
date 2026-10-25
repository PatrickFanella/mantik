package storage

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"testing"
	"time"
)

func TestOwnedAssessmentExcludedFailureRetained(t *testing.T) {
	m := NewMemory()
	evaluationAssessmentSelectionContracts(t, m, true)
	var r OwnedAllocationRecord
	var policy allocation.PolicyRecord
	for _, item := range m.ownedAllocations.Records {
		if len(item.Record.AdmissionChecks) != 1 {
			continue
		}
		c := item.Record.AdmissionChecks[0]
		if len(c.EvaluationEvidence) != 1 || c.EvaluationEvidence[0].DecisionEvidenceStatus != "selected_research_evidence" || c.EvaluationEvidence[0].SampleStatus != "pending" {
			continue
		}
		r = clone(item.Record)
		for _, p := range item.Policies.Records {
			if p.SHA256 == r.PolicySHA256 {
				policy = p
			}
		}
		break
	}
	if r.Schema == "" {
		t.Fatal("missing owner comparison fixture")
	}
	target := r.AdmissionChecks[0].EvaluationEvidence[0].AssessmentID
	actual := PaperEvaluationAssessmentCatalog{ReceiverID: m.receiverID, Records: []PaperEvaluationAssessment{}}
	// This negative binder fixture substitutes a failed compiled check and its
	// exact pin. It is not accepted by public storage; separate owner-corruption
	// contracts require actual recipe rederivation and reject fabricated failures.
	for _, a := range m.evaluationAssessments {
		a = clone(a)
		if a.Command.ID == target {
			for i := range a.Checks {
				if a.Checks[i].StrategyID == r.AdmissionChecks[0].StrategyID && a.Checks[i].Name == "leakage" {
					a.Checks[i].Status = "failed"
					a.Checks[i].ReasonCode = "retained_candidate_source_unavailable_at_finalization"
				}
			}
			a.SHA256 = ""
			a.SHA256 = workerplan.Hash(a)
			for i := range r.EvaluationContext.Decisions {
				for j := range r.EvaluationContext.Decisions[i].Assessments {
					pin := &r.EvaluationContext.Decisions[i].Assessments[j]
					if pin.ID == target {
						pin.SHA256 = a.SHA256
					}
				}
			}
		}
		actual.Records = append(actual.Records, a)
	}
	for i := range actual.Records {
		a := &actual.Records[i]
		if a.Command.ID != target {
			continue
		}
		for j := range a.Evidence.Rows {
			if a.Evidence.Rows[j].Decision.Command.ID == r.AdmissionChecks[0].DecisionID {
				a.Evidence.Rows[j].Status = "source_unavailable_at_decision"
			}
		}
		a.SHA256 = ""
		a.SHA256 = workerplan.Hash(*a)
		for j := range r.EvaluationContext.Decisions {
			for k := range r.EvaluationContext.Decisions[j].Assessments {
				pin := &r.EvaluationContext.Decisions[j].Assessments[k]
				if pin.ID == target {
					pin.SHA256 = a.SHA256
				}
			}
		}
	}
	got, err := bindOwnedAssessmentReadiness(r, policy, actual)
	if err != nil || len(got) != 1 || got[0].Schema != "mantik-owned-admission-readiness-v6" || got[0].Gates[2].Status != "failed" || got[0].Gates[2].ReasonCode != "retained_necessary_evaluation_check_failed" || got[0].EvaluationEvidence[0].SampleStatus != "pending" || len(got[0].EvaluationEvidence[0].FailedChecks) != 1 || got[0].AdmissionAvailable || got[0].ExecutionAvailable || got[0].QualificationCredit {
		t.Fatal(got, err)
	}
	legacy := clone(r)
	legacy.Schema = "mantik-owned-allocation-deferral-v13"
	old, err := bindOwnedAssessmentReadiness(legacy, policy, actual)
	if err != nil || old[0].Schema != "mantik-owned-admission-readiness-v5" || old[0].Gates[2].Status != "unavailable" || len(old[0].EvaluationEvidence[0].FailedChecks) != 1 {
		t.Fatal("legacy excluded failure semantics changed", old, err)
	}
	r.AdmissionChecks = got
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	command := OwnedAdmissionReviewCommand{ID: "excluded-failure-review", ReceiverID: m.receiverID, ComparisonID: r.Command.ID, ComparisonSHA256: r.SHA256, DecisionID: got[0].DecisionID, DecisionSHA256: got[0].DecisionSHA256}
	source := OwnedAllocationCatalog{ReceiverID: m.receiverID, Records: []OwnedAllocationRecord{r}}
	review, err := newOwnedAdmissionReview(command, source, 1, r.At.Add(time.Nanosecond))
	if err != nil || review.Status != "blocked" || review.AdmissionAvailable || review.ExecutionAvailable || review.QualificationCredit {
		t.Fatal("excluded source failure did not block review", review, err)
	}
	for _, status := range []string{"decision_not_retained", "decision_before_window", "decision_after_window", "quote_before_window", "quote_after_window", "abstention", "synthetic_not_selected"} {
		other := clone(actual)
		for i := range other.Records {
			a := &other.Records[i]
			if a.Command.ID != target {
				continue
			}
			for j := range a.Evidence.Rows {
				if a.Evidence.Rows[j].Decision.Command.ID == got[0].DecisionID {
					a.Evidence.Rows[j].Status = status
				}
			}
		}
		// Negative binder fixtures deliberately keep selected pins. Public owner
		// replay rederives these envelopes and rejects such caller substitutions.
		bound, e := bindOwnedAssessmentReadiness(r, policy, other)
		if e != nil || bound[0].Gates[2].Status != "unavailable" {
			t.Fatal("unrelated exclusion inherited a failure", status, bound, e)
		}
	}

}

func excludedAssessmentFailureMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY singleton),'[]'::jsonb)::text FROM mantik_owned_allocations"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=66; COMMENT ON TABLE mantik_owned_allocations IS 'schema65 excluded assessment failure fixture'; CREATE FUNCTION reject_excluded_assessment_failure_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=66 THEN RAISE EXCEPTION 'excluded assessment failure migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_excluded_assessment_failure_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_excluded_assessment_failure_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); err != nil || marker != "schema65 excluded assessment failure fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_excluded_assessment_failure_migration ON mantik_schema; DROP FUNCTION reject_excluded_assessment_failure_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote allocation payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=66").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=66"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("excluded assessment failure migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=66", hash); err != nil {
		t.Fatal(err)
	}
}
