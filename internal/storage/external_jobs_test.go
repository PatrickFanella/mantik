package storage_test

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

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"github.com/jackc/pgx/v5"
)

type externalBackend interface {
	storage.Store
	storage.NewsStore
	storage.ReviewStore
	storage.HypothesisStore
	storage.TrialStore
	storage.TrialAttemptStore
	storage.TrialRunStore
	storage.ExternalJobStore
}

func TestMemoryExternalTrialJobs(t *testing.T) { externalContracts(t, storage.NewMemory(), "") }
func TestPostgresContractsExternalTrialJobs(t *testing.T) {
	s, config := externalPostgres(t, "externaljobs")
	externalContracts(t, s, config)
}
func externalSource(t *testing.T, s externalBackend) (trial.Registration, trial.Run, time.Time) {
	t.Helper()
	ctx := context.Background()
	r, at := externalRegistration(t, s)
	now := func() time.Time { return at }
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(d)
	if _, err = s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "native-source", TrialID: r.ID}, now); err != nil {
		t.Fatal(err)
	}
	w, err := s.BeginTrialRun(ctx, r.ReceiverID, "native-source", string(input), "source-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	n, err := experiment.Execute(ctx, w.Run.NativeAttemptID, r.Recipe, d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(n)
	run, err := s.FinishTrialRun(ctx, r.ReceiverID, "native-source", "source-owner", string(raw), now)
	if err != nil {
		t.Fatal(err)
	}
	return r, run, at
}
func externalCommand(r trial.Registration, run trial.Run, id string) workertrial.Command {
	return workertrial.Command{ReceiverID: r.ReceiverID, RequestID: id, AttemptID: run.AttemptID, NativeRunSHA256: run.SHA256, ImageSHA256: "sha256:" + strings.Repeat("a", 64), AdapterSHA256: strings.Repeat("b", 64), RequirementsSHA256: strings.Repeat("c", 64)}
}
func externalContracts(t *testing.T, s externalBackend, config string) {
	ctx := context.Background()
	r, run, at := externalSource(t, s)
	at = at.Add(time.Second)
	now := func() time.Time { return at }
	c := externalCommand(r, run, "declared")
	before, _ := s.Snapshot(ctx)
	var wg sync.WaitGroup
	results := make(chan workertrial.Record, 8)
	for range 8 {
		wg.Go(func() {
			v, err := s.DeclareExternalJob(ctx, c, now)
			if err != nil {
				t.Error(err)
				return
			}
			results <- v
		})
	}
	wg.Wait()
	close(results)
	var first workertrial.Record
	count := 0
	for v := range results {
		count++
		if v.Validate() != nil {
			t.Fatal("invalid receipt")
		}
		if first.SHA256 == "" {
			first = v
		} else if !reflect.DeepEqual(first, v) {
			t.Fatal("concurrent declaration diverged")
		}
	}
	if count != 8 || first.ExecutionStarted || first.HostStateKnown || first.QualificationCredit {
		t.Fatal(first, count)
	}
	if _, err := s.DeclareExternalJob(ctx, c, func() time.Time { panic("retry resampled clock") }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dataset.json", "recipe.json", "native.json"} {
		a, err := s.ExternalJobInput(ctx, c.RequestID, name)
		if err != nil || a.ID != c.RequestID {
			t.Fatal(name, err)
		}
		if _, err = workertrial.Input(first, name, a.Content); err != nil {
			t.Fatal(err)
		}
	}
	mutated := first
	mutated.Job.Recipe.Methods[0].ID = "changed"
	actual, err := s.ExternalJob(ctx, c.RequestID)
	if err != nil || actual.Job.Recipe.Methods[0].ID == "changed" {
		t.Fatal("returned declaration aliases store", err)
	}
	// Restore first after testing caller-owned mutation of nested slices.
	first = actual
	for _, change := range []func(*workertrial.Command){func(c *workertrial.Command) { c.ImageSHA256 = "sha256:" + strings.Repeat("d", 64) }, func(c *workertrial.Command) { c.AdapterSHA256 = strings.Repeat("d", 64) }, func(c *workertrial.Command) { c.NativeRunSHA256 = strings.Repeat("d", 64) }} {
		changed := c
		change(&changed)
		if _, err = s.DeclareExternalJob(ctx, changed, now); !errors.Is(err, trial.ErrConflict) {
			t.Fatal("identity rebound", err)
		}
	}
	foreign := c
	foreign.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err = s.DeclareExternalJob(ctx, foreign, now); !errors.Is(err, storage.ErrReceiverMismatch) {
		t.Fatal(err)
	}
	missing := c
	missing.RequestID = "missing"
	missing.AttemptID = "missing"
	if _, err = s.DeclareExternalJob(ctx, missing, now); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	badPin := c
	badPin.RequestID = "wrong-source"
	badPin.NativeRunSHA256 = strings.Repeat("d", 64)
	if _, err = s.DeclareExternalJob(ctx, badPin, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("wrong source receipt accepted", err)
	}
	backward := c
	backward.RequestID = "backward"
	if _, err = s.DeclareExternalJob(ctx, backward, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("backward clock accepted", err)
	}
	if _, err = s.ExternalJobInput(ctx, c.RequestID, "../native.json"); !errors.Is(err, trial.ErrInvalid) {
		t.Fatal(err)
	}
	if pg, ok := s.(*storage.Postgres); ok {
		connection, err := pgx.Connect(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
		if _, err = connection.Exec(ctx, "ALTER TABLE mantik_external_trial_job_inputs ADD CONSTRAINT reject_input CHECK(id!='rollback' OR reference!='native.json')"); err != nil {
			t.Fatal(err)
		}
		failed := c
		failed.RequestID = "rollback"
		if _, err = s.DeclareExternalJob(ctx, failed, now); err == nil {
			t.Fatal("failed input persistence acknowledged")
		}
		if _, err = s.ExternalJob(ctx, "rollback"); !errors.Is(err, trial.ErrNotFound) {
			t.Fatal("partial declaration survived", err)
		}
		var orphan int
		if err = connection.QueryRow(ctx, "SELECT count(*) FROM mantik_external_trial_job_inputs WHERE id='rollback'").Scan(&orphan); err != nil || orphan != 0 {
			t.Fatal("partial inputs survived", orphan, err)
		}
		if _, err = connection.Exec(ctx, "ALTER TABLE mantik_external_trial_job_inputs DROP CONSTRAINT reject_input"); err != nil {
			t.Fatal(err)
		}
		pg.Close()
		reopened, err := storage.OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		actual, err = s.ExternalJob(ctx, c.RequestID)
		if err != nil || !reflect.DeepEqual(actual, first) {
			t.Fatal("restart changed declaration", err)
		}
		for _, a := range first.Job.Inputs {
			input, err := s.ExternalJobInput(ctx, c.RequestID, a.Reference)
			if err != nil || input.SHA256 != a.SHA256 {
				t.Fatal("restart changed input", err)
			}
		}
	}
	// A declared worker cannot borrow a running or failed native reference.
	input, err := s.TrialRunArtifact(ctx, run.AttemptID, "input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "source-pending", TrialID: r.ID}, now); err != nil {
		t.Fatal(err)
	}
	pending, err := s.BeginTrialRun(ctx, r.ReceiverID, "source-pending", input.Content, "pending-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeclareExternalJob(ctx, externalCommand(r, pending.Run, "reject-running"), now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("running native source accepted", err)
	}
	failedRaw, _ := json.Marshal(map[string]any{"id": pending.Run.NativeAttemptID, "recipe_sha256": pending.Run.RecipeSHA256, "input_sha256": pending.Run.InputSHA256, "status": "failed", "error_code": "dataset_pin_mismatch", "evidence_class": "synthetic_unqualified", "qualified": false, "results": []any{}})
	failed, err := s.FinishTrialRun(ctx, r.ReceiverID, pending.Run.AttemptID, "pending-owner", string(failedRaw), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeclareExternalJob(ctx, externalCommand(r, failed, "reject-failed"), now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("failed native source accepted", err)
	}
	// Completed late native evidence remains comparable, with its deadline flag
	// retained rather than being silently promoted to timely execution.
	if _, err = s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "source-late", TrialID: r.ID}, now); err != nil {
		t.Fatal(err)
	}
	lateWork, err := s.BeginTrialRun(ctx, r.ReceiverID, "source-late", input.Content, "late-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := s.TrialRunArtifact(ctx, run.AttemptID, "result")
	if err != nil {
		t.Fatal(err)
	}
	var lateResult map[string]any
	_ = json.Unmarshal([]byte(reference.Content), &lateResult)
	lateResult["id"] = lateWork.Run.NativeAttemptID
	lateRaw, _ := json.Marshal(lateResult)
	at = at.Add(31 * time.Second)
	late, err := s.FinishTrialRun(ctx, r.ReceiverID, lateWork.Run.AttemptID, "late-owner", string(lateRaw), now)
	if err != nil || late.DeadlineMet {
		t.Fatal(late, err)
	}
	declaredLate, err := s.DeclareExternalJob(ctx, externalCommand(r, late, "late-declared"), now)
	if err != nil || declaredLate.NativeDeadlineMet || declaredLate.QualificationCredit {
		t.Fatal("late source history changed", declaredLate, err)
	}
	for i := 2; i < workertrial.MaxJobs; i++ {
		next := c
		next.RequestID = fmt.Sprintf("job-%d", i)
		if _, err = s.DeclareExternalJob(ctx, next, now); err != nil {
			t.Fatal(err)
		}
	}
	overflow := c
	overflow.RequestID = "overflow"
	if _, err = s.DeclareExternalJob(ctx, overflow, now); !errors.Is(err, trial.ErrCapacity) {
		t.Fatal("capacity evicted history", err)
	}
	if _, err = s.DeclareExternalJob(ctx, c, func() time.Time { panic("capacity retry clock") }); err != nil {
		t.Fatal(err)
	}
	full, err := s.(storage.ExternalHistoryStore).ExternalTrialHistory(ctx, r.ID)
	if err != nil || full.Validate() != nil || full.JobCount != workertrial.MaxJobs || full.UnattemptedJobs != workertrial.MaxJobs {
		t.Fatal("full job catalogue truncated", err)
	}
	after, _ := s.Snapshot(ctx)
	retainedRun, err := s.TrialRun(ctx, run.AttemptID)
	if err != nil || !reflect.DeepEqual(run, retainedRun) || !reflect.DeepEqual(before, after) {
		t.Fatal("declaration changed native execution or operations", err)
	}
}
func TestPostgresContractsExternalJobUpgrade(t *testing.T) {
	ctx := context.Background()
	s, config := externalPostgres(t, "externalupgrade")
	connection, err := pgx.Connect(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	r, run, at := externalSource(t, s)
	if _, err := connection.Exec(ctx, "ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DELETE FROM mantik_schema WHERE version>=17"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := storage.OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	retained, err := upgraded.TrialRun(ctx, run.AttemptID)
	if err != nil || !reflect.DeepEqual(retained, run) {
		t.Fatal("upgrade changed parent run", err)
	}
	if _, err = upgraded.DeclareExternalJob(ctx, externalCommand(r, run, "after-upgrade"), func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=17"); err != nil {
		t.Fatal(err)
	}
	if bad, err := storage.OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
