package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// eligibilityFixture reuses the selected assessment fixture's real decision,
// declaration, policy and strategy. Real assessments only exist after the
// outcome deadline, when that decision's proposal has expired, so the test
// substitutes an owner catalogue holding one complete assessment timed before
// the comparison. Public storage would refuse it; this exercises the actual
// comparison owner and replay with that catalogue.
type eligibilityFixture struct {
	m        *Memory
	sources  func() (ownedAllocationJournal, error)
	command  OwnedAllocationCommand
	at       time.Time
	complete PaperEvaluationAssessment
	strategy string
}

func newEligibilityFixture(t *testing.T) eligibilityFixture {
	t.Helper()
	fixtureCapitalScale = 4
	defer func() { fixtureCapitalScale = 1 }()
	m := NewMemory()
	evaluationAssessmentSelectionContracts(t, m, true)
	first := m.ownedAllocations.Records[0].Record
	if len(first.AdmissionChecks) != 1 || first.EvaluationContext == nil || len(first.EvaluationContext.Decisions[0].Declarations) != 1 {
		t.Fatal("missing selected comparison fixture")
	}
	check := first.AdmissionChecks[0]
	declaration := first.EvaluationContext.Decisions[0].Declarations[0]
	var base *PaperEvaluationAssessment
	for i := range m.evaluationAssessments {
		a := m.evaluationAssessments[i]
		if a.Command.DeclarationID == declaration.ID && (base == nil || a.AssessedAt.After(base.AssessedAt)) {
			base = &a
		}
	}
	if base == nil {
		t.Fatal("missing declaration assessment")
	}
	complete := clone(*base)
	complete.Command.ID = "complete-assessment"
	complete.AssessedAt = first.At.Add(-time.Nanosecond)
	complete.Status, complete.EvaluationComplete, complete.Recipe, complete.CompleteStrategyIDs = "complete", true, PaperStatisticalAssessmentRecipe, []string{check.StrategyID}
	for i := range complete.Checks {
		if complete.Checks[i].Status != "not_applicable" {
			complete.Checks[i].Status, complete.Checks[i].ReasonCode = "passed", "fixture_complete_check"
		}
	}
	complete.SHA256 = ""
	complete.SHA256 = workerplan.Hash(complete)
	return eligibilityFixture{m: m, command: OwnedAllocationCommand{ID: "grant", ReceiverID: m.receiverID, Decisions: first.Command.Decisions}, at: first.At, complete: complete, strategy: check.StrategyID}
}

func (f eligibilityFixture) decide(c PaperDecisionCommand, _ PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
	if r, ok := f.m.paperDecisions[c.ID]; ok {
		return r.Record.Evaluation, nil
	}
	return PaperDecisionEvaluation{}, errors.New("unknown fixture decision")
}

func (f eligibilityFixture) append(t *testing.T, journal ownedAllocationJournal, c OwnedAllocationCommand, at time.Time, assessments ...PaperEvaluationAssessment) (ownedAllocationJournal, OwnedAllocationReceipt, error) {
	t.Helper()
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture declares a later plan; a real comparison clock never
	// precedes a retained declaration, so pass the catalogue as of this clock.
	available := declarations
	available.Records = nil
	for _, d := range declarations.Records {
		if !d.DeclaredAt.After(at) {
			available.Records = append(available.Records, d)
		}
	}
	declarations = available
	catalog := PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: assessments}
	return appendOwnedAllocation(context.Background(), f.m.receiverID, journal, policies, decisions, strategies, f.m.paperDecisions, c, func() time.Time { return at }, time.Time{}, declarations, catalog)
}

func (f eligibilityFixture) replay(t *testing.T, journal ownedAllocationJournal, assessments ...PaperEvaluationAssessment) (OwnedAllocationCatalog, error) {
	t.Helper()
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	return replayOwnedAllocations(context.Background(), f.m.receiverID, journal, policies, decisions, strategies, f.m.paperDecisions, declarations, PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: assessments})
}

