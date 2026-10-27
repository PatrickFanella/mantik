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
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type corporateAssessmentBackend interface {
	assessmentBackend
	CorporateActionSourceStore
	PaperEvaluationCorporateInspectionStore
	PaperEvaluationCorporateChainStore
}

func TestMemoryEvaluationCorporateSourceCuts(t *testing.T) {
	evaluationCorporateSourceCuts(t, NewMemory())
}
func TestPostgresContractsEvaluationCorporateSourceCuts(t *testing.T) {
	p := timerPostgres(t, "corporatecuts")
	evaluationCorporateSourceCuts(t, p)
	corporateCutSourceCommitFailure(t, p)
	corporateCutMigration(t, p)
}
func evaluationCorporateSourceCuts(t *testing.T, s corporateAssessmentBackend) {
	t.Helper()
	ctx := context.Background()
	plan, at := declarationFixture(t, s, "prospective_market_unqualified")
	declared, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Error("no decision should be evaluated")
		return PaperDecisionEvaluation{}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Error("no outcome should be evaluated")
		return PaperOutcomeEvaluation{}, nil
	}
	source := corporateSourceFixture(plan.ReceiverID)
	source.Recording.ReceivedAt = at.Add(time.Second)
	first, err := s.RetainCorporateActionSource(ctx, source, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	// A schema71 source could have been retained beyond the old shared clock.
	// Simulate that actual legacy envelope without rewriting its original bytes.
	legacyCommand := clone(source)
	legacyCommand.ID = "legacy-future-source"
	legacyCommand.Recording.RawRef = "artifact:legacy-future-source"
	legacyRecord, err := newCorporateActionSource(legacyCommand, 2, first.Record.SHA256, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	switch b := s.(type) {
	case *Memory:
		b.corporateActionSources = append(b.corporateActionSources, legacyRecord)
	case *Postgres:
		raw, _ := json.Marshal(legacyRecord)
		if _, err := b.pool.Exec(ctx, "INSERT INTO mantik_corporate_action_sources(id,sequence,payload) VALUES($1,$2,$3)", legacyRecord.Command.ID, legacyRecord.Sequence, raw); err != nil {
			t.Fatal(err)
		}
	}
	earlyCommand := PaperEvaluationAssessmentCommand{ID: "cannot-omit-held-source", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declared.Record.SHA256}
	_, earlyErr := s.AssessPaperEvaluation(ctx, earlyCommand, func() time.Time { return at.Add(3 * time.Second) }, de, oe)
	switch b := s.(type) {
	case *Memory:
		b.corporateActionSources = b.corporateActionSources[:1]
	case *Postgres:
		if _, err := b.pool.Exec(ctx, "DELETE FROM mantik_corporate_action_sources WHERE id=$1", legacyRecord.Command.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(earlyErr, ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("issuance silently omitted a source already held by the owner", earlyErr)
	}
	c := PaperEvaluationAssessmentCommand{ID: "corporate-cut-one", ReceiverID: plan.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declared.Record.SHA256}
	cutAt := at.Add(3 * time.Second)
	r, err := s.AssessPaperEvaluation(ctx, c, func() time.Time { return cutAt }, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	if r.Record.Schema != "mantik-owned-paper-evaluation-assessment-v5" || r.Record.CorporateSourceCut == nil || len(r.Record.CorporateSourceCut.Sources) != 1 || r.Record.CorporateSourceCut.Sources[0].SHA256 != first.Record.SHA256 || !r.Record.CorporateSourceCut.AsOf.Equal(cutAt) || r.Record.CorporateSourceCut.DeclarationSHA256 != declared.Record.SHA256 {
		t.Fatal("actual source cut missing", r)
	}
	assertCorporateCutNoGrant(t, r.Record)
	inspection, err := s.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
	if err != nil || inspection.AssessmentSHA256 != r.Record.SHA256 || inspection.SourceCutSHA256 != r.Record.CorporateSourceCut.SHA256 || len(inspection.Pages) != 1 || inspection.Pages[0].Pin.SHA256 != first.Record.SHA256 || inspection.Pages[0].CaptureID != source.Recording.CaptureID || inspection.Pages[0].Revision != source.Recording.Revision || inspection.Pages[0].AcquisitionCustody != "caller_reported_response_only_request_and_completion_unavailable" || inspection.ProviderAttested || inspection.CoverageComplete || inspection.EconomicApplicationReady || inspection.AdmissionAvailable || inspection.ExecutionAvailable || inspection.QualificationCredit {
		t.Fatal("inspection did not resolve actual custody pins", inspection, err)
	}
	page := inspection.Pages[0]
	if page.QueryDateBasis != "inclusive_process_date" || page.Start != source.Recording.Start || page.End != source.Recording.End || !reflect.DeepEqual(page.Symbols, source.Recording.Symbols) || !page.ReceivedAt.Equal(source.Recording.ReceivedAt) || page.PageState != "more_pages_unavailable" || page.NextPageToken == nil || *page.NextPageToken != "next+cursor" || page.RecordsObserved != 1 {
		t.Fatal("inspection changed source query or pagination metadata", page)
	}
	if _, err := s.PaperEvaluationCorporateInspection(ctx, "missing", de, oe); !errors.Is(err, ErrPaperEvaluationAssessmentNotFound) {
		t.Fatal("missing assessment became a source inspection", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PaperEvaluationCorporateInspection(cancelled, c.ID, de, oe); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled inspection returned a record", err)
	}
	second := clone(source)
	second.ID = "source-after-cut"
	second.Recording.Revision = 2
	second.Recording.RawRef = "artifact:source-after-cut"
	for _, clock := range []time.Time{cutAt.Add(-time.Nanosecond), cutAt} {
		if _, err := s.RetainCorporateActionSource(ctx, second, func() time.Time { return clock }); !errors.Is(err, feedcollector.ErrClockBackwards) {
			t.Fatal("later write entered retained cut", err)
		}
	}
	late, err := s.RetainCorporateActionSource(ctx, second, func() time.Time { return cutAt.Add(time.Nanosecond) })
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { t.Fatal("exact retry read clock"); return time.Time{} }
	retried, err := s.AssessPaperEvaluation(ctx, c, noClock, de, oe)
	if err != nil || !retried.Duplicate || !reflect.DeepEqual(retried.Record, r.Record) {
		t.Fatal("later source changed retained cut", err)
	}
	if _, err := s.RetainCorporateActionSource(ctx, source, noClock); err != nil {
		t.Fatal("old source retry lost", err)
	}
	afterLate, err := s.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
	if err != nil || !reflect.DeepEqual(afterLate, inspection) {
		t.Fatal("late revision changed assessment inspection", afterLate, err)
	}
	afterLate.Pages[0].Symbols[0] = "MUTATED"
	again, err := s.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
	if err != nil || !reflect.DeepEqual(again, inspection) {
		t.Fatal("inspection aliased owner data", err)
	}
	c2 := c
	c2.ID = "corporate-cut-two"
	newer, err := s.AssessPaperEvaluation(ctx, c2, func() time.Time { return cutAt.Add(time.Second) }, de, oe)
	if err != nil || len(newer.Record.CorporateSourceCut.Sources) != 2 || newer.Record.CorporateSourceCut.Sources[1].SHA256 != late.Record.SHA256 {
		t.Fatal("new cut omitted actual later source", err)
	}
	set := func(v PaperEvaluationAssessment) {
		switch b := s.(type) {
		case *Memory:
			b.evaluationAssessments[v.Command.ID] = clone(v)
		case *Postgres:
			raw, _ := json.Marshal(v)
			if _, err := b.pool.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, v.Command.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, kind := range []string{"omitted_source", "replaced_source", "coverage_grant"} {
		bad := clone(r.Record)
		switch kind {
		case "omitted_source":
			bad.CorporateSourceCut.Sources = []PaperEvaluationCorporateSourcePin{}
		case "replaced_source":
			bad.CorporateSourceCut.Sources[0].SHA256 = late.Record.SHA256
		case "coverage_grant":
			bad.CorporateSourceCut.CoverageComplete = true
		}
		bad.CorporateSourceCut.SHA256 = ""
		bad.CorporateSourceCut.SHA256 = workerplan.Hash(*bad.CorporateSourceCut)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		set(bad)
		_, readErr := s.PaperEvaluationAssessments(ctx, de, oe)
		_, retryErr := s.AssessPaperEvaluation(ctx, c, noClock, de, oe)
		_, inspectErr := s.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
		_, chainErr := s.PaperEvaluationCorporatePageChain(ctx, c.ID, source.ID, de, oe)
		set(r.Record)
		if !errors.Is(readErr, ErrPaperEvaluationAssessmentInvalid) || !errors.Is(retryErr, ErrPaperEvaluationAssessmentInvalid) || !errors.Is(inspectErr, ErrPaperEvaluationAssessmentInvalid) || !errors.Is(chainErr, ErrPaperEvaluationAssessmentInvalid) {
			t.Fatal("self-rehashed source cut substituted actual owner", kind, readErr, retryErr)
		}
	}
	// Actual old v4 receipts retain their bytes and acquire no retrospective cut.
	legacy, err := paperEvaluationAssessmentRecipe(r.Record.Command, r.Record.Evidence, r.Record.WarmUp, "mantik-owned-paper-evaluation-assessment-v4")
	if err != nil {
		t.Fatal(err)
	}
	set(legacy)
	old, err := s.AssessPaperEvaluation(ctx, c, noClock, de, oe)
	oldInspection, inspectErr := s.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
	oldChain, chainErr := s.PaperEvaluationCorporatePageChain(ctx, c.ID, source.ID, de, oe)
	set(r.Record)
	if chainErr != nil || oldChain.Status != "source_cut_unavailable_for_legacy_assessment" || len(oldChain.Pages) != 0 {
		t.Fatal("legacy chain invented source cut", chainErr)
	}
	if inspectErr != nil || oldInspection.Status != "source_cut_unavailable_for_legacy_assessment" || len(oldInspection.Pages) != 0 || oldInspection.SourceCutSHA256 != "" || oldInspection.AssessmentSHA256 != legacy.SHA256 {
		t.Fatal("legacy inspection invented a retrospective cut", oldInspection, inspectErr)
	}
	if err != nil || !old.Duplicate || !reflect.DeepEqual(old.Record, legacy) || old.Record.CorporateSourceCut != nil {
		t.Fatal("legacy v4 receipt reinterpreted", err)
	}
	// Equal-clock concurrent writers must serialize: either the source is in the
	// cut or its append is rejected. There is no later backdated insertion.
	third := clone(second)
	third.ID = "source-racing-cut"
	third.Recording.RawRef = "artifact:source-racing-cut"
	third.Recording.Revision = 3
	c3 := c
	c3.ID = "corporate-cut-racing"
	when := cutAt.Add(2 * time.Second)
	var wg sync.WaitGroup
	var writeErr, errorAssessment error
	var raced PaperEvaluationAssessmentReceipt
	wg.Go(func() { _, writeErr = s.RetainCorporateActionSource(ctx, third, func() time.Time { return when }) })
	wg.Go(func() {
		raced, errorAssessment = s.AssessPaperEvaluation(ctx, c3, func() time.Time { return when }, de, oe)
	})
	wg.Wait()
	if errorAssessment != nil || writeErr != nil && !errors.Is(writeErr, feedcollector.ErrClockBackwards) {
		t.Fatal(writeErr, errorAssessment)
	}
	want := 2
	if writeErr == nil {
		want = 3
	}
	if len(raced.Record.CorporateSourceCut.Sources) != want {
		t.Fatal("source/cut writes were not atomic", writeErr, raced)
	}
	assertCorporateCutNoGrant(t, raced.Record)
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		again, err := reopened.AssessPaperEvaluation(ctx, c, noClock, de, oe)
		inspected, inspectErr := reopened.PaperEvaluationCorporateInspection(ctx, c.ID, de, oe)
		reopened.Close()
		if err != nil || !reflect.DeepEqual(again.Record, r.Record) {
			t.Fatal("reopen changed custody cut", err)
		}
		if inspectErr != nil || !reflect.DeepEqual(inspected, inspection) {
			t.Fatal("reopen changed pinned source inspection", inspectErr)
		}
	}
}

func corporateCutSourceCommitFailure(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	before, err := p.CorporateActionSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var clockBefore, clockAfter string
	query := "SELECT payload::text FROM mantik_feed_collectors WHERE singleton"
	if err := p.pool.QueryRow(ctx, query).Scan(&clockBefore); err != nil {
		t.Fatal(err)
	}
	c := corporateSourceFixture(before.ReceiverID)
	c.ID = "source-after-rollback"
	c.Recording.RawRef = "artifact:source-after-rollback"
	at := before.Records[len(before.Records)-1].RetainedAt.Add(time.Hour)
	if _, err := p.pool.Exec(ctx, `CREATE FUNCTION reject_corporate_clock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'manufactured source clock commit failure'; END $$; CREATE TRIGGER reject_corporate_clock BEFORE UPDATE ON mantik_feed_collectors FOR EACH ROW EXECUTE FUNCTION reject_corporate_clock()`); err != nil {
		t.Fatal(err)
	}
	_, writeErr := p.RetainCorporateActionSource(ctx, c, func() time.Time { return at })
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_corporate_clock ON mantik_feed_collectors; DROP FUNCTION reject_corporate_clock()"); err != nil {
		t.Fatal(err)
	}
	after, err := p.CorporateActionSources(ctx)
	if err != nil || writeErr == nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed shared-clock update leaked a retained source", writeErr, err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&clockAfter); err != nil || clockBefore != clockAfter {
		t.Fatal("failed source write changed the shared clock", err)
	}
	if _, err := p.RetainCorporateActionSource(ctx, c, func() time.Time { return at }); err != nil {
		t.Fatal("failed attempt consumed source identity", err)
	}
}
func assertCorporateCutNoGrant(t *testing.T, r PaperEvaluationAssessment) {
	t.Helper()
	cut := r.CorporateSourceCut
	if cut == nil || cut.ProviderAttested || cut.CoverageComplete || cut.EconomicApplicationReady || cut.AdmissionAvailable || cut.ExecutionAvailable || cut.QualificationCredit || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal("custody cut granted unsupported authority", r)
	}
	for _, c := range r.Checks {
		if c.Name == "benchmark_comparison" && c.Status != "unavailable" {
			t.Fatal("source presence invented a benchmark", c)
		}
	}
}
func corporateCutMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	q := "SELECT jsonb_agg(payload ORDER BY id)::text FROM mantik_paper_evaluation_assessments"
	if err := p.pool.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=72; COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'cut rollback marker'; CREATE FUNCTION reject_corporate_cut_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=72 THEN RAISE EXCEPTION 'manufactured cut migration rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_corporate_cut_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_corporate_cut_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration rejection ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_assessments'::regclass)").Scan(&marker); err != nil || marker != "cut rollback marker" {
		t.Fatal("cut migration escaped rollback", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_corporate_cut_migration ON mantik_schema; DROP FUNCTION reject_corporate_cut_migration()"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.pool.QueryRow(ctx, q).Scan(&after); err != nil || before != after {
		t.Fatal("cut migration rewrote receipts", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=72").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=72"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("cut migration checksum ignored")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=72", hash); err != nil {
		t.Fatal(err)
	}
}
