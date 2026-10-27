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

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Manufactured internal owner input; service tests separately exercise actual
// configured journal reads. No public endpoint accepts this proof as input.
func journalCorporateSourceFixture(owner, id string) (CorporateActionSourceCommand, alpacarecord.CorporateActionAcquisition) {
	c := corporateSourceFixture(owner)
	c.ID = id
	c.Recording.CaptureID = "acquired-" + id
	if len(c.Recording.CaptureID) > 40 {
		c.Recording.CaptureID = "acquired-recovery"
	}
	c.Recording.EvidenceClass = "prospective_market_unqualified"
	journal := "journal-manufactured"
	c.Recording.RawRef = "journal:" + journal + ":capture-" + c.Recording.CaptureID + "-r1.json"
	at := c.Recording.ReceivedAt
	a := alpacarecord.CorporateActionAcquisition{Schema: "mantik-journal-corporate-acquisition-v1", Method: "retained-intent-attempts-capture-completion-v1", JournalID: journal, CollectionID: c.Recording.CaptureID, IntentSHA256: strings.Repeat("a", 64), CompletionSHA256: strings.Repeat("b", 64), CaptureRef: c.Recording.RawRef, CaptureSHA256: strings.Repeat("c", 64), RecordingSHA256: workerplan.Hash(c.Recording), SourceSHA256: c.Recording.SHA256, RequestedAt: at.Add(-time.Second), ResponseAt: at, CompletedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Attempts: []alpacarecord.CorporateActionAcquisitionAttempt{{Number: 1, StartSHA256: strings.Repeat("d", 64), ResponseSHA256: strings.Repeat("e", 64), StartedAt: at.Add(-time.Nanosecond), EndedAt: at, State: "response", HTTPStatus: 200}}, Authority: "journal_owned_recorded_acquisition_only"}
	a.SHA256 = workerplan.Hash(a)
	return c, a
}

type journalCorporateBackend interface {
	corporateSourceBackend
	JournalCorporateActionSourceStore
}

func TestMemoryJournalCorporateSources(t *testing.T) {
	journalCorporateSources(t, NewMemory())
	journalCorporateAssessment(t, NewMemory())
}
func TestPostgresContractsJournalCorporateSources(t *testing.T) {
	p := timerPostgres(t, "journalcorporate")
	journalCorporateSources(t, p)
	journalCorporateRollback(t, p)
	journalCorporateMigration(t, p)
	journalCorporateAssessment(t, timerPostgres(t, "journalassessment"))
}

type journalAssessmentBackend interface {
	corporateAssessmentBackend
	JournalCorporateActionSourceStore
}

