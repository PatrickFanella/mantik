package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type assessmentBackend interface {
	evaluationEvidenceBackend
	PaperEvaluationAssessmentStore
}

func TestMemoryPaperEvaluationAssessments(t *testing.T) {
	evaluationAssessmentContracts(t, NewMemory())
}
func TestPostgresContractsPaperEvaluationAssessments(t *testing.T) {
	evaluationAssessmentContracts(t, timerPostgres(t, "evaluationassessments"))
}
func evaluationAssessmentContracts(t *testing.T, s assessmentBackend) {
	ctx := context.Background()
	plan, at := declarationFixture(t, s, "prospective_market_unqualified")
	declaration, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	start := plan.Plan.WindowStart
	reg := feedcollector.Registration{ID: "assessment-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, plan.ReceiverID, c, func() time.Time { return start.Add(-time.Second) }); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(start, "assessment-quote")
	input.EvidenceClass = reg.EvidenceClass
	if _, err = s.SubmitCollectorResearch(ctx, plan.ReceiverID, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}, input, func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	de := func(_ PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_fixture_only", PriceCents: b.Feed.Routing.Inputs[input.ID].Input.PriceCents, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	oe := func(b PaperOutcomeBasis, _ time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "waiting_price", ReasonCode: "fixture_no_economic_result", DueAt: b.Decision.FinalizedAt.Add(time.Hour), WindowEnd: b.Decision.FinalizedAt.Add(2 * time.Hour)}, nil
	}
	dc := PaperDecisionCommand{ID: "first", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	first, err := s.FinalizePaperDecision(ctx, dc, func() time.Time { return start }, de)
	if err != nil {
		t.Fatal(err)
	}
	c := PaperEvaluationAssessmentCommand{ID: "pending", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}
	clockReads := 0
	clock := func() time.Time { clockReads++; return start.Add(time.Second) }
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_assessment_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'assessment commit fixture rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_assessment_commit AFTER INSERT ON mantik_paper_evaluation_assessments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_assessment_commit()`); err != nil {
			t.Fatal(err)
		}
		failed, e := s.AssessPaperEvaluation(ctx, c, clock, de, oe)
		if e == nil || !reflect.DeepEqual(failed, PaperEvaluationAssessmentReceipt{}) {
			t.Fatal("commit failure acknowledged", failed, e)
		}
		all, e := s.PaperEvaluationAssessments(ctx, de, oe)
		if e != nil || len(all.Records) != 0 {
			t.Fatal("commit failure persisted", all, e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_assessment_commit ON mantik_paper_evaluation_assessments; DROP FUNCTION reject_assessment_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	clockReads = 0
	pending, err := s.AssessPaperEvaluation(ctx, c, clock, de, oe)
	if err != nil || clockReads != 1 || pending.Duplicate || pending.Record.Status != "pending" {
		t.Fatal(pending, clockReads, err)
	}
	noClock := func() time.Time { t.Fatal("retry consulted clock"); return time.Time{} }
	duplicate, err := s.AssessPaperEvaluation(ctx, c, noClock, de, oe)
	if err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, pending.Record) {
		t.Fatal(duplicate, err)
	}
	wrong := c
	wrong.DeclarationSHA256 = strings.Repeat("f", 64)
	if _, err = s.AssessPaperEvaluation(ctx, wrong, noClock, de, oe); !errors.Is(err, ErrPaperEvaluationAssessmentConflict) {
		t.Fatal("changed retry", err)
	}
	before, _ := s.Snapshot(ctx)
	deadline := plan.Plan.OutcomeDeadline
	c.ID = "closed"
	closed, err := s.AssessPaperEvaluation(ctx, c, func() time.Time { return deadline }, de, oe)
	if err != nil || closed.Record.Status != "blocked" || closed.Record.EvaluationComplete || closed.Record.AdmissionAvailable || closed.Record.ExecutionAvailable || closed.Record.QualificationCredit || len(closed.Record.Checks) != len(plan.Plan.Variants)*8 {
		t.Fatal(closed, err)
	}
	for _, check := range closed.Record.Checks {
		if check.Name == "sample_sufficiency" {
			if check.Status != "failed" {
				t.Fatal(check)
			}
		} else if check.Status != "unavailable" {
			t.Fatal(check)
		}
	}
	// Equal and earlier source writes fail before mutation and leave IDs reusable.
	for _, when := range []time.Time{deadline.Add(-time.Nanosecond), deadline} {
		next := dc
		next.ID = "after-cut"
		r, e := s.FinalizePaperDecision(ctx, next, func() time.Time { return when }, de)
		if !errors.Is(e, ErrPaperEvaluationAssessmentConflict) || !reflect.DeepEqual(r, PaperDecisionReceipt{}) {
			t.Fatal("backdated decision", r, e)
		}
		oc := PaperOutcomeCommand{ID: "after-cut-outcome", ReceiverID: dc.ReceiverID, DecisionID: first.Record.Command.ID, DecisionSHA256: first.Record.SHA256}
		o, e := s.AssessPaperOutcome(ctx, oc, func() time.Time { return when }, de, oe)
		if !errors.Is(e, ErrPaperEvaluationAssessmentConflict) || !reflect.DeepEqual(o, PaperOutcomeReceipt{}) {
			t.Fatal("backdated outcome", o, e)
		}
	}
	later := dc
	later.ID = "after-cut"
	if _, err = s.FinalizePaperDecision(ctx, later, func() time.Time { return deadline.Add(time.Nanosecond) }, de); err != nil {
		t.Fatal(err)
	}
	oc := PaperOutcomeCommand{ID: "after-cut-outcome", ReceiverID: dc.ReceiverID, DecisionID: first.Record.Command.ID, DecisionSHA256: first.Record.SHA256}
	if _, err = s.AssessPaperOutcome(ctx, oc, func() time.Time { return deadline.Add(2 * time.Nanosecond) }, de, oe); err != nil {
		t.Fatal(err)
	}
	np := clone(plan)
	np.ID = "later-plan"
	np.Plan.FamilyID = "later-family"
	np.Plan.WindowStart = deadline.Add(time.Hour)
	np.Plan.WindowEnd = deadline.Add(2 * time.Hour)
	np.Plan.OutcomeDeadline = deadline.Add(4 * time.Hour)
	if _, err = s.DeclarePaperEvaluation(ctx, np, func() time.Time { return deadline }); !errors.Is(err, ErrPaperEvaluationAssessmentConflict) {
		t.Fatal("backdated declaration", err)
	}
	if _, err = s.DeclarePaperEvaluation(ctx, np, func() time.Time { return deadline.Add(3 * time.Nanosecond) }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeclarePaperEvaluation(ctx, plan, noClock); err != nil {
		t.Fatal("historical declaration retry", err)
	}
	if _, err = s.FinalizePaperDecision(ctx, dc, noClock, de); err != nil {
		t.Fatal("historical decision retry", err)
	}
	all, err := s.PaperEvaluationAssessments(ctx, de, oe)
	if err != nil || len(all.Records) != 2 {
		t.Fatal(all, err)
	}
	for _, record := range all.Records {
		want := closed.Record
		if record.Command.ID == "pending" {
			want = pending.Record
		}
		if !reflect.DeepEqual(want, record) {
			t.Fatal("later source rewrote assessment", record.Command.ID)
		}
	}
	// Assessment and source append at one clock serialize: include the new source
	// or reject its timestamp. Both outcomes must retain a replayable catalogue.
	raceAt := deadline.Add(time.Second)
	raceCommand := c
	raceCommand.ID = "raced"
	source := dc
	source.ID = "raced-source"
	var wg sync.WaitGroup
	var ar PaperEvaluationAssessmentReceipt
	var ae, se error
	wg.Add(2)
	go func() {
		defer wg.Done()
		ar, ae = s.AssessPaperEvaluation(ctx, raceCommand, func() time.Time { return raceAt }, de, oe)
	}()
	go func() {
		defer wg.Done()
		_, se = s.FinalizePaperDecision(ctx, source, func() time.Time { return raceAt }, de)
	}()
	wg.Wait()
	if ae != nil || se != nil && !errors.Is(se, ErrPaperEvaluationAssessmentConflict) {
		t.Fatal("source race", ae, se)
	}
	found := false
	for _, row := range ar.Record.Evidence.Rows {
		if row.Decision.Command.ID == source.ID {
			found = true
		}
	}
	if (se == nil) != found {
		t.Fatal("source cut omitted committed decision", se, found)
	}
	// Rehashing a fabricated check or deleting retained evidence cannot replace
	// service-owned replay. Exact retries fail as well as catalogue reads.
	saved := closed.Record
	set := func(record PaperEvaluationAssessment) {
		switch b := s.(type) {
		case *Memory:
			b.mu.Lock()
			b.evaluationAssessments[c.ID] = clone(record)
			b.mu.Unlock()
		case *Postgres:
			raw, _ := json.Marshal(record)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, c.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, change := range []func(*PaperEvaluationAssessment){func(r *PaperEvaluationAssessment) {
		r.Checks[0].Status = "passed"
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
		r.Evidence.Rows = r.Evidence.Rows[:0]
		r.Evidence.SHA256 = ""
		r.Evidence.SHA256 = workerplan.Hash(r.Evidence)
		v, e := newPaperEvaluationAssessment(r.Command, r.Evidence)
		if e != nil {
			t.Fatal(e)
		}
		*r = v
	}} {
		damaged := clone(saved)
		change(&damaged)
		set(damaged)
		if got, e := s.PaperEvaluationAssessments(ctx, de, oe); e == nil || len(got.Records) != 0 {
			t.Fatal("rehashed corruption accepted", got, e)
		}
		if got, e := s.AssessPaperEvaluation(ctx, c, noClock, de, oe); e == nil || !reflect.DeepEqual(got, PaperEvaluationAssessmentReceipt{}) {
			t.Fatal("corrupt retry accepted", got, e)
		}
		set(saved)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	c.ID = "cancelled"
	if _, err = s.AssessPaperEvaluation(cancelled, c, noClock, de, oe); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("assessment changed operational state")
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		got, e := reopened.PaperEvaluationAssessments(ctx, de, oe)
		if e != nil || len(got.Records) != 3 {
			t.Fatal("reopen lost assessments", got, e)
		}
		evaluationAssessmentMigrationContract(t, pg)
	}
}
func TestPaperEvaluationAssessmentCountCannotGrant(t *testing.T) {
	for _, observed := range []int{0, 2, 1000} {
		e := PaperEvaluationEvidence{Schema: "mantik-owned-paper-evaluation-evidence-v3", ReceiverID: "receiver-abcdefghijklmnop", AsOf: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Phase: "outcome_deadline_elapsed", Declaration: PaperEvaluationDeclaration{Command: PaperEvaluationDeclarationCommand{ID: "plan"}, SHA256: strings.Repeat("a", 64), PlannedChecks: []string{"sample_sufficiency", "leakage"}}, Variants: []PaperEvaluationVariantCount{{StrategyID: "strategy", SampleBound: paperEvaluationSampleBound(2, observed, true)}}}
		e.SHA256 = workerplan.Hash(e)
		c := PaperEvaluationAssessmentCommand{ID: "assessment", ReceiverID: e.ReceiverID, DeclarationID: "plan", DeclarationSHA256: e.Declaration.SHA256}
		r, err := newPaperEvaluationAssessment(c, e)
		if err != nil || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
			t.Fatal(r, err)
		}
		if observed < 2 && r.Checks[0].Status != "failed" || observed >= 2 && r.Checks[0].Status != "unavailable" {
			t.Fatal(r.Checks)
		}
	}
}
func evaluationAssessmentMigrationContract(t *testing.T, p *Postgres) {
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_declarations ORDER BY id LIMIT 1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Migration rollback uses a separate disposable database so issued receipts
	// and the reopened backend remain intact and independently readable.
	upgrade := timerPostgres(t, "assessmentupgrade")
	if _, err := upgrade.pool.Exec(ctx, `DROP TABLE mantik_paper_evaluation_assessments; DELETE FROM mantik_schema WHERE version=50; CREATE FUNCTION reject_assessment_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=50 THEN RAISE EXCEPTION 'assessment migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assessment_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_assessment_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.migrate(ctx); err == nil {
		t.Fatal("migration failure accepted")
	}
	var exists bool
	if err := upgrade.pool.QueryRow(ctx, "SELECT to_regclass('mantik_paper_evaluation_assessments') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("DDL not rolled back", exists, err)
	}
	if _, err := upgrade.pool.Exec(ctx, "DROP TRIGGER reject_assessment_migration ON mantik_schema; DROP FUNCTION reject_assessment_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=50").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=50"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("checksum change accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=50", digest); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_declarations ORDER BY id LIMIT 1").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote declaration", err)
	}
}
