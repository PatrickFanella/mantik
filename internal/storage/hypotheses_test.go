package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

type hypothesisBackend interface {
	reviewBackend
	HypothesisStore
	HypothesisCatalogueStore
}

func TestMemoryHypothesisPublication(t *testing.T) { hypothesisContracts(t, NewMemory()) }
func TestPostgresContractsHypothesisPublication(t *testing.T) {
	hypothesisContracts(t, timerPostgres(t, "hypotheses"))
}
func candidateDeclaration(t *testing.T, s hypothesisBackend, receiver, id, caseID string, version int, now func() time.Time) hypothesis.Declaration {
	t.Helper()
	ctx := context.Background()
	a, err := s.StartReview(ctx, receiver, id, caseID, version, now)
	if err != nil {
		t.Fatal(err)
	}
	c := a.Attempt.Pin.Group.Claims[0]
	for _, claim := range a.Attempt.Pin.Group.Claims {
		if claim.Claim.SourceID == "new-source" {
			c = claim
		}
	}
	d := hypothesis.Fixture()
	d.Origins = []hypothesis.Origin{{Reference: c.Reference, ClaimKey: c.Claim.Key, RecordingSHA256: c.Claim.RecordingSHA256}}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: c.Claim.Key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{c.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{c.Claim.Key}}}})
	a, err = s.CompleteReview(ctx, receiver, id, []json.RawMessage{tool, proposal}, now)
	if err != nil || a.Attempt.Result.Outcome != "propose_hypothesis" {
		t.Fatal(a, err)
	}
	return d
}
func hypothesisContracts(t *testing.T, s hypothesisBackend) {
	ctx := context.Background()
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := catalog.ReceiverID
	empty, err := s.HypothesisCatalogue(ctx)
	if err != nil || empty.Validate() != nil || empty.ReceiverID != receiver || empty.PublicationCount != 0 {
		t.Fatal(empty, err)
	}
	receipt, err := s.AdmitNews(ctx, receiver, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := receipt.Matches[0].CaseID
	d := candidateDeclaration(t, s, receiver, "review-one", caseID, 1, now)
	before, _ := s.Snapshot(ctx)
	casesBefore, _ := s.NewsCases(ctx)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p, err := s.PublishHypothesis(ctx, receiver, "first", "review-one", d, now)
			if err != nil || p.QuestionReused {
				t.Error(p, err)
			}
		})
	}
	wg.Wait()
	first, err := s.PublishHypothesis(ctx, receiver, "first", "review-one", d, now)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Hypothesis(ctx, first.Sealed.ID)
	if err != nil || len(q.Publications) != 1 || !reflect.DeepEqual(first, q.Publications[0]) {
		t.Fatal(q, err)
	}
	if _, err = s.PublishHypothesis(ctx, "receiver-abcdefghijklmnop", "wrong", "review-one", d, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	changed := clone(d)
	changed.Label = "new-label"
	if _, err = s.PublishHypothesis(ctx, receiver, "first", "review-one", changed, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("identity overwritten", err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "first", "other-review", d, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("review rebound", err)
	}
	for n := range 8 {
		wg.Go(func() {
			p, err := s.PublishHypothesis(ctx, receiver, fmt.Sprintf("reuse-%d", n), "review-one", changed, now)
			if err != nil || !p.QuestionReused || p.Sealed.ID != first.Sealed.ID {
				t.Error(p, err)
			}
			c, err := s.HypothesisCatalogue(ctx)
			if err != nil || c.Validate() != nil || c.QuestionCount != 1 || c.PublicationCount < 2 || c.PublicationCount > 9 {
				t.Error("incoherent concurrent catalogue", c, err)
			}
		})
	}
	wg.Wait()
	other := clone(d)
	other.HorizonSeconds++
	later := at.Add(time.Second)
	separate, err := s.PublishHypothesis(ctx, receiver, "other-question", "review-one", other, func() time.Time { return later })
	if err != nil || separate.QuestionReused || separate.Sealed.ID == first.Sealed.ID {
		t.Fatal("different question conflated", err)
	}
	isolated, err := s.Hypothesis(ctx, separate.Sealed.ID)
	if err != nil || len(isolated.Publications) != 1 || isolated.Publications[0].ID != "other-question" {
		t.Fatal("unrelated history selected", err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "backdated", "review-one", d, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("publication clock moved backwards", err)
	}
	at = later
	after, _ := s.Snapshot(ctx)
	casesAfter, _ := s.NewsCases(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(casesBefore, casesAfter) {
		t.Fatal("publication changed news or execution state")
	}
	catalogueBefore, err := s.HypothesisCatalogue(ctx)
	if err != nil || catalogueBefore.Validate() != nil || catalogueBefore.QuestionCount != 2 || catalogueBefore.PublicationCount != 10 {
		t.Fatal(catalogueBefore, err)
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		q, err = s.Hypothesis(ctx, first.Sealed.ID)
		if err != nil || len(q.Publications) != 9 || !reflect.DeepEqual(first, q.Publications[0]) {
			t.Fatal("restart lost publication", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_hypothesis_publications ADD CONSTRAINT reject_publication CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.PublishHypothesis(ctx, receiver, "rollback", "review-one", d, now); err == nil {
			t.Fatal("failed write acknowledged")
		}
		q, err = s.Hypothesis(ctx, first.Sealed.ID)
		if err != nil || len(q.Publications) != 9 {
			t.Fatal("partial write", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_hypothesis_publications DROP CONSTRAINT reject_publication"); err != nil {
			t.Fatal(err)
		}
	}
	catalogueAfter, err := s.HypothesisCatalogue(ctx)
	if err != nil || !reflect.DeepEqual(catalogueBefore, catalogueAfter) {
		t.Fatal("restart or failed write changed catalogue", err)
	}
	if _, err = s.CloseNewsCase(ctx, receiver, caseID, 1, now); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.PublishHypothesis(ctx, receiver, "first", "review-one", d, func() time.Time { panic("retry consulted clock") })
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatal("closed-case retry lost receipt", err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "closed", "review-one", d, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("closed review republished", err)
	}
	r.SourceID = "new-source"
	if _, err = s.AdmitNews(ctx, receiver, r, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "stale", "review-one", d, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("stale review republished", err)
	}
	newer := candidateDeclaration(t, s, receiver, "review-two", caseID, 2, now)
	newer.Label = "second-origin"
	// Deliberately cite the newer source even when its claim sorts before the old.
	a, _ := s.Review(ctx, "review-two")
	if newer.Origins[0].RecordingSHA256 == d.Origins[0].RecordingSHA256 {
		t.Fatal("new origin not retained")
	}
	p, err := s.PublishHypothesis(ctx, receiver, "new-review", "review-two", newer, now)
	if err != nil || !p.QuestionReused || p.ReviewResultID != a.Attempt.Result.ID || p.Sealed.ID != first.Sealed.ID {
		t.Fatal(p, err)
	}
	for n := 11; n < hypothesis.MaxPublications; n++ {
		if _, err = s.PublishHypothesis(ctx, receiver, fmt.Sprintf("fill-%d", n), "review-two", newer, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "overflow", "review-two", newer, now); !errors.Is(err, hypothesis.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "first", "review-one", d, now); err != nil {
		t.Fatal("capacity blocked retry", err)
	}
	q, err = s.Hypothesis(ctx, first.Sealed.ID)
	if err != nil || len(q.Publications) != 31 || q.QualificationCredit || q.Authority != "research_declaration_only" {
		t.Fatal(q, err)
	}
	full, err := s.HypothesisCatalogue(ctx)
	if err != nil || full.Validate() != nil || full.PublicationCount != 32 || full.QuestionCount != 2 {
		t.Fatal("catalogue lost closed or superseded publications", full, err)
	}
	full.Questions[0].Publications[0].Sealed.Declaration.Methods[0].SHA256 = "mutated"
	again, err := s.HypothesisCatalogue(ctx)
	if err != nil || again.Validate() != nil {
		t.Fatal("caller mutated stored catalogue", err)
	}
	q.Publications[0].Sealed.Declaration.Mechanism.Statement = "mutated"
	actual, err := s.Hypothesis(ctx, first.Sealed.ID)
	if err != nil || actual.Publications[0].Sealed.Declaration.Mechanism.Statement == "mutated" {
		t.Fatal("caller mutated history", err)
	}
	if _, err = s.Hypothesis(ctx, "hypothesis-0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, hypothesis.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "missing", "missing", d, now); !errors.Is(err, hypothesis.ErrCapacity) && !errors.Is(err, review.ErrNotFound) {
		t.Fatal(err)
	}
	// Corrupt one retained binding: discovery must fail as a whole rather than
	// silently dropping the publication or returning an apparently valid subset.
	switch backend := s.(type) {
	case *Memory:
		backend.mu.Lock()
		bad := backend.publications["first"]
		bad.Sealed.DeclarationSHA256 = "changed"
		backend.publications["first"] = bad
		backend.mu.Unlock()
	case *Postgres:
		if _, err = backend.pool.Exec(ctx, `UPDATE mantik_hypothesis_publications SET payload=jsonb_set(payload,'{sealed,declaration_sha256}','"changed"') WHERE id='first'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.HypothesisCatalogue(ctx); err == nil {
		t.Fatal("corrupt retained binding returned a catalogue")
	}
}
func TestPostgresContractsHypothesisUpgrade(t *testing.T) {
	s := timerPostgres(t, "hypothesisupgrade")
	ctx := context.Background()
	before, _ := s.NewsCases(ctx)
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DELETE FROM mantik_schema WHERE version>=13"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	after, err := upgraded.NewsCases(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed retained state", err)
	}
	var count int
	if err = upgraded.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_hypothesis_publications").Scan(&count); err != nil || count != 0 {
		t.Fatal(err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=13"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("checksum tamper admitted")
	}
}

func TestMemoryHypothesisCloseRace(t *testing.T) { hypothesisCloseRace(t, NewMemory()) }
func TestPostgresContractsHypothesisCloseRace(t *testing.T) {
	hypothesisCloseRace(t, timerPostgres(t, "hypothesisrace"))
}
func hypothesisCloseRace(t *testing.T, s hypothesisBackend) {
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := admitted.Matches[0].CaseID
	d := candidateDeclaration(t, s, catalog.ReceiverID, "review", caseID, 1, now)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var p hypothesis.Publication
	var publishErr, closeErr error
	wg.Go(func() {
		<-start
		p, publishErr = s.PublishHypothesis(ctx, catalog.ReceiverID, "publication", "review", d, now)
	})
	wg.Go(func() { <-start; _, closeErr = s.CloseNewsCase(ctx, catalog.ReceiverID, caseID, 1, now) })
	close(start)
	wg.Wait()
	if closeErr != nil || publishErr != nil && !errors.Is(publishErr, hypothesis.ErrConflict) {
		t.Fatal(publishErr, closeErr)
	}
	if publishErr == nil {
		q, err := s.Hypothesis(ctx, p.Sealed.ID)
		if err != nil || len(q.Publications) != 1 {
			t.Fatal(err)
		}
	} else {
		sealed, _ := hypothesis.Seal(d)
		if _, err := s.Hypothesis(ctx, sealed.ID); !errors.Is(err, hypothesis.ErrNotFound) {
			t.Fatal("rejected publication persisted", err)
		}
	}
	if _, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "after-close", "review", d, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("closed case admitted publication", err)
	}
	a, _ := s.Review(ctx, "review")
	if a.ReviewApplicable || a.EvidenceCurrent {
		t.Fatal("publication kept case open")
	}
}
