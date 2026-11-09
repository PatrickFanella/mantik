package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

func TestMemoryPaperOwnerViews(t *testing.T) {
	paperOwnerViewContract(t, NewMemory())
}

func TestPostgresContractsPaperOwnerViews(t *testing.T) {
	paperOwnerViewContract(t, timerPostgres(t, "paperownerview"))
}

func paperOwnerViewContract(t *testing.T, s interface {
	paperArchiveBackend
	PaperFeedAuditStore
}) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "owner-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "owner_view_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "owner_view_fixture_only"}, nil
	}
	command := PaperDecisionCommand{ID: "original", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}
	at = at.Add(time.Nanosecond)
	d, err := s.FinalizePaperDecision(ctx, command, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "outcome", ReceiverID: plan.ReceiverID, DecisionID: command.ID, DecisionSHA256: d.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	archiveFeed := func() PaperFeedCheckpoint {
		t.Helper()
		at = at.Add(time.Nanosecond)
		r, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe)
		if err != nil || r.PaperCustody == nil {
			t.Fatal("source custody", r, err)
		}
		c, err := s.PaperFeedCheckpoint(ctx, r.PaperCustody.Header.Sequence, de, oe)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	legacy := archiveFeed()
	at = at.Add(time.Nanosecond)
	first, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe)
	if err != nil || first.Header.Sequence != 1 {
		t.Fatal(err)
	}
	// Creation order independently keeps a later owner out of an old checkpoint,
	// even if its event clock is historical. Production also fences that clock;
	// only this disposable damage fixture bypasses that separate guard.
	original, err := s.PaperArchivedRecord(ctx, "decision", "original", de, oe)
	if err != nil {
		t.Fatal(err)
	}
	var later retainedPaperDecision
	if json.Unmarshal(original.Record.Payload, &later) != nil {
		t.Fatal("original fixture envelope")
	}
	command.ID = "later"
	later.Record, err = paperDecisionForSchema(later.Record.Schema, command, later.Basis, legacy.CutAt, de)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := s.(*Memory); ok {
		m.paperDecisions[command.ID] = later
		m.retainPaperRecordEpoch("decision", command.ID, later.Record.SHA256, later.Record.FinalizedAt)
	} else {
		pg := s.(*Postgres)
		raw, _ := json.Marshal(later)
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", command.ID, raw); err == nil {
			t.Fatal("historical writer bypassed the source clock fence")
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_decisions DISABLE TRIGGER mantik_paper_decision_feed_cut"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", command.ID, raw); err != nil {
			t.Fatal("historical clock damage fixture", err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_decisions ENABLE TRIGGER mantik_paper_decision_feed_cut"); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := s.PaperFeedCheckpoint(ctx, legacy.Sequence, de, oe); err != nil || !reflect.DeepEqual(c, legacy) {
		t.Fatal("later epoch entered historical source membership", err)
	}
	current := archiveFeed()
	if current.Decisions != 2 || current.Outcomes != 1 || current.PaperArchiveSequence != 1 {
		t.Fatal("current owner cut lost later record", current.PaperFeedCheckpointHeader)
	}
	at = at.Add(time.Nanosecond)
	second, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe)
	if err != nil || second.Header.Sequence != 2 {
		t.Fatal(err)
	}
	// No production path retires owners in schema 106. Simulate the atomic
	// retirement planned for a later owner, only inside this disposable fixture.
	if m, ok := s.(*Memory); ok {
		keep := m.paperDecisions["original"]
		delete(m.paperDecisions, "original")
		if _, err := s.PaperFeedCheckpoint(ctx, legacy.Sequence, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("missing unretired owner fell back to an archive", err)
		}
		m.paperDecisions["original"] = keep
		m.paperRecordRetiredEpochs = map[string]int64{"decision:original": 2, "outcome:outcome": 2}
		delete(m.paperDecisions, "original")
		delete(m.paperOutcomes, "outcome")
	} else {
		pg := s.(*Postgres)
		tx, err := pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "DELETE FROM mantik_paper_decisions WHERE id='original'"); err != nil {
			t.Fatal(err)
		}
		if _, err := readPaperCheckpointOwners(ctx, tx, legacy.PaperArchiveSequence, legacy.CutAt); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("missing unretired SQL owner fell back to an archive", err)
		}
		_ = tx.Rollback(ctx)
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE record_id='original'"); err == nil {
			t.Fatal("schema 106 allowed an unapproved retirement")
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE (kind,record_id) IN (('decision','original'),('outcome','outcome')); ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_outcomes WHERE id='outcome'; DELETE FROM mantik_paper_decisions WHERE id='original'"); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=106"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("interval migration replay", err)
		}
		var plan string
		tx, err = pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+paperCheckpointOwnerQuery, int64(0), legacy.CutAt.Unix(), legacy.CutAt.Nanosecond(), 513).Scan(&plan); err != nil || !strings.Contains(plan, "mantik_paper_owner_interval") {
			t.Fatal("historical membership cannot use interval index", plan, err)
		}
		_ = tx.Rollback(ctx)
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal("restart lost interval metadata", err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
	}
	for _, want := range []struct {
		epoch               int64
		cut                 time.Time
		decisions, outcomes int
	}{
		{0, d.Record.FinalizedAt.Add(-time.Nanosecond), 0, 0},
		{0, d.Record.FinalizedAt, 1, 0},
		{0, legacy.CutAt, 1, 1},
		{1, legacy.CutAt, 2, 1},
		{2, legacy.CutAt, 1, 0},
	} {
		var owners paperArchiveOwners
		if m, ok := s.(*Memory); ok {
			owners, err = m.paperCheckpointOwnersLocked(ctx, want.epoch, want.cut)
		} else {
			tx, txErr := s.(*Postgres).pool.Begin(ctx)
			if txErr != nil {
				t.Fatal(txErr)
			}
			owners, err = readPaperCheckpointOwners(ctx, tx, want.epoch, want.cut)
			_ = tx.Rollback(ctx)
		}
		if err != nil || len(owners.decisions) != want.decisions || len(owners.outcomes) != want.outcomes {
			t.Fatal("owner interval or nanosecond boundary changed", want, err)
		}
	}
	for _, want := range []PaperFeedCheckpoint{legacy, current} {
		if got, err := s.PaperFeedCheckpoint(ctx, want.Sequence, de, oe); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("retirement changed original checkpoint", want.Sequence, err)
		}
	}
	for n := int64(1); n <= 2; n++ {
		if _, err := s.PaperRecordArchive(ctx, n, de, oe); err != nil {
			t.Fatal("later retirement changed original archive membership", n, err)
		}
	}
	newCut := archiveFeed()
	if newCut.Decisions != 1 || newCut.Outcomes != 0 || newCut.PaperArchiveSequence != 2 || !newCut.CutAt.Equal(second.Header.ArchivedAt) {
		t.Fatal("retired originals entered new owner cut or lowered its clock", newCut.PaperFeedCheckpointHeader)
	}
	if _, err := s.AuditPaperFeed(ctx, de, oe); err != nil {
		t.Fatal("historical checkpoint source audit after retirement", err)
	}
	// Raw original availability is required for replay after physical removal.
	if m, ok := s.(*Memory); ok {
		m.paperRecordArchives[0].Records[0].Payload = json.RawMessage(`{}`)
	} else if _, err := s.(*Postgres).pool.Exec(ctx, "ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives; UPDATE mantik_paper_record_archives SET payload='{}'::bytea WHERE sequence=1; ALTER TABLE mantik_paper_record_archives ENABLE TRIGGER mantik_immutable_paper_archives"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PaperFeedCheckpoint(ctx, legacy.Sequence, de, oe); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("missing original accepted after retirement", err)
	}
	if got, err := s.PaperFeedCheckpoint(ctx, newCut.Sequence, de, oe); err != nil || !reflect.DeepEqual(got, newCut) {
		t.Fatal("unrelated retired archive body affected current cut", err)
	}
}
