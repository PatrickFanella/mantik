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
func TestMemoryPaperEvaluationAssessmentsSelected(t *testing.T) {
	evaluationAssessmentSelectionContracts(t, NewMemory(), true)
}
func TestPostgresContractsPaperEvaluationAssessmentsSelected(t *testing.T) {
	evaluationAssessmentSelectionContracts(t, timerPostgres(t, "assessmentselected"), true)
}
func evaluationAssessmentContracts(t *testing.T, s assessmentBackend) {
	evaluationAssessmentSelectionContracts(t, s, false)
}
func evaluationAssessmentSelectionContracts(t *testing.T, s assessmentBackend, selectedFixture bool) {
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
	if selectedFixture {
		input.OccurredAt = start
	}
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
	// A real owner assessment issued before finalization cannot select a later
	// decision merely because it shares the declaration's strategy and policy.
	earlyCommand := PaperEvaluationAssessmentCommand{ID: "early", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}
	early, err := s.AssessPaperEvaluation(ctx, earlyCommand, func() time.Time { return start }, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeWarmUp(t, early.Record, plan)
	assertNativeCalibration(t, early.Record, plan)
	early = legacyAssessmentFixture(t, s, early, de, oe)
	dc := PaperDecisionCommand{ID: "first", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	first, err := s.FinalizePaperDecision(ctx, dc, func() time.Time { return start.Add(time.Nanosecond) }, de)
	if err != nil {
		t.Fatal(err)
	}
	earlyComparisonCommand := OwnedAllocationCommand{ID: "before-assessment-decision-selection", ReceiverID: plan.ReceiverID, Decisions: []OwnedDecisionPin{{ID: first.Record.Command.ID, SHA256: first.Record.SHA256}}}
	earlyComparison, e := s.CompareOwnedAllocation(ctx, earlyComparisonCommand, func() time.Time { return start.Add(2 * time.Nanosecond) }, de, oe)
	if e != nil {
		t.Fatal(e)
	}
	earlyReadiness := earlyComparison.Record.AdmissionChecks[0]
	if len(earlyReadiness.EvaluationEvidence) != 1 || earlyReadiness.EvaluationEvidence[0].AssessmentSHA256 != early.Record.SHA256 || earlyReadiness.EvaluationEvidence[0].DecisionEvidenceStatus != "decision_not_retained" || earlyReadiness.Gates[2].Status != "unavailable" || earlyReadiness.AdmissionAvailable {
		t.Fatal("same scope selected absent decision", earlyReadiness)
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
		if e != nil || len(all.Records) != 1 {
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
	assertNativeWarmUp(t, pending.Record, plan)
	assertNativeCalibration(t, pending.Record, plan)
	pending = legacyCalibrationFixture(t, s, pending, de, oe)
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
	comparisonCommand := OwnedAllocationCommand{ID: "assessment-comparison", ReceiverID: plan.ReceiverID, Decisions: []OwnedDecisionPin{{ID: first.Record.Command.ID, SHA256: first.Record.SHA256}}}
	comparison, err := s.CompareOwnedAllocation(ctx, comparisonCommand, func() time.Time { return start.Add(2 * time.Second) }, de, oe)
	if err != nil || comparison.Duplicate {
		t.Fatal("owner-bound comparison", comparison, err)
	}
	evaluationContext := comparison.Record.EvaluationContext
	if comparison.Record.Schema != "mantik-owned-allocation-deferral-v9" || evaluationContext.Schema != "mantik-owned-allocation-evaluation-context-v2" || evaluationContext.AssessmentSourceSHA256 == "" || len(evaluationContext.Decisions) != 1 || len(evaluationContext.Decisions[0].Assessments) != 2 || evaluationContext.Decisions[0].Assessments[1].SHA256 != pending.Record.SHA256 || evaluationContext.Decisions[0].Assessments[1].Status != "pending" || evaluationContext.AdmissionAvailable || evaluationContext.ExecutionAvailable || evaluationContext.QualificationCredit {
		t.Fatal("actual pending assessment context", evaluationContext)
	}
	// Neither equality nor a backdated new identity may enter a retained cut.
	late := c
	late.ID = "backdated-assessment"
	for _, at := range []time.Time{comparison.Record.At.Add(-time.Nanosecond), comparison.Record.At} {
		got, e := s.AssessPaperEvaluation(ctx, late, func() time.Time { return at }, de, oe)
		if !errors.Is(e, ErrPaperEvaluationAssessmentInvalid) || !reflect.DeepEqual(got, PaperEvaluationAssessmentReceipt{}) {
			t.Fatal("backdated assessment entered comparison", got, e)
		}
	}
	if got, e := s.AssessPaperEvaluation(ctx, c, noClock, de, oe); e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, pending.Record) {
		t.Fatal("assessment exact retry after comparison", got, e)
	}
	var journal ownedAllocationJournal
	switch b := s.(type) {
	case *Memory:
		journal = clone(b.ownedAllocations)
	case *Postgres:
		var raw []byte
		if e := b.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(raw, &journal); e != nil {
			t.Fatal(e)
		}
	}
	writeComparison := func(j ownedAllocationJournal) {
		switch b := s.(type) {
		case *Memory:
			b.ownedAllocations = clone(j)
		case *Postgres:
			raw, _ := json.Marshal(j)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1 WHERE singleton", raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, mutate := range []func(*OwnedAllocationEvaluationContext){
		func(v *OwnedAllocationEvaluationContext) {
			v.Decisions[0].Assessments = nil
			v.Decisions[0].Status = "declared_not_evaluated"
		},
		func(v *OwnedAllocationEvaluationContext) { v.Decisions[0].Assessments[0].Status = "incomplete" },
		func(v *OwnedAllocationEvaluationContext) {
			v.Decisions[0].Assessments[0].DeclarationSHA256 = strings.Repeat("f", 64)
		},
		func(v *OwnedAllocationEvaluationContext) { v.AssessmentSourceSHA256 = workerplan.Hash("omitted-owner") },
	} {
		damaged := clone(journal)
		v := &damaged.Records[0].Record
		mutate(v.EvaluationContext)
		v.SHA256 = ""
		v.SHA256 = workerplan.Hash(*v)
		writeComparison(damaged)
		if got, e := s.CompareOwnedAllocation(ctx, comparisonCommand, noClock, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("rehashed assessment binding retry", got, e)
		}
		if got, e := s.OwnedAllocations(ctx, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("rehashed assessment binding catalogue", got, e)
		}
		if got, e := s.(PaperAttributionStore).PaperAttribution(ctx, de, oe); e == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
			t.Fatal("rehashed assessment binding attribution", got, e)
		}
		writeComparison(journal)
	}
	readiness := comparison.Record.AdmissionChecks[0]
	expectedDecisionStatus, expectedGate := "quote_before_window", "unavailable"
	if selectedFixture {
		expectedDecisionStatus, expectedGate = "selected_research_evidence", "pending"
	}
	if readiness.Schema != "mantik-owned-admission-readiness-v3" || readiness.Binding.EvaluationBindingSHA256 != workerplan.Hash(evaluationContext.Decisions[0]) || len(readiness.EvaluationEvidence) != 1 || readiness.EvaluationEvidence[0].AssessmentSHA256 != pending.Record.SHA256 || readiness.EvaluationEvidence[0].DecisionEvidenceStatus != expectedDecisionStatus || readiness.Gates[2].Status != expectedGate {
		t.Fatal("exact decision evidence readiness", readiness)
	}
	admissionReviewContracts(t, s, comparison.Record, de, oe, selectedFixture)
	for _, mutate := range []func(*OwnedAdmissionReadiness){
		func(v *OwnedAdmissionReadiness) {
			v.EvaluationEvidence[0].DecisionEvidenceStatus = "quote_after_window"
		},
		func(v *OwnedAdmissionReadiness) { v.EvaluationEvidence[0].SampleStatus = "passed" },
		func(v *OwnedAdmissionReadiness) { v.EvaluationEvidence[0].EvidenceSHA256 = strings.Repeat("f", 64) },
		func(v *OwnedAdmissionReadiness) { v.EvaluationEvidence = nil },
		func(v *OwnedAdmissionReadiness) { v.Binding.EvaluationBindingSHA256 = strings.Repeat("f", 64) },
		func(v *OwnedAdmissionReadiness) { v.Gates[2].Status = "passed"; v.AdmissionAvailable = true },
	} {
		damaged := clone(journal)
		v := &damaged.Records[0].Record
		mutate(&v.AdmissionChecks[0])
		v.AdmissionChecks[0].SHA256 = ""
		v.AdmissionChecks[0].SHA256 = workerplan.Hash(v.AdmissionChecks[0])
		v.SHA256 = ""
		v.SHA256 = workerplan.Hash(*v)
		writeComparison(damaged)
		if got, e := s.CompareOwnedAllocation(ctx, comparisonCommand, noClock, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("rehashed decision readiness retry", got, e)
		}
		if got, e := s.OwnedAllocations(ctx, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("rehashed decision readiness catalogue", got, e)
		}
		if got, e := s.(PaperAttributionStore).PaperAttribution(ctx, de, oe); e == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
			t.Fatal("rehashed decision readiness attribution", got, e)
		}
		writeComparison(journal)
	}
	for _, callbacks := range [][]PaperOutcomeEvaluator{nil, {nil}, {oe, oe}} {
		got, e := s.CompareOwnedAllocation(ctx, comparisonCommand, noClock, de, callbacks...)
		if e == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("missing/invalid outcome owner returned retry", got, e)
		}
		all, e := s.OwnedAllocations(ctx, de, callbacks...)
		if e == nil || !reflect.DeepEqual(all, OwnedAllocationCatalog{}) {
			t.Fatal("missing/invalid outcome owner returned catalogue", all, e)
		}
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
		} else if check.Name == "forecast_calibration" {
			if check.Status != "not_applicable" || check.ReasonCode != "compiled_threshold_predicate_has_no_probability_forecast" {
				t.Fatal(check)
			}
		} else if check.Name == "warm_up" {
			if check.Status != "not_applicable" || check.ReasonCode != "compiled_threshold_predicate_has_no_lookback" {
				t.Fatal(check)
			}
		} else if check.Status != "unavailable" {
			t.Fatal(check)
		}
	}
	// Equal assessment clocks use the greatest immutable ID, independent of
	// catalogue iteration order. Both receipts remain retained and inspectable.
	tiedCommand := c
	tiedCommand.ID = "z-closed"
	tied, e := s.AssessPaperEvaluation(ctx, tiedCommand, func() time.Time { return deadline }, de, oe)
	if e != nil {
		t.Fatal(e)
	}
	closedComparisonCommand := comparisonCommand
	closedComparisonCommand.ID = "closed-assessment-comparison"
	closedComparison, e := s.CompareOwnedAllocation(ctx, closedComparisonCommand, func() time.Time { return deadline.Add(time.Nanosecond) }, de, oe)
	if e != nil {
		t.Fatal(e)
	}
	closedReadiness := closedComparison.Record.AdmissionChecks[0]
	expectedClosedGate := "unavailable"
	if selectedFixture {
		expectedClosedGate = "failed"
	}
	if len(closedReadiness.EvaluationEvidence) != 1 || closedReadiness.EvaluationEvidence[0].AssessmentSHA256 != tied.Record.SHA256 || closedReadiness.Gates[2].Status != expectedClosedGate || closedReadiness.Status != "expired" || closedReadiness.AdmissionAvailable || closedReadiness.ExecutionAvailable || closedReadiness.QualificationCredit {
		t.Fatal("latest assessment necessary failure", closedReadiness)
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
	if _, err = s.DeclarePaperEvaluation(ctx, np, func() time.Time { return deadline }); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
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
	if err != nil || len(all.Records) != 4 {
		t.Fatal(all, err)
	}
	for _, record := range all.Records {
		want := closed.Record
		switch record.Command.ID {
		case "pending":
			want = pending.Record
		case "early":
			want = early.Record
		case "z-closed":
			want = tied.Record
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
		r.ForecastCalibration.OutputKind = "probability_forecast"
		r.ForecastCalibration.Strategies[0].SHA256 = strings.Repeat("f", 64)
		r.ForecastCalibration.SHA256 = ""
		r.ForecastCalibration.SHA256 = workerplan.Hash(*r.ForecastCalibration)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
		r.ForecastCalibration.Status = "passed"
		r.ForecastCalibration.SHA256 = ""
		r.ForecastCalibration.SHA256 = workerplan.Hash(*r.ForecastCalibration)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
		r.ForecastCalibration = nil
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
		r.WarmUp.RequiredLookbackObservations = 10
		r.WarmUp.SHA256 = ""
		r.WarmUp.SHA256 = workerplan.Hash(*r.WarmUp)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
		r.WarmUp.Strategies[0].Command.Definition.EntryAtOrBelowCents--
		r.WarmUp.SHA256 = ""
		r.WarmUp.SHA256 = workerplan.Hash(*r.WarmUp)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}, func(r *PaperEvaluationAssessment) {
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
		if got, e := s.CompareOwnedAllocation(ctx, comparisonCommand, noClock, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("corrupt assessment returned allocation retry", got, e)
		}
		if got, e := s.OwnedAllocations(ctx, de, oe); e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("corrupt assessment returned allocation catalogue", got, e)
		}
		if got, e := s.(PaperAttributionStore).PaperAttribution(ctx, de, oe); e == nil || !reflect.DeepEqual(got, PaperAttributionCatalog{}) {
			t.Fatal("corrupt assessment returned attribution", got, e)
		}

		set(saved)
	}
	// Later legitimate assessments do not rewrite or reissue a past comparison.
	retry, e := s.CompareOwnedAllocation(ctx, comparisonCommand, noClock, de, oe)
	if e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, comparison.Record) {
		t.Fatal("owner replay rewrote comparison", retry, e)
	}
	wrongOutcome := func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		v, e := oe(b, at)
		v.ReasonCode = "different_compiled_recipe"
		return v, e
	}
	if got, e := s.OwnedAllocations(ctx, de, wrongOutcome); e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
		t.Fatal("wrong outcome owner accepted", got, e)
	}
	var compareErr, assessmentErr error
	var compared OwnedAllocationReceipt
	concurrentCommand := comparisonCommand
	concurrentCommand.ID = "assessment-race-comparison"
	concurrentAssessment := c
	concurrentAssessment.ID = "comparison-raced-assessment"
	wg.Add(2)
	go func() {
		defer wg.Done()
		compared, compareErr = s.CompareOwnedAllocation(ctx, concurrentCommand, func() time.Time { return raceAt.Add(time.Second) }, de, oe)
	}()
	go func() {
		defer wg.Done()
		_, assessmentErr = s.AssessPaperEvaluation(ctx, concurrentAssessment, func() time.Time { return raceAt.Add(time.Second) }, de, oe)
	}()
	wg.Wait()
	if assessmentErr != nil && errors.Is(assessmentErr, ErrPaperEvaluationAssessmentInvalid) {
		_, assessmentErr = s.AssessPaperEvaluation(ctx, concurrentAssessment, func() time.Time { return raceAt.Add(time.Second + time.Nanosecond) }, de, oe)
	}
	if compareErr != nil || assessmentErr != nil {
		t.Fatal("comparison/assessment owner race", compareErr, assessmentErr)
	}
	if got, e := s.CompareOwnedAllocation(ctx, concurrentCommand, noClock, de, oe); e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, compared.Record) {
		t.Fatal("raced comparison retry", got, e)
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
		if e != nil || len(got.Records) != 6 {
			t.Fatal("reopen lost assessments", got, e)
		}
		evaluationAssessmentMigrationContract(t, pg)
		allocationAssessmentContextMigrationContract(t, pg)
		assessmentDecisionReadinessMigrationContract(t, pg)
		nativeWarmUpMigrationContract(t, pg)
		nativeCalibrationMigrationContract(t, pg)
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
	if _, err := upgrade.pool.Exec(ctx, `DROP TABLE mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DELETE FROM mantik_schema WHERE version>=50; CREATE FUNCTION reject_assessment_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=50 THEN RAISE EXCEPTION 'assessment migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assessment_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_assessment_migration()`); err != nil {
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

func allocationAssessmentContextMigrationContract(t *testing.T, p *Postgres) {
	ctx := context.Background()
	var before []byte
	if e := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&before); e != nil {
		t.Fatal(e)
	}
	upgrade := timerPostgres(t, "assessmentcontextupgrade")
	if _, e := upgrade.pool.Exec(ctx, `DROP TABLE IF EXISTS mantik_owned_admission_reviews; DELETE FROM mantik_schema WHERE version>=51; COMMENT ON TABLE mantik_owned_allocations IS 'schema50 fixture'; CREATE FUNCTION reject_assessment_context_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=51 THEN RAISE EXCEPTION 'assessment context migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assessment_context_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_assessment_context_migration()`); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e == nil {
		t.Fatal("migration failure accepted")
	}
	var marker string
	if e := upgrade.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); e != nil || marker != "schema50 fixture" {
		t.Fatal("migration comment not rolled back", marker, e)
	}
	if _, e := upgrade.pool.Exec(ctx, "DROP TRIGGER reject_assessment_context_migration ON mantik_schema; DROP FUNCTION reject_assessment_context_migration()"); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var digest string
	if e := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=51").Scan(&digest); e != nil {
		t.Fatal(e)
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=51"); e != nil {
		t.Fatal(e)
	}
	if e := p.migrate(ctx); e == nil {
		t.Fatal("context fence checksum ignored")
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=51", digest); e != nil {
		t.Fatal(e)
	}
	var after []byte
	if e := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote allocation", e)
	}
}

