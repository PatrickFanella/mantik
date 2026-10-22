package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type evaluationEvidenceBackend interface {
	ownedAllocationBackend
	PaperEvaluationDeclarationStore
	PaperEvaluationEvidenceStore
	PaperOutcomeStore
}

func TestMemoryPaperEvaluationEvidence(t *testing.T) {
	for _, class := range []string{"prospective_market_unqualified"} {
		t.Run(class, func(t *testing.T) { evaluationEvidenceContracts(t, NewMemory(), class) })
	}
}
func TestPostgresContractsPaperEvaluationEvidence(t *testing.T) {
	for _, class := range []string{"prospective_market_unqualified"} {
		t.Run(class, func(t *testing.T) { evaluationEvidenceContracts(t, timerPostgres(t, "evaluationevidence"), class) })
	}
}
func evaluationEvidenceContracts(t *testing.T, s evaluationEvidenceBackend, class string) {
	t.Helper()
	ctx := context.Background()
	c, at := declarationFixture(t, s, class)
	if _, err := s.DeclarePaperEvaluation(ctx, c, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	start := c.Plan.WindowStart
	reg := feedcollector.Registration{ID: "evaluation-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: class, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, cmd := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, c.ReceiverID, cmd, func() time.Time { return start.Add(-2 * time.Second) }); err != nil {
			t.Fatalf("collector operation=%s receiver=%s command=%+v: %v", cmd.Operation, c.ReceiverID, cmd, err)
		}
	}
	input := collectorInput(start.Add(time.Second), "evaluation-quote")
	input.EvidenceClass = class
	if _, err := s.SubmitCollectorResearch(ctx, c.ReceiverID, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}, input, func() time.Time { return start.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	eval := func(cmd PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		action := "entry_candidate"
		if cmd.ID == "abstain" {
			action = "abstain"
		}
		return PaperDecisionEvaluation{Action: action, ReasonCode: "storage_fixture_only", PriceCents: b.Feed.Routing.Inputs[input.ID].Input.PriceCents, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	var first PaperDecisionReceipt
	for _, spec := range []struct {
		id      string
		variant int
		at      time.Time
	}{{"first", 0, start.Add(time.Second)}, {"alias", 0, start.Add(2 * time.Second)}, {"paired-variant", 1, start.Add(2 * time.Second)}, {"abstain", 0, start.Add(2 * time.Second)}, {"late", 0, c.Plan.WindowEnd}} {
		v := c.Plan.Variants[spec.variant]
		r, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: spec.id, ReceiverID: c.ReceiverID, StrategyID: v.ID, StrategySHA256: v.SHA256, RegistrationID: reg.ID}, func() time.Time { return spec.at }, eval)
		if err != nil {
			t.Fatal(spec.id, err)
		}
		if spec.id == "first" {
			first = r
		}
	}
	outcomeEval := func(b PaperOutcomeBasis, _ time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "waiting_price", ReasonCode: "no_forward_price_fixture", DueAt: b.Decision.FinalizedAt.Add(time.Hour), WindowEnd: b.Decision.FinalizedAt.Add(2 * time.Hour)}, nil
	}
	for _, id := range []string{"assessment-one", "assessment-two"} {
		if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: id, ReceiverID: c.ReceiverID, DecisionID: first.Record.Command.ID, DecisionSHA256: first.Record.SHA256}, func() time.Time { return c.Plan.OutcomeDeadline }, eval, outcomeEval); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	clock := func() time.Time { reads++; return c.Plan.OutcomeDeadline }
	got, err := s.PaperEvaluationEvidence(ctx, c.ID, clock, eval, outcomeEval)
	if err != nil || reads != 1 || len(got.Rows) != 5 || got.Phase != "outcome_deadline_elapsed" || got.IndependentSamplesAvailable || got.EvaluationAvailable || got.AdmissionAvailable || got.ExecutionAvailable || got.QualificationCredit {
		t.Fatal(got, reads, err)
	}
	want := got.SHA256
	unsigned := clone(got)
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != want {
		t.Fatal("report hash")
	}
	statuses := map[string]PaperEvaluationEvidenceRow{}
	for _, r := range got.Rows {
		statuses[r.Decision.Command.ID] = r
	}
	if statuses["late"].Status != "decision_after_window" || statuses["abstain"].Status != "abstention" || len(statuses["first"].Outcomes) != 2 || got.Variants[0].ObservedOutcomeDecisions != 0 {
		t.Fatal(statuses, got.Variants)
	}
	if class == "synthetic_unqualified" {
		if got.DistinctProviderOccurrences != 0 || got.Variants[0].SelectedDecisions != 0 || statuses["first"].Status != "synthetic_not_selected" {
			t.Fatal(got)
		}
	} else if got.DistinctProviderOccurrences != 1 || got.Variants[0].SelectedDecisions != 1 || got.Variants[1].SelectedDecisions != 1 || statuses["alias"].Status != "duplicate_strategy_occurrence" || statuses["alias"].CanonicalDecisionID != "first" {
		t.Fatal(got)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed state", err)
	}
	got.Rows[0].Decision.Status = "changed"
	again, err := s.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return c.Plan.OutcomeDeadline }, eval, outcomeEval)
	if err != nil || again.SHA256 != want {
		t.Fatal("detachment", err)
	}
	if _, err = s.PaperEvaluationEvidence(ctx, "missing", clock, eval, outcomeEval); !errors.Is(err, ErrPaperEvaluationDeclarationNotFound) {
		t.Fatal(err)
	}
	if _, err = s.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return at }, eval, outcomeEval); !errors.Is(err, ErrPaperEvaluationEvidenceInvalid) {
		t.Fatal("backdated clock", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.PaperEvaluationEvidence(canceled, c.ID, func() time.Time { panic("cancelled read clock") }, eval, outcomeEval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			r, err := s.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return c.Plan.OutcomeDeadline }, eval, outcomeEval)
			if err != nil || r.SHA256 != want {
				t.Error("concurrent read", err)
			}
		})
	}
	wg.Wait()
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		r, err := reopened.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return c.Plan.OutcomeDeadline }, eval, outcomeEval)
		if err != nil || r.SHA256 != want {
			t.Fatal("reconnect", err)
		}
	}
}

