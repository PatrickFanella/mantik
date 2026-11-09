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

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type ownedAllocationBackend interface {
	allocationBackend
	decisionBackend
	CollectorSubmissionStore
	OwnedAllocationStore
}

func TestMemoryOwnedAllocations(t *testing.T) { ownedAllocationContracts(t, NewMemory()) }
func TestPostgresContractsOwnedAllocations(t *testing.T) {
	ownedAllocationContracts(t, timerPostgres(t, "ownedallocations"))
}
func ownedAllocationContracts(t *testing.T, s ownedAllocationBackend) {
	ownedAllocationCostContracts(t, s, false)
}
func TestMemoryOwnedExecutionCosts(t *testing.T) { ownedAllocationCostContracts(t, NewMemory(), true) }
func TestPostgresContractsOwnedExecutionCosts(t *testing.T) {
	ownedAllocationCostContracts(t, timerPostgres(t, "ownedcosts"), true)
}
func TestMemoryOwnedLooserModelQuoteExpiry(t *testing.T) {
	ownedAllocationCostContracts(t, NewMemory(), true, 120)
}
func TestMemoryOwnedStrictModelQuoteExpiry(t *testing.T) {
	ownedAllocationCostContracts(t, NewMemory(), true, 10)
}
func TestPostgresContractsOwnedStrictModelQuoteExpiry(t *testing.T) {
	p := timerPostgres(t, "strictmodelexpiry")
	ownedAllocationCostContracts(t, p, true, 10)
	executionModelExpiryMigration(t, p)
	excludedAssessmentFailureMigration(t, p)
}
func ownedAllocationCostContracts(t *testing.T, s ownedAllocationBackend, withModel bool, modelAge ...int64) {
	ctx := context.Background()
	at := time.Now().UTC()
	clock := func() time.Time { return at }
	account := policyAccount(t, s, "owner")
	owner := account.ReceiverID
	d := strategyDefinition("baseline-v1")
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, clock)
	if err != nil {
		t.Fatal(err)
	}
	p := policyCommand(account, "policy")
	p.Policy.Strategies[0].ArtifactSHA256 = strategy.Record.DefinitionSHA256
	p.Policy.Symbols[0].ID = "SPY"
	if withModel {
		p.ExecutionModel = &paperfill.Model{Method: paperfill.Method, SlippageBPS: 10, FeeBPS: 20, CommissionPerFillCents: 3, LiquidityBPS: 5000, LatencyMillis: 1000, MaxQuoteAgeSeconds: 60}
		if len(modelAge) != 0 {
			p.ExecutionModel.MaxQuoteAgeSeconds = modelAge[0]
		}
	}
	policyReceipt, err := s.RegisterAllocationPolicy(ctx, p, clock)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker-one"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(at, "allocation-quote")
	if _, err = s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, input, clock); err != nil {
		t.Fatal(err)
	}
	// Storage contracts use a pure test evaluator, never an admission issuer.
	// The HTTP contract separately exercises the actual calendar/price evaluator.
	eval := func(c PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		quoteID := input.ID
		if c.ID == "alias-capture" {
			quoteID = collectorInput(at, "alias-input").ID
		}
		if c.ID == "fresh-protected-decision" || c.ID == "fresh-protected-alias" {
			quoteID = collectorInput(at.Add(500*time.Millisecond), "fresh-protected-input").ID
		}
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_contract_only", PriceCents: b.Feed.Routing.Inputs[quoteID].Input.PriceCents, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: quoteID}}}, nil
	}
	opening, e := s.OwnedAllocations(ctx, eval)
	if e != nil || len(opening.Capital) != 1 || len(opening.Records) != 0 || len(opening.Ledgers) != 0 {
		t.Fatal("opening capital readback", opening, e)
	}
	assertOwnedCapital(t, opening.Capital[0], account, "registered_opening_capital")
	decision, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "first", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, eval)
	if err != nil {
		t.Fatal(err)
	}
	var declaration PaperEvaluationDeclarationReceipt
	var declarationCommand PaperEvaluationDeclarationCommand
	if withModel {
		stress := *p.ExecutionModel
		stress.FeeBPS++
		declarationCommand = PaperEvaluationDeclarationCommand{ID: "allocation-plan", ReceiverID: owner, Recipe: PaperEvaluationRecipe, PolicyID: p.Policy.ID, PolicySHA256: policyReceipt.Record.SHA256, Plan: PaperEvaluationPlan{FamilyID: "allocation-family", Variants: []PaperEvaluationVariant{{ID: d.ID, SHA256: strategy.Record.SHA256}}, WindowStart: at.Add(time.Hour), WindowEnd: at.Add(2 * time.Hour), OutcomeDeadline: at.Add(4 * time.Hour), MinIndependentDecisions: 30, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 3600, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress}}
		declaration, err = s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, declarationCommand, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
	}
	c := OwnedAllocationCommand{ID: "comparison", ReceiverID: owner, Decisions: []OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}
	noClock := func() time.Time { panic("historical retry read clock") }
	var calls atomic.Int32
	var wg sync.WaitGroup
	now := func() time.Time { calls.Add(1); return at.Add(time.Second) }
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		// The INSERT succeeds, but a deferred constraint rejects COMMIT.
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit rollback'; END $$;
 CREATE CONSTRAINT TRIGGER reject_owned_commit AFTER INSERT OR UPDATE ON mantik_owned_allocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_owned_commit()`); err != nil {
			t.Fatal(err)
		}
		failed, e := s.CompareOwnedAllocation(ctx, c, clock, eval)
		if e == nil || !strings.Contains(e.Error(), "test commit rollback") || !reflect.DeepEqual(failed, OwnedAllocationReceipt{}) {
			t.Fatal("commit failure returned staged receipt", failed, e)
		}
		empty, e := s.OwnedAllocations(ctx, eval)
		if e != nil || len(empty.Records) != 0 || len(empty.Ledgers) != 0 {
			t.Fatal("commit failure retained partial journal", empty, e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_commit ON mantik_owned_allocations; DROP FUNCTION reject_owned_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_allocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_owned_allocation BEFORE INSERT OR UPDATE ON mantik_owned_allocations FOR EACH ROW EXECUTE FUNCTION reject_owned_allocation()`); err != nil {
			t.Fatal(err)
		}
		if failed, e := s.CompareOwnedAllocation(ctx, c, clock, eval); e == nil || !reflect.DeepEqual(failed, OwnedAllocationReceipt{}) {
			t.Fatal("failed insert returned receipt", failed, e)
		}
		empty, e := s.OwnedAllocations(ctx, eval)
		if e != nil || len(empty.Records) != 0 {
			t.Fatal("partial journal after failed insert", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_allocation ON mantik_owned_allocations; DROP FUNCTION reject_owned_allocation()"); err != nil {
			t.Fatal(err)
		}
	}
	// The collector owner can advance without finalizing another decision.
	// New comparisons must not be backdated before that actual source clock,
	// even when the selected decision and declaration are older and valid.
	if _, err = s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "comparison-clock-heartbeat", Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, now); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	backdated, e := s.CompareOwnedAllocation(ctx, c, func() time.Time { return at.Add(time.Second - time.Nanosecond) }, eval)
	if !errors.Is(e, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(backdated, OwnedAllocationReceipt{}) {
		t.Fatal("comparison preceded the actual collector clock", backdated, e)
	}
	emptyAfterClockFailure, e := s.OwnedAllocations(ctx, eval)
	if e != nil || !reflect.DeepEqual(emptyAfterClockFailure, opening) {
		t.Fatal("clock failure changed allocation state", emptyAfterClockFailure, e)
	}
	before, err = s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The same command remains usable at the exact source clock after failure.
	for range 8 {
		wg.Go(func() {
			if _, err := s.CompareOwnedAllocation(ctx, c, now, eval); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	original, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(original.Records) != 1 || len(original.Ledgers) != 1 {
		t.Fatal(original, err)
	}
	if len(original.Capital) != 1 {
		t.Fatal(original)
	}
	assertOwnedCapital(t, original.Capital[0], account, "replayed_deferral_journal")
	if original.Capital[0].LedgerSHA256 != original.Ledgers[0].SHA256 || !original.Capital[0].AsOf.Equal(original.Records[0].At) || original.Capital[0].LedgerSHA256 == opening.Capital[0].LedgerSHA256 {
		t.Fatal("capital lost actual journal head", original.Capital)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_allocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_owned_allocation BEFORE UPDATE ON mantik_owned_allocations FOR EACH ROW EXECUTE FUNCTION reject_owned_allocation()`); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.ID = "failed-update"
		if receipt, e := s.CompareOwnedAllocation(ctx, failed, now, eval); e == nil || !reflect.DeepEqual(receipt, OwnedAllocationReceipt{}) {
			t.Fatal("failed update returned receipt", receipt, e)
		}
		unchanged, e := s.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(unchanged, original) {
			t.Fatal("partial journal after failed update", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_allocation ON mantik_owned_allocations; DROP FUNCTION reject_owned_allocation()"); err != nil {
			t.Fatal(err)
		}
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, `CREATE FUNCTION reject_owned_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test update commit rollback'; END $$;
 CREATE CONSTRAINT TRIGGER reject_owned_commit AFTER UPDATE ON mantik_owned_allocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_owned_commit()`); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.ID = "failed-commit-update"
		if receipt, e := s.CompareOwnedAllocation(ctx, failed, now, eval); e == nil || !strings.Contains(e.Error(), "test update commit rollback") || !reflect.DeepEqual(receipt, OwnedAllocationReceipt{}) {
			t.Fatal("failed update commit returned receipt", receipt, e)
		}
		unchanged, e := s.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(unchanged, original) {
			t.Fatal("failed commit changed prior journal", e)
		}
		if _, err = pg.pool.Exec(ctx, "DROP TRIGGER reject_owned_commit ON mantik_owned_allocations; DROP FUNCTION reject_owned_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	first := original.Records[0]
	ownedAllocationEntryClockContract(t, s, eval)
	// A second finalization/request ID must not multiply the same market source
	// into another proposal. Neither same-batch nor historical alias checks read
	// a clock, and a failed attempt leaves the atomic comparison journal intact.
	aliasCommand := decision.Record.Command
	aliasCommand.ID = "alias-decision"
	alias, e := s.FinalizePaperDecision(ctx, aliasCommand, now, eval)
	if e != nil {
		t.Fatal(e)
	}
	aliasComparison := clone(c)
	aliasComparison.ID = "alias-comparison"
	aliasComparison.Decisions = []OwnedDecisionPin{{ID: alias.Record.Command.ID, SHA256: alias.Record.SHA256}}
	if _, e = s.CompareOwnedAllocation(ctx, aliasComparison, noClock, eval); !errors.Is(e, ErrOwnedAllocationConflict) {
		t.Fatal("same source acquired a new proposal identity", e)
	}
	aliasComparison.ID = "alias-batch"
	aliasComparison.Decisions = append(aliasComparison.Decisions, c.Decisions...)
	if _, e = s.CompareOwnedAllocation(ctx, aliasComparison, noClock, eval); !errors.Is(e, ErrOwnedAllocationConflict) {
		t.Fatal("same-batch alias accepted", e)
	}
	// Comparisons and alias rejections so far leave operational state untouched.
	priorCapture, e := s.Snapshot(ctx)
	if e != nil || !reflect.DeepEqual(before, priorCapture) {
		t.Fatal("comparison changed operational fixture before source publication", e)
	}
	// A new capture ID, raw pin and receipt time still describe one occurrence.
	aliasInput := collectorInput(at, "alias-input")
	aliasInput.ReceivedAt = at.Add(time.Millisecond)
	// New custody follows the retained finalization; market occurrence is unchanged.
	now = func() time.Time { calls.Add(1); return at.Add(time.Second + time.Nanosecond) }
	if _, e = s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, aliasInput, now); e != nil {
		t.Fatal(e)
	}
	// This explicit feed publication changes routing state. Subsequent
	// comparison operations must preserve that new source-owned baseline.
	before, e = s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	aliasCommand.ID = "alias-capture"
	alias, e = s.FinalizePaperDecision(ctx, aliasCommand, now, eval)
	if e != nil || alias.Record.Evaluation.PriceCents != aliasInput.PriceCents || alias.Record.Evaluation.Coverage.Quote.InputID != aliasInput.ID {
		t.Fatal("alias must resolve its actual new retained capture", e)
	}
	aliasComparison.ID = "capture-alias-comparison"
	aliasComparison.Decisions = []OwnedDecisionPin{{ID: alias.Record.Command.ID, SHA256: alias.Record.SHA256}}
	if _, e = s.CompareOwnedAllocation(ctx, aliasComparison, noClock, eval); !errors.Is(e, ErrOwnedAllocationConflict) {
		t.Fatal("capture alias accepted", e)
	}
	unchanged, e := s.OwnedAllocations(ctx, eval)
	if e != nil || !reflect.DeepEqual(unchanged, original) {
		t.Fatal("alias rejection altered journal", e)
	}
	if b := first.AdmissionChecks[0].Binding; b == nil || b.AccountSHA256 != account.SHA256 || b.AccountID != account.Config.ID || b.PolicyID != p.Policy.ID {
		t.Fatal("owner binding", b)
	}
	assertOwnedReadiness(t, first, decision.Record, strategy.Record, input.ID, d.InputEvidenceClass, "deferred")
	evaluationContext := first.EvaluationContext
	if evaluationContext == nil || evaluationContext.Schema != "mantik-owned-allocation-evaluation-context-v2" || evaluationContext.Authority != "service_owned_incomplete_assessments_and_predeclarations_only" || evaluationContext.AdmissionAvailable || evaluationContext.ExecutionAvailable || evaluationContext.QualificationCredit || len(evaluationContext.Decisions) != 1 {
		t.Fatal("evaluation context", evaluationContext)
	}
	binding := evaluationContext.Decisions[0]
	if binding.DecisionID != decision.Record.Command.ID || binding.DecisionSHA256 != decision.Record.SHA256 || binding.StrategySHA256 != strategy.Record.SHA256 || binding.PolicySHA256 != policyReceipt.Record.SHA256 {
		t.Fatal("owner plan binding", binding)
	}
	if withModel {
		if binding.Status != "declared_not_evaluated" || !reflect.DeepEqual(binding.Declarations, []OwnedDecisionPin{{ID: declaration.Record.Command.ID, SHA256: declaration.Record.SHA256}}) {
			t.Fatal(binding)
		}
		later := clone(declarationCommand)
		later.ID = "later-plan"
		later.Plan.FamilyID = "later-family"
		for _, rejectedAt := range []time.Time{first.At.Add(-time.Nanosecond), first.At} {
			if failed, e := s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, later, func() time.Time { return rejectedAt }); !errors.Is(e, ErrPaperEvaluationDeclarationInvalid) || !reflect.DeepEqual(failed, PaperEvaluationDeclarationReceipt{}) {
				t.Fatal("plan entered completed comparison clock cut", failed, e)
			}
		}
		if retry, e := s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, declarationCommand, noClock); e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, declaration.Record) {
			t.Fatal("historical plan retry read new clock", e)
		}
		// Failed clock attempts consume no ID; the same plan can follow the cut.
		if _, e := s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, later, func() time.Time { return first.At.Add(time.Nanosecond) }); e != nil {
			t.Fatal(e)
		}
		historical, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
		if e != nil || !historical.Duplicate || !reflect.DeepEqual(historical.Record, first) {
			t.Fatal("later declaration rewrote old context", e)
		}
		now = func() time.Time { calls.Add(1); return at.Add(2 * time.Second) }
		read, e := s.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(read, original) {
			t.Fatal("historical context read", e)
		}
	} else if binding.Status != "declaration_unavailable" || len(binding.Declarations) != 0 {
		t.Fatal(binding)
	}
	detachedContext, e := s.OwnedAllocations(ctx, eval)
	if e != nil {
		t.Fatal(e)
	}
	detachedContext.Records[0].EvaluationContext.Decisions[0].PolicyID = "changed"
	if read, e := s.OwnedAllocations(ctx, eval); e != nil || !reflect.DeepEqual(read, original) {
		t.Fatal("detached evaluation context", e)
	}

	if withModel {
		if len(first.ExecutionCosts) != 1 || first.Schema != OwnedAllocationGrantSchema {
			t.Fatal(first)
		}
		cost := first.ExecutionCosts[0]
		if cost.Quantity != d.Quantity || cost.MaxDebitCents != d.Quantity*10023 || cost.FeeCeilingCents != d.Quantity*23 || cost.LimitCents != 10000 || cost.ModelSHA256 != workerplan.Hash(*p.ExecutionModel) || cost.QuoteInputID != input.ID || cost.ProtectiveFunding == nil || cost.ProtectiveFunding.ExitShortfallCeilingCents != d.Quantity*3 || cost.ProtectiveFunding.TotalFundingCents != cost.MaxDebitCents+d.Quantity*3 || first.Entry.Plan.Decisions[0].Proposal.NotionalCents != cost.ProtectiveFunding.TotalFundingCents {
			t.Fatal(cost)
		}
	}
	plan := first.Entry.Plan
	if plan == nil || plan.Decisions[0].Status != "defer" || plan.Decisions[0].Reasons[0] != "admission_unavailable" || plan.PlannedReservationCents != 0 || first.ExecutionAvailable || first.QualificationCredit {
		t.Fatal(first)
	}
	expectedAge := int64(60)
	if withModel {
		expectedAge = min(expectedAge, p.ExecutionModel.MaxQuoteAgeSeconds)
	}
	if !plan.Decisions[0].Proposal.ValidUntil.Equal(input.OccurredAt.Add(time.Duration(expectedAge) * time.Second)) {
		t.Fatal("expiry extended", plan)
	}
	retry, err := s.CompareOwnedAllocation(ctx, c, noClock, eval)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, first) {
		t.Fatal(retry, err)
	}
	retry.Record.Entry.Plan.Decisions[0].Reasons[0] = "mutated"
	retry.Record.AdmissionChecks[0].Binding.AccountID = "mutated"
	detached, err := s.OwnedAllocations(ctx, eval)
	if err != nil || !reflect.DeepEqual(detached, original) {
		t.Fatal("returned binding aliased owner state", err)
	}
	bad := clone(c)
	bad.Decisions[0].SHA256 = workerplan.Hash("changed")
	if _, err = s.CompareOwnedAllocation(ctx, bad, noClock, eval); !errors.Is(err, ErrOwnedAllocationConflict) {
		t.Fatal(err)
	}
	bad = clone(c)
	bad.ReceiverID = "receiver-other_other_other"
	if _, err = s.CompareOwnedAllocation(ctx, bad, noClock, eval); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CompareOwnedAllocation(canceled, c, noClock, eval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(modelAge) != 0 {
		until := plan.Decisions[0].Proposal.ValidUntil
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			policies, decisions, strategies, e := m.ownedAllocationSourcesLocked(eval)
			if e != nil {
				m.mu.Unlock()
				t.Fatal(e)
			}
			_, old, e := ownedComparisonFunding(c, policies, decisions, strategies, m.paperDecisions, first.At, true, false)
			m.mu.Unlock()
			if e != nil || !old.Proposals[0].ValidUntil.Equal(input.OccurredAt.Add(60*time.Second)) {
				t.Fatal("legacy model expiry reinterpreted", e)
			}
		}

		if !first.ExecutionCosts[0].ValidUntil.Equal(until) || !first.ExecutionSafety[0].ProposalValidUntil.Equal(until) || !first.AdmissionChecks[0].ValidUntil.Equal(until) {
			t.Fatal("owner expiry bindings diverged")
		}
		beforeBoundary := clone(c)
		beforeBoundary.ID = "model-before-expiry"
		active, e := s.CompareOwnedAllocation(ctx, beforeBoundary, func() time.Time { return until.Add(-time.Nanosecond) }, eval)
		if e != nil || active.Record.Entry.Plan.Decisions[0].Reasons[0] != "admission_unavailable" {
			t.Fatal("expired before boundary", active, e)
		}
		atBoundary := clone(c)
		atBoundary.ID = "model-at-expiry"
		expired, e := s.CompareOwnedAllocation(ctx, atBoundary, func() time.Time { return until }, eval)
		if e != nil || expired.Record.Entry.Plan.Decisions[0].Reasons[0] != "decision_expired" || expired.Record.AdmissionChecks[0].Status != "expired" {
			t.Fatal("expiry boundary extended", expired, e)
		}
		if retry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, first) {
			t.Fatal("expiry changed original retry", e)
		}
		current, e := s.OwnedAllocations(ctx, eval)
		if e != nil {
			t.Fatal(e)
		}
		if pg, ok := s.(*Postgres); ok {
			reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
			if e != nil {
				t.Fatal(e)
			}
			got, e := reopened.OwnedAllocations(ctx, eval)
			reopened.Close()
			if e != nil || !reflect.DeepEqual(got, current) {
				t.Fatal("restart changed model expiry", e)
			}
		}
		return
	}
	// Reconsideration is a new retained comparison; expiration does not reserve
	// or release capital and exact old retries remain historical.
	next := clone(c)
	next.ID = "expired"
	expired, err := s.CompareOwnedAllocation(ctx, next, func() time.Time { return at.Add(time.Minute) }, eval)
	if err != nil || expired.Record.Entry.Plan.Decisions[0].Reasons[0] != "decision_expired" {
		t.Fatal(expired, err)
	}
	assertOwnedReadiness(t, expired.Record, decision.Record, strategy.Record, input.ID, d.InputEvidenceClass, "expired")
	// A second matching policy cannot choose another account for this decision.
	other := policyAccount(t, s, "other")
	overlap := policyCommand(other, "overlap")
	overlap.Policy.Strategies = clone(p.Policy.Strategies)
	overlap.Policy.Symbols = clone(p.Policy.Symbols)
	if _, err = s.RegisterAllocationPolicy(ctx, overlap, func() time.Time { return at.Add(61 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	ambiguous := clone(c)
	ambiguous.ID = "ambiguous"
	if _, err = s.CompareOwnedAllocation(ctx, ambiguous, func() time.Time { return at.Add(62 * time.Second) }, eval); !errors.Is(err, ErrOwnedAllocationConflict) {
		t.Fatal("ambiguous owner accepted", err)
	}
	// The new policy must not change the already recorded assignment.
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err != nil {
		t.Fatal("later policy rewrote history", err)
	}
	// Remove only this test's overlap in private test state to exercise retention
	// capacity. Production has no delete/reassignment endpoint.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		delete(m.allocationPolicies, "overlap")
		m.mu.Unlock()
	} else {
		pg := s.(*Postgres)
		if _, err = pg.pool.Exec(ctx, "DELETE FROM mantik_allocation_policies WHERE id='overlap'"); err != nil {
			t.Fatal(err)
		}
	}
	// Each append replays the whole journal, so fill a lowered bound.
	maxOwnedAllocations = 32
	defer func() { maxOwnedAllocations = MaxOwnedAllocations }()
	for i := 2; i < maxOwnedAllocations; i++ {
		q := clone(c)
		q.ID = fmt.Sprintf("comparison-%02d", i)
		if _, err = s.CompareOwnedAllocation(ctx, q, func() time.Time { return at.Add(time.Minute) }, eval); err != nil {
			t.Fatal(i, err)
		}
	}
	full, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(full.Records) != maxOwnedAllocations {
		t.Fatal(err)
	}
	overflow := clone(c)
	overflow.ID = "overflow"
	if _, err = s.CompareOwnedAllocation(ctx, overflow, noClock, eval); !errors.Is(err, ErrOwnedAllocationCapacity) {
		t.Fatal(err)
	}
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err != nil {
		t.Fatal(err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("comparison changed operational fixture", err)
	}
	if pg, ok := s.(*Postgres); ok {
		url := pg.pool.Config().ConnString()
		reopened, err := OpenPostgres(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		got, err := reopened.OwnedAllocations(ctx, eval)
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatal("restart changed journal", err)
		}
	}
	// A self-rehashed counterfeit entry cannot bypass owner derivation/replay.
	var journal ownedAllocationJournal
	if m, ok := s.(*Memory); ok {
		journal = clone(m.ownedAllocations)
	} else {
		pg := s.(*Postgres)
		var raw []byte
		if err = pg.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &journal) != nil {
			t.Fatal("decode")
		}
	}
	// Capacity, overflow, full-journal retry and full-journal reopen were proved
	// above. Corruption and legacy cases need a valid multi-entry history, not
	// thirty-two repeated comparisons on every read/retry. Keep the first two
	// original entries (including their original hashes and prefix bindings).
	journal.Records = clone(journal.Records[:2])
	writeJournal := func(j ownedAllocationJournal) {
		for i := range j.Records {
			if j.Records[i].Record.Schema != OwnedAllocationGrantSchema && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v14" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v13" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v12" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v11" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v10" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v8" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v7" && j.Records[i].Record.Schema != "mantik-owned-allocation-deferral-v6" {
				j.Records[i].EvaluationDeclarations = nil
			}
		}
		t.Helper()
		if m, ok := s.(*Memory); ok {
			m.ownedAllocations = clone(j)
		} else {
			raw, e := json.Marshal(j)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1", raw); e != nil {
				t.Fatal(e)
			}
		}
	}
	writeJournal(journal)
	if prefix, err := s.OwnedAllocations(ctx, eval); err != nil || len(prefix.Records) != 2 {
		t.Fatal("corruption fixture is not a valid retained prefix", prefix, err)
	}
	// v12 records retain their exact bytes and clock-free retry representation.
	legacy12 := clone(journal)
	for i := range legacy12.Records {
		v := &legacy12.Records[i].Record
		v.Schema = "mantik-owned-allocation-deferral-v12"
		v.Authority, v.Expirations = "service_owned_allocation_deferral_only", nil
		for j := range v.AdmissionChecks {
			v.AdmissionChecks[j].Schema = "mantik-owned-admission-readiness-v5"
			v.AdmissionChecks[j].SHA256 = ""
			v.AdmissionChecks[j].SHA256 = workerplan.Hash(v.AdmissionChecks[j])
		}
		v.SHA256 = ""
		v.SHA256 = workerplan.Hash(*v)
	}
	writeJournal(legacy12)
	if got, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, legacy12.Records[0].Record) {
		t.Fatal("legacy12 retry rewritten", e)
	}
	writeJournal(journal)
	// Rebuild all portable hashes using actual retained finalized aliases. The
	// writer rejects these; replay must enforce the same v5 source invariant.
	forgeAlias := func(batch bool) ownedAllocationJournal {
		t.Helper()
		policy := journal.Records[0].Policies.Records[0]
		ledger, e := allocation.NewLedger(policy)
		if e != nil {
			t.Fatal(e)
		}
		prefix := []retainedOwnedAllocation{}
		selected := []PaperDecisionRecord{alias.Record}
		if batch {
			selected = append(selected, decision.Record)
		} else {
			ledger = clone(original.Ledgers[0])
			prefix = append(prefix, clone(journal.Records[0]))
		}
		r := clone(first)
		r.Schema = "mantik-owned-allocation-deferral-v11"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.At = alias.Record.FinalizedAt
		r.Command = OwnedAllocationCommand{ID: "forged-source-alias", ReceiverID: owner, Decisions: []OwnedDecisionPin{}}
		comparison := allocation.Comparison{Proposals: []allocation.Proposal{}, Admissions: []allocation.Admission{}}
		r.AdmissionChecks = []OwnedAdmissionReadiness{}
		if withModel {
			r.ExecutionCosts = []OwnedExecutionCost{}
		}
		for _, d := range selected {
			r.Command.Decisions = append(r.Command.Decisions, OwnedDecisionPin{ID: d.Command.ID, SHA256: d.SHA256})
			proposal := clone(first.Entry.Command.Comparison.Proposals[0])
			proposal.ID = d.Command.ID
			proposal.DecisionSHA256 = d.SHA256
			comparison.Proposals = append(comparison.Proposals, proposal)
			check := clone(first.AdmissionChecks[0])
			check.CheckedAt = r.At
			check.DecisionID = d.Command.ID
			check.DecisionSHA256 = d.SHA256
			check.BasisSHA256 = d.BasisSHA256
			check.QuoteInputID = d.Evaluation.Coverage.Quote.InputID
			r.AdmissionChecks = append(r.AdmissionChecks, check)
			if withModel {
				cost := clone(first.ExecutionCosts[0])
				cost.DecisionID = d.Command.ID
				cost.DecisionSHA256 = d.SHA256
				cost.QuoteInputID = d.Evaluation.Coverage.Quote.InputID
				cost.SHA256 = ""
				cost.SHA256 = workerplan.Hash(cost)
				r.ExecutionCosts = append(r.ExecutionCosts, cost)
			}
		}
		ledger, _, _, e = allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: r.Command.ID, Comparison: &comparison}, func() time.Time { return r.At })
		if e != nil {
			t.Fatal(e)
		}
		r.Entry = ledger.Entries[len(ledger.Entries)-1]
		r.LedgerSHA256 = ledger.SHA256
		r.AdmissionChecks, e = bindOwnedAdmissionReadiness(r, policy)
		if e != nil {
			t.Fatal(e)
		}
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(r)
		return ownedAllocationJournal{Records: append(prefix, retainedOwnedAllocation{Record: r, Policies: clone(journal.Records[0].Policies)})}
	}
	for _, batch := range []bool{false, true} {
		forged := forgeAlias(batch)
		writeJournal(forged)
		got, e := s.OwnedAllocations(ctx, eval)
		if !errors.Is(e, ErrOwnedAllocationConflict) || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("replay accepted rehashed source aliases", batch, e)
		}
		retry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
		if !errors.Is(e, ErrOwnedAllocationConflict) || !reflect.DeepEqual(retry, OwnedAllocationReceipt{}) {
			t.Fatal("retry hid rehashed source aliases", batch, e)
		}
		// Older schemas did not retain this replay invariant. Keep their exact
		// historical bytes/retries, while new writes still apply the guard.
		legacy := clone(forged)
		for i := range legacy.Records {
			r := &legacy.Records[i].Record
			r.Schema = "mantik-owned-allocation-deferral-v2"
			r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
			r.ExecutionSafety = nil
			if withModel {
				r.Schema = "mantik-owned-allocation-deferral-v4"
				r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
				r.ExecutionSafety = nil
			}
			legacyReadiness(r)
			r.SHA256 = ""
			r.SHA256 = workerplan.Hash(*r)
		}
		writeJournal(legacy)
		got, e = s.OwnedAllocations(ctx, eval)
		if e != nil || len(got.Records) != len(legacy.Records) {
			t.Fatal("legacy source aliases changed", batch, e)
		}
		for i, item := range legacy.Records {
			if !reflect.DeepEqual(got.Records[i], item.Record) {
				t.Fatal("legacy bytes changed")
			}
			retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval)
			if e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, item.Record) {
				t.Fatal("legacy retry changed", e)
			}
		}
		newCommand := clone(c)
		newCommand.ID = "new-comparison-on-legacy-aliases"
		receipt, e := s.CompareOwnedAllocation(ctx, newCommand, noClock, eval)
		if !errors.Is(e, ErrOwnedAllocationConflict) || !reflect.DeepEqual(receipt, OwnedAllocationReceipt{}) {
			t.Fatal("new write bypassed legacy source aliases", e)
		}
	}
	legacyV8 := clone(journal)
	for i := range legacyV8.Records {
		r := &legacyV8.Records[i].Record
		r.Schema = "mantik-owned-allocation-deferral-v8"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.ExecutionSafety = nil
		var p allocation.PolicyRecord
		for _, v := range legacyV8.Records[i].Policies.Records {
			if v.SHA256 == r.PolicySHA256 {
				p = v
			}
		}
		r.AdmissionChecks, e = bindOwnedAdmissionReadiness(*r, p)
		if e != nil {
			t.Fatal(e)
		}
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}
	writeJournal(legacyV8)
	for _, item := range legacyV8.Records {
		if retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval); e != nil || !reflect.DeepEqual(retry.Record, item.Record) {
			t.Fatal("v8 exact retry", e)
		}
	}
	legacyV7 := clone(legacyV8)
	for i := range legacyV7.Records {
		r := &legacyV7.Records[i].Record
		r.Schema = "mantik-owned-allocation-deferral-v7"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.ExecutionSafety = nil
		r.EvaluationContext.Schema = "mantik-owned-allocation-evaluation-context-v1"
		r.EvaluationContext.AssessmentSourceSHA256 = ""
		r.EvaluationContext.Authority = "service_owned_predeclarations_only"
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}
	writeJournal(legacyV7)
	if old, e := s.OwnedAllocations(ctx, eval); e != nil || len(old.Records) != len(legacyV7.Records) {
		t.Fatal("v7 compatibility", e)
	}
	for _, item := range legacyV7.Records {
		if retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval); e != nil || !reflect.DeepEqual(retry.Record, item.Record) {
			t.Fatal("v7 exact retry", e)
		}
	}
	legacyV6 := clone(legacyV7)
	for i := range legacyV6.Records {
		r := &legacyV6.Records[i].Record
		r.Schema = "mantik-owned-allocation-deferral-v6"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.ExecutionSafety = nil
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}
	writeJournal(legacyV6)
	if pg, ok := s.(*Postgres); ok {
		completeEvaluationCaptureMigrationContract(t, pg)
	}
	if old, e := s.OwnedAllocations(ctx, eval); e != nil || len(old.Records) != len(legacyV6.Records) {
		t.Fatal("v6 compatibility", e)
	} else {
		for i, item := range legacyV6.Records {
			if !reflect.DeepEqual(old.Records[i], item.Record) {
				t.Fatal("v6 rewritten")
			}
		}
	}
	for _, item := range legacyV6.Records {
		if retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval); e != nil || !reflect.DeepEqual(retry.Record, item.Record) {
			t.Fatal("v6 retry changed", e)
		}
	}
	legacyV5 := clone(legacyV8)
	for i := range legacyV5.Records {
		r := &legacyV5.Records[i].Record
		r.Schema = "mantik-owned-allocation-deferral-v5"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.ExecutionSafety = nil
		r.EvaluationContext = nil
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}
	writeJournal(legacyV5)
	if pg, ok := s.(*Postgres); ok {
		evaluationContextMigrationContract(t, pg)
	}
	oldV5, e := s.OwnedAllocations(ctx, eval)
	if e != nil {
		t.Fatal("v5 compatibility", e)
	}
	for i, item := range legacyV5.Records {
		if !reflect.DeepEqual(item.Record, oldV5.Records[i]) {
			t.Fatal("v5 rewritten")
		}
		retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval)
		if e != nil || !reflect.DeepEqual(retry.Record, item.Record) {
			t.Fatal("v5 retry", e)
		}
	}
	if withModel {
		damaged := clone(journal)
		v := &damaged.Records[0]
		v.EvaluationDeclarations.Records = []PaperEvaluationDeclaration{}
		var policy allocation.PolicyRecord
		for _, p := range v.Policies.Records {
			if p.SHA256 == v.Record.PolicySHA256 {
				policy = p
			}
		}
		v.Record.EvaluationContext = ownedAllocationEvaluationContext(v.Record, policy, *v.EvaluationDeclarations)
		v.Record.SHA256 = ""
		v.Record.SHA256 = workerplan.Hash(v.Record)
		writeJournal(damaged)
		if got, e := s.OwnedAllocations(ctx, eval); e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("omitted actual plan accepted after complete rehash", e)
		}
		if got, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil || !reflect.DeepEqual(got, OwnedAllocationReceipt{}) {
			t.Fatal("retry accepted omitted actual plan", e)
		}
		writeJournal(journal)
	}
	for _, mutate := range []func(*retainedOwnedAllocation){
		func(v *retainedOwnedAllocation) { v.EvaluationDeclarations = nil },
		func(v *retainedOwnedAllocation) { v.Record.EvaluationContext = nil },
		func(v *retainedOwnedAllocation) { v.Record.EvaluationContext.AdmissionAvailable = true },
		func(v *retainedOwnedAllocation) { v.Record.EvaluationContext.SourceSHA256 = workerplan.Hash("other") },
		func(v *retainedOwnedAllocation) {
			v.Record.EvaluationContext.Decisions[0].PolicySHA256 = workerplan.Hash("other")
		},
		func(v *retainedOwnedAllocation) { v.Record.EvaluationContext.Decisions[0].Status = "evaluated" },
	} {
		damaged := clone(journal)
		mutate(&damaged.Records[0])
		damaged.Records[0].Record.SHA256 = ""
		damaged.Records[0].Record.SHA256 = workerplan.Hash(damaged.Records[0].Record)
		writeJournal(damaged)
		got, e := s.OwnedAllocations(ctx, eval)
		if e == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("rehashed evaluation context accepted", got, e)
		}
		receipt, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
		if e == nil || !reflect.DeepEqual(receipt, OwnedAllocationReceipt{}) {
			t.Fatal("context corruption hidden by retry", receipt, e)
		}
	}
	writeJournal(journal)
	writeJournal(journal)
	// Preserve prior v2 (without model) and v4 (with protective funding) bytes.
	legacyBound := clone(journal)
	for i := range legacyBound.Records {
		r := &legacyBound.Records[i].Record
		r.Schema = "mantik-owned-allocation-deferral-v2"
		r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
		r.ExecutionSafety = nil
		if withModel {
			r.Schema = "mantik-owned-allocation-deferral-v4"
			r.Authority, r.Expirations = "service_owned_allocation_deferral_only", nil
			r.ExecutionSafety = nil
		}
		legacyReadiness(r)
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
	}
	writeJournal(legacyBound)
	if pg, ok := s.(*Postgres); ok {
		admissionBindingMigrationContract(t, pg)
	}
	legacyRead, e := s.OwnedAllocations(ctx, eval)
	if e != nil || len(legacyRead.Records) != len(legacyBound.Records) {
		t.Fatal("legacy v2/v4 replay", e)
	}
	for i, item := range legacyBound.Records {
		if !reflect.DeepEqual(legacyRead.Records[i], item.Record) {
			t.Fatal("legacy bytes changed")
		}
		retry, e := s.CompareOwnedAllocation(ctx, item.Record.Command, noClock, eval)
		if e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, item.Record) {
			t.Fatal("legacy historical retry", e)
		}
		for _, check := range item.Record.AdmissionChecks {
			raw, err := json.Marshal(check)
			var fields map[string]json.RawMessage
			if err != nil || json.Unmarshal(raw, &fields) != nil || fields["binding"] != nil {
				t.Fatal("legacy readiness wire changed")
			}
		}
	}
	writeJournal(journal)
	if withModel {
		// Reconstruct the prior v3 journal from the same actual owner sources.
		// New v4 records must coexist without changing the old entry amount/hash.
		legacy := clone(journal)
		legacy.Records = legacy.Records[:1]
		old := &legacy.Records[0].Record
		old.Schema = "mantik-owned-allocation-deferral-v3"
		old.Authority, old.Expirations = "service_owned_allocation_deferral_only", nil
		old.ExecutionSafety = nil
		legacyReadiness(old)
		old.ExecutionCosts[0].ProtectiveFunding = nil
		old.ExecutionCosts[0].SHA256 = ""
		old.ExecutionCosts[0].SHA256 = workerplan.Hash(old.ExecutionCosts[0])
		cmd := clone(old.Entry.Command)
		cmd.Comparison.Proposals[0].NotionalCents = old.ExecutionCosts[0].MaxDebitCents
		priorPolicy := legacy.Records[0].Policies.Records[0]
		ledger, e := allocation.NewLedger(priorPolicy)
		if e != nil {
			t.Fatal(e)
		}
		ledger, _, _, e = allocation.AppendLedger(ctx, ledger, cmd, func() time.Time { return old.At })
		if e != nil {
			t.Fatal(e)
		}
		old.Entry = ledger.Entries[0]
		old.LedgerSHA256 = ledger.SHA256
		old.SHA256 = ""
		old.SHA256 = workerplan.Hash(*old)
		encoded, _ := json.Marshal(old.ExecutionCosts[0])
		var wire map[string]json.RawMessage
		if json.Unmarshal(encoded, &wire) != nil || wire["protective_funding"] != nil {
			t.Fatal("v3 wire changed")
		}
		writeJournal(legacy)
		retry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
		if e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, *old) {
			t.Fatal("v3 retry", e)
		}
		// An old decision cannot acquire a changed immutable proposal amount.
		incompatible := clone(c)
		incompatible.ID = "changed-old-budget"
		if _, e = s.CompareOwnedAllocation(ctx, incompatible, now, eval); !errors.Is(e, ErrOwnedAllocationConflict) {
			t.Fatal("old budget was reinterpreted", e)
		}
		freshDecision := decision.Record.Command
		freshDecision.ID = "fresh-protected-decision"
		// Use a genuinely later market occurrence, not a new name for the old
		// quote, when exercising a fresh v4 comparison after legacy v3 history.
		freshInput := collectorInput(at.Add(500*time.Millisecond), "fresh-protected-input")
		if _, e = s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, freshInput, now); e != nil {
			t.Fatal(e)
		}
		finalized, e := s.FinalizePaperDecision(ctx, freshDecision, now, eval)
		if e != nil {
			t.Fatal(e)
		}
		freshAliasCommand := freshDecision
		freshAliasCommand.ID = "fresh-protected-alias"
		freshAlias, e := s.FinalizePaperDecision(ctx, freshAliasCommand, now, eval)
		if e != nil {
			t.Fatal(e)
		}
		newBatch := clone(c)
		newBatch.ID = "new-source-alias-batch"
		newBatch.Decisions = []OwnedDecisionPin{{ID: freshAlias.Record.Command.ID, SHA256: freshAlias.Record.SHA256}, {ID: finalized.Record.Command.ID, SHA256: finalized.Record.SHA256}}
		if _, e = s.CompareOwnedAllocation(ctx, newBatch, noClock, eval); !errors.Is(e, ErrOwnedAllocationConflict) {
			t.Fatal("previously unseen source aliases accepted in one batch", e)
		}
		mixedCommand := clone(c)
		mixedCommand.ID = "mixed-funding"
		mixedCommand.Decisions = []OwnedDecisionPin{{ID: finalized.Record.Command.ID, SHA256: finalized.Record.SHA256}}
		// At the same clock, serialization permits either a complete capture
		// of the new plan or rejection of that plan after the comparison.
		racingPlan := clone(declarationCommand)
		racingPlan.ID = "racing-plan"
		racingPlan.Plan.FamilyID = "racing-family"
		planCatalogBefore, e := s.(PaperEvaluationDeclarationStore).PaperEvaluationDeclarations(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var racingReceipt PaperEvaluationDeclarationReceipt
		var racingErr, comparisonErr error
		var writers sync.WaitGroup
		writers.Go(func() {
			racingReceipt, racingErr = s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, racingPlan, now)
		})
		writers.Go(func() { _, comparisonErr = s.CompareOwnedAllocation(ctx, mixedCommand, now, eval) })
		writers.Wait()
		if comparisonErr != nil {
			t.Fatal("serialized comparison", comparisonErr)
		}
		if racingErr != nil && (!errors.Is(racingErr, ErrPaperEvaluationDeclarationInvalid) || !reflect.DeepEqual(racingReceipt, PaperEvaluationDeclarationReceipt{})) {
			t.Fatal("serialized plan result", racingReceipt, racingErr)
		}
		mixed, e := s.OwnedAllocations(ctx, eval)
		if e != nil || len(mixed.Records) != 2 || !reflect.DeepEqual(mixed.Records[0], *old) || mixed.Records[1].Schema != OwnedAllocationGrantSchema {
			t.Fatal("mixed v3/v4", e)
		}
		for _, record := range mixed.Records {
			if record.Command.ID != mixedCommand.ID {
				continue
			}
			found := false
			for _, pin := range record.EvaluationContext.Decisions[0].Declarations {
				if pin.ID == racingPlan.ID {
					found = true
				}
			}
			if found != (racingErr == nil) {
				t.Fatal("comparison omitted serialized plan", found, racingErr)
			}
		}
		// This branch installs manufactured legacy journal bytes. Restore its
		// temporary plan alongside those bytes before testing the original history.
		if racingErr == nil {
			if m, ok := s.(*Memory); ok {
				if actual, ok := m.paperEvaluationPlans[racingPlan.ID]; !ok || !reflect.DeepEqual(actual, racingReceipt.Record) {
					t.Fatal("temporary plan changed before fixture restore")
				}
				delete(m.paperEvaluationPlans, racingPlan.ID)
			} else {
				rawPlan, e := json.Marshal(racingReceipt.Record)
				if e != nil {
					t.Fatal(e)
				}
				removed, e := s.(*Postgres).pool.Exec(ctx, "DELETE FROM mantik_paper_evaluation_declarations WHERE id=$1 AND payload=$2", racingPlan.ID, rawPlan)
				if e != nil || removed.RowsAffected() != 1 {
					t.Fatal("temporary plan fixture restore", e)
				}
			}
		}
		if restoredPlans, e := s.(PaperEvaluationDeclarationStore).PaperEvaluationDeclarations(ctx); e != nil || !reflect.DeepEqual(restoredPlans, planCatalogBefore) {
			t.Fatal("temporary branch changed declaration catalogue", e)
		}
		writeJournal(journal)
	}
	// Old records retain the original v1 JSON/hash contract in a mixed journal.
	if !withModel {
		legacy := clone(journal)
		old := &legacy.Records[0].Record
		old.Schema = "mantik-owned-allocation-deferral-v1"
		old.Authority, old.Expirations = "service_owned_allocation_deferral_only", nil
		old.ExecutionSafety = nil
		old.AdmissionChecks = nil
		old.EvaluationContext = nil
		old.SHA256 = ""
		old.SHA256 = workerplan.Hash(*old)
		rawLegacy, _ := json.Marshal(old)
		var wire map[string]json.RawMessage
		if json.Unmarshal(rawLegacy, &wire) != nil || wire["admission_checks"] != nil {
			t.Fatal("legacy wire changed")
		}
		writeJournal(legacy)
		mixed, e := s.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(mixed.Records[0], *old) || mixed.Records[1].Schema != OwnedAllocationGrantSchema {
			t.Fatal("legacy replay", e)
		}
		oldRetry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
		if e != nil || !oldRetry.Duplicate || !reflect.DeepEqual(oldRetry.Record, *old) {
			t.Fatal("legacy retry", e)
		}
	}
	if withModel {
		if len(first.ExecutionSafety) != 1 || first.ExecutionSafety[0].Safety != strategy.Record.Command.Definition.Safety || first.ExecutionSafety[0].CostCeilingSHA256 != first.ExecutionCosts[0].SHA256 || first.ExecutionSafety[0].ComparisonID != first.Command.ID || first.ExecutionSafety[0].AdmissionAvailable || first.ExecutionSafety[0].ExecutionAvailable || first.ExecutionSafety[0].QualificationCredit {
			t.Fatal("missing actual execution safety binding", first.ExecutionSafety)
		}
		if first.AdmissionChecks[0].Schema != "mantik-owned-admission-readiness-v7" || first.AdmissionChecks[0].Binding.ExecutionSafetySHA256 != first.ExecutionSafety[0].SHA256 {
			t.Fatal("readiness did not bind actual safety requirements")
		}
		for _, replacement := range []string{"", workerplan.Hash("other-safety")} {
			bad := clone(journal)
			v := &bad.Records[0].Record
			check := &v.AdmissionChecks[0]
			check.Binding.ExecutionSafetySHA256 = replacement
			check.SHA256 = ""
			check.SHA256 = workerplan.Hash(*check)
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(*v)
			writeJournal(bad)
			if _, e := s.OwnedAllocations(ctx, eval); e == nil {
				t.Fatal("counterfeit readiness safety pin accepted")
			}
			if _, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil {
				t.Fatal("retry hid counterfeit readiness safety pin")
			}
		}
		for _, version := range []int{10, 11} {
			legacy := clone(journal)
			for i := range legacy.Records {
				v := &legacy.Records[i].Record
				v.Schema = fmt.Sprintf("mantik-owned-allocation-deferral-v%d", version)
				v.Authority, v.Expirations = "service_owned_allocation_deferral_only", nil
				for j := range v.AdmissionChecks {
					c := &v.AdmissionChecks[j]
					c.Schema = "mantik-owned-admission-readiness-v4"
					c.Binding.ExecutionSafetySHA256 = ""
					c.SHA256 = ""
					c.SHA256 = workerplan.Hash(*c)
				}
				if version == 10 {
					v.ExecutionSafety = nil
				}
				v.SHA256 = ""
				v.SHA256 = workerplan.Hash(*v)
			}
			writeJournal(legacy)
			oldRetry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
			if e != nil || !oldRetry.Duplicate || !reflect.DeepEqual(oldRetry.Record, legacy.Records[0].Record) {
				t.Fatal("legacy v10 bytes/retry changed", e)
			}
			if p, ok := s.(*Postgres); ok {
				reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
				if e != nil {
					t.Fatal(e)
				}
				got, e := reopened.CompareOwnedAllocation(ctx, c, noClock, eval)
				reopened.Close()
				if e != nil || !got.Duplicate || !reflect.DeepEqual(got.Record, legacy.Records[0].Record) {
					t.Fatal("legacy10 reopen", e)
				}
			}
		}
		writeJournal(journal)
		for _, mutate := range []func(*OwnedExecutionSafety){
			func(v *OwnedExecutionSafety) { v.Safety.StopCents++ }, func(v *OwnedExecutionSafety) { v.Safety.TargetCents++ }, func(v *OwnedExecutionSafety) { v.Safety.MaxHoldingSeconds++ }, func(v *OwnedExecutionSafety) { v.Safety.MaxQuoteAgeSeconds++ }, func(v *OwnedExecutionSafety) { v.DecisionSHA256 = workerplan.Hash("other") }, func(v *OwnedExecutionSafety) { v.AccountSHA256 = workerplan.Hash("other") }, func(v *OwnedExecutionSafety) { v.CostCeilingSHA256 = workerplan.Hash("other") }, func(v *OwnedExecutionSafety) { v.ExecutionAvailable = true },
		} {
			bad := clone(journal)
			v := &bad.Records[0].Record
			mutate(&v.ExecutionSafety[0])
			v.ExecutionSafety[0].SHA256 = ""
			v.ExecutionSafety[0].SHA256 = workerplan.Hash(v.ExecutionSafety[0])
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(*v)
			writeJournal(bad)
			if _, e := s.OwnedAllocations(ctx, eval); e == nil {
				t.Fatal("rehashed safety binding accepted")
			}
			if _, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil {
				t.Fatal("retry hid rehashed safety binding")
			}
		}
	}
	if withModel {
		for _, mutate := range []func(*OwnedExecutionCost){
			func(c *OwnedExecutionCost) { c.ProtectiveFunding.ExitShortfallCeilingCents-- }, func(c *OwnedExecutionCost) { c.ProtectiveFunding.TotalFundingCents-- }, func(c *OwnedExecutionCost) { c.ProtectiveFunding = nil }, func(c *OwnedExecutionCost) { c.MaxDebitCents-- }, func(c *OwnedExecutionCost) { c.FeeCeilingCents = 0 }, func(c *OwnedExecutionCost) { c.ModelSHA256 = workerplan.Hash("other") }, func(c *OwnedExecutionCost) { c.LimitCents++ },
		} {
			bad := clone(journal)
			r := &bad.Records[0].Record
			mutate(&r.ExecutionCosts[0])
			r.ExecutionCosts[0].SHA256 = ""
			r.ExecutionCosts[0].SHA256 = workerplan.Hash(r.ExecutionCosts[0])
			r.SHA256 = ""
			r.SHA256 = workerplan.Hash(*r)
			writeJournal(bad)
			if _, e := s.OwnedAllocations(ctx, eval); e == nil {
				t.Fatal("counterfeit cost read")
			}
			if _, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil {
				t.Fatal("retry hid counterfeit cost")
			}
		}
	}
	// Rehashing both layers cannot manufacture admission, qualification or source class.
	for _, mutate := range []func(*OwnedAdmissionReadiness){
		func(r *OwnedAdmissionReadiness) { r.AdmissionAvailable = true },
		func(r *OwnedAdmissionReadiness) { r.ExecutionAvailable = true },
		func(r *OwnedAdmissionReadiness) { r.QualificationCredit = true },
		func(r *OwnedAdmissionReadiness) { r.Status = "accepted" },
		func(r *OwnedAdmissionReadiness) { r.EvidenceClass = "qualified" },
		func(r *OwnedAdmissionReadiness) { r.ValidUntil = r.ValidUntil.Add(time.Hour) },
		func(r *OwnedAdmissionReadiness) { r.Binding = nil },
		func(r *OwnedAdmissionReadiness) { r.Binding.ComparisonID = "other" },
		func(r *OwnedAdmissionReadiness) { r.Binding.PolicyID = "other" },
		func(r *OwnedAdmissionReadiness) { r.Binding.PolicySHA256 = workerplan.Hash("other") },
		func(r *OwnedAdmissionReadiness) { r.Binding.AccountID = "other" },
		func(r *OwnedAdmissionReadiness) { r.Binding.AccountSHA256 = workerplan.Hash("other") },
		func(r *OwnedAdmissionReadiness) { r.Binding.ProposalSHA256 = workerplan.Hash("other") },
		func(r *OwnedAdmissionReadiness) { r.Binding.LedgerSHA256 = workerplan.Hash("other") },
		func(r *OwnedAdmissionReadiness) { r.Binding.ExecutionCostSHA256 = workerplan.Hash("other") },
	} {
		bad := clone(journal)
		r := &bad.Records[0].Record
		mutate(&r.AdmissionChecks[0])
		r.AdmissionChecks[0].SHA256 = ""
		r.AdmissionChecks[0].SHA256 = workerplan.Hash(r.AdmissionChecks[0])
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
		writeJournal(bad)
		if _, e := s.OwnedAllocations(ctx, eval); e == nil {
			t.Fatal("counterfeit readiness read")
		}
		if _, e := s.CompareOwnedAllocation(ctx, c, noClock, eval); e == nil {
			t.Fatal("retry hid counterfeit readiness")
		}
	}
	writeJournal(journal)
	historicalBefore, e := s.OwnedAllocations(ctx, eval)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "comparison-clock-later-renewal", Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, func() time.Time { return at.Add(3 * time.Second) }); e != nil {
		t.Fatal(e)
	}
	historicalAfter, e := s.OwnedAllocations(ctx, eval)
	if e != nil || !reflect.DeepEqual(historicalBefore, historicalAfter) {
		t.Fatal("new source clock rewrote historical comparisons", historicalAfter, e)
	}
	historicalRetry, e := s.CompareOwnedAllocation(ctx, c, noClock, eval)
	if e != nil || !historicalRetry.Duplicate || !reflect.DeepEqual(historicalRetry.Record, first) {
		t.Fatal("new source clock invalidated exact retry", historicalRetry, e)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		restored, e := reopened.OwnedAllocations(ctx, eval)
		if e != nil || !reflect.DeepEqual(restored, historicalBefore) {
			t.Fatal("source clock changed restart replay", restored, e)
		}
	}
	journal.Records[0].Record.Entry.Plan.Decisions[0].Status = "reserve_plan"
	r := &journal.Records[0].Record
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	if m, ok := s.(*Memory); ok {
		m.ownedAllocations = journal
	} else {
		raw, _ := json.Marshal(journal)
		if _, err = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1", raw); err != nil {
			t.Fatal(err)
		}
	}
	if partial, e := s.OwnedAllocations(ctx, eval); e == nil || !reflect.DeepEqual(partial, OwnedAllocationCatalog{}) {
		t.Fatal("counterfeit entry returned projection", partial, e)
	}
	if _, err = s.CompareOwnedAllocation(ctx, c, noClock, eval); err == nil {
		t.Fatal("retry hid corrupt journal")
	}
}

// Rehash the whole accounting prefix and readiness binding after changing only
// the ledger time. Accounting remains internally valid; source ownership must
// still tie that time to the actual comparison record.
func ownedAllocationEntryClockContract(t *testing.T, s ownedAllocationBackend, eval PaperDecisionEvaluator) {
	t.Helper()
	ctx := context.Background()
	var original ownedAllocationJournal
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		original = clone(m.ownedAllocations)
		m.mu.Unlock()
	} else {
		var raw []byte
		if err := s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
	}
	write := func(j ownedAllocationJournal) {
		t.Helper()
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.ownedAllocations = clone(j)
			m.mu.Unlock()
			return
		}
		raw, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_allocations SET payload=$1", raw); err != nil {
			t.Fatal(err)
		}
	}
	defer write(original)
	forged := clone(original)
	if len(forged.Records) != 1 {
		t.Fatal("fixture requires one comparison")
	}
	item := &forged.Records[0]
	r := &item.Record
	policy := item.Policies.Records[0]
	ledger, err := allocation.NewLedger(policy)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _, _, err = allocation.AppendLedger(ctx, ledger, r.Entry.Command, func() time.Time { return r.At.Add(time.Nanosecond) })
	if err != nil {
		t.Fatal(err)
	}
	r.Entry = ledger.Entries[0]
	r.LedgerSHA256 = ledger.SHA256
	r.AdmissionChecks, err = bindOwnedAdmissionReadiness(*r, policy)
	if err != nil {
		t.Fatal(err)
	}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	write(forged)
	if got, err := s.OwnedAllocations(ctx, eval); err == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
		t.Fatal("rehashed ledger time detached from comparison accepted", got, err)
	}
	if receipt, err := s.CompareOwnedAllocation(ctx, r.Command, func() time.Time { panic("corrupt retry read clock") }, eval); err == nil || !reflect.DeepEqual(receipt, OwnedAllocationReceipt{}) {
		t.Fatal("retry hid mismatched ledger time", receipt, err)
	}
}

func TestPostgresContractsOwnedAllocationUpgradeRollback(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "allocationupgrade")
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := policyAccount(t, s, "preserved")
	p := policyCommand(a, "preserved")
	if _, err = s.RegisterAllocationPolicy(ctx, p, func() time.Time { return time.Now().UTC() }); err != nil {
		t.Fatal(err)
	}
	policies, err := s.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	url := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=39"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	got, err := up.AllocationPolicies(ctx)
	if err != nil || !reflect.DeepEqual(policies, got) {
		t.Fatal("upgrade rewrote funding", err)
	}
	after, err := up.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed fixture", err)
	}
	// SQL failure rollback is exercised by the seeded owner contract above;
	// this empty migration must remain readable meanwhile.
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "empty"}, nil
	}
	empty, err := up.OwnedAllocations(ctx, eval)
	if err != nil || len(empty.Records) != 0 {
		t.Fatal(empty, err)
	}
	if _, err = up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=39"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, url); err == nil {
		broken.Close()
		t.Fatal("changed39 accepted")
	}
}

func assertOwnedReadiness(t *testing.T, r OwnedAllocationRecord, decision PaperDecisionRecord, strategy paperstrategy.Record, quote, class, status string) {
	t.Helper()
	if r.Schema != OwnedAllocationGrantSchema || len(r.AdmissionChecks) != 1 {
		t.Fatal(r)
	}
	c := r.AdmissionChecks[0]
	hash := c.SHA256
	c.SHA256 = ""
	if workerplan.Hash(c) != hash || c.Schema != "mantik-owned-admission-readiness-v7" || c.ReceiverID != r.Command.ReceiverID || c.DecisionID != decision.Command.ID || c.DecisionSHA256 != decision.SHA256 || c.StrategyID != strategy.Command.Definition.ID || c.StrategySHA256 != strategy.SHA256 || c.DefinitionSHA256 != strategy.DefinitionSHA256 || c.BasisSHA256 != decision.BasisSHA256 || c.QuoteInputID != quote || c.EvidenceClass != class || !c.CheckedAt.Equal(r.At) || !c.ValidUntil.Equal(r.Entry.Plan.Decisions[0].Proposal.ValidUntil) || c.Status != status || c.AdmissionAvailable || c.ExecutionAvailable || c.QualificationCredit || c.Authority != "service_owned_admission_readiness_only" {
		t.Fatal(c)
	}
	b := c.Binding
	if b == nil || b.ComparisonID != r.Command.ID || b.PolicySHA256 != r.PolicySHA256 || b.AccountID != r.Entry.Plan.AccountID || !decisionPin.MatchString(b.AccountSHA256) || b.PolicyID == "" || b.LedgerSHA256 != r.LedgerSHA256 || b.ProposalSHA256 != workerplan.Hash(r.Entry.Plan.Decisions[0].Proposal) {
		t.Fatal("unbound readiness", b)
	}
	if len(r.ExecutionCosts) == 0 && b.ExecutionCostSHA256 != "" || len(r.ExecutionCosts) == 1 && b.ExecutionCostSHA256 != r.ExecutionCosts[0].SHA256 {
		t.Fatal("cost binding", b)
	}
	if len(r.ExecutionSafety) == 0 && b.ExecutionSafetySHA256 != "" || len(r.ExecutionSafety) == 1 && b.ExecutionSafetySHA256 != r.ExecutionSafety[0].SHA256 {
		t.Fatal("safety requirements not bound", b)
	}
	if len(c.Gates) != 4 || c.Gates[0].Status != "verified" || c.Gates[1].ReasonCode != class || c.Gates[2].Status != "unavailable" || c.Gates[3].Status != "unavailable" {
		t.Fatal(c.Gates)
	}
	if status == "expired" && !reflect.DeepEqual(c.ReconsiderTriggers, []string{"new_finalized_decision"}) {
		t.Fatal(c)
	}
}

// Construct a prior wire version only for replay compatibility checks.
func legacyReadiness(r *OwnedAllocationRecord) {
	r.EvaluationContext = nil
	for i := range r.AdmissionChecks {
		c := &r.AdmissionChecks[i]
		c.Schema = "mantik-owned-admission-readiness-v1"
		c.Binding = nil
		c.SHA256 = ""
		c.SHA256 = workerplan.Hash(*c)
	}
}

// Failure of the schema fence must roll back its comment/version without
// rewriting the real legacy owner journal already retained by the contract.
func admissionBindingMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&before); err != nil {
		t.Fatal(err)
	}
	state, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, `DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=47;
 COMMENT ON TABLE mantik_owned_allocations IS 'schema46 migration fixture';
 CREATE FUNCTION reject_binding_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=47 THEN RAISE EXCEPTION 'test migration rollback'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_binding_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_binding_migration()`); err != nil {
		t.Fatal(err)
	}
	if err = p.migrate(ctx); err == nil {
		t.Fatal("failed schema fence committed")
	}
	var maximum int
	var comment string
	if err = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 46 {
		t.Fatal(maximum, err)
	}
	if err = p.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&comment); err != nil || comment != "schema46 migration fixture" {
		t.Fatal("migration comment did not roll back", comment, err)
	}
	if _, err = p.pool.Exec(ctx, "DROP TRIGGER reject_binding_migration ON mantik_schema; DROP FUNCTION reject_binding_migration()"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = p.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 107 {
		t.Fatal(maximum, err)
	}
	var after []byte
	if err = p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed journal bytes", err)
	}
	current, err := p.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(state, current) {
		t.Fatal("migration changed operational state", err)
	}
	var digest string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=47").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=47"); err != nil {
		t.Fatal(err)
	}
	if err = p.migrate(ctx); err == nil {
		t.Fatal("changed schema47 checksum accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=47", digest); err != nil {
		t.Fatal(err)
	}
}

func evaluationContextMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=48;
 CREATE FUNCTION reject_context_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=48 THEN RAISE EXCEPTION 'test context migration rollback'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_context_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_context_migration()`); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		broken.Close()
		t.Fatal("failed context migration opened")
	}
	var maximum int
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 47 {
		t.Fatal("context migration not rolled back", maximum, err)
	}
	var after []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed context migration rewrote legacy journal", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_context_migration ON mantik_schema; DROP FUNCTION reject_context_migration()"); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 107 {
		t.Fatal("context migration missing", maximum, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("context migration rewrote legacy journal", err)
	}
	var digest string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=48").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=48"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		broken.Close()
		t.Fatal("changed schema48 accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=48", digest); err != nil {
		t.Fatal(err)
	}
}

func completeEvaluationCaptureMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=49;
 CREATE FUNCTION reject_complete_capture_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=49 THEN RAISE EXCEPTION 'test context migration rollback'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_complete_capture_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_complete_capture_migration()`); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		broken.Close()
		t.Fatal("failed context migration opened")
	}
	var maximum int
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 48 {
		t.Fatal("context migration not rolled back", maximum, err)
	}
	var after []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed context migration rewrote legacy journal", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_complete_capture_migration ON mantik_schema; DROP FUNCTION reject_complete_capture_migration()"); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 107 {
		t.Fatal("context migration missing", maximum, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocations WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("context migration rewrote legacy journal", err)
	}
	var digest string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=49").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=49"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		broken.Close()
		t.Fatal("changed schema49 accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=49", digest); err != nil {
		t.Fatal(err)
	}
	// A supported reopen must not weaken the older/newer binary fence.
	var future int
	if err := p.pool.QueryRow(ctx, "SELECT coalesce(max(version),0)+1 FROM mantik_schema").Scan(&future); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "INSERT INTO mantik_schema(version,sha256) VALUES($1,'future-schema-fixture')", future); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		broken.Close()
		t.Fatal("future schema accepted")
	}
	if _, err := p.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version=$1 AND sha256='future-schema-fixture'", future); err != nil {
		t.Fatal(err)
	}
}
