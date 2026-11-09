package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type paperArchiveBackend interface {
	assessmentBackend
	DecisionReviewStore
	PaperRecordArchiveStore
	PaperRecordArchiveAuditStore
	PaperFeedArchiveStore
}

func TestMemoryPaperRecordArchives(t *testing.T) {
	paperRecordArchiveContract(t, NewMemory())
}

func TestPostgresContractsPaperRecordArchives(t *testing.T) {
	paperRecordArchiveContract(t, timerPostgres(t, "paperrecordarchive"))
}

func assertRestoredPaperRecordArchive(t *testing.T, s *Postgres, owner string) {
	t.Helper()
	ctx := t.Context()
	a, err := s.PaperRecordArchive(ctx, 1, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || a.Validate() != nil || a.ReceiverID != owner || a.Decisions != 2 || a.Outcomes != 2 || a.Assessments != 1 || len(a.Records) != 5 {
		t.Fatal("restored paper archive lost actual owners", err)
	}
	original, err := s.PaperArchivedRecord(ctx, "decision", "recovery-decision", recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || original.Archive != a.PaperRecordArchiveHeader || original.Record.CreatedEpoch != 0 {
		t.Fatal("restored original paper record lookup", err)
	}
	var retained retainedPaperDecision
	if json.Unmarshal(original.Record.Payload, &retained) != nil || retained.Record.Validate() != nil || retained.Record.Command.ID != "recovery-decision" || retained.Record.SHA256 != original.Record.RecordSHA256 {
		t.Fatal("restored paper archive changed original envelope")
	}
	retry, err := s.ArchivePaperRecords(ctx, owner, func() time.Time { t.Fatal("restored paper archive retry sampled clock"); return time.Time{} }, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || !retry.Duplicate || retry.Header != a.PaperRecordArchiveHeader {
		t.Fatal("restored paper archive retry changed identity", err)
	}
	audit, err := s.AuditPaperRecordArchives(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || audit.Status != "verified" || audit.Archives != 1 || audit.Records != 5 || audit.AdmissionAvailable || audit.ExecutionAvailable || audit.QualificationCredit {
		t.Fatal("restored paper archive original-source audit", audit, err)
	}
	t.Logf("paper archive restore preserved archive=%s records=%s originals=%s", a.SHA256, a.RecordsSHA256, audit.OriginalsSHA256)
}

func paperRecordArchiveContract(t *testing.T, s paperArchiveBackend) {
	t.Helper()
	ctx := t.Context()
	plan, base := declarationFixture(t, s, "prospective_market_unqualified")
	declaration, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return base.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "archive-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "paper_archive_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "paper_archive_fixture_only"}, nil
	}
	at = at.Add(time.Nanosecond)
	command := PaperDecisionCommand{ID: "original", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	decision, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "outcome", ReceiverID: plan.ReceiverID, DecisionID: command.ID, DecisionSHA256: decision.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperEvaluation(ctx, PaperEvaluationAssessmentCommand{ID: "assessment", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ReviewPaperDecision(ctx, DecisionReviewCommand{ID: "review", ReceiverID: plan.ReceiverID, DecisionID: command.ID, DecisionSHA256: decision.Record.SHA256, Claims: []DecisionReviewClaim{{Field: "action", Value: "abstain"}}}, now, de); err != nil {
		t.Fatal(err)
	}
	before, err := s.PaperDecisions(ctx, de)
	if err != nil {
		t.Fatal(err)
	}
	initialSource, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
	if err != nil || initialSource.PaperCustody == nil || initialSource.PaperCustody.Header.PaperArchiveSequence != 0 {
		t.Fatal("pre-archive epoch-zero source custody", initialSource, err)
	}
	at = at.Add(time.Second)
	if pg, ok := s.(*Postgres); ok {
		dropPaperRecordArchiveSchema(t, pg)
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=105"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated schema-104 upgrade", err)
		}
		if got, err := s.PaperDecisions(ctx, de); err != nil || !reflect.DeepEqual(got, before) {
			t.Fatal("creation epoch backfill rewrote paper envelopes", err)
		}
	}
	at = at.Add(time.Nanosecond)
	canceled, cancel := context.WithCancel(ctx)
	cancelEval := func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) (PaperDecisionEvaluation, error) {
		cancel()
		return de(c, b, at)
	}
	if r, err := s.ArchivePaperRecords(canceled, plan.ReceiverID, now, cancelEval, oe); err == nil || r != (PaperRecordArchiveReceipt{}) {
		t.Fatal("canceled paper archive acknowledged", r, err)
	}
	cancel()
	if _, err := s.PaperRecordArchive(ctx, 1, de, oe); !errors.Is(err, ErrPaperRecordArchiveNotFound) {
		t.Fatal("canceled archive left ownership", err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err := pg.pool.Exec(ctx, `CREATE FUNCTION reject_paper_archive_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'paper archive COMMIT fixture'; END $$; CREATE CONSTRAINT TRIGGER reject_paper_archive_commit AFTER INSERT ON mantik_paper_record_archives DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_paper_archive_commit()`); err != nil {
			t.Fatal(err)
		}
		if r, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err == nil || r != (PaperRecordArchiveReceipt{}) {
			t.Fatal("failed COMMIT acknowledged paper archive", r, err)
		}
		var count int
		if err := pg.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_paper_archive_members").Scan(&count); err != nil || count != 0 {
			t.Fatal("failed COMMIT left archive members", err)
		}
		if _, err := pg.pool.Exec(ctx, "DROP TRIGGER reject_paper_archive_commit ON mantik_paper_record_archives; DROP FUNCTION reject_paper_archive_commit()"); err != nil {
			t.Fatal(err)
		}
	}
	var clocks, created atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, func() time.Time { clocks.Add(1); return at }, de, oe)
			if err != nil {
				t.Error(err)
				return
			}
			if !r.Duplicate {
				created.Add(1)
			}
			if r.Header.Sequence != 1 || r.Header.Decisions != 1 || r.Header.Outcomes != 1 || r.Header.Assessments != 1 || r.Header.Reviews != 1 || r.Header.Validate() != nil {
				t.Error("wrong paper archive owners", r)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if clocks.Load() != 1 || created.Load() != 1 {
		t.Fatal("concurrent archive created multiple source cuts", clocks.Load(), created.Load())
	}
	first, err := s.PaperRecordArchive(ctx, 1, de, oe)
	if err != nil || first.Validate() != nil || len(first.Records) != 4 {
		t.Fatal("original paper archive readback", err)
	}
	for _, r := range first.Records {
		if r.CreatedEpoch != 0 {
			t.Fatal("predecessor gained a later creation epoch", r.Kind, r.CreatedEpoch)
		}
	}
	noClock := func() time.Time { t.Fatal("same paper archive sampled another clock"); return time.Time{} }
	advanced, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, noClock, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
	if err != nil || !advanced.Duplicate || advanced.Header != initialSource.Header || advanced.PaperCustody == nil || advanced.PaperCustody.Header.PaperArchiveSequence != 1 || !advanced.PaperCustody.Header.CutAt.Equal(first.ArchivedAt) {
		t.Fatal("unchanged feed lost the paper epoch clock floor", advanced, err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, advanced.PaperCustody.Header.Sequence, de, oe); err != nil {
		t.Fatal("source-free epoch gap did not replay", err)
	}
	if r, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, noClock, de, oe); err != nil || !r.Duplicate || r.Header != first.PaperRecordArchiveHeader {
		t.Fatal("archive retry changed identity", r, err)
	}
	if got, err := s.PaperDecisions(ctx, de); err != nil || !reflect.DeepEqual(got, before) {
		t.Fatal("custody altered active paper owners", err)
	}
	original, err := s.PaperArchivedRecord(ctx, "decision", command.ID, de, oe)
	if err != nil || original.Archive.Sequence != 1 {
		t.Fatal("original paper ID lookup", err)
	}
	var retained retainedPaperDecision
	if json.Unmarshal(original.Record.Payload, &retained) != nil || !reflect.DeepEqual(retained.Record, decision.Record) {
		t.Fatal("archive changed original decision envelope")
	}
	at = at.Add(time.Nanosecond)
	command.ID = "later"
	if pg, ok := s.(*Postgres); ok {
		// An already-running predecessor writes its original retained envelope.
		// The database reserves its identity and actual creation epoch too.
		later := retained
		later.Record, err = paperDecisionForSchema(retained.Record.Schema, command, retained.Basis, at, de)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(later)
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", command.ID, raw); err != nil {
			t.Fatal("older running writer lost creation metadata", err)
		}
	} else if _, err := s.FinalizePaperDecision(ctx, command, now, de); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	second, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe)
	if err != nil || second.Duplicate || second.Header.Sequence != 2 || second.Header.PreviousSHA256 != first.SHA256 {
		t.Fatal("successive paper source cut", second, err)
	}
	readback, err := s.PaperRecordArchive(ctx, 2, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range readback.Records {
		if r.Kind == "decision" && r.ID == "later" {
			found = true
			if r.CreatedEpoch != 1 {
				t.Fatal("new owner lost creation epoch", r.CreatedEpoch)
			}
		}
	}
	if !found {
		t.Fatal("new paper owner omitted")
	}
	if got, err := s.PaperRecordArchive(ctx, 1, de, oe); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("later records changed historical snapshot", err)
	}
	if got, err := s.PaperArchivedRecord(ctx, "decision", "original", de, oe); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal("original lookup moved to a later copy", err)
	}
	page, err := s.PaperRecordArchives(ctx, 0, 1)
	if err != nil || len(page.Records) != 1 || page.NextAfter != 1 {
		t.Fatal("paper archive page", page, err)
	}
	page, err = s.PaperRecordArchives(ctx, page.NextAfter, 1)
	if err != nil || len(page.Records) != 1 || page.Records[0] != second.Header || page.NextAfter != 0 {
		t.Fatal("paper archive next page", page, err)
	}
	if audit, err := s.AuditPaperRecordArchives(ctx, de, oe); err != nil || audit.Status != "verified" || audit.Archives != 2 || audit.Records != 5 || audit.AdmissionAvailable || audit.ExecutionAvailable || audit.QualificationCredit {
		t.Fatal("paper archive original-source audit", audit, err)
	}
	at = at.Add(time.Nanosecond)
	command.ID = "unarchived"
	unarchived, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PaperArchivedRecord(ctx, "decision", command.ID, de, oe); !errors.Is(err, ErrPaperRecordArchiveNotFound) {
		t.Fatal("new current-epoch record was falsely archived", err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_paper_record_epochs WHERE kind='decision' AND record_id='unarchived'"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=105"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err == nil {
			t.Fatal("DDL replay guessed an old epoch for a lost identity")
		}
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos) VALUES('decision','unarchived',2,$1,$2,$3)", unarchived.Record.SHA256, unarchived.Record.FinalizedAt.Unix(), unarchived.Record.FinalizedAt.Nanosecond()); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("repaired original creation metadata did not replay", err)
		}
	}
	at = at.Add(time.Nanosecond)
	feedArchive, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
	if err != nil || feedArchive.PaperCustody == nil || feedArchive.PaperCustody.Header.Schema != "mantik-paper-feed-checkpoint-v2" || feedArchive.PaperCustody.Header.PaperArchiveSequence != 2 {
		t.Fatal("paper source custody lost its actual logical epoch", feedArchive, err)
	}
	checkpoint, err := s.PaperFeedCheckpoint(ctx, feedArchive.PaperCustody.Header.Sequence, de, oe)
	if err != nil || checkpoint.Validate() != nil {
		t.Fatal("epoch-bound paper source replay", err)
	}
	if pg, ok := s.(*Postgres); ok {
		old := clone(checkpoint)
		old.Schema, old.PaperArchiveSequence = "mantik-paper-feed-checkpoint-v1", 0
		old.Sequence, old.PreviousSHA256 = checkpoint.Sequence+1, checkpoint.SHA256
		old.SourceSHA256 = paperFeedSourceIdentity(old.Archive.SHA256, old.RecordsSHA256, old.CutAt, 0)
		old.SHA256 = ""
		old.SHA256 = workerplan.Hash(old.PaperFeedCheckpointHeader)
		if old.Validate() != nil {
			t.Fatal("fixture did not create a valid predecessor envelope")
		}
		header, _ := json.Marshal(old.PaperFeedCheckpointHeader)
		raw, _ := json.Marshal(old)
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_feed_checkpoints(sequence,archive_sequence,cut_seconds,cut_nanos,source_sha256,sha256,header,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", old.Sequence, old.Archive.Sequence, old.CutAt.Unix(), old.CutAt.Nanosecond(), old.SourceSHA256, old.SHA256, header, raw); err == nil {
			t.Fatal("older custody writer discarded the actual paper archive epoch")
		}
	}
	first.Context.Strategies.Records = nil
	if got, err := s.PaperRecordArchive(ctx, 1, de, oe); err != nil || got.Context.Strategies.Records == nil {
		t.Fatal("archive readback was not detached", err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=105"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated paper archive DDL replay", err)
		}
		if got, err := s.PaperRecordArchive(ctx, 2, de, oe); err != nil || !reflect.DeepEqual(got, readback) {
			t.Fatal("DDL replay replaced an original archive or creation epoch", err)
		}
		// Even a predecessor which removes an active row cannot reuse its ID.
		tx, err := pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM mantik_paper_decisions WHERE id='original'"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES('original',$1)", original.Record.Payload); err == nil {
			t.Fatal("older writer reused reserved paper identity")
		}
		_ = tx.Rollback(ctx)
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		if got, err := s.PaperArchivedRecord(ctx, "decision", "original", de, oe); err != nil || !reflect.DeepEqual(got, original) {
			t.Fatal("restart lost original paper custody", err)
		}
		if _, err := reopened.pool.Exec(ctx, "DELETE FROM mantik_paper_record_epochs WHERE kind='decision' AND record_id='original'"); err == nil {
			t.Fatal("original identity reservation was mutable")
		}
	}
	// Rehashing an omitted owner cannot replace the independently retained
	// membership rows that closed the original archive's complete owner set.
	first, err = s.PaperRecordArchive(ctx, 1, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	forged := clone(first)
	forged.Records = forged.Records[:len(forged.Records)-1]
	forged.Reviews--
	forged.RecordsSHA256 = workerplan.Hash(paperArchivePins(forged.Records))
	forged.SourceSHA256 = workerplan.Hash(struct {
		Context PaperArchiveContext
		Records []ArchivedPaperRecord
	}{forged.Context, forged.Records})
	forged.SHA256 = ""
	forged.SHA256 = workerplan.Hash(forged.PaperRecordArchiveHeader)
	if forged.Validate() != nil {
		t.Fatal("fixture did not produce a self-consistent false envelope")
	}
	if m, ok := s.(*Memory); ok {
		m.paperRecordArchives[0] = forged
		m.paperArchiveSeals[0] = forged.PaperRecordArchiveHeader
		m.paperArchiveMembers[0] = paperArchivePins(forged.Records)
	} else {
		pg := s.(*Postgres)
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_record_archives SET payload=payload WHERE sequence=1"); err == nil {
			t.Fatal("archive payload was mutable")
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(forged)
		header, _ := json.Marshal(forged.PaperRecordArchiveHeader)
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_record_archives SET sha256=$1,records_sha256=$2,header=$3,payload=$4 WHERE sequence=1", forged.SHA256, forged.RecordsSHA256, header, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives ENABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
			t.Fatal(err)
		}
		for _, sql := range []string{
			"ALTER TABLE mantik_paper_archive_batches DISABLE TRIGGER mantik_immutable_paper_batches",
			"ALTER TABLE mantik_paper_archive_members DISABLE TRIGGER mantik_immutable_paper_members",
		} {
			if _, err := pg.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_archive_batches SET archive_sha256=$1,source_sha256=$2,records_sha256=$3,record_count=$4 WHERE archive_sequence=1", forged.SHA256, forged.SourceSHA256, forged.RecordsSHA256, len(forged.Records)); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_paper_archive_members WHERE archive_sequence=1 AND kind='review'"); err != nil {
			t.Fatal(err)
		}
		for _, sql := range []string{
			"ALTER TABLE mantik_paper_archive_batches ENABLE TRIGGER mantik_immutable_paper_batches",
			"ALTER TABLE mantik_paper_archive_members ENABLE TRIGGER mantik_immutable_paper_members",
		} {
			if _, err := pg.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, err := s.PaperRecordArchive(ctx, 1, de, oe); err == nil || !reflect.DeepEqual(got, PaperRecordArchive{}) {
		t.Fatal("self-rehashed paper archive omission returned partial success", err)
	}
	if audit, err := s.AuditPaperRecordArchives(ctx, de, oe); err == nil || audit != (PaperRecordArchiveAudit{}) {
		t.Fatal("paper archive audit accepted self-rehashed omission", err)
	}
	if _, err := s.PaperArchivedRecord(ctx, "review", "review", de, oe); err == nil || errors.Is(err, ErrPaperRecordArchiveNotFound) {
		t.Fatal("lost first archive silently selected a later copy", err)
	}
}
