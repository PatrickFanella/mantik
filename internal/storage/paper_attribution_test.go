package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5/pgxpool"
)

type attributionBackend interface {
	ownedAllocationBackend
	PaperOutcomeStore
	PaperAttributionStore
	OwnedAdmissionReviewStore
}

func TestMemoryPaperAttribution(t *testing.T) { paperAttributionContracts(t, NewMemory()) }
func TestPostgresContractsPaperAttribution(t *testing.T) {
	paperAttributionContracts(t, timerPostgres(t, "paperattribution"))
}
func paperAttributionContracts(t *testing.T, s attributionBackend) {
	t.Helper()
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
	p.ExecutionModel = &paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60, FeeBPS: 20}
	policy, err := s.RegisterAllocationPolicy(ctx, p, clock)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(at, "attribution-quote")
	if _, err = s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}, input, clock); err != nil {
		t.Fatal(err)
	}
	de := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
		if c.ID == "abstain" {
			return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "storage_fixture_no_action"}, nil
		}
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_fixture_only", Signal: true, PriceCents: 10000, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	oe := func(b PaperOutcomeBasis, at time.Time) (PaperOutcomeEvaluation, error) {
		if b.Decision.Evaluation.Action == "abstain" {
			return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "decision_abstained"}, nil
		}
		return PaperOutcomeEvaluation{Status: "observed", ReasonCode: "storage_fixture_only", EntryPriceCents: 10000, ObservedPriceCents: 10100, PriceDeltaCents: 100}, nil
	}
	empty, err := s.PaperAttribution(ctx, de, oe)
	if err != nil || len(empty.Rows) != 0 {
		t.Fatal(empty, err)
	}
	decision, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "candidate", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, de)
	if err != nil {
		t.Fatal(err)
	}
	abstain, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "abstain", ReceiverID: owner, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, de)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.PaperAttribution(ctx, de, oe)
	if err != nil || len(missing.Rows) != 2 || missing.Rows[1].ObservationStatus != "not_assessed" {
		t.Fatal(missing, err)
	}
	for _, c := range []PaperOutcomeCommand{{ID: "observation-one", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, {ID: "same-decision-another-assessment", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, {ID: "no-action", ReceiverID: owner, DecisionID: abstain.Record.Command.ID, DecisionSHA256: abstain.Record.SHA256}} {
		if _, err = s.AssessPaperOutcome(ctx, c, clock, de, oe); err != nil {
			t.Fatal(err)
		}
	}
	stress := *p.ExecutionModel
	stress.FeeBPS++
	plan := PaperEvaluationDeclarationCommand{ID: "attribution-plan", ReceiverID: owner, Recipe: PaperEvaluationRecipe, PolicyID: p.Policy.ID, PolicySHA256: policy.Record.SHA256, Plan: PaperEvaluationPlan{FamilyID: "attribution-family", Variants: []PaperEvaluationVariant{{ID: d.ID, SHA256: strategy.Record.SHA256}}, WindowStart: at.Add(time.Hour), WindowEnd: at.Add(2 * time.Hour), OutcomeDeadline: at.Add(4 * time.Hour), MinIndependentDecisions: 30, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 3600, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress}}
	declaration, err := s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, plan, clock)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := s.CompareOwnedAllocation(ctx, OwnedAllocationCommand{ID: "comparison", ReceiverID: owner, Decisions: []OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}, clock, de)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	original, err := s.PaperAttribution(ctx, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	if original.Schema != "mantik-owned-paper-attribution-v3" || original.ReceiverID != owner || original.ExecutionAvailable || original.QualificationCredit || original.Authority != "service_owned_phase_attribution_only" || len(original.Rows) != 2 {
		t.Fatal(original)
	}
	row := original.Rows[1]
	if !reflect.DeepEqual(row.Decision, decision.Record) || row.DefinitionSHA256 != strategy.Record.DefinitionSHA256 || row.EvidenceClass != d.InputEvidenceClass || row.Method != paperstrategy.Method || row.Symbol != "SPY" || row.QualificationCredit || len(row.Observations) != 2 || len(row.Allocations) != 1 {
		t.Fatal(row)
	}
	if row.Observations[0].Evaluation.PriceDeltaCents != 100 || row.Economics.FeesCents != nil || row.Economics.RealizedPnLCents != nil || row.Economics.ExecutedQuantity != nil || row.Economics.BaselineStatus != "economic_baselines_unavailable" {
		t.Fatal("price change became economic outcome", row)
	}
	a := row.Allocations[0]
	if a.ExecutionSafety == nil || !reflect.DeepEqual(*a.ExecutionSafety, comparison.Record.ExecutionSafety[0]) {
		t.Fatal("missing retained execution safety attribution", a)
	}
	if a.Status != "defer" || a.Reasons[0] != "admission_unavailable" || a.RecordSHA256 != comparison.Record.SHA256 || a.PolicySHA256 != comparison.Record.PolicySHA256 || a.AccountID != account.Config.ID || a.CostCeiling == nil || a.CostCeiling.FeeCeilingCents == 0 || a.RequestedFundingCents != comparison.Record.ExecutionCosts[0].ProtectiveFunding.TotalFundingCents {
		t.Fatal(a)
	}
	if a.AdmissionReadiness == nil || !reflect.DeepEqual(*a.AdmissionReadiness, comparison.Record.AdmissionChecks[0]) || a.AdmissionReadiness.Binding == nil || a.AdmissionReadiness.Binding.ComparisonID != a.ComparisonID {
		t.Fatal("missing retained admission readiness", a)
	}
	if a.EvaluationContext == nil || a.EvaluationContext.SourceSHA256 != comparison.Record.EvaluationContext.SourceSHA256 || !reflect.DeepEqual(a.EvaluationContext.Binding, comparison.Record.EvaluationContext.Decisions[0]) || a.EvaluationContext.AdmissionAvailable || a.EvaluationContext.ExecutionAvailable || a.EvaluationContext.QualificationCredit || !reflect.DeepEqual(a.EvaluationContext.Binding.Declarations, []OwnedDecisionPin{{ID: declaration.Record.Command.ID, SHA256: declaration.Record.SHA256}}) {
		t.Fatal("retained evaluation context", a.EvaluationContext)
	}
	plan.ID = "later-attribution-plan"
	plan.Plan.FamilyID = "later-attribution-family"
	if _, err := s.(PaperEvaluationDeclarationStore).DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	if view, err := s.PaperAttribution(ctx, de, oe); err != nil || !reflect.DeepEqual(view, original) {
		t.Fatal("later declaration rewrote attribution", err)
	}
	if original.Rows[0].Decision.Evaluation.Action != "abstain" || original.Rows[0].Observations[0].Evaluation.Status != "not_applicable" || len(original.Rows[0].Allocations) != 0 {
		t.Fatal("valid abstention lost", original.Rows[0])
	}
	// Concurrent readers retain a consistent detached owner snapshot.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			view, e := s.PaperAttribution(ctx, de, oe)
			if e != nil || !reflect.DeepEqual(view, original) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	changed := clone(original)
	changed.Rows[1].Observations[0].Evaluation.PriceDeltaCents = 999
	changed.Rows[1].Allocations[0].Reasons[0] = "forged"
	changed.Rows[1].Allocations[0].EvaluationContext.Binding.Declarations[0].ID = "forged"
	changed.Rows[1].Allocations[0].CostCeiling.FeeCeilingCents = 0
	changed.Rows[1].Allocations[0].AdmissionReadiness.Binding.ComparisonID = "forged"
	changed.Rows[1].Allocations[0].AdmissionReadiness.Gates[0].ReasonCode = "forged"
	again, err := s.PaperAttribution(ctx, de, oe)
	if err != nil || !reflect.DeepEqual(again, original) {
		t.Fatal("mutable readback", err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("attribution changed operational state")
	}
	// A concurrent owner write can appear wholly before or after a read, never
	// as an orphaned outcome or a mixed-phase partial response.
	wg.Go(func() {
		_, e := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "concurrent-assessment", ReceiverID: owner, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, func() time.Time { return at.Add(time.Second) }, de, oe)
		if e != nil {
			t.Error(e)
		}
	})
	for range 2 {
		wg.Go(func() {
			for range 2 {
				view, e := s.PaperAttribution(ctx, de, oe)
				if e != nil {
					t.Error(e)
					return
				}
				if len(view.Rows) != 2 || len(view.Rows[1].Observations) < 2 || len(view.Rows[1].Observations) > 3 || len(view.Rows[1].Allocations) != 1 || view.Rows[1].Economics.FeesCents != nil {
					t.Error("mixed owner snapshot", view)
				}
			}
		})
	}
	wg.Wait()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.PaperAttribution(canceled, de, oe); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	attributionAdmissionReviewContracts(t, s, comparison.Record, decision.Record, de, oe)
	// A self-rehashed outcome may not hide behind another selected decision.
	var retained retainedPaperOutcome
	if m, ok := s.(*Memory); ok {
		retained = clone(m.paperOutcomes["observation-one"])
	} else {
		var raw []byte
		if err = s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_outcomes WHERE id='observation-one'").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &retained) != nil {
			t.Fatal("decode retained outcome")
		}
	}
	retained.Record.Evaluation.PriceDeltaCents = 999
	retained.Record.SHA256 = ""
	retained.Record.SHA256 = workerplan.Hash(retained.Record)
	if m, ok := s.(*Memory); ok {
		m.paperOutcomes["observation-one"] = retained
	} else {
		raw, _ := json.Marshal(retained)
		if _, err = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_outcomes SET payload=$1 WHERE id='observation-one'", raw); err != nil {
			t.Fatal(err)
		}
	}
	if partial, err := s.PaperAttribution(ctx, de, oe); err == nil || partial.Rows != nil {
		t.Fatal("corrupt source produced partial attribution", partial, err)
	}
}

