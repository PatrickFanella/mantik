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

func TestMemoryPaperOutcomeMembership(t *testing.T) {
	paperOutcomeMembershipContract(t, NewMemory())
}
func TestPostgresContractsPaperOutcomeMembership(t *testing.T) {
	paperOutcomeMembershipContract(t, timerPostgres(t, "outcomemembership"))
}

type outcomeMembershipBackend interface {
	decisionTimeBackend
	PaperOutcomeStore
	PaperOutcomeSourceStore
}

func paperOutcomeMembershipContract(t *testing.T, s outcomeMembershipBackend) {
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
	original, err := s.PaperOutcomeSources(ctx, outcomeCommand.ID, eval, outcomeEval)
	if err != nil {
		t.Fatal(err)
	}
	saved := retainedPaperOutcome{original.Outcome, original.Basis}
	set := func(v retainedPaperOutcome) {
		t.Helper()
		if m, ok := s.(*Memory); ok {
			m.mu.Lock()
			m.paperOutcomes[outcomeCommand.ID] = clone(v)
			m.mu.Unlock()
		} else {
			raw, e := json.Marshal(v)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_outcomes SET payload=$1 WHERE id=$2", raw, outcomeCommand.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	if saved.Record.Schema != "mantik-owned-paper-outcome-v2" {
		t.Fatal("new outcome omitted versioned membership contract", saved.Record.Schema)
	}
	// Legacy envelopes preserve their original replay contract, bytes and hash.
	legacy := clone(saved)
	legacy.Record, err = paperOutcomeForSchema("mantik-owned-paper-outcome-v1", outcomeCommand, legacy.Basis, saved.Record.EvaluatedAt, outcomeEval)
	if err != nil {
		t.Fatal(err)
	}
	set(legacy)
	legacyBytes, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.AssessPaperOutcome(ctx, outcomeCommand, func() time.Time { panic("legacy retry clock") }, eval, outcomeEval)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, legacy.Record) {
		t.Fatal("legacy outcome changed", retry, err)
	}
	legacySources, err := s.PaperOutcomeSources(ctx, outcomeCommand.ID, eval, outcomeEval)
	if err != nil {
		t.Fatal(err)
	}
	readBytes, err := json.Marshal(retainedPaperOutcome{legacySources.Outcome, legacySources.Basis})
	if err != nil || string(readBytes) != string(legacyBytes) {
		t.Fatal("legacy outcome bytes changed", err)
	}
	set(saved)
	for _, kind := range []string{"admission", "publication"} {
		t.Run(kind+"_omission", func(t *testing.T) {
			forged := clone(saved)
			if kind == "admission" {
				delete(forged.Basis.Observation.Feed.Collectors.Admissions, input.ID)
				delete(forged.Basis.Observation.Feed.Routing.Inputs, input.ID)
			} else {
				for key := range forged.Basis.Observation.Feed.Records {
					delete(forged.Basis.Observation.Feed.Records, key)
				}
			}
			forged.Record.BasisSHA256 = workerplan.Hash(forged.Basis)
			forged.Record.SHA256 = ""
			forged.Record.SHA256 = workerplan.Hash(forged.Record)
			set(forged)
			defer set(saved)
			if _, e := s.PaperOutcomeSources(ctx, outcomeCommand.ID, eval, outcomeEval); e == nil {
				t.Error("rehashed omission acknowledged as owned outcome sources")
			}
			if _, e := s.PaperOutcomes(ctx, eval, outcomeEval); e == nil {
				t.Error("rehashed omission acknowledged in outcome catalogue")
			}
			if _, e := s.AssessPaperOutcome(ctx, outcomeCommand, func() time.Time { panic("retry clock") }, eval, outcomeEval); e == nil {
				t.Error("exact retry hid source omission")
			}
		})
	}
	// A later legitimate source write must not enlarge the historical cut.
	at = at.Add(time.Second)
	later := collectorInput(at, "membership-later")
	if _, err = s.SubmitCollectorResearch(ctx, owner, fence, later, clock); err != nil {
		t.Fatal(err)
	}
	publish("membership-later-raw")
	actual, err := s.PaperOutcomeSources(ctx, outcomeCommand.ID, eval, outcomeEval)
	if err != nil || !reflect.DeepEqual(actual, original) {
		t.Fatal("later source changed retained outcome", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		actual, e := reopened.PaperOutcomeSources(ctx, outcomeCommand.ID, eval, outcomeEval)
		if e != nil || !reflect.DeepEqual(actual, original) {
			t.Fatal("reopen changed retained outcome", e)
		}
		outcomeMembershipMigrationContract(t, pg, outcomeCommand.ID)
	}
}

func outcomeMembershipMigrationContract(t *testing.T, p *Postgres, id string) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_outcomes WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=58; COMMENT ON TABLE mantik_paper_outcomes IS 'schema57 decision membership fixture'; CREATE FUNCTION reject_outcome_membership_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=58 THEN RAISE EXCEPTION 'decision membership migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_outcome_membership_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_outcome_membership_migration()`); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("migration failure ignored")
	}
	var max int
	var marker string
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&max); err != nil || max != 57 {
		t.Fatal("membership fence migration partially committed", max, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_paper_outcomes'::regclass)").Scan(&marker); err != nil || marker != "schema57 decision membership fixture" {
		t.Fatal("membership fence comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_outcome_membership_migration ON mantik_schema; DROP FUNCTION reject_outcome_membership_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	var after []byte
	if err = p.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_outcomes WHERE id=$1", id).Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote outcome payload", err)
	}
	var hash string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=58").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=58"); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("membership fence checksum corruption accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=58", hash); err != nil {
		t.Fatal(err)
	}
}
