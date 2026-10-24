package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"testing"
	"time"
)

func TestPostgresContractsDecisionOutcomeReadCommit(t *testing.T) {
	s := timerPostgres(t, "decisionoutcomereadcommit")
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	clock := func() time.Time { return at }
	initial, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	def := strategyDefinition("membership-strategy")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, clock)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "membership-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	input := collectorInput(at, "membership-input")
	if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock); err != nil {
		t.Fatal(err)
	}
	publish := func(id string) {
		t.Helper()
		if _, e := s.PublishFeedEvidence(ctx, owner, reg.ID, id, fence, json.RawMessage(`{"fixture":"membership"}`), clock, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); e != nil {
			t.Fatal(e)
		}
	}
	publish("membership-raw")
	command := PaperDecisionCommand{ID: "membership-decision", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "membership_fixture_only"}, nil
	}
	at = at.Add(time.Second)
	if _, err = s.FinalizePaperDecision(ctx, command, clock, eval); err != nil {
		t.Fatal(err)
	}
	// These sources arrive after decision finalization but before outcome capture.
	at = at.Add(time.Second)
	input = collectorInput(at, "outcome-input")
	if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, clock); err != nil {
		t.Fatal(err)
	}
	publish("outcome-raw")
	decision, err := s.PaperDecisionSources(ctx, command.ID, eval)
	if err != nil {
		t.Fatal(err)
	}
	outcomeCommand := PaperOutcomeCommand{ID: "membership-outcome", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: decision.Decision.SHA256}
	outcomeEval := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "membership_fixture_only"}, nil
	}
	at = at.Add(time.Second)
	if _, err = s.AssessPaperOutcome(ctx, outcomeCommand, clock, eval, outcomeEval); err != nil {
		t.Fatal(err)
	}

	decisions, err := s.PaperDecisions(ctx, eval)
	if err != nil || len(decisions.Records) != 1 {
		t.Fatal(decisions, err)
	}
	outcomes, err := s.PaperOutcomes(ctx, eval, outcomeEval)
	if err != nil || len(outcomes.Records) != 1 {
		t.Fatal(outcomes, err)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { panic("retry read clock") }
	operations := []struct {
		name string
		call func(context.Context, *Postgres) (any, error)
	}{
		{"decisions", func(c context.Context, p *Postgres) (any, error) { return p.PaperDecisions(c, eval) }},
		{"outcomes", func(c context.Context, p *Postgres) (any, error) { return p.PaperOutcomes(c, eval, outcomeEval) }},
		{"decision_sources", func(c context.Context, p *Postgres) (any, error) { return p.PaperDecisionSources(c, command.ID, eval) }},
		{"outcome_sources", func(c context.Context, p *Postgres) (any, error) {
			return p.PaperOutcomeSources(c, outcomeCommand.ID, eval, outcomeEval)
		}},
		{"decision_retry", func(c context.Context, p *Postgres) (any, error) {
			return p.FinalizePaperDecision(c, command, noClock, eval)
		}},
		{"outcome_retry", func(c context.Context, p *Postgres) (any, error) {
			return p.AssessPaperOutcome(c, outcomeCommand, noClock, eval, outcomeEval)
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			tracer := &cancelCollectorCommit{cancel: cancel}
			config := s.pool.Config()
			config.ConnConfig.Tracer = tracer
			pool, e := pgxpool.NewWithConfig(ctx, config)
			if e != nil {
				t.Fatal(e)
			}
			got, e := op.call(request, &Postgres{pool: pool})
			pool.Close()
			if !tracer.fired.Load() || e == nil || !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed COMMIT acknowledged owner catalogue/source/retry: %#v, %v; fired=%v", got, e, tracer.fired.Load())
			}
			actual, e := s.PaperDecisions(ctx, eval)
			if e != nil || !reflect.DeepEqual(actual, decisions) {
				t.Fatal("failed read changed decisions", e)
			}
			actualOutcomes, e := s.PaperOutcomes(ctx, eval, outcomeEval)
			if e != nil || !reflect.DeepEqual(actualOutcomes, outcomes) {
				t.Fatal("failed read changed outcomes", e)
			}
		})
	}
	reopened, err := OpenPostgres(ctx, s.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := reopened.PaperDecisions(ctx, eval)
	if err != nil || !reflect.DeepEqual(actual, decisions) {
		t.Fatal("reopen changed decisions", err)
	}
	actualOutcomes, err := reopened.PaperOutcomes(ctx, eval, outcomeEval)
	if err != nil || !reflect.DeepEqual(actualOutcomes, outcomes) {
		t.Fatal("reopen changed outcomes", err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("owner reads changed operations", err)
	}
	accountAfter, err := s.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accountAfter, accounts) {
		t.Fatal("owner reads changed capital", err)
	}
}