// Selector-only exclusions use explicit manufactured records. Source custody
// and replay are separately covered through both real storage backends above.
func TestPaperEvaluationEvidenceExclusionsAndOutcomeDedup(t *testing.T) {
	m := NewMemory()
	evaluationEvidenceContracts(t, m, "prospective_market_unqualified")
	plans := PaperEvaluationDeclarationCatalog{ReceiverID: m.receiverID, Records: []PaperEvaluationDeclaration{}}
	for _, r := range m.paperEvaluationPlans {
		plans.Records = append(plans.Records, clone(r))
	}
	decisions := PaperDecisionCatalog{ReceiverID: m.receiverID, Records: []PaperDecisionRecord{}}
	for _, r := range m.paperDecisions {
		decisions.Records = append(decisions.Records, clone(r.Record))
	}
	outcomes := PaperOutcomeCatalog{ReceiverID: m.receiverID, Records: []PaperOutcomeRecord{}}
	for _, r := range m.paperOutcomes {
		outcomes.Records = append(outcomes.Records, clone(r.Record))
	}
	at := plans.Records[0].Command.Plan.OutcomeDeadline
	for name, change := range map[string]func(map[string]retainedPaperDecision){
		"source_unavailable_at_decision": func(all map[string]retainedPaperDecision) {
			r := all["first"]
			id := r.Record.Evaluation.Coverage.Quote.InputID
			q := r.Basis.Feed.Routing.Inputs[id]
			q.Input.Provenance.AvailableAt = r.Record.FinalizedAt.Add(time.Nanosecond)
			r.Basis.Feed.Routing.Inputs[id] = q
			all["first"] = r
		},
		"quote_before_window": func(all map[string]retainedPaperDecision) {
			r := all["first"]
			id := r.Record.Evaluation.Coverage.Quote.InputID
			q := r.Basis.Feed.Routing.Inputs[id]
			q.Input.OccurredAt = plans.Records[0].Command.Plan.WindowStart.Add(-time.Nanosecond)
			r.Basis.Feed.Routing.Inputs[id] = q
			all["first"] = r
		},
		"synthetic_not_selected": func(all map[string]retainedPaperDecision) {
			r := all["first"]
			r.Basis.Strategy.Command.Definition.InputEvidenceClass = "synthetic_unqualified"
			id := r.Record.Evaluation.Coverage.Quote.InputID
			q := r.Basis.Feed.Routing.Inputs[id]
			q.Input.EvidenceClass = "synthetic_unqualified"
			r.Basis.Feed.Routing.Inputs[id] = q
			all["first"] = r
		},
	} {
		t.Run(name, func(t *testing.T) {
			all := clone(m.paperDecisions)
			change(all)
			r, err := paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, decisions, outcomes, all, at)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range r.Rows {
				if row.Decision.Command.ID == "first" && row.Status != name {
					t.Fatal(row)
				}
			}
		})
	}
	observed := clone(outcomes)
	for i := range observed.Records {
		observed.Records[i].Evaluation.Status = "observed"
	}
	r, err := paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, decisions, observed, m.paperDecisions, at)
	if err != nil || r.Variants[0].ObservedOutcomeDecisions != 1 || r.IndependentSamplesAvailable || r.QualificationCredit {
		t.Fatal(r, err)
	}
	observed.Records[0].EvaluatedAt = at.Add(time.Nanosecond)
	if _, err = paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, decisions, observed, m.paperDecisions, at); !errors.Is(err, ErrPaperEvaluationEvidenceInvalid) {
		t.Fatal("future outcome accepted", err)
	}
	base, err := paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, decisions, outcomes, m.paperDecisions, at)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	padded := clone(decisions)
	oldLength := len(padded.Records[0].Evaluation.ReasonCode)
	padded.Records[0].Evaluation.ReasonCode = strings.Repeat("x", MaxPaperEvaluationEvidenceBytes-len(raw)+oldLength+1)
	// This manufactured response would be only one byte over the final wire
	// limit, while its unsigned form still fits. The hash bytes count too.
	if _, err = paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, padded, outcomes, m.paperDecisions, at); !errors.Is(err, ErrPaperEvaluationEvidenceInvalid) {
		t.Fatal("final hash escaped response byte limit", err)
	}
}
