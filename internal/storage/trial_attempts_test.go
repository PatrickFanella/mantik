package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type attemptBackend interface {
	trialBackend
	TrialAttemptStore
}

func attemptFixture(t *testing.T, s trialBackend) (trial.Registration, time.Time) {
	t.Helper()
	ctx := context.Background()
	source := news.Fixture()
	at := source.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	catalog, _ := s.NewsCases(ctx)
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, source, now)
	if err != nil {
		t.Fatal(err)
	}
	d := candidateDeclaration(t, s, catalog.ReceiverID, "review", admitted.Matches[0].CaseID, 1, now)
	p, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "publication", "review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RegisterTrial(ctx, trial.Command{ReceiverID: catalog.ReceiverID, RequestID: "trial", PublicationID: p.ID, Recipe: trialPlan(t)}, now)
	if err != nil {
		t.Fatal(err)
	}
	return r, at
}
func TestMemoryTrialAttemptIntents(t *testing.T) { attemptContracts(t, NewMemory()) }
func TestPostgresContractsTrialAttemptIntents(t *testing.T) {
	attemptContracts(t, timerPostgres(t, "attemptintents"))
}
func attemptContracts(t *testing.T, s attemptBackend) {
	ctx := context.Background()
	r, at := attemptFixture(t, s)
	now := func() time.Time { return at }
	c := trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "first", TrialID: r.ID}
	before, _ := s.Snapshot(ctx)
	casesBefore, _ := s.NewsCases(ctx)
	registrationBefore, _ := s.Trial(ctx, r.ID)
	h, err := s.TrialAttempts(ctx, r.ID)
	if err != nil || len(h.Attempts) != 0 {
		t.Fatal(h, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			a, err := s.StartTrialAttempt(ctx, c, now)
			if err != nil || a.Validate() != nil || a.Status != "pending" || a.ExecutionStarted {
				t.Error(a, err)
			}
		})
	}
	wg.Wait()
	first, err := s.TrialAttempt(ctx, c.RequestID)
	if err != nil || first.RegistrationSHA256 != r.SHA256 || first.RecipeSHA256 != r.Recipe.SHA256 || first.DatasetSHA256 != r.Recipe.DatasetSHA256 {
		t.Fatal(first, err)
	}
	retry, err := s.StartTrialAttempt(ctx, c, func() time.Time { panic("retry clock") })
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("start retry changed intent", err)
	}
	changed := c
	changed.TrialID = "other"
	if _, err = s.StartTrialAttempt(ctx, changed, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("attempt rebound", err)
	}
	wrong := c
	wrong.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err = s.StartTrialAttempt(ctx, wrong, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	if _, err = s.CancelTrialAttempt(ctx, wrong.ReceiverID, c.RequestID, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	if _, err = s.CancelTrialAttempt(ctx, c.ReceiverID, c.RequestID, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("backdated cancellation", err)
	}
	at = at.Add(time.Second)
	for range 8 {
		wg.Go(func() {
			a, err := s.CancelTrialAttempt(ctx, c.ReceiverID, c.RequestID, now)
			if err != nil || a.Status != "cancelled" || a.IntentSHA256 != first.IntentSHA256 || a.ExecutionStarted {
				t.Error(a, err)
			}
		})
	}
	wg.Wait()
	cancelled, err := s.TrialAttempt(ctx, c.RequestID)
	if err != nil || cancelled.Validate() != nil || cancelled.ClosedAt != at {
		t.Fatal(cancelled, err)
	}
	if _, err = s.CancelTrialAttempt(ctx, c.ReceiverID, c.RequestID, func() time.Time { panic("cancel retry clock") }); err != nil {
		t.Fatal(err)
	}
	retry, err = s.StartTrialAttempt(ctx, c, func() time.Time { panic("cancelled start retry clock") })
	if err != nil || !reflect.DeepEqual(retry, cancelled) {
		t.Fatal("cancelled intent resurrected", err)
	}
	pending := c
	pending.RequestID = "pending"
	pendingRecord, err := s.StartTrialAttempt(ctx, pending, now)
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
		actual, err := s.TrialAttempt(ctx, "pending")
		if err != nil || !reflect.DeepEqual(actual, pendingRecord) {
			t.Fatal("restart changed pending intent", err)
		}
		actual, err = s.TrialAttempt(ctx, "first")
		if err != nil || !reflect.DeepEqual(actual, cancelled) {
			t.Fatal("restart changed terminal intent", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_trial_attempts ADD CONSTRAINT reject_cancel CHECK(id!='pending' OR payload->>'status'='pending')"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.CancelTrialAttempt(ctx, c.ReceiverID, "pending", now); err == nil {
			t.Fatal("failed cancellation acknowledged")
		}
		actual, err = s.TrialAttempt(ctx, "pending")
		if err != nil || !reflect.DeepEqual(actual, pendingRecord) {
			t.Fatal("partial cancellation retained", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_trial_attempts DROP CONSTRAINT reject_cancel; ALTER TABLE mantik_trial_attempts ADD CONSTRAINT reject_start CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		failed := c
		failed.RequestID = "rollback"
		if _, err = s.StartTrialAttempt(ctx, failed, now); err == nil {
			t.Fatal("failed start acknowledged")
		}
		if _, err = s.TrialAttempt(ctx, "rollback"); !errors.Is(err, trial.ErrNotFound) {
			t.Fatal("partial start", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_trial_attempts DROP CONSTRAINT reject_start"); err != nil {
			t.Fatal(err)
		}
	}
	for n := 2; n < trial.MaxAttempts; n++ {
		copy := c
		copy.RequestID = fmt.Sprintf("intent-%d", n)
		if _, err = s.StartTrialAttempt(ctx, copy, now); err != nil {
			t.Fatal(err)
		}
	}
	overflow := c
	overflow.RequestID = "overflow"
	if _, err = s.StartTrialAttempt(ctx, overflow, now); !errors.Is(err, trial.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.StartTrialAttempt(ctx, c, now); err != nil {
		t.Fatal("capacity blocked retry", err)
	}
	if _, err = s.CancelTrialAttempt(ctx, c.ReceiverID, "pending", now); err != nil {
		t.Fatal("capacity blocked cancellation", err)
	}
	h, err = s.TrialAttempts(ctx, r.ID)
	if err != nil || len(h.Attempts) != 32 {
		t.Fatal(h, err)
	}
	after, _ := s.Snapshot(ctx)
	casesAfter, _ := s.NewsCases(ctx)
	registrationAfter, _ := s.Trial(ctx, r.ID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(casesBefore, casesAfter) || !reflect.DeepEqual(registrationBefore, registrationAfter) {
		t.Fatal("attempt intent changed execution, case or registration")
	}
	if _, err = s.TrialAttempts(ctx, "unknown"); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
}
func TestPostgresContractsAttemptIntentUpgrade(t *testing.T) {
	s := timerPostgres(t, "attemptupgrade")
	r, _ := attemptFixture(t, s)
	ctx := context.Background()
	config := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DELETE FROM mantik_schema WHERE version>=15"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	actual, err := upgraded.Trial(ctx, r.ID)
	if err != nil || !reflect.DeepEqual(actual, r) {
		t.Fatal("upgrade changed trial", err)
	}
	h, err := upgraded.TrialAttempts(ctx, r.ID)
	if err != nil || len(h.Attempts) != 0 {
		t.Fatal(err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=15"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("checksum tamper admitted")
	}
}
