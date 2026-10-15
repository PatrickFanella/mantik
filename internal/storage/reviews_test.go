package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

type reviewBackend interface {
	Store
	NewsStore
	ReviewStore
}

func TestMemoryReviewAttempts(t *testing.T) { reviewContracts(t, NewMemory()) }
func TestPostgresContractsReviewAttempts(t *testing.T) {
	reviewContracts(t, timerPostgres(t, "reviews"))
}
func reviewSteps(key string) []json.RawMessage {
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "request_evidence", ReasonCodes: []string{"independence_not_assessed"}, ClaimKeys: []string{key}, Uncertainty: "unverified"}})
	return []json.RawMessage{tool, proposal}
}
func reviewContracts(t *testing.T, s reviewBackend) {
	ctx := context.Background()
	r := news.Fixture()
	r.Original += "\nUNSELECTED REVIEW SOURCE"
	r.OriginalSHA256 = news.SHA256(r.Original)
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := catalog.ReceiverID
	receipt, err := s.AdmitNews(ctx, receiver, r, now)
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Matches[0].CaseID
	if _, err = s.StartReview(ctx, "receiver-abcdefghijklmnop", "wrong", id, 1, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	started, err := s.StartReview(ctx, receiver, "current", id, 1, now)
	if err != nil || started.Attempt.Status != "pending" {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(started)
	if strings.Contains(string(raw), "UNSELECTED REVIEW SOURCE") {
		t.Fatal("pin retained full original")
	}
	steps := reviewSteps(started.Attempt.Pin.Group.Claims[0].Claim.Key)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			view, err := s.CompleteReview(ctx, receiver, "current", steps, now)
			if err != nil || !view.ReviewApplicable || view.Attempt.Result == nil {
				t.Error(view, err)
			}
		})
	}
	wg.Wait()
	complete, err := s.Review(ctx, "current")
	if err != nil || complete.Attempt.Status != "completed" || !complete.ReviewApplicable {
		t.Fatal(complete, err)
	}
	repeated, err := s.CompleteReview(ctx, receiver, "current", steps, now)
	if err != nil || !reflect.DeepEqual(repeated, complete) {
		t.Fatal("retry changed completion", err)
	}
	if _, err = s.CompleteReview(ctx, receiver, "current", []json.RawMessage{json.RawMessage(`{"kind":"proposal","narrative":"changed"}`)}, now); !errors.Is(err, review.ErrConflict) {
		t.Fatal("changed completion overwritten", err)
	}
	repeated, err = s.StartReview(ctx, receiver, "current", id, 1, now)
	if err != nil || !reflect.DeepEqual(repeated, complete) {
		t.Fatal("start retry invented a new attempt", err)
	}
	if _, err = s.StartReview(ctx, receiver, "current", id, 2, now); !errors.Is(err, review.ErrConflict) {
		t.Fatal("request identity rebound", err)
	}
	stale, err := s.StartReview(ctx, receiver, "stale", id, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	copy := r
	copy.SourceID = "distribution"
	if _, err = s.AdmitNews(ctx, receiver, copy, now); err != nil {
		t.Fatal(err)
	}
	outdated, err := s.CompleteReview(ctx, receiver, "stale", steps, now)
	if err != nil || outdated.Attempt.Status != "superseded" || outdated.ReviewApplicable || outdated.EvidenceCurrent || outdated.Attempt.Result == nil || outdated.Attempt.Pin.Subject.SnapshotSHA256 != stale.Attempt.Pin.Subject.SnapshotSHA256 {
		t.Fatal("stale result lost or applied", outdated, err)
	}
	old, err := s.Review(ctx, "current")
	if err != nil || old.ReviewApplicable || old.EvidenceCurrent || !reflect.DeepEqual(old.Attempt, complete.Attempt) {
		t.Fatal("readback ignored newer evidence or rewrote history", err)
	}
	if _, err = s.StartReview(ctx, receiver, "closed", id, 2, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseNewsCase(ctx, receiver, id, 2, now); err != nil {
		t.Fatal(err)
	}
	closed, err := s.CompleteReview(ctx, receiver, "closed", steps, now)
	if err != nil || closed.Attempt.Status != "superseded" || closed.ReviewApplicable {
		t.Fatal("closed case consumed review", err)
	}
	copy.SourceID = "another-source"
	if _, err = s.AdmitNews(ctx, receiver, copy, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartReview(ctx, receiver, "expired", id, 3, now); err != nil {
		t.Fatal(err)
	}
	at = at.Add(30 * time.Second)
	expired, err := s.CompleteReview(ctx, receiver, "expired", steps, now)
	if err != nil || expired.Attempt.Status != "expired" || expired.ReviewApplicable || expired.Attempt.Result == nil {
		t.Fatal("deadline completion applied or lost output", err)
	}
	pending, err := s.StartReview(ctx, receiver, "pending", id, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	newsBefore, err := s.NewsCases(ctx)
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
		actual, err := s.Review(ctx, "pending")
		if err != nil || !reflect.DeepEqual(actual, pending) {
			t.Fatal("restart replaced pending pin/deadline", err)
		}
		actual, err = s.Review(ctx, "current")
		if err != nil || !reflect.DeepEqual(actual, old) {
			t.Fatal("restart changed completed attempt", err)
		}
		if _, err = s.StartReview(ctx, receiver, "rollback", id, 3, now); err != nil {
			t.Fatal(err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews ADD CONSTRAINT reject_review_finish CHECK(id!='rollback' OR payload->>'status'='pending')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CompleteReview(ctx, receiver, "rollback", steps, now); err == nil {
			t.Fatal("failed review persistence acknowledged")
		}
		actual, err = s.Review(ctx, "rollback")
		if err != nil || actual.Attempt.Status != "pending" || actual.Attempt.Result != nil || actual.Attempt.StepsSHA256 != "" {
			t.Fatal("partial completion write", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_reviews DROP CONSTRAINT reject_review_finish"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CompleteReview(ctx, receiver, "pending", steps, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartReview(ctx, receiver, "raw-artifact", id, 3, now); err != nil {
		t.Fatal(err)
	}
	malformed := []json.RawMessage{json.RawMessage(`{"kind":"proposal","kind":"tool","tool":{"name":"inspect_news_claim","claim_key":"x"}}`)}
	if _, err = s.CompleteReview(ctx, receiver, "raw-artifact", malformed, now); err != nil {
		t.Fatal(err)
	}
	rawView, err := s.Review(ctx, "raw-artifact")
	if err != nil || strings.Count(rawView.Attempt.Steps[0], `"kind"`) != 2 || rawView.Attempt.Result.ReasonCode != "malformed_model_artifact" {
		t.Fatal("raw invalid artifact normalized away", err)
	}
	newsAfter, err := s.NewsCases(ctx)
	if err != nil || !reflect.DeepEqual(newsBefore, newsAfter) {
		t.Fatal("review changed cases", err)
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("review changed operations or fixture state", err)
	}
	pending.Attempt.Pin.Group.Claims[0].Claim.Assertion = "tampered"
	actual, err := s.Review(ctx, "pending")
	if err != nil || actual.Attempt.Pin.Group.Claims[0].Claim.Assertion == "tampered" {
		t.Fatal("caller mutated pin", err)
	}
}
func TestMemoryReviewCapacityAndSnapshotBounds(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Matches[0].CaseID
	for n := range review.MaxAttempts {
		if _, err = s.StartReview(ctx, catalog.ReceiverID, fmt.Sprintf("attempt-%d", n), id, 1, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.StartReview(ctx, catalog.ReceiverID, "overflow", id, 1, now); !errors.Is(err, review.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.StartReview(ctx, catalog.ReceiverID, "attempt-0", id, 1, now); err != nil {
		t.Fatal("capacity blocked retry", err)
	}
}
func TestPostgresContractsReviewUpgrade(t *testing.T) {
	s := timerPostgres(t, "reviewupgrade")
	ctx := context.Background()
	before, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DELETE FROM mantik_schema WHERE version>=12"); err != nil {
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
		t.Fatal("migration changed news or receiver", err)
	}
	var count int
	if err = upgraded.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_news_reviews").Scan(&count); err != nil || count != 0 {
		t.Fatal(err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=12"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("migration tamper admitted")
	}
}

func TestMemoryReviewSnapshotClaimBound(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	for n := range 33 {
		copy := r
		copy.SourceID = fmt.Sprintf("source-%d", n)
		if _, err := s.AdmitNews(ctx, catalog.ReceiverID, copy, now); err != nil {
			t.Fatal(err)
		}
	}
	id := newscase.CaseID(r.EvidenceClass, r.Claims[0].EventKey)
	if _, err := s.StartReview(ctx, catalog.ReceiverID, "too-broad", id, 33, now); !errors.Is(err, newscase.ErrCapacity) {
		t.Fatal("unbounded evidence captured", err)
	}
	if _, err := s.Review(ctx, "too-broad"); !errors.Is(err, review.ErrNotFound) {
		t.Fatal("partial broad attempt retained", err)
	}
}