func journalCorporateAssessment(t *testing.T, s journalAssessmentBackend) {
	t.Helper()
	ctx := context.Background()
	plan, at := declarationFixture(t, s, "prospective_market_unqualified")
	declaration, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	c, a := journalCorporateSourceFixture(plan.ReceiverID, "assessment-journal")
	source, err := s.RetainJournalCorporateActionSource(ctx, c, a, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Error("unexpected decision evaluation")
		return PaperDecisionEvaluation{}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Error("unexpected outcome evaluation")
		return PaperOutcomeEvaluation{}, nil
	}
	command := PaperEvaluationAssessmentCommand{ID: "journal-assessment", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}
	r, err := s.AssessPaperEvaluation(ctx, command, func() time.Time { return at.Add(3 * time.Second) }, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	if r.Record.CorporateSourceCut == nil || len(r.Record.CorporateSourceCut.Sources) != 1 || r.Record.CorporateSourceCut.Sources[0].SHA256 != source.Record.SHA256 {
		t.Fatal("assessment lost imported owner binding")
	}
	inspection, err := s.PaperEvaluationCorporateInspection(ctx, command.ID, de, oe)
	if err != nil || len(inspection.Pages) != 1 || inspection.Pages[0].AcquisitionSHA256 != a.SHA256 || inspection.Pages[0].AcquisitionCustody != "service_retained_configured_journal_acquisition" || inspection.AdmissionAvailable || inspection.EconomicApplicationReady {
		t.Fatal("inspection lost imported custody", inspection, err)
	}
}

func journalCorporateMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	q := "SELECT jsonb_agg(payload ORDER BY sequence)::text FROM mantik_corporate_action_sources"
	var before, after, comment, hash string
	if err := p.pool.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=73; COMMENT ON TABLE mantik_corporate_action_sources IS 'journal rollback marker'; CREATE FUNCTION reject_journal_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=73 THEN RAISE EXCEPTION 'manufactured journal migration failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_journal_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_journal_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_corporate_action_sources'::regclass)").Scan(&comment); err != nil || comment != "journal rollback marker" {
		t.Fatal("migration escaped rollback", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_journal_migration ON mantik_schema; DROP FUNCTION reject_journal_migration()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := p.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.pool.QueryRow(ctx, q).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed source payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=73").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=73"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration checksum ignored")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=73", hash); err != nil {
		t.Fatal(err)
	}
}
func journalCorporateSources(t *testing.T, s journalCorporateBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacy := corporateSourceFixture(receiver.ID)
	old, err := s.RetainCorporateActionSource(ctx, legacy, func() time.Time { return legacy.Recording.ReceivedAt.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	c, a := journalCorporateSourceFixture(receiver.ID, "journal-one")
	at := old.Record.RetainedAt.Add(time.Second)
	r, err := s.RetainJournalCorporateActionSource(ctx, c, a, func() time.Time { return at })
	if err != nil || r.Duplicate || r.Record.Schema != "mantik-owned-corporate-action-source-v2" || r.Record.Acquisition == nil || !reflect.DeepEqual(*r.Record.Acquisition, a) || r.Record.ProviderAttested || r.Record.CoverageComplete || r.Record.EconomicApplicationReady || r.Record.AdmissionAvailable || r.Record.ExecutionAvailable || r.Record.QualificationCredit {
		t.Fatal(r, err)
	}
	noClock := func() time.Time { t.Fatal("exact retry or invalid command read clock"); return time.Time{} }
	retry, err := s.RetainJournalCorporateActionSource(ctx, c, a, noClock)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r.Record) {
		t.Fatal("retry changed", err)
	}
	if _, err := s.RetainCorporateActionSource(ctx, c, noClock); !errors.Is(err, ErrCorporateActionSourceConflict) {
		t.Fatal("caller-reported source adopted journal origin", err)
	}
	cat, err := s.CorporateActionSources(ctx)
	if err != nil || !reflect.DeepEqual(cat.Records[0], old.Record) {
		t.Fatal("legacy source changed", err)
	}
	bad := a
	bad.RecordingSHA256 = strings.Repeat("f", 64)
	bad.SHA256 = ""
	bad.SHA256 = workerplan.Hash(bad)
	newc := c
	newc.ID = "bad-source"
	if _, err := s.RetainJournalCorporateActionSource(ctx, newc, bad, noClock); !errors.Is(err, ErrCorporateActionSourceInvalid) {
		t.Fatal("rehashed binding accepted", err)
	}
	next, nextA := journalCorporateSourceFixture(receiver.ID, "journal-race")
	var wg sync.WaitGroup
	results := make(chan CorporateActionSourceReceipt, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.RetainJournalCorporateActionSource(ctx, next, nextA, func() time.Time { return at.Add(time.Second) })
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	fresh := 0
	for v := range results {
		if !v.Duplicate {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("duplicate source append", fresh)
	}
	set := func(v CorporateActionSourceRecord) {
		switch b := s.(type) {
		case *Memory:
			b.corporateActionSources[1] = clone(v)
		case *Postgres:
			raw, _ := json.Marshal(v)
			if _, e := b.pool.Exec(ctx, "UPDATE mantik_corporate_action_sources SET payload=$1 WHERE id=$2", raw, v.Command.ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	corrupt := clone(r.Record)
	corrupt.Acquisition.CoverageComplete = true
	corrupt.Acquisition.SHA256 = ""
	corrupt.Acquisition.SHA256 = workerplan.Hash(*corrupt.Acquisition)
	corrupt.SHA256 = ""
	corrupt.SHA256 = workerplan.Hash(corrupt)
	set(corrupt)
	_, readErr := s.CorporateActionSources(ctx)
	_, retryErr := s.RetainJournalCorporateActionSource(ctx, c, a, noClock)
	set(r.Record)
	if !errors.Is(readErr, ErrCorporateActionSourceInvalid) || !errors.Is(retryErr, ErrCorporateActionSourceInvalid) {
		t.Fatal("rehashed proof grant accepted", readErr, retryErr)
	}
	if p, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		got, e := reopened.RetainJournalCorporateActionSource(ctx, c, a, noClock)
		if e != nil || !reflect.DeepEqual(got.Record, r.Record) {
			t.Fatal("reopen lost binding", e)
		}
	}
}
func journalCorporateRollback(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	before, err := p.CorporateActionSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, a := journalCorporateSourceFixture(before.ReceiverID, "rollback-journal")
	at := before.Records[len(before.Records)-1].RetainedAt.Add(time.Hour)
	var clock string
	if err := p.pool.QueryRow(ctx, "SELECT payload::text FROM mantik_feed_collectors WHERE singleton").Scan(&clock); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `CREATE FUNCTION reject_import_clock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'manufactured imported source commit failure'; END $$; CREATE TRIGGER reject_import_clock BEFORE UPDATE ON mantik_feed_collectors FOR EACH ROW EXECUTE FUNCTION reject_import_clock()`); err != nil {
		t.Fatal(err)
	}
	_, writeErr := p.RetainJournalCorporateActionSource(ctx, c, a, func() time.Time { return at })
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_import_clock ON mantik_feed_collectors; DROP FUNCTION reject_import_clock()"); err != nil {
		t.Fatal(err)
	}
	after, err := p.CorporateActionSources(ctx)
	var later string
	clockErr := p.pool.QueryRow(ctx, "SELECT payload::text FROM mantik_feed_collectors WHERE singleton").Scan(&later)
	if writeErr == nil || err != nil || clockErr != nil || clock != later || !reflect.DeepEqual(before, after) {
		t.Fatal("failed import escaped transaction", writeErr, err, clockErr)
	}
	if _, err := p.RetainJournalCorporateActionSource(ctx, c, a, func() time.Time { return at }); err != nil {
		t.Fatal("rollback consumed identity", err)
	}
}