func attributionAdmissionReviewContracts(t *testing.T, s attributionBackend, comparison OwnedAllocationRecord, decision PaperDecisionRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) {
	t.Helper()
	ctx := context.Background()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	get := func() PaperAttributionCatalog {
		t.Helper()
		v, err := s.PaperAttribution(ctx, de, oe)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	initial := get()
	for _, row := range initial.Rows {
		if h := row.AdmissionReviewHistory; h == nil || h.Records == nil || len(h.Records) != 0 || h.CurrentID != "" {
			t.Fatal("missing explicit empty review history", row)
		}
	}
	command := OwnedAdmissionReviewCommand{ID: "review-one", ReceiverID: decision.Command.ReceiverID, ComparisonID: comparison.Command.ID, ComparisonSHA256: comparison.SHA256, DecisionID: decision.Command.ID, DecisionSHA256: decision.SHA256}
	first, err := s.ReviewOwnedAdmission(ctx, command, func() time.Time { return comparison.At.Add(time.Second) }, de, oe)
	if err != nil || first.Record.Status != "deferred" {
		t.Fatal(first, err)
	}
	retry, err := s.ReviewOwnedAdmission(ctx, command, func() time.Time { panic("retry read fresh clock") }, de, oe)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	one := get()
	if one.SourceSHA256 == initial.SourceSHA256 || one.Rows[1].SHA256 == initial.Rows[1].SHA256 || !reflect.DeepEqual(one.Rows[0], initial.Rows[0]) {
		t.Fatal("review scope/source binding")
	}
	command.ID, command.PreviousID, command.PreviousSHA256 = "review-expired", first.Record.Command.ID, first.Record.SHA256
	var wg sync.WaitGroup
	wg.Go(func() {
		receipt, err := s.ReviewOwnedAdmission(ctx, command, func() time.Time { return first.Record.Readiness.ValidUntil }, de, oe)
		if err != nil || receipt.Record.Status != "expired" {
			t.Error(receipt, err)
		}
	})
	for range 2 {
		wg.Go(func() {
			view, err := s.PaperAttribution(ctx, de, oe)
			if err != nil {
				t.Error(err)
				return
			}
			h := view.Rows[1].AdmissionReviewHistory
			if h == nil || len(h.Records) < 1 || len(h.Records) > 2 || h.CurrentID != h.Records[len(h.Records)-1].Command.ID {
				t.Error("mixed review snapshot", h)
			}
		})
	}
	wg.Wait()
	final := get()
	h := final.Rows[1].AdmissionReviewHistory
	actual, err := s.OwnedAdmissionReviews(ctx, de, oe)
	if err != nil || !reflect.DeepEqual(h.Records, actual.Records) || len(actual.CurrentIDs) != 1 || h.CurrentID != actual.CurrentIDs[0] || h.SourceSHA256 != workerplan.Hash(actual) || h.AdmissionAvailable || h.ExecutionAvailable || h.QualificationCredit || final.Rows[1].Economics.FeesCents != nil {
		t.Fatal("review owner changed by attribution", h, actual, err)
	}
	changed := clone(final)
	changed.Rows[1].AdmissionReviewHistory.Records[0].Readiness.Gates[0].ReasonCode = "forged"
	changed.Rows[1].AdmissionReviewHistory.CurrentID = "forged"
	if !reflect.DeepEqual(get(), final) {
		t.Fatal("mutable review projection")
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review attribution changed operational state", err)
	}
	if pg, ok := s.(*Postgres); ok {
		request, cancel := context.WithCancel(ctx)
		tracer := &cancelCollectorCommit{cancel: cancel}
		config := pg.pool.Config()
		config.ConnConfig.Tracer = tracer
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		partial, err := (&Postgres{pool: pool}).PaperAttribution(request, de, oe)
		pool.Close()
		cancel()
		if !tracer.fired.Load() || err == nil || !reflect.DeepEqual(partial, PaperAttributionCatalog{}) {
			t.Fatal("failed read COMMIT exposed review history", partial, err, tracer.fired.Load())
		}
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		restored, err := reopened.PaperAttribution(ctx, de, oe)
		reopened.Close()
		if err != nil || !reflect.DeepEqual(restored, final) {
			t.Fatal("reopen changed review attribution", err)
		}
	}
	// A self-rehashed assertion is not an owner review, even when selecting a
	// different decision. Preserve and restore the exact original owner record.
	original := actual.Records[0]
	for name, mutate := range map[string]func(*OwnedAdmissionReview){
		"grant":      func(r *OwnedAdmissionReview) { r.AdmissionAvailable = true },
		"comparison": func(r *OwnedAdmissionReview) { r.Command.ComparisonSHA256 = workerplan.Hash("other") },
		"decision":   func(r *OwnedAdmissionReview) { r.Command.DecisionID = "missing" },
		"status":     func(r *OwnedAdmissionReview) { r.Status = "admitted" },
		"predecessor": func(r *OwnedAdmissionReview) {
			r.Command.PreviousID = "other"
			r.Command.PreviousSHA256 = workerplan.Hash("other")
		},
	} {
		t.Run("attribution review "+name, func(t *testing.T) {
			write := func(r OwnedAdmissionReview) {
				if m, ok := s.(*Memory); ok {
					m.admissionReviews[r.Command.ID] = r
					return
				}
				raw, _ := json.Marshal(r)
				if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_owned_admission_reviews SET payload=$1 WHERE id=$2", raw, r.Command.ID); err != nil {
					t.Fatal(err)
				}
			}
			bad := clone(original)
			mutate(&bad)
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			write(bad)
			defer write(original)
			if partial, err := s.PaperAttribution(ctx, de, oe); err == nil || !reflect.DeepEqual(partial, PaperAttributionCatalog{}) {
				t.Fatal("corrupt review produced partial attribution", partial, err)
			}
		})
	}
	if !reflect.DeepEqual(get(), final) {
		t.Fatal("restored owner changed")
	}
}
