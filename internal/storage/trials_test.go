package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type trialBackend interface {
	hypothesisBackend
	TrialStore
}

func TestMemoryTrialRegistration(t *testing.T) { trialContracts(t, NewMemory()) }
func TestPostgresContractsTrialRegistration(t *testing.T) {
	trialContracts(t, timerPostgres(t, "trials"))
}
func trialPlan(t *testing.T) experimentplan.Recipe {
	t.Helper()
	catalog, err := experimentplan.BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	r, err := experimentplan.Seal(experimentplan.Recipe{Schema: experimentplan.RecipeSchema, ID: "declared-trial", Version: "v1", DatasetID: "declared-data", DatasetVersion: "v1", DatasetSHA256: strings.Repeat("a", 64), WindowStart: at, WindowEnd: at.Add(time.Hour), Cutoffs: []time.Time{at, at.Add(time.Hour)}, RequiredKeys: []string{"first", "last"}, Methods: catalog.Methods[:1], Benchmarks: catalog.Methods[2:], CapitalCents: 100000, CostModel: experimentplan.CostModel, PlannedMetrics: experimentplan.Metrics()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func trialContracts(t *testing.T, s trialBackend) {
	ctx := context.Background()
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	catalog, _ := s.NewsCases(ctx)
	receiver := catalog.ReceiverID
	admitted, err := s.AdmitNews(ctx, receiver, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := admitted.Matches[0].CaseID
	d := candidateDeclaration(t, s, receiver, "review", caseID, 1, now)
	publication, err := s.PublishHypothesis(ctx, receiver, "publication", "review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.HypothesisTrials(ctx, publication.Sealed.ID)
	if err != nil || len(h.Registrations) != 0 {
		t.Fatal("empty question history", h, err)
	}
	before, _ := s.Snapshot(ctx)
	casesBefore, _ := s.NewsCases(ctx)
	reviewsBefore, _ := s.Review(ctx, "review")
	pubsBefore, _ := s.Hypothesis(ctx, publication.Sealed.ID)
	c := trial.Command{ReceiverID: receiver, RequestID: "root", PublicationID: publication.ID, Recipe: trialPlan(t)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			registered, err := s.RegisterTrial(ctx, c, now)
			if err != nil || registered.Validate() != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	first, err := s.Trial(ctx, "root")
	if err != nil || first.PublicationSHA256 != trial.Hash(publication) || first.DeclarationSHA256 != publication.Sealed.DeclarationSHA256 || first.DatasetAvailabilityVerified || first.QualificationCredit {
		t.Fatal(first, err)
	}
	repeated, err := s.RegisterTrial(ctx, c, func() time.Time { panic("retry consulted clock") })
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatal("retry changed receipt", err)
	}
	for _, name := range []string{"recipe", "publication", "parent", "receiver"} {
		t.Run(name, func(t *testing.T) {
			changed := clone(c)
			switch name {
			case "recipe":
				changed.Recipe.DatasetID = "another-data"
				changed.Recipe, _ = experimentplan.Seal(changed.Recipe)
			case "publication":
				changed.PublicationID = "other"
			case "parent":
				changed.ParentID = "unknown"
			case "receiver":
				changed.ReceiverID = "receiver-abcdefghijklmnop"
			}
			_, err := s.RegisterTrial(ctx, changed, now)
			if name == "receiver" {
				if !errors.Is(err, ErrReceiverMismatch) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, trial.ErrConflict) {
				t.Fatal("request rebound", err)
			}
		})
	}
	missing := clone(c)
	missing.RequestID = "missing"
	missing.PublicationID = "missing"
	if _, err = s.RegisterTrial(ctx, missing, now); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	child := clone(c)
	child.RequestID = "child"
	child.ParentID = "root"
	child.Recipe.DatasetID = "next-data"
	child.Recipe, _ = experimentplan.Seal(child.Recipe)
	second, err := s.RegisterTrial(ctx, child, now)
	if err != nil || second.ParentSHA256 != first.SHA256 {
		t.Fatal("parent not bound", second, err)
	}
	// Closing a decision case does not delete or adjudicate a research hypothesis.
	if _, err = s.CloseNewsCase(ctx, receiver, caseID, 1, now); err != nil {
		t.Fatal(err)
	}
	historical := clone(c)
	historical.RequestID = "historical"
	if _, err = s.RegisterTrial(ctx, historical, now); err != nil {
		t.Fatal("case closure changed research validity", err)
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
		actual, err := s.Trial(ctx, "child")
		if err != nil || !reflect.DeepEqual(actual, second) {
			t.Fatal("restart changed trial", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_hypothesis_trials ADD CONSTRAINT reject_trial CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		failed := clone(c)
		failed.RequestID = "rollback"
		if _, err = s.RegisterTrial(ctx, failed, now); err == nil {
			t.Fatal("failed persistence acknowledged")
		}
		if _, err = s.Trial(ctx, "rollback"); !errors.Is(err, trial.ErrNotFound) {
			t.Fatal("partial trial retained", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_hypothesis_trials DROP CONSTRAINT reject_trial"); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("trial registration changed execution state")
	}
	casesAfter, _ := s.NewsCases(ctx)
	if len(casesAfter.Cases) != len(casesBefore.Cases) || casesAfter.Cases[0].Status != "closed" || casesAfter.Cases[0].Version != casesBefore.Cases[0].Version {
		t.Fatal("trial changed case evidence")
	}
	reviewsAfter, _ := s.Review(ctx, "review")
	pubsAfter, _ := s.Hypothesis(ctx, publication.Sealed.ID)
	if !reflect.DeepEqual(reviewsBefore.Attempt, reviewsAfter.Attempt) || !reflect.DeepEqual(pubsBefore, pubsAfter) {
		t.Fatal("trial rewrote provenance")
	}
	for n := 3; n < trial.MaxRegistrations; n++ {
		copy := clone(c)
		copy.RequestID = fmt.Sprintf("fill-%d", n)
		copy.ParentID = "child"
		if _, err = s.RegisterTrial(ctx, copy, now); err != nil {
			t.Fatal(err)
		}
	}
	overflow := clone(c)
	overflow.RequestID = "overflow"
	if _, err = s.RegisterTrial(ctx, overflow, now); !errors.Is(err, trial.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.RegisterTrial(ctx, c, now); err != nil {
		t.Fatal("capacity blocked retry", err)
	}
	h, err = s.HypothesisTrials(ctx, publication.Sealed.ID)
	if err != nil || len(h.Registrations) != 32 || h.QualificationCredit {
		t.Fatal(h, err)
	}
	first.Recipe.RequiredKeys[0] = "tampered"
	actual, err := s.Trial(ctx, "root")
	if err != nil || actual.Recipe.RequiredKeys[0] == "tampered" {
		t.Fatal("caller mutated plan", err)
	}
}
func TestPostgresContractsTrialUpgrade(t *testing.T) {
	s := timerPostgres(t, "trialupgrade")
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	d := candidateDeclaration(t, s, catalog.ReceiverID, "review", admitted.Matches[0].CaseID, 1, now)
	publication, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "publication", "review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Hypothesis(ctx, publication.Sealed.ID)
	reviewBefore, _ := s.Review(ctx, "review")
	config := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DELETE FROM mantik_schema WHERE version>=14"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	after, err := upgraded.Hypothesis(ctx, publication.Sealed.ID)
	reviewAfter, reviewErr := upgraded.Review(ctx, "review")
	if err != nil || reviewErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(reviewBefore, reviewAfter) {
		t.Fatal("upgrade changed provenance", err, reviewErr)
	}
	h, err := upgraded.HypothesisTrials(ctx, publication.Sealed.ID)
	if err != nil || len(h.Registrations) != 0 {
		t.Fatal(err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=14"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
