package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type paperFeedBackend interface {
	decisionTimeBackend
	PaperOutcomeStore
	FeedArchiveStore
	FeedArchiveSourceStore
	PaperFeedArchiveStore
}

func TestMemoryPaperFeedCheckpoints(t *testing.T) { paperFeedCheckpointContracts(t, NewMemory()) }
func TestPostgresContractsPaperFeedCheckpoints(t *testing.T) {
	paperFeedCheckpointContracts(t, timerPostgres(t, "paperfeedcustody"))
}

func assertRestoredPaperFeedCheckpoint(t *testing.T, s *Postgres, owner string) {
	t.Helper()
	ctx := t.Context()
	c, err := s.PaperFeedCheckpoint(ctx, 1, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || c.Validate() != nil || c.ReceiverID != owner || c.Decisions != 1 || c.Outcomes != 1 || c.Records[0].ID != "recovery-decision" || c.Records[1].ID != "recovery-outcome" {
		t.Fatal("restored paper source custody", c, err)
	}
	noClock := func() time.Time { t.Fatal("restored custody retry sampled time"); return time.Time{} }
	retry, err := s.RotateFeed(ctx, owner, noClock, func(FeedEvidenceSnapshot, time.Time) error { return nil }, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || !retry.Duplicate || retry.Header != c.Archive || retry.PaperCustody == nil || !retry.PaperCustody.Duplicate || retry.PaperCustody.Header != c.PaperFeedCheckpointHeader || retry.Retention == nil || !retry.Retention.Duplicate {
		t.Fatal("restored custody retry changed original identity", retry, err)
	}
	feed, err := s.FeedEvidence(ctx)
	if err != nil || feed.Retention == nil || feed.Retention.SHA256 != retry.Retention.SHA256 || feed.Collectors.ArchiveSHA256 != feed.Retention.SHA256 || len(feed.Collectors.Commands) != 1 {
		t.Fatal("restored retirement lost approved owner root", err)
	}
	decisions, err := s.PaperDecisions(ctx, recoveryFixtureDecisionEvaluator)
	if err != nil || len(decisions.Records) != 2 {
		t.Fatal("restored retired-feed decision owners", err)
	}
	var later PaperDecisionRecord
	for _, d := range decisions.Records {
		if d.Command.ID == "recovery-retired-feed-decision" {
			later = d
		}
	}
	if later.Schema != paperDecisionSchemaV4 {
		t.Fatal("restored archive-aware paper schema changed")
	}
	duplicate, err := s.FinalizePaperDecision(ctx, later.Command, noClock, recoveryFixtureDecisionEvaluator)
	if err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, later) {
		t.Fatal("restored archive-aware paper retry changed identity", err)
	}
	if audit, err := s.AuditPaperFeed(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator); err != nil || audit.Decisions != 2 || audit.Outcomes != 2 || audit.Checkpoints != 1 {
		t.Fatal("restored retirement original-source audit", audit, err)
	}
	audit, err := s.AuditFeedArchiveSources(ctx)
	if err != nil || audit.Archives != 1 || audit.Status != "verified" || audit.Sources != 6 {
		t.Fatal("restored archive indexes changed", audit, err)
	}
	t.Logf("paper custody restore preserved checkpoint=%s records=%s archive=%s", c.SHA256, c.RecordsSHA256, c.Archive.SHA256)
}

func paperFeedCheckpointContracts(t *testing.T, s paperFeedBackend) {
	t.Helper()
	ctx := t.Context()
	at := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	base := at
	now := func() time.Time { return at }
	noClock := func() time.Time { t.Fatal("historical retry read clock"); return time.Time{} }
	initial, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	def := strategyDefinition("custody")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, now)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "custody-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, now); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	if _, err := s.SubmitCollectorResearch(ctx, owner, fence, collectorInput(at, "custody-input"), now); err != nil {
		t.Fatal(err)
	}
	publish := func(key string) {
		t.Helper()
		if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, key, fence, json.RawMessage(`{"fixture":"custody"}`), now, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	publish("original")
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "custody_fixture"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "custody_fixture"}, nil
	}
	command := PaperDecisionCommand{ID: "first", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	at = base.Add(time.Second)
	decision, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	outCommand := PaperOutcomeCommand{ID: "outcome", ReceiverID: owner, DecisionID: command.ID, DecisionSHA256: decision.Record.SHA256}
	at = base.Add(2 * time.Second)
	outcome, err := s.AssessPaperOutcome(ctx, outCommand, now, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	at = base.Add(3 * time.Second)
	broken := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{}, ErrPaperDecisionInvalid
	}
	if _, err := s.ArchivePaperFeed(ctx, owner, now, check, broken, oe); err == nil {
		t.Fatal("failed paper replay committed source custody")
	}
	if _, err := s.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("source survived failed paper replay", err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 1, de, oe); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("checkpoint survived failed paper replay", err)
	}
	if _, err := s.ArchivePaperFeed(ctx, owner, now, check, nil, oe); !errors.Is(err, ErrFeedArchiveInvalid) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var receipt FeedArchiveReceipt
	sourceWrites, paperWrites := 0, 0
	for range 6 {
		wg.Go(func() {
			r, err := s.ArchivePaperFeed(ctx, owner, now, check, de, oe)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if !r.Duplicate {
				sourceWrites++
			}
			if r.PaperCustody == nil {
				t.Error("paper custody receipt omitted")
				return
			}
			if !r.PaperCustody.Duplicate {
				paperWrites++
			}
			receipt = r
		})
	}
	wg.Wait()
	if sourceWrites != 1 || paperWrites != 1 || receipt.PaperCustody == nil {
		t.Fatal("archive transaction duplicated", sourceWrites, paperWrites)
	}
	checkpoint, err := s.PaperFeedCheckpoint(ctx, 1, de, oe)
	if err != nil || checkpoint.Validate() != nil || checkpoint.Decisions != 1 || checkpoint.Outcomes != 1 || checkpoint.Archive != receipt.Header || !checkpoint.CutAt.Equal(at) || checkpoint.Records[0].RecordSHA256 != decision.Record.SHA256 || checkpoint.Records[1].RecordSHA256 != outcome.Record.SHA256 {
		t.Fatal("checkpoint omitted actual paper source identities", checkpoint, err)
	}
	after, err := s.FeedEvidence(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("checkpoint changed active feed", err)
	}
	original := clone(checkpoint)
	checkpoint.Records[0].RecordSHA256 = "changed"
	got, err := s.PaperFeedCheckpoint(ctx, 1, de, oe)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal("checkpoint read escaped ownership", err)
	}
	retry, err := s.ArchivePaperFeed(ctx, owner, noClock, check, de, oe)
	if err != nil || !retry.Duplicate || retry.PaperCustody == nil || !retry.PaperCustody.Duplicate || retry.PaperCustody.Header != original.PaperFeedCheckpointHeader {
		t.Fatal("checkpoint retry changed identity", retry, err)
	}
	if _, err := s.FinalizePaperDecision(ctx, command, noClock, de); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssessPaperOutcome(ctx, outCommand, noClock, de, oe); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{0, -time.Nanosecond} {
		at = original.CutAt.Add(offset)
		c := command
		c.ID = "forbidden"
		o := outCommand
		o.ID = "forbidden"
		if _, err := s.FinalizePaperDecision(ctx, c, now, de); !errors.Is(err, feedcollector.ErrClockBackwards) {
			t.Fatal("paper decision crossed sealed cut", offset, err)
		}
		if _, err := s.AssessPaperOutcome(ctx, o, now, de, oe); !errors.Is(err, feedcollector.ErrClockBackwards) {
			t.Fatal("paper outcome crossed sealed cut", offset, err)
		}
	}
	at = original.CutAt.Add(time.Nanosecond)
	later := command
	later.ID = "later"
	if _, err := s.FinalizePaperDecision(ctx, later, now, de); err != nil {
		t.Fatal("one-nanosecond later decision refused", err)
	}
	// New paper state can advance custody on the same immutable feed cut. The
	// checkpoint, not an invented new feed timestamp, records the closed cut.
	second, err := s.ArchivePaperFeed(ctx, owner, noClock, check, de, oe)
	if err != nil || !second.Duplicate || second.PaperCustody == nil || second.PaperCustody.Duplicate || second.PaperCustody.Header.Sequence != 2 || !second.PaperCustody.Header.CutAt.Equal(at) || second.PaperCustody.Header.PreviousSHA256 != original.SHA256 {
		t.Fatal("same-source paper custody did not advance", second, err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 1, de, oe); err != nil {
		t.Fatal("later paper record rewrote historical checkpoint", err)
	}
	at = original.CutAt
	if _, err := s.PublishFeedEvidence(ctx, owner, reg.ID, "clock-rejected", fence, json.RawMessage(`{"fixture":"rejected"}`), now, func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); !errors.Is(err, feedcollector.ErrClockBackwards) {
		t.Fatal("source crossed extended paper cut", err)
	}
	if pg, ok := s.(*Postgres); ok {
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		got, err := s.PaperFeedCheckpoint(ctx, 1, de, oe)
		if err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("restart changed checkpoint", err)
		}
		// A pre-103 writer can bypass Go checks, but cannot insert at the sealed
		// nanosecond through the database. The following nanosecond remains legal.
		tx, err := reopened.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := readPaperDecisions(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		old := stored[command.ID]
		old.Record.Command.ID = "old-writer"
		old.Record.FinalizedAt = second.PaperCustody.Header.CutAt
		old.Record.SHA256 = ""
		old.Record.SHA256 = workerplan.Hash(old.Record)
		payload, _ := json.Marshal(old)
		if _, err := reopened.pool.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", old.Record.Command.ID, payload); err == nil || !strings.Contains(err.Error(), "paper source checkpoint clock moved backwards") {
			t.Fatal("older paper writer crossed checkpoint", err)
		}
		old.Record.FinalizedAt = old.Record.FinalizedAt.Add(time.Nanosecond)
		old.Record.SHA256 = ""
		old.Record.SHA256 = workerplan.Hash(old.Record)
		payload, _ = json.Marshal(old)
		if _, err := reopened.pool.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", old.Record.Command.ID, payload); err != nil {
			t.Fatal("SQL guard rounded away allowed nanosecond", err)
		}
		at = base.Add(4 * time.Second)
		publish("new-source")
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_paper_feed_checkpoints ADD CONSTRAINT reject_checkpoint CHECK(sequence<>3)"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ArchivePaperFeed(ctx, owner, now, check, de, oe); err == nil {
			t.Fatal("checkpoint failure acknowledged source archive")
		}
		if _, err := s.FeedArchive(ctx, 2); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("checkpoint failure left source archive", err)
		}
		if _, err := s.FeedArchiveSource(ctx, "publication", reg.ID+":new-source"); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("checkpoint failure left source index", err)
		}
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_paper_feed_checkpoints DROP CONSTRAINT reject_checkpoint"); err != nil {
			t.Fatal(err)
		}
		// A deferred failure must roll back the source, indexes and checkpoint
		// even after all their INSERT statements succeeded.
		for _, sql := range []string{
			"CREATE FUNCTION reject_paper_checkpoint_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture checkpoint COMMIT failure'; END; $$",
			"CREATE CONSTRAINT TRIGGER reject_checkpoint_commit AFTER INSERT ON mantik_paper_feed_checkpoints DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_paper_checkpoint_commit()",
		} {
			if _, err := reopened.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.ArchivePaperFeed(ctx, owner, now, check, de, oe); err == nil || !strings.Contains(err.Error(), "fixture checkpoint COMMIT failure") {
			t.Fatal("deferred failure acknowledged archive", err)
		}
		if _, err := s.FeedArchive(ctx, 2); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("failed COMMIT retained source", err)
		}
		if _, err := s.PaperFeedCheckpoint(ctx, 3, de, oe); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("failed COMMIT retained checkpoint", err)
		}
		if _, err := s.FeedArchiveSource(ctx, "publication", reg.ID+":new-source"); !errors.Is(err, ErrFeedArchiveNotFound) {
			t.Fatal("failed COMMIT retained index", err)
		}
		if _, err := reopened.pool.Exec(ctx, "DROP TRIGGER reject_checkpoint_commit ON mantik_paper_feed_checkpoints; DROP FUNCTION reject_paper_checkpoint_commit()"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ArchivePaperFeed(ctx, owner, now, check, de, oe); err != nil {
			t.Fatal("checkpoint rollback consumed identity", err)
		}
	}
	// Rehashed incomplete pin sets cannot replace a closed owner's actual records.
	current, err := s.PaperFeedCheckpoint(ctx, 2, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	forged := clone(current)
	forged.Records = forged.Records[1:]
	forged.Decisions--
	forged.RecordsSHA256 = workerplan.Hash(forged.Records)
	forged.SourceSHA256 = paperFeedSourceIdentity(forged.Archive.SHA256, forged.RecordsSHA256, forged.CutAt, forged.PaperArchiveSequence)
	forged.SHA256 = ""
	forged.SHA256 = workerplan.Hash(forged.PaperFeedCheckpointHeader)
	if forged.Validate() != nil {
		t.Fatal("forged test envelope is malformed")
	}
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.paperFeedCheckpoints[1] = forged
		m.mu.Unlock()
	} else {
		raw, _ := json.Marshal(forged)
		header, _ := json.Marshal(forged.PaperFeedCheckpointHeader)
		if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_feed_checkpoints SET payload=$1,header=$2,sha256=$3,source_sha256=$4 WHERE sequence=2", raw, header, forged.SHA256, forged.SourceSHA256); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 2, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("self-rehashed omission replaced actual owner cut", err)
	}
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.paperFeedCheckpoints[1] = current
		m.feedArchives[0].Source.ReceiverID = "receiver-corrupt"
		m.mu.Unlock()
	} else {
		raw, _ := json.Marshal(current)
		header, _ := json.Marshal(current.PaperFeedCheckpointHeader)
		if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_paper_feed_checkpoints SET payload=$1,header=$2,sha256=$3,source_sha256=$4 WHERE sequence=2", raw, header, current.SHA256, current.SourceSHA256); err != nil {
			t.Fatal(err)
		}
		if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_feed_archives SET payload=convert_to('{}','UTF8') WHERE sequence=1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PaperFeedCheckpoint(ctx, 1, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("archive corruption did not fail custody inspection", err)
	}
	// Current paper reads keep using active owners; historical body corruption is
	// visible in custody inspection without becoming a new operational dependency.
	if _, err := s.PaperDecisions(ctx, de); err != nil {
		t.Fatal("active decisions depended on archive body", err)
	}
	if _, err := s.PaperOutcomes(ctx, de, oe); err != nil {
		t.Fatal("active outcomes depended on archive body", err)
	}
}