func TestOwnedEligibilityReservesReviewsAndExpires(t *testing.T) {
	f := newEligibilityFixture(t)
	empty := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
	journal, receipt, err := f.append(t, empty, f.command, f.at, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	r := receipt.Record
	admissions := r.Entry.Command.Comparison.Admissions
	if r.Schema != OwnedAllocationGrantSchema || r.Authority != ownedAllocationGrantAuthority || len(admissions) != 1 || admissions[0].EvidenceSHA256 != f.complete.SHA256 || !admissions[0].ValidFrom.Equal(f.complete.AssessedAt) || r.Expirations != nil {
		t.Fatal("owner admission", r.Schema, admissions, r.Expirations)
	}
	decision := r.Entry.Plan.Decisions[0]
	if decision.Status != "reserve_plan" {
		t.Fatal("eligible proposal was not reserved", decision)
	}
	c := r.AdmissionChecks[0]
	if c.Schema != OwnedEligibilityReadinessSchema || !c.EvaluationComplete || !c.PaperOperationallyEligible || !c.AdmissionAvailable || c.ExecutionAvailable || c.QualificationCredit || c.Status != "eligible" || c.Gates[2].Status != "verified" || c.Gates[3].Status != "unavailable" {
		t.Fatal("readiness v7", c)
	}
	catalog, err := f.replay(t, journal, f.complete)
	if err != nil {
		t.Fatal("grant replay", err)
	}
	notional := decision.Proposal.NotionalCents
	capital := catalog.Capital[0]
	if capital.Schema != "mantik-owned-allocation-capital-v2" || len(capital.Reservations) != 1 || capital.AvailableCashCents != capital.Account.Config.OpeningCashCents-notional || len(capital.Commitments) != 1 || capital.ExecutionAvailable || capital.QualificationCredit {
		t.Fatal("reserved capital readback", capital)
	}
	review, err := newOwnedAdmissionReview(OwnedAdmissionReviewCommand{ID: "eligibility-review", ReceiverID: f.m.receiverID, ComparisonID: r.Command.ID, ComparisonSHA256: r.SHA256, DecisionID: c.DecisionID, DecisionSHA256: c.DecisionSHA256}, catalog, 1, r.At)
	if err != nil || review.Schema != "mantik-owned-admission-review-v2" || review.Status != "eligible" || !review.AdmissionAvailable || review.ExecutionAvailable || review.QualificationCredit {
		t.Fatal("eligibility review", review, err)
	}

	// Without the complete assessment the retained admission cannot be rederived.
	if _, err := f.replay(t, journal); err == nil {
		t.Fatal("admission replayed without its assessment")
	}

	// The next comparison after the proposal expires releases the reservation
	// before evaluating, with the same decision now expired.
	later := decision.Proposal.ValidUntil
	retry := f.command
	retry.ID = "after-expiry"
	next, receipt, err := f.append(t, journal, retry, later, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	r2 := receipt.Record
	if len(r2.Expirations) != 1 || r2.Expirations[0].Command.Effect.Kind != "release" || r2.Expirations[0].Command.Effect.AmountCents != notional || r2.Entry.Plan.Decisions[0].Status != "reject" {
		t.Fatal("expiry release", r2.Expirations, r2.Entry.Plan.Decisions)
	}
	if r2.AdmissionChecks[0].PaperOperationallyEligible || r2.AdmissionChecks[0].Status != "expired" {
		t.Fatal("expired proposal stayed eligible", r2.AdmissionChecks[0])
	}
	catalog, err = f.replay(t, next, f.complete)
	if err != nil {
		t.Fatal("expiry replay", err)
	}
	capital = catalog.Capital[0]
	if capital.AvailableCashCents != capital.Account.Config.OpeningCashCents || capital.Reservations[0].ReservedCents != 0 || capital.Reservations[0].ReleasedCents != notional || len(capital.Orders) != 0 {
		t.Fatal("released capital readback", capital)
	}
	if _, again, err := f.append(t, next, retry, later.Add(time.Hour), f.complete); err != nil || !again.Duplicate {
		t.Fatal("exact retry after expiry", again, err)
	}

	// A dropped or altered release is refused on replay.
	dropped := clone(next)
	dropped.Records[1].Record.Expirations = nil
	dropped.Records[1].Record.SHA256 = ""
	dropped.Records[1].Record.SHA256 = workerplan.Hash(dropped.Records[1].Record)
	if _, err := f.replay(t, dropped, f.complete); err == nil {
		t.Fatal("dropped expiry release accepted")
	}
}

func TestOwnedEligibilityWithdrawnByLaterBlockedAssessment(t *testing.T) {
	f := newEligibilityFixture(t)
	blocked := clone(f.complete)
	blocked.Command.ID = "blocked-assessment"
	blocked.AssessedAt = f.at
	blocked.Status, blocked.EvaluationComplete, blocked.CompleteStrategyIDs = "blocked", false, nil
	blocked.Checks[0].Status = "failed"
	blocked.SHA256 = ""
	blocked.SHA256 = workerplan.Hash(blocked)
	journal, receipt, err := f.append(t, ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, f.command, f.at, f.complete, blocked)
	if err != nil {
		t.Fatal(err)
	}
	r := receipt.Record
	if len(r.Entry.Command.Comparison.Admissions) != 0 || r.Entry.Plan.Decisions[0].Status != "defer" || r.AdmissionChecks[0].PaperOperationallyEligible || r.AdmissionChecks[0].AdmissionAvailable {
		t.Fatal("blocked assessment did not withdraw eligibility", r.Entry.Command.Comparison, r.Entry.Plan.Decisions, r.AdmissionChecks[0])
	}
	if _, err := f.replay(t, journal, f.complete, blocked); err != nil {
		t.Fatal(err)
	}
	// Replay rederives the withdrawal; a forged admission is refused.
	forged := clone(journal)
	rec := &forged.Records[0].Record
	_, granted, err := f.append(t, ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, f.command, f.at, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	rec.Entry.Command.Comparison.Admissions = granted.Record.Entry.Command.Comparison.Admissions
	rec.SHA256 = ""
	rec.SHA256 = workerplan.Hash(*rec)
	if _, err := f.replay(t, forged, f.complete, blocked); err == nil {
		t.Fatal("forged admission accepted")
	}
}

func TestOwnedEvaluationAdmissionWindowAndExecutionModel(t *testing.T) {
	f := newEligibilityFixture(t)
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	policy, comparison, err := ownedComparison(f.command, policies, decisions, strategies, f.m.paperDecisions, f.at)
	if err != nil {
		t.Fatal(err)
	}
	catalog := PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: []PaperEvaluationAssessment{f.complete}}
	admit := func(p allocation.PolicyRecord, at time.Time) int {
		got, err := ownedEvaluationAdmissions(comparison.Proposals, p, strategies, declarations, catalog, at)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	var ttl time.Duration
	for _, d := range declarations.Records {
		if d.Command.ID == f.complete.Command.DeclarationID {
			ttl = time.Duration(d.Command.Plan.AdmissionTTLSeconds) * time.Second
		}
	}
	if admit(policy, f.at) != 1 || admit(policy, f.complete.AssessedAt.Add(ttl-time.Nanosecond)) != 1 {
		t.Fatal("admission missing inside its window")
	}
	if admit(policy, f.complete.AssessedAt.Add(ttl)) != 0 || admit(policy, f.complete.AssessedAt.Add(-time.Nanosecond)) != 0 {
		t.Fatal("admission outside its window")
	}
	noModel := clone(policy)
	noModel.Command.ExecutionModel = nil
	if admit(noModel, f.at) != 0 {
		t.Fatal("admission without protective execution model")
	}
	changed := clone(policy)
	changed.SHA256 = strings.Repeat("f", 64)
	if admit(changed, f.at) != 0 {
		t.Fatal("admission for another policy version")
	}
}
