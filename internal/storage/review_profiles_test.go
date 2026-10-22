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
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

type profileBackend interface {
	reviewBackend
	ReviewProfileStore
	HypothesisStore
}

func TestMemoryRetainedReviewProfiles(t *testing.T) { profileContracts(t, NewMemory()) }
func TestPostgresContractsRetainedReviewProfiles(t *testing.T) {
	profileContracts(t, timerPostgres(t, "reviewprofiles"))
}
func profileSteps(key string, d hypothesis.Declaration) []json.RawMessage {
	steps := []investigation.Step{{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: key}}}
	for _, ref := range d.Methods {
		steps = append(steps, investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_native_method", MethodID: ref.ID, MethodSHA256: ref.SHA256}})
	}
	steps = append(steps, investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", ReasonCodes: []string{"synthetic_method_candidate"}, ClaimKeys: []string{key}, Uncertainty: "unverified", Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{key}, Methods: d.Methods}}})
	raw := []json.RawMessage{}
	for _, step := range steps {
		b, _ := json.Marshal(step)
		raw = append(raw, b)
	}
	return raw
}
func profileContracts(t *testing.T, s profileBackend) {
	t.Helper()
	ctx := context.Background()
	source := news.Fixture()
	at := source.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("retry/unknown profile sampled clock") }
	catalog, _ := s.NewsCases(ctx)
	receiver := catalog.ReceiverID
	receipt, err := s.AdmitNews(ctx, receiver, source, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := receipt.Matches[0].CaseID
	if _, err = s.StartReviewProfile(ctx, receiver, "unknown", caseID, 1, "unknown", noClock); !errors.Is(err, review.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, "unknown"); !errors.Is(err, review.ErrNotFound) {
		t.Fatal("unknown profile left attempt", err)
	}
	// Concurrent equal starts and completions retain one application profile.
	var wg sync.WaitGroup
	starts := make(chan review.View, 8)
	for range 8 {
		wg.Go(func() {
			v, err := s.StartReviewProfile(ctx, receiver, "method", caseID, 1, investigation.MethodTypeID, now)
			if err != nil {
				t.Error(err)
				return
			}
			starts <- v
		})
	}
	wg.Wait()
	close(starts)
	var first review.View
	for v := range starts {
		if first.Attempt.ID == "" {
			first = v
		} else if !reflect.DeepEqual(first, v) {
			t.Fatal("start diverged")
		}
	}
	if first.Attempt.Profile == nil || first.Attempt.Profile.Validate() != nil || first.Attempt.Profile.Contract.TypeID != investigation.MethodTypeID || len(first.Attempt.Profile.Methods) != 2 {
		t.Fatal(first)
	}
	d := hypothesis.Fixture()
	claim := first.Attempt.Pin.Group.Claims[0]
	d.Origins = []hypothesis.Origin{{Reference: claim.Reference, ClaimKey: claim.Claim.Key, RecordingSHA256: claim.Claim.RecordingSHA256}}
	steps := profileSteps(claim.Claim.Key, d)
	completions := make(chan review.View, 8)
	for range 8 {
		wg.Go(func() {
			v, err := s.CompleteReview(ctx, receiver, "method", steps, now)
			if err != nil {
				t.Error(err)
				return
			}
			completions <- v
		})
	}
	wg.Wait()
	close(completions)
	var complete review.View
	for v := range completions {
		if complete.Attempt.ID == "" {
			complete = v
		} else if !reflect.DeepEqual(complete, v) {
			t.Fatal("completion diverged")
		}
	}
	if !complete.ReviewApplicable || complete.Attempt.Result.Schema != investigation.MethodSchema || complete.Attempt.Result.ProfileSHA256 != first.Attempt.Profile.SHA256 || len(complete.Attempt.Result.Checks) != 6 {
		t.Fatal(complete)
	}
	if v, err := s.StartReviewProfile(ctx, receiver, "method", caseID, 1, investigation.MethodTypeID, noClock); err != nil || !reflect.DeepEqual(v, complete) {
		t.Fatal("retry changed profile", err)
	}
	if _, err = s.StartReview(ctx, receiver, "method", caseID, 1, noClock); !errors.Is(err, review.ErrConflict) {
		t.Fatal("default rebound method profile", err)
	}
	if v, err := s.CompleteReview(ctx, receiver, "method", steps, noClock); err != nil || !reflect.DeepEqual(v, complete) {
		t.Fatal(err)
	}
	publication, err := s.PublishHypothesis(ctx, receiver, "published", "method", d, now)
	if err != nil || publication.Sealed.QualificationCredit {
		t.Fatal(publication, err)
	}
	changed := d
	half, _, _ := hypothesis.PinMethod(library.HalfSizeID)
	changed.Methods = []hypothesis.MethodRef{half}
	if _, err = s.PublishHypothesis(ctx, receiver, "changed-methods", "method", changed, now); !errors.Is(err, hypothesis.ErrConflict) {
		t.Fatal("publication changed reviewed method set", err)
	}
	// An incomplete method investigation remains an audited abstention.
	if _, err = s.StartReviewProfile(ctx, receiver, "uninspected", caseID, 1, investigation.MethodTypeID, now); err != nil {
		t.Fatal(err)
	}
	missing := []json.RawMessage{steps[0], steps[len(steps)-1]}
	v, err := s.CompleteReview(ctx, receiver, "uninspected", missing, now)
	if err != nil || v.Attempt.Result.ReasonCode != "method_not_eligible" || len(v.Attempt.Steps) != 2 {
		t.Fatal(v, err)
	}
	// Recreate actual schema18-style pending records without the new field.
	for _, id := range []string{"legacy-plain", "legacy-reject"} {
		if _, err = s.StartReview(ctx, receiver, id, caseID, 1, now); err != nil {
			t.Fatal(err)
		}
		switch backend := s.(type) {
		case *Memory:
			backend.mu.Lock()
			a := backend.reviews[id]
			a.Profile = nil
			backend.reviews[id] = a
			backend.mu.Unlock()
		case *Postgres:
			if _, err = backend.pool.Exec(ctx, "UPDATE mantik_news_reviews SET payload=payload-'profile' WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacy, err := s.CompleteReview(ctx, receiver, "legacy-plain", reviewSteps(claim.Claim.Key), now)
	if err != nil || legacy.Attempt.Profile != nil || legacy.Attempt.Result.Schema != investigation.Schema || legacy.Attempt.Result.ProfileSHA256 != "" || len(legacy.Attempt.Result.Checks) != 5 {
		t.Fatal(legacy, err)
	}
	rejected, err := s.CompleteReview(ctx, receiver, "legacy-reject", steps, now)
	if err != nil || rejected.Attempt.Result.ReasonCode != "malformed_model_artifact" || rejected.Attempt.Result.TypeID != investigation.TypeID {
		t.Fatal(rejected, err)
	}
	pending, err := s.StartReviewProfile(ctx, receiver, "pending-method", caseID, 1, investigation.MethodTypeID, now)
	if err != nil {
		t.Fatal(err)
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
		for id, want := range map[string]review.View{"method": complete, "legacy-plain": legacy, "pending-method": pending} {
			actual, err := s.Review(ctx, id)
			if err != nil || !reflect.DeepEqual(actual, want) {
				t.Fatal("restart changed profile/result", id, err)
			}
		}
		if _, err = s.StartReviewProfile(ctx, receiver, "rollback", caseID, 1, investigation.MethodTypeID, now); err != nil {
			t.Fatal(err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews ADD CONSTRAINT reject_profile_finish CHECK(id!='rollback' OR payload->>'status'='pending')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CompleteReview(ctx, receiver, "rollback", steps, now); err == nil {
			t.Fatal("failed write acknowledged")
		}
		actual, err := s.Review(ctx, "rollback")
		if err != nil || actual.Attempt.Status != "pending" || actual.Attempt.Result != nil || actual.Attempt.Profile.SHA256 != first.Attempt.Profile.SHA256 {
			t.Fatal("partial finish or lost profile", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews DROP CONSTRAINT reject_profile_finish"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CloseNewsCase(ctx, receiver, caseID, 1, now); err != nil {
		t.Fatal(err)
	}
	stale, err := s.CompleteReview(ctx, receiver, "pending-method", steps, now)
	if err != nil || stale.Attempt.Status != "superseded" || stale.ReviewApplicable || stale.Attempt.Result.ProfileSHA256 != pending.Attempt.Profile.SHA256 {
		t.Fatal(stale, err)
	}
	// Reopening evidence and finishing at the deadline keeps an expired result.
	copy := source
	copy.SourceID = "second-source"
	if _, err = s.AdmitNews(ctx, receiver, copy, now); err != nil {
		t.Fatal(err)
	}
	exp, err := s.StartReviewProfile(ctx, receiver, "expired-method", caseID, 2, investigation.MethodTypeID, now)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(30 * time.Second)
	expired, err := s.CompleteReview(ctx, receiver, "expired-method", steps, now)
	if err != nil || expired.Attempt.Status != "expired" || expired.ReviewApplicable || expired.Attempt.Profile.SHA256 != exp.Attempt.Profile.SHA256 {
		t.Fatal(expired, err)
	}
	for n := 0; n < review.MaxAttempts; n++ {
		_, err = s.StartReviewProfile(ctx, receiver, fmt.Sprintf("capacity-%d", n), caseID, 2, investigation.MethodTypeID, now)
		if errors.Is(err, review.ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.StartReviewProfile(ctx, receiver, "overflow", caseID, 2, investigation.MethodTypeID, now); !errors.Is(err, review.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.StartReviewProfile(ctx, receiver, "method", caseID, 1, investigation.MethodTypeID, noClock); err != nil {
		t.Fatal("capacity blocked exact profile retry", err)
	}
	if _, err = s.StartReviewProfile(ctx, receiver, "overflow-reuse", caseID, 2, investigation.ReuseTypeID, noClock); !errors.Is(err, review.ErrCapacity) {
		t.Fatal("reuse bypassed shared capacity", err)
	}

}
func TestPostgresContractsReviewProfileUpgrade(t *testing.T) {
	s := timerPostgres(t, "profileupgrade")
	ctx := context.Background()
	source := news.Fixture()
	now := func() time.Time { return source.ReceivedAt.Add(time.Hour) }
	catalog, _ := s.NewsCases(ctx)
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, source, now)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.StartReview(ctx, catalog.ReceiverID, "legacy-pending", admitted.Matches[0].CaseID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; UPDATE mantik_news_reviews SET payload=payload-'profile'; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DELETE FROM mantik_schema WHERE version>=19"); err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	actual, err := upgraded.Review(ctx, "legacy-pending")
	pending.Attempt.Profile = nil
	if err != nil || !reflect.DeepEqual(actual, pending) {
		t.Fatal("upgrade rewrote legacy pin", err)
	}
	result, err := upgraded.CompleteReview(ctx, catalog.ReceiverID, "legacy-pending", reviewSteps(pending.Attempt.Pin.Group.Claims[0].Claim.Key), now)
	if err != nil || result.Attempt.Result.TypeID != investigation.TypeID || result.Attempt.Result.ProfileSHA256 != "" {
		t.Fatal(result, err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=19"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed profile migration admitted")
	}
}
