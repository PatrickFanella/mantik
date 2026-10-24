package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type decisionTimeBackend interface {
	decisionBackend
	CollectorSubmissionStore
	FeedEvidenceStore
}

func TestMemoryPaperDecisionSourceTimes(t *testing.T) {
	paperDecisionSourceTimeContract(t, NewMemory())
}
func TestPostgresContractsPaperDecisionSourceTimes(t *testing.T) {
	paperDecisionSourceTimeContract(t, timerPostgres(t, "decisiontimes"))
}
func paperDecisionSourceTimeContract(t *testing.T, s decisionTimeBackend) {
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	initial, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	def := strategyDefinition("time-strategy")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "time-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "time-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "time-claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, owner, c, func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
	}
	command := PaperDecisionCommand{ID: "time-decision", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	eval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "custody_fixture_only"}, nil
	}
	finalized, err := s.FinalizePaperDecision(ctx, command, func() time.Time { return at.Add(time.Second) }, eval)
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.PaperDecisionSources(ctx, command.ID, eval)
	if err != nil {
		t.Fatal(err)
	}
	// Append genuine owner records after finalization. These later inputs must
	// remain available without entering the earlier decision's retained basis.
	later := at.Add(2 * time.Second)
	input := collectorInput(later, "time-later-input")
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	if _, err = s.SubmitCollectorResearch(ctx, owner, fence, input, func() time.Time { return later }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishFeedEvidence(ctx, owner, reg.ID, "time-later-raw", fence, json.RawMessage(`{"fixture":"later-publication"}`), func() time.Time { return later }, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	futureReg := reg
	futureReg.ID = "time-later-registration"
	futureReg.Symbol = "QQQ"
	futureReg.JournalID = "later-journal"
	if _, err = s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "later-register", Operation: "register", RegistrationID: futureReg.ID, Registration: &futureReg}, func() time.Time { return later }); err != nil {
		t.Fatal(err)
	}
	current, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := s.PaperStrategies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	currentBasis, err := decisionBasis(command, strategies, current)
	if err != nil {
		t.Fatal(err)
	}
	// Actual owner records at the exact decision clock are available. The
	// guard rejects only later custody; it does not invent a one-tick delay.
	if _, err = newPaperDecision(command, currentBasis, later, eval); err != nil {
		t.Fatal("equal custody boundary rejected", err)
	}
	stable, err := s.PaperDecisionSources(ctx, command.ID, eval)
	if err != nil || !reflect.DeepEqual(stable, original) {
		t.Fatal("later owner writes changed historical basis", stable, err)
	}
	set := func(v retainedPaperDecision) {
		switch b := s.(type) {
		case *Memory:
			b.paperDecisions[command.ID] = clone(v)
		case *Postgres:
			raw, _ := json.Marshal(v)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_paper_decisions SET payload=$1 WHERE id=$2", raw, command.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	saved := retainedPaperDecision{original.Decision, original.Basis}
	changes := map[string]func(*retainedPaperDecision){
		"future_collector_clock": func(v *retainedPaperDecision) { v.Basis.Feed.Collectors.LastClock = current.Collectors.LastClock },
		"future_admission": func(v *retainedPaperDecision) {
			v.Basis.Feed.Collectors.Admissions[input.ID] = clone(current.Collectors.Admissions[input.ID])
			v.Basis.Feed.Routing.Inputs[input.ID] = clone(current.Routing.Inputs[input.ID])
		},
		"future_publication": func(v *retainedPaperDecision) {
			for k, r := range current.Records {
				v.Basis.Feed.Records[k] = clone(r)
			}
		},
		"future_registration": func(v *retainedPaperDecision) {
			v.Record.Command.RegistrationID = futureReg.ID
			v.Basis.Feed.Collectors.Entries = map[string]feedcollector.Entry{futureReg.ID: clone(current.Collectors.Entries[futureReg.ID])}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			forged := clone(saved)
			change(&forged)
			forged.Record.BasisSHA256 = workerplan.Hash(forged.Basis)
			forged.Record.SHA256 = ""
			forged.Record.SHA256 = workerplan.Hash(forged.Record)
			// Even an evaluator that ignores all input timestamps must not receive a
			// temporally impossible basis. Custody belongs to the storage owner.
			called := false
			_, e := newPaperDecision(forged.Record.Command, forged.Basis, forged.Record.FinalizedAt, func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
				called = true
				return eval(c, b, at)
			})
			if e == nil || called {
				t.Error("future custody reached finalization evaluator", e, called)
			}
			set(forged)
			all, e := s.PaperDecisions(ctx, eval)
			if e == nil || len(all.Records) != 0 {
				t.Error("rehashed future basis accepted by catalogue", e)
			}
			source, e := s.PaperDecisionSources(ctx, command.ID, eval)
			if e == nil || !reflect.DeepEqual(source, PaperDecisionSources{}) {
				t.Error("rehashed future basis accepted by source read", e)
			}
			retry, e := s.FinalizePaperDecision(ctx, command, func() time.Time { t.Error("corrupt retry read clock"); return later }, eval)
			if e == nil || !reflect.DeepEqual(retry, PaperDecisionReceipt{}) {
				t.Error("rehashed future basis accepted by exact retry", e)
			}
			// Outcome custody uses the same guard without manufacturing a decision.
			strategies, e := s.PaperStrategies(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if e = verifyPaperDecisionBasis(forged.Record.Command, forged.Basis, forged.Record.FinalizedAt, strategies, current); e == nil {
				t.Error("future custody accepted by shared outcome guard")
			}
			set(saved)
		})
	}
	retry, err := s.FinalizePaperDecision(ctx, command, func() time.Time { t.Fatal("valid retry read clock"); return later }, eval)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, finalized.Record) {
		t.Fatal("legitimate historical retry changed", retry, err)
	}
	after, err := s.FeedEvidence(ctx)
	if err != nil || !reflect.DeepEqual(current, after) {
		t.Fatal("custody inspection changed actual source owner", err)
	}
	if p, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		read, e := reopened.PaperDecisionSources(ctx, command.ID, eval)
		if e != nil || !reflect.DeepEqual(read, original) {
			t.Fatal("reopen changed actual historical basis", read, e)
		}
	}
}
