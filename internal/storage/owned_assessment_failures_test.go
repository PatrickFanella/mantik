package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"testing"
	"time"
)

// Rewrite only a test fixture's current comparison to the previous compiled
// representation, then exercise actual owner replay/retry and PostgreSQL reopen.
func legacyAssessmentFailureBindingReplay(t *testing.T, s assessmentBackend, current OwnedAllocationRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) {
	t.Helper()
	for _, schema := range []string{"mantik-owned-allocation-deferral-v9", "mantik-owned-allocation-deferral-v13"} {
		t.Run(schema, func(t *testing.T) {
			ctx := context.Background()
			var journal ownedAllocationJournal
			switch b := s.(type) {
			case *Memory:
				journal = clone(b.ownedAllocations)
			case *Postgres:
				var raw []byte
				if err := b.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &journal); err != nil {
					t.Fatal(err)
				}
			}
			original := clone(journal)
			actual, err := s.PaperEvaluationAssessments(ctx, de, oe)
			if err != nil {
				t.Fatal(err)
			}
			old := clone(current)
			old.Schema = schema
			old.Authority, old.Expirations = "service_owned_allocation_deferral_only", nil
			if schema == "mantik-owned-allocation-deferral-v9" {
				old.ExecutionSafety = nil
			}
			found := false
			for i := range journal.Records {
				item := &journal.Records[i]
				if item.Record.Command.ID != current.Command.ID {
					continue
				}
				var policy allocation.PolicyRecord
				for _, p := range item.Policies.Records {
					if p.SHA256 == current.PolicySHA256 {
						policy = p
					}
				}
				old.AdmissionChecks, err = bindOwnedAssessmentReadiness(old, policy, actual)
				if err != nil {
					t.Fatal(err)
				}
				old.SHA256 = ""
				old.SHA256 = workerplan.Hash(old)
				item.Record = old
				found = true
			}
			expectedReadiness := "mantik-owned-admission-readiness-v3"
			if schema == "mantik-owned-allocation-deferral-v13" {
				expectedReadiness = "mantik-owned-admission-readiness-v5"
			}
			if !found || old.AdmissionChecks[0].Schema != expectedReadiness {
				t.Fatal("missing legacy comparison")
			}
			set := func(j ownedAllocationJournal) {
				switch b := s.(type) {
				case *Memory:
					b.ownedAllocations = clone(j)
				case *Postgres:
					raw, e := json.Marshal(j)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = b.pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1 WHERE singleton", raw); e != nil {
						t.Fatal(e)
					}
				}
			}
			set(journal)
			defer set(original)
			check := func(b OwnedAllocationStore) {
				got, e := b.CompareOwnedAllocation(ctx, old.Command, func() time.Time { t.Fatal("legacy v9 retry read clock"); return time.Time{} }, de, oe)
				if e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, old) {
					t.Fatal("legacy v9 replay changed", got, e)
				}
			}
			check(s)
			if b, ok := s.(*Postgres); ok {
				reopened, e := OpenPostgres(ctx, b.pool.Config().ConnString())
				if e != nil {
					t.Fatal(e)
				}
				defer reopened.Close()
				check(reopened)
			}

		})
	}
}

func TestOwnedAssessmentFailuresDominatePending(t *testing.T) {
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
	got, err := bindOwnedAssessmentReadiness(r, policy, actual)
	if err != nil || len(got) != 1 || got[0].Schema != "mantik-owned-admission-readiness-v7" || got[0].Gates[2].Status != "failed" || got[0].Gates[2].ReasonCode != "retained_necessary_evaluation_check_failed" || got[0].EvaluationEvidence[0].SampleStatus != "pending" || len(got[0].EvaluationEvidence[0].FailedChecks) != 1 || got[0].AdmissionAvailable || got[0].ExecutionAvailable || got[0].QualificationCredit {
		t.Fatal(got, err)
	}
	legacy := clone(r)
	legacy.Schema = "mantik-owned-allocation-deferral-v9"
	legacy.Authority, legacy.Expirations = "service_owned_allocation_deferral_only", nil
	old, err := bindOwnedAssessmentReadiness(legacy, policy, actual)
	if err != nil || old[0].Schema != "mantik-owned-admission-readiness-v3" || old[0].Gates[2].Status != "pending" || len(old[0].EvaluationEvidence[0].FailedChecks) != 0 {
		t.Fatal("legacy semantics changed", old, err)
	}
	r.AdmissionChecks = got
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	c := OwnedAdmissionReviewCommand{ID: "failure-review", ReceiverID: m.receiverID, ComparisonID: r.Command.ID, ComparisonSHA256: r.SHA256, DecisionID: got[0].DecisionID, DecisionSHA256: got[0].DecisionSHA256}
	source := OwnedAllocationCatalog{ReceiverID: m.receiverID, Records: []OwnedAllocationRecord{r}}
	review, err := newOwnedAdmissionReview(c, source, 1, r.At.Add(time.Nanosecond))
	if err != nil || review.Status != "blocked" || review.AdmissionAvailable || review.ExecutionAvailable || review.QualificationCredit {
		t.Fatal(review, err)
	}
	expired, err := newOwnedAdmissionReview(c, source, 1, got[0].ValidUntil)
	if err != nil || expired.Status != "expired" {
		t.Fatal("expiry precedence changed", expired, err)
	}
}

func assessmentFailureBindingMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY singleton),'[]'::jsonb)::text FROM mantik_owned_allocations"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=62; COMMENT ON TABLE mantik_owned_allocations IS 'schema61 failure binding fixture'; CREATE FUNCTION reject_failure_binding_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=62 THEN RAISE EXCEPTION 'failure binding migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_failure_binding_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_failure_binding_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); err != nil || marker != "schema61 failure binding fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_failure_binding_migration ON mantik_schema; DROP FUNCTION reject_failure_binding_migration()"); err != nil {
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
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=62").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=62"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("failure binding migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=62", hash); err != nil {
		t.Fatal(err)
	}
}