func assessmentDecisionReadinessMigrationContract(t *testing.T, p *Postgres) {
	ctx := context.Background()
	var before []byte
	if e := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&before); e != nil {
		t.Fatal(e)
	}
	upgrade := timerPostgres(t, "assessmentreadinessupgrade")
	if _, e := upgrade.pool.Exec(ctx, `DROP TABLE IF EXISTS mantik_owned_admission_reviews; DELETE FROM mantik_schema WHERE version>=52; COMMENT ON TABLE mantik_owned_allocations IS 'schema51 fixture'; CREATE FUNCTION reject_assessment_readiness_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=52 THEN RAISE EXCEPTION 'assessment context migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assessment_readiness_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_assessment_readiness_migration()`); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e == nil {
		t.Fatal("migration failure accepted")
	}
	var marker string
	if e := upgrade.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); e != nil || marker != "schema51 fixture" {
		t.Fatal("migration comment not rolled back", marker, e)
	}
	if _, e := upgrade.pool.Exec(ctx, "DROP TRIGGER reject_assessment_readiness_migration ON mantik_schema; DROP FUNCTION reject_assessment_readiness_migration()"); e != nil {
		t.Fatal(e)
	}
	if e := upgrade.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var digest string
	if e := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=52").Scan(&digest); e != nil {
		t.Fatal(e)
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=52"); e != nil {
		t.Fatal(e)
	}
	if e := p.migrate(ctx); e == nil {
		t.Fatal("context fence checksum ignored")
	}
	if _, e := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=52", digest); e != nil {
		t.Fatal(e)
	}
	var after []byte
	if e := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote allocation", e)
	}
}
