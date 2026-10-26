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
	for _, v := range got.Variants {
		b := v.SampleBound
		if b.RequiredIndependentDecisions != c.Plan.MinIndependentDecisions || b.ObservedOccurrenceUpperBound != v.ObservedOutcomeDecisions || b.Status != "insufficient_occurrences" || b.IndependenceEstablished || b.AdmissionAvailable || b.QualificationCredit {
			t.Fatal("invalid owner sample bound", v)
		}
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

	entryBackend := s.(PaperEvaluationEntrySourceStore)
	entryReads := 0
	entry, err := entryBackend.PaperEvaluationEntrySources(ctx, c.ID, func() time.Time { entryReads++; return c.Plan.OutcomeDeadline }, eval, outcomeEval)
	if err != nil || entryReads != 1 || entry.Evidence.SHA256 != got.SHA256 || entry.Policy.SHA256 != c.PolicySHA256 || len(entry.Rows) != 2 {
		t.Fatal("atomic entry selection", entry, entryReads, err)
	}
	for _, row := range entry.Rows {
		if row.Decision.Command.ID == "first" {
			if row.Status != "entry_sources_selected" || row.Outcome == nil || row.Outcome.Outcome.Command.ID != "assessment-one" || row.BaseCost == nil || row.StressCost == nil || row.BaseCost.DecisionSHA256 != first.Record.SHA256 || row.StressCost.ModelSHA256 != workerplan.Hash(c.Plan.StressModel) {
				t.Fatal("entry owner/model binding", row)
			}
		} else if row.Status != "outcome_sources_unavailable" || row.Outcome != nil || row.BaseCost != nil {
			t.Fatal("invented source", row)
		}
	}
	entry.Policy.SHA256 = "changed"
	againEntry, err := entryBackend.PaperEvaluationEntrySources(ctx, c.ID, func() time.Time { return c.Plan.OutcomeDeadline }, eval, outcomeEval)
	if err != nil || againEntry.Policy.SHA256 != c.PolicySHA256 {
		t.Fatal("entry source detachment", err)
	}
	if _, err = entryBackend.PaperEvaluationEntrySources(ctx, c.ID, nil, eval, outcomeEval); !errors.Is(err, ErrPaperEvaluationEvidenceInvalid) {
		t.Fatal("nil entry clock", err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed state", err)
	}
	got.Rows[0].Decision.Status = "changed"
	got.Variants[0].SampleBound.RequiredIndependentDecisions = 0
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
	// A real late owner assessment stays retained and inspectable, but cannot
	// extend the declared outcome window on a later evidence read.
	lateAt := c.Plan.OutcomeDeadline.Add(time.Nanosecond)
	late, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "assessment-late", ReceiverID: c.ReceiverID, DecisionID: first.Record.Command.ID, DecisionSHA256: first.Record.SHA256}, func() time.Time { return lateAt }, eval, outcomeEval)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return lateAt.Add(time.Second) }, eval, outcomeEval)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range report.Rows {
		if row.Decision.Command.ID == "first" {
			found = true
			if len(row.Outcomes) != 2 || len(row.LateOutcomes) != 1 || !reflect.DeepEqual(row.LateOutcomes[0], late.Record) {
				t.Fatal("deadline partition lost owner evidence", row)
			}
		}
	}
	lateEntry, entryErr := entryBackend.PaperEvaluationEntrySources(ctx, c.ID, func() time.Time { return lateAt.Add(time.Second) }, eval, outcomeEval)
	if entryErr != nil {
		t.Fatal(entryErr)
	}
	for _, row := range lateEntry.Rows {
		if row.Outcome != nil && row.Outcome.Outcome.Command.ID == "assessment-late" {
			t.Fatal("late outcome expanded entry cut")
		}
	}
	if !found || report.Schema != "mantik-owned-paper-evaluation-evidence-v3" || report.EvaluationAvailable || report.AdmissionAvailable || report.ExecutionAvailable || report.QualificationCredit {
		t.Fatal(report)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		restored, err := reopened.PaperEvaluationEvidence(ctx, c.ID, func() time.Time { return report.AsOf }, eval, outcomeEval)
		if err != nil || !reflect.DeepEqual(restored, report) {
			t.Fatal("reconnect changed deadline partition", restored, err)
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
	at := plans.Records[0].Command.Plan.OutcomeDeadline.Add(time.Second)
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
	if err != nil || r.Variants[0].ObservedOutcomeDecisions != 1 || r.Variants[0].SampleBound.ObservedOccurrenceUpperBound != 1 || r.Variants[0].SampleBound.Status != "insufficient_occurrences" || r.IndependentSamplesAvailable || r.QualificationCredit {
		t.Fatal(r, err)
	}

	lateOnly := clone(observed)
	deadline := plans.Records[0].Command.Plan.OutcomeDeadline
	for i := range lateOnly.Records {
		lateOnly.Records[i].EvaluatedAt = deadline.Add(time.Nanosecond)
	}
	lateReport, err := paperEvaluationEvidence(context.Background(), plans.Records[0].Command.ID, plans, decisions, lateOnly, m.paperDecisions, at)
	if err != nil || lateReport.Variants[0].ObservedOutcomeDecisions != 0 || lateReport.Variants[0].SampleBound.ObservedOccurrenceUpperBound != 0 {
		t.Fatal("late observed attempts enlarged declared sample", lateReport, err)
	}
	for _, row := range lateReport.Rows {
		if row.Decision.Command.ID == "first" && (len(row.Outcomes) != 0 || len(row.LateOutcomes) != len(lateOnly.Records)) {
			t.Fatal("late raw attempts not retained", row)
		}
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

func TestPaperEvaluationSampleBounds(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, observed := range []int{0, 1, 2, 3, 1000000} {
			b := paperEvaluationSampleBound(2, observed, complete)
			status, reason := "pending_outcome_deadline", "predeclared_outcome_window_not_closed"
			if complete {
				status = "independence_unproven"
				reason = "occurrence_count_does_not_establish_independent_samples"
				if observed < 2 {
					status = "insufficient_occurrences"
					reason = "observed_occurrence_upper_bound_below_predeclared_minimum"
				}
			}
			if b.Method != "predeclared-observed-occurrence-upper-bound-v1" || b.RequiredIndependentDecisions != 2 || b.ObservedOccurrenceUpperBound != observed || b.Status != status || b.ReasonCode != reason || b.IndependenceEstablished || b.AdmissionAvailable || b.QualificationCredit {
				t.Fatal(complete, observed, b)
			}
		}
	}
}
