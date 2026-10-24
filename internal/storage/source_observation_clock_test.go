package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type sourceClockBackend interface {
	assessmentBackend
	CollectorSubmissionStore
	FeedEvidenceStore
	PaperDecisionSourceStore
}

func TestMemorySourceObservationClock(t *testing.T) {
	sourceObservationClockContract(t, func(t *testing.T) sourceClockBackend { return NewMemory() })
}
func TestPostgresContractsSourceObservationClock(t *testing.T) {
	sourceObservationClockContract(t, func(t *testing.T) sourceClockBackend { return timerPostgres(t, "sourceclock") })
	sourceObservationClockMigrationContract(t, timerPostgres(t, "sourceclockmigration"))
}
func sourceObservationClockContract(t *testing.T, makeStore func(*testing.T) sourceClockBackend) {
	for _, phase := range []string{"decision", "outcome", "assessment"} {
		for _, operation := range []string{"collector_command", "input_admission", "raw_publication"} {
			t.Run(phase+"_"+operation, func(t *testing.T) {
				s := makeStore(t)
				ctx := context.Background()
				plan, at := declarationFixture(t, s, "prospective_market_unqualified")
				declaration, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(time.Second) })
				if err != nil {
					t.Fatal(err)
				}
				reg := feedcollector.Registration{ID: "source-clock-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
				for _, c := range []feedcollector.Command{{RequestID: "source-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "source-claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
					if _, err = s.FeedCollectorCommand(ctx, plan.ReceiverID, c, func() time.Time { return at.Add(2 * time.Second) }); err != nil {
						t.Fatal(err)
					}
				}
				command := PaperDecisionCommand{ID: "source-cut-decision", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
				de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
					return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "source_clock_fixture"}, nil
				}
				oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
					return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "source_clock_fixture"}, nil
				}
				cut := at.Add(10 * time.Second)
				decision, err := s.FinalizePaperDecision(ctx, command, func() time.Time { return cut }, de)
				if err != nil {
					t.Fatal(err)
				}
				if phase != "decision" {
					cut = at.Add(20 * time.Second)
					if _, err = s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "source-cut-outcome", ReceiverID: plan.ReceiverID, DecisionID: command.ID, DecisionSHA256: decision.Record.SHA256}, func() time.Time { return cut }, de, oe); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "assessment" {
					cut = at.Add(30 * time.Second)
					if _, err = s.AssessPaperEvaluation(ctx, PaperEvaluationAssessmentCommand{ID: "source-cut-assessment", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}, func() time.Time { return cut }, de, oe); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.FeedEvidence(ctx)
				if err != nil {
					t.Fatal(err)
				}
				sourceBefore, err := s.PaperDecisionSources(ctx, command.ID, de)
				if err != nil {
					t.Fatal(err)
				}
				fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
				input := collectorInput(at.Add(2*time.Second), "source-clock-input")
				renew := feedcollector.Command{RequestID: "source-renew", Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
				raw := json.RawMessage(`{"fixture":"source-clock-only"}`)
				call := func(when time.Time) (any, error) {
					switch operation {
					case "collector_command":
						return s.FeedCollectorCommand(ctx, plan.ReceiverID, renew, func() time.Time { return when })
					case "input_admission":
						return s.SubmitCollectorResearch(ctx, plan.ReceiverID, fence, input, func() time.Time { return when })
					default:
						return s.PublishFeedEvidence(ctx, plan.ReceiverID, reg.ID, "source-clock-raw", fence, raw, func() time.Time { return when }, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil })
					}
				}
				for _, when := range []time.Time{cut.Add(-time.Nanosecond), cut} {
					got, e := call(when)
					if !errors.Is(e, feedcollector.ErrClockBackwards) || !reflect.ValueOf(got).IsZero() {
						t.Errorf("new source entered retained %s cut: %#v, %v", phase, got, e)
					}
					after, e := s.FeedEvidence(ctx)
					if e != nil || !reflect.DeepEqual(before, after) {
						t.Error("rejected source consumed identity or changed owner", e)
					}
				}
				got, err := call(cut.Add(time.Nanosecond))
				if err != nil || reflect.ValueOf(got).IsZero() {
					t.Fatal("strictly later same identity rejected", got, err)
				}
				committed, err := s.FeedEvidence(ctx)
				if err != nil {
					t.Fatal(err)
				}
				// Only new writes use the new fence. A retained exact retry remains a read
				// of the original operation, even when its sampled clock is before the cut.
				if retry, e := call(cut.Add(-time.Nanosecond)); e != nil || reflect.ValueOf(retry).IsZero() {
					t.Fatal("historical retry changed", retry, e)
				}
				after, err := s.FeedEvidence(ctx)
				if err != nil || !reflect.DeepEqual(committed, after) {
					t.Fatal("retry changed owner", err)
				}
				sourceAfter, err := s.PaperDecisionSources(ctx, command.ID, de)
				if err != nil || !reflect.DeepEqual(sourceBefore, sourceAfter) {
					t.Fatal("later source changed earlier decision basis", sourceAfter, err)
				}
				if phase == "decision" && operation == "input_admission" {
					// The source writer and finalizer share the ownership lock. At equality,
					// either the input commits first and is captured, or finalization commits
					// first and the new input is rejected without consuming its identity.
					raceAt := at.Add(40 * time.Second)
					racedInput := collectorInput(at.Add(2*time.Second), "raced-source-clock")
					racedCommand := command
					racedCommand.ID = "source-race-decision"
					var wg sync.WaitGroup
					var sourceErr, decisionErr error
					wg.Add(2)
					go func() {
						defer wg.Done()
						_, sourceErr = s.SubmitCollectorResearch(ctx, plan.ReceiverID, fence, racedInput, func() time.Time { return raceAt })
					}()
					go func() {
						defer wg.Done()
						_, decisionErr = s.FinalizePaperDecision(ctx, racedCommand, func() time.Time { return raceAt }, de)
					}()
					wg.Wait()
					if decisionErr != nil || sourceErr != nil && !errors.Is(sourceErr, feedcollector.ErrClockBackwards) {
						t.Fatal("source/finalizer race", sourceErr, decisionErr)
					}
					basis, e := s.PaperDecisionSources(ctx, racedCommand.ID, de)
					if e != nil {
						t.Fatal(e)
					}
					_, captured := basis.Basis.Feed.Collectors.Admissions[racedInput.ID]
					if captured != (sourceErr == nil) {
						t.Fatal("source commit order not reflected in basis", captured, sourceErr)
					}
					if sourceErr != nil {
						if _, e = s.SubmitCollectorResearch(ctx, plan.ReceiverID, fence, racedInput, func() time.Time { return raceAt.Add(time.Nanosecond) }); e != nil {
							t.Fatal("raced identity could not retry", e)
						}
					}
				}
				if p, ok := s.(*Postgres); ok {
					reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
					if e != nil {
						t.Fatal(e)
					}
					defer reopened.Close()
					actual, e := reopened.PaperDecisionSources(ctx, command.ID, de)
					if e != nil || !reflect.DeepEqual(actual, sourceBefore) {
						t.Fatal("restart changed historical basis", actual, e)
					}
				}
			})
		}
	}
}

func sourceObservationClockMigrationContract(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_feed_collectors WHERE singleton").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version=56; COMMENT ON TABLE mantik_feed_collectors IS 'schema55 source clock fixture'; CREATE FUNCTION reject_source_clock_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=56 THEN RAISE EXCEPTION 'source clock migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_source_clock_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_source_clock_migration()`); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("migration failure ignored")
	}
	var max int
	var marker string
	if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&max); err != nil || max != 55 {
		t.Fatal("source fence migration partially committed", max, err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_feed_collectors'::regclass)").Scan(&marker); err != nil || marker != "schema55 source clock fixture" {
		t.Fatal("source fence comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_source_clock_migration ON mantik_schema; DROP FUNCTION reject_source_clock_migration()"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	var after []byte
	if err = p.pool.QueryRow(ctx, "SELECT payload FROM mantik_feed_collectors WHERE singleton").Scan(&after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("migration rewrote collector history", err)
	}
	var hash string
	if err = p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=56").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=56"); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenPostgres(ctx, p.pool.Config().ConnString()); err == nil {
		got.Close()
		t.Fatal("source fence checksum corruption accepted")
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=56", hash); err != nil {
		t.Fatal(err)
	}
}
