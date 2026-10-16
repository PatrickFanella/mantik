package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	researchquestion "git.subcult.tv/subculture-collective/mantik/internal/question"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

type reuseBackend interface {
	profileBackend
	HypothesisCatalogueStore
}

func TestPostgresContractsReviewInventoryUpgrade(t *testing.T) {
	s := timerPostgres(t, "inventoryupgrade")
	ctx := context.Background()
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	catalog, _ := s.NewsCases(ctx)
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := admitted.Matches[0].CaseID
	pending, err := s.StartReviewProfile(ctx, catalog.ReceiverID, "old-pending", caseID, 1, investigation.MethodTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	candidateDeclaration(t, s, catalog.ReceiverID, "old-completed", caseID, 1, now)
	completed, err := s.Review(ctx, "old-completed")
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = s.pool.QueryRow(ctx, "SELECT jsonb_agg(payload ORDER BY id)::text FROM mantik_news_reviews").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DELETE FROM mantik_schema WHERE version>=20"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, migrations.ReviewProfiles); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var after string
	if err = upgraded.pool.QueryRow(ctx, "SELECT jsonb_agg(payload ORDER BY id)::text FROM mantik_news_reviews").Scan(&after); err != nil || before != after {
		t.Fatal("schema 19->20 rewrote old attempts", err)
	}
	for id, expected := range map[string]review.View{"old-pending": pending, "old-completed": completed} {
		actual, err := upgraded.Review(ctx, id)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("upgrade changed review", id, err)
		}
	}
	reuse, err := upgraded.StartReviewProfile(ctx, catalog.ReceiverID, "reuse", caseID, 1, investigation.ReuseTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = upgraded.pool.Exec(ctx, `UPDATE mantik_news_reviews SET payload=jsonb_set(payload,'{hypothesis_inventory}','null') WHERE id='reuse'`); err == nil {
		t.Fatal("SQL allowed missing reuse inventory")
	}
	actual, err := upgraded.Review(ctx, "reuse")
	if err != nil || !reflect.DeepEqual(reuse, actual) {
		t.Fatal("rejected row changed pin", err)
	}
	if _, err = upgraded.pool.Exec(ctx, `UPDATE mantik_news_reviews SET payload=jsonb_set(payload,'{hypothesis_inventory,sha256}',to_jsonb(repeat('f',64))) WHERE id='reuse'`); err != nil {
		t.Fatal(err)
	}
	if _, err = upgraded.Review(ctx, "reuse"); err == nil {
		t.Fatal("corrupt inventory hash accepted on read")
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=20"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("inventory migration checksum change admitted")
	}
}

func reuseSteps(key string, d hypothesis.Declaration, source hypothesis.Result) []json.RawMessage {
	steps := profileSteps(key, d)
	var proposal investigation.Step
	_ = json.Unmarshal(steps[len(steps)-1], &proposal)
	proposal.Proposal.Candidate.Reuse = &researchquestion.Ref{ID: source.ID, DeclarationSHA256: source.DeclarationSHA256}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_hypothesis", HypothesisID: source.ID, DeclarationSHA256: source.DeclarationSHA256}})
	p, _ := json.Marshal(proposal)
	return append(append(steps[:len(steps)-1], tool), p)
}

func TestMemoryRetainedHypothesisReuse(t *testing.T) { reuseContracts(t, NewMemory()) }
func TestPostgresContractsRetainedHypothesisReuse(t *testing.T) {
	reuseContracts(t, timerPostgres(t, "reviewreuse"))
}

func reuseContracts(t *testing.T, s reuseBackend) {
	t.Helper()
	ctx := context.Background()
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("retry sampled clock") }
	c, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := c.ReceiverID
	admitted, err := s.AdmitNews(ctx, receiver, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := admitted.Matches[0].CaseID
	empty, err := s.StartReviewProfile(ctx, receiver, "empty", caseID, 1, investigation.ReuseTypeID, now)
	if err != nil || empty.Attempt.HypothesisInventory == nil || len(empty.Attempt.HypothesisInventory.Declarations) != 0 {
		t.Fatal(empty, err)
	}
	d := candidateDeclaration(t, s, receiver, "seed-review", caseID, 1, now)
	seed, err := s.PublishHypothesis(ctx, receiver, "seed", "seed-review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	future, err := s.CompleteReview(ctx, receiver, "empty", reuseSteps(d.Origins[0].ClaimKey, d, seed.Sealed), now)
	if err != nil || future.Attempt.Result.Outcome != "abstain" || future.Attempt.Result.ReasonCode != "hypothesis_not_eligible" || !reflect.DeepEqual(future.Attempt.HypothesisInventory, empty.Attempt.HypothesisInventory) {
		t.Fatal("future publication leaked into captured inventory", future, err)
	}
	if _, err = s.StartReviewProfile(ctx, receiver, "backdated", caseID, 1, investigation.ReuseTypeID, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, review.ErrConflict) {
		t.Fatal("catalogue predates publication", err)
	}
	if _, err = s.Review(ctx, "backdated"); !errors.Is(err, review.ErrNotFound) {
		t.Fatal(err)
	}
	r.SourceID = "second-source"
	if _, err = s.AdmitNews(ctx, receiver, r, now); err != nil {
		t.Fatal(err)
	}
	starts := make(chan review.View, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v, err := s.StartReviewProfile(ctx, receiver, "reuse", caseID, 2, investigation.ReuseTypeID, now)
			if err != nil {
				t.Error(err)
				return
			}
			starts <- v
		})
	}
	wg.Wait()
	close(starts)
	var started review.View
	for v := range starts {
		if started.Attempt.ID == "" {
			started = v
		} else if !reflect.DeepEqual(started, v) {
			t.Fatal("concurrent capture diverged")
		}
	}
	if started.Attempt.HypothesisInventory == nil || started.Attempt.HypothesisInventory.Validate() != nil || len(started.Attempt.HypothesisInventory.Declarations) != 1 {
		t.Fatal(started)
	}
	current := clone(d)
	for _, claim := range started.Attempt.Pin.Group.Claims {
		if claim.Claim.SourceID == r.SourceID {
			current.Origins = []hypothesis.Origin{{Reference: claim.Reference, ClaimKey: claim.Claim.Key, RecordingSHA256: claim.Claim.RecordingSHA256}}
		}
	}
	current.Label = "new-attribution"
	if current.Origins[0].RecordingSHA256 == d.Origins[0].RecordingSHA256 {
		t.Fatal("missing new source")
	}
	// Extend the authoritative catalogue after capture, without changing news.
	later := candidateDeclaration(t, s, receiver, "later-review", caseID, 2, now)
	later.Label = "later-publication"
	if _, err = s.PublishHypothesis(ctx, receiver, "later", "later-review", later, now); err != nil {
		t.Fatal(err)
	}
	updated, err := s.HypothesisCatalogue(ctx)
	if err != nil || updated.SHA256 == started.Attempt.HypothesisInventory.CatalogueSHA256 {
		t.Fatal("catalogue did not extend", err)
	}
	steps := reuseSteps(current.Origins[0].ClaimKey, current, seed.Sealed)
	completions := make(chan review.View, 8)
	for range 8 {
		wg.Go(func() {
			v, err := s.CompleteReview(ctx, receiver, "reuse", steps, now)
			if err != nil {
				t.Error(err)
				return
			}
			completions <- v
		})
	}
	wg.Wait()
	close(completions)
	var completed review.View
	for v := range completions {
		if completed.Attempt.ID == "" {
			completed = v
		} else if !reflect.DeepEqual(completed, v) {
			t.Fatal("concurrent completion diverged")
		}
	}
	result := completed.Attempt.Result
	if !completed.ReviewApplicable || result == nil || result.Outcome != "propose_hypothesis" || len(result.Checks) != 7 || result.ProfileSHA256 != started.Attempt.Profile.SHA256 || result.HypothesisInventorySHA256 != started.Attempt.HypothesisInventory.SHA256 || !review.ResultMatchesProfile(completed.Attempt, *result) {
		t.Fatal(completed)
	}
	for _, check := range result.Checks {
		if !check.Passed {
			t.Fatal(check)
		}
	}
	for _, mutate := range []func(*investigation.Result){func(r *investigation.Result) { r.HypothesisInventorySHA256 = "changed" }, func(r *investigation.Result) { r.HypothesisCatalogueSHA256 = "changed" }, func(r *investigation.Result) { r.HypothesisReceiverID = "changed" }} {
		bad := clone(*result)
		mutate(&bad)
		if review.ResultMatchesProfile(completed.Attempt, bad) {
			t.Fatal("result rebound inventory")
		}
	}
	if v, err := s.StartReviewProfile(ctx, receiver, "reuse", caseID, 2, investigation.ReuseTypeID, noClock); err != nil || !reflect.DeepEqual(v, completed) {
		t.Fatal("retry recaptured catalogue", err)
	}
	if v, err := s.CompleteReview(ctx, receiver, "reuse", steps, noClock); err != nil || !reflect.DeepEqual(v, completed) {
		t.Fatal("retry changed result", err)
	}
	if _, err = s.StartReviewProfile(ctx, receiver, "reuse", caseID, 2, investigation.MethodTypeID, noClock); !errors.Is(err, review.ErrConflict) {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	changed := clone(current)
	changed.HorizonSeconds++
	if _, err = s.PublishHypothesis(ctx, receiver, "changed-question", "reuse", changed, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("reuse changed declared question", err)
	}
	publication, err := s.PublishHypothesis(ctx, receiver, "reused", "reuse", current, now)
	if err != nil || !publication.QuestionReused || publication.Sealed.ID != seed.Sealed.ID || publication.Sealed.DeclarationSHA256 == seed.Sealed.DeclarationSHA256 || !reflect.DeepEqual(publication.Sealed.Declaration.Origins, current.Origins) {
		t.Fatal(publication, err)
	}
	q, err := s.Hypothesis(ctx, seed.Sealed.ID)
	if err != nil || len(q.Publications) != 3 {
		t.Fatal("reuse rewrote original lineage", q, err)
	}
	foundSeed := false
	for _, p := range q.Publications {
		if p.ID == seed.ID {
			foundSeed = reflect.DeepEqual(p, seed)
		}
	}
	if !foundSeed {
		t.Fatal("original publication changed")
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("research changed operations")
	}
	if !hypothesis.ReuseSourceExists(completed.Attempt, []hypothesis.Publication{seed}) || hypothesis.ReuseSourceExists(completed.Attempt, nil) {
		t.Fatal("publication source not independently bound")
	}
	badSource := clone(seed)
	badSource.Sealed.Declaration.Mechanism.Statement = "changed"
	if hypothesis.ReuseSourceExists(completed.Attempt, []hypothesis.Publication{badSource}) {
		t.Fatal("changed retained source accepted")
	}
	rollback, err := s.StartReviewProfile(ctx, receiver, "rollback", caseID, 2, investigation.ReuseTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, err = pg.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews ADD CONSTRAINT reject_reuse_completion CHECK(id!='rollback' OR payload->>'status'='pending')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CompleteReview(ctx, receiver, "rollback", steps, now); err == nil {
			t.Fatal("failed completion acknowledged")
		}
		actual, err := s.Review(ctx, "rollback")
		if err != nil || !reflect.DeepEqual(actual, rollback) {
			t.Fatal("failed completion lost pin", err)
		}
		if _, err = pg.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews DROP CONSTRAINT reject_reuse_completion; ALTER TABLE mantik_news_reviews ADD CONSTRAINT reject_reuse_start CHECK(id!='failed-start')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.StartReviewProfile(ctx, receiver, "failed-start", caseID, 2, investigation.ReuseTypeID, now); err == nil {
			t.Fatal("failed start acknowledged")
		}
		if _, err = s.Review(ctx, "failed-start"); !errors.Is(err, review.ErrNotFound) {
			t.Fatal("partial start retained", err)
		}
		if _, err = pg.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews DROP CONSTRAINT reject_reuse_start"); err != nil {
			t.Fatal(err)
		}
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
	}
	for id, expected := range map[string]review.View{"reuse": completed, "rollback": rollback, "empty": future} {
		actual, err := s.Review(ctx, id)
		if err != nil || !reflect.DeepEqual(actual.Attempt, expected.Attempt) {
			t.Fatal("restart lost completed or pending catalogue pin", id, err)
		}
	}
	if _, err = s.CompleteReview(ctx, receiver, "rollback", steps, now); err != nil {
		t.Fatal(err)
	}
	expiring, err := s.StartReviewProfile(ctx, receiver, "expiring", caseID, 2, investigation.ReuseTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.StartReviewProfile(ctx, receiver, "stale", caseID, 2, investigation.ReuseTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	r.SourceID = "third-source"
	if _, err = s.AdmitNews(ctx, receiver, r, now); err != nil {
		t.Fatal(err)
	}
	superseded, err := s.CompleteReview(ctx, receiver, "stale", steps, now)
	if err != nil || superseded.Attempt.Status != "superseded" || superseded.ReviewApplicable || !reflect.DeepEqual(superseded.Attempt.HypothesisInventory, stale.Attempt.HypothesisInventory) {
		t.Fatal(superseded, err)
	}
	at = at.Add(31 * time.Second)
	expired, err := s.CompleteReview(ctx, receiver, "expiring", steps, now)
	if err != nil || expired.Attempt.Status != "expired" || expired.ReviewApplicable || !reflect.DeepEqual(expired.Attempt.HypothesisInventory, expiring.Attempt.HypothesisInventory) {
		t.Fatal(expired, err)
	}
	if _, err = s.PublishHypothesis(ctx, receiver, "expired-publication", "expiring", current, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal(err)
	}
	if repeated, err := s.PublishHypothesis(ctx, receiver, "reused", "reuse", current, noClock); err != nil || !reflect.DeepEqual(repeated, publication) {
		t.Fatal("stale exact retry lost receipt", err)
	}
	bad := clone(completed.Attempt)
	bad.HypothesisInventory.SHA256 = "changed"
	if _, err = review.Definition(bad); err == nil {
		t.Fatal("corrupt retained inventory accepted")
	}
	legacy := clone(completed.Attempt)
	legacy.Profile = nil
	if _, err = review.Definition(legacy); err == nil {
		t.Fatal("legacy attempt accepted inventory")
	}
}
