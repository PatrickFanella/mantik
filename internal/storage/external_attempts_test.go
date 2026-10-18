package storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
	"github.com/jackc/pgx/v5"
)

type reportedBackend interface {
	externalBackend
	storage.ExternalAttemptStore
}

func TestMemoryExternalAttemptHistory(t *testing.T) { reportedContracts(t, storage.NewMemory(), "") }
func TestPostgresContractsExternalAttemptHistory(t *testing.T) {
	s, config := externalPostgres(t, "externalattempts")
	reportedContracts(t, s, config)
}
func reportedContracts(t *testing.T, s reportedBackend, config string) {
	ctx := context.Background()
	r, run, at := externalSource(t, s)
	now := func() time.Time { return at }
	job, err := s.DeclareExternalJob(ctx, externalCommand(r, run, "external-job"), now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	command := func(id string) workertrial.AttemptCommand {
		return workertrial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: id, JobID: job.Command.RequestID, DeclarationSHA256: job.SHA256}
	}
	var wg sync.WaitGroup
	intents := make(chan workertrial.Intent, 8)
	for range 8 {
		wg.Go(func() {
			i, err := s.StartExternalAttempt(ctx, command("same"), now)
			if err != nil {
				t.Error(err)
				return
			}
			intents <- i
		})
	}
	wg.Wait()
	close(intents)
	var first workertrial.Intent
	count := 0
	for i := range intents {
		count++
		if i.Validate() != nil {
			t.Fatal(i)
		}
		if first.SHA256 == "" {
			first = i
		} else if !reflect.DeepEqual(first, i) {
			t.Fatal("concurrent start differs")
		}
	}
	if count != 8 {
		t.Fatal(count)
	}
	noClock := func() time.Time { panic("retry sampled clock") }
	if _, err = s.StartExternalAttempt(ctx, command("same"), noClock); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ExternalAttempt(ctx, "same")
	if err != nil || pending.Outcome != nil {
		t.Fatal(pending, err)
	}
	if _, err = s.ExternalAttemptOutput(ctx, "same", "stdout"); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("pending invented output", err)
	}
	changed := command("same")
	changed.DeclarationSHA256 = strings.Repeat("d", 64)
	if _, err = s.StartExternalAttempt(ctx, changed, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal(err)
	}
	missing := command("missing")
	missing.JobID = "missing"
	if _, err = s.StartExternalAttempt(ctx, missing, now); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	wrong := command("wrong-pin")
	wrong.DeclarationSHA256 = strings.Repeat("d", 64)
	if _, err = s.StartExternalAttempt(ctx, wrong, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal(err)
	}
	foreign := command("foreign")
	foreign.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err = s.StartExternalAttempt(ctx, foreign, now); !errors.Is(err, storage.ErrReceiverMismatch) {
		t.Fatal(err)
	}
	report := workertrial.ReportCommand{ReceiverID: r.ReceiverID, AttemptID: "same", IntentSHA256: first.SHA256, ReportedStatus: "completed", Stdout: string(reportedBytes(reportedFixture(t, job.Job))), Stderr: "diagnostic\n"}
	at = at.Add(time.Second)
	out := make(chan workertrial.Outcome, 8)
	for range 8 {
		wg.Go(func() {
			o, err := s.ReportExternalAttempt(ctx, report, now)
			if err != nil {
				t.Error(err)
				return
			}
			out <- o
		})
	}
	wg.Wait()
	close(out)
	var completed workertrial.Outcome
	count = 0
	for o := range out {
		count++
		if completed.SHA256 == "" {
			completed = o
		} else if !reflect.DeepEqual(completed, o) {
			t.Fatal("concurrent outcome differs")
		}
	}
	if count != 8 || completed.Validate(first) != nil || !completed.ComparisonValid || completed.HostStateKnown || completed.QualificationCredit || !*completed.AllComparableMetricsMatch {
		t.Fatal(completed, count)
	}
	if _, err = s.ReportExternalAttempt(ctx, report, noClock); err != nil {
		t.Fatal(err)
	}
	if i, err := s.StartExternalAttempt(ctx, command("same"), noClock); err != nil || !reflect.DeepEqual(first, i) {
		t.Fatal("terminal changed original intent", err)
	}
	changedReport := report
	changedReport.Stdout += "\n"
	if _, err = s.ReportExternalAttempt(ctx, changedReport, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("terminal replaced", err)
	}
	for name, want := range map[string]string{"stdout": report.Stdout, "stderr": report.Stderr} {
		a, err := s.ExternalAttemptOutput(ctx, "same", name)
		if err != nil || a.Content != want {
			t.Fatal(name, err)
		}
	}
	// Every failure disposition has its own intent; malformed bytes remain exact.
	statuses := []string{"mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted"}
	for _, status := range statuses {
		i, err := s.StartExternalAttempt(ctx, command(status), now)
		if err != nil {
			t.Fatal(err)
		}
		c := report
		c.AttemptID = status
		c.IntentSHA256 = i.SHA256
		c.ReportedStatus = status
		c.Stdout = "{partial malformed output\n"
		if status == "mismatch" {
			v := reportedFixture(t, job.Job)
			item := v["results"].([]any)[0].(map[string]any)
			item["metrics"].(map[string]int64)["marked_pnl_cents"]++
			comparison := item["comparisons"].(map[string]any)["marked_pnl_cents"].(map[string]any)
			comparison["external"] = item["metrics"].(map[string]int64)["marked_pnl_cents"]
			comparison["delta"] = 1
			comparison["matches"] = false
			v["all_comparable_metrics_match"] = false
			c.Stdout = string(reportedBytes(v))
		}
		if status == "failed" {
			v := reportedFixture(t, job.Job)
			v["status"] = "failed"
			v["error_code"] = "library_execution_failed"
			v["results"] = v["results"].([]any)[:1]
			v["all_comparable_metrics_match"] = false
			c.Stdout = string(reportedBytes(v))
		}
		o, err := s.ReportExternalAttempt(ctx, c, now)
		if err != nil || o.ReportedStatus != status || o.Validate(i) != nil {
			t.Fatal(status, o, err)
		}
		a, err := s.ExternalAttemptOutput(ctx, status, "stdout")
		if err != nil || a.Content != c.Stdout {
			t.Fatal("failed output changed", err)
		}
	}
	// A success statement without a correctly pinned comparison is rejected,
	// leaving the intent pending so the same failure can be reported honestly.
	bad, err := s.StartExternalAttempt(ctx, command("bad-result"), now)
	if err != nil {
		t.Fatal(err)
	}
	c := report
	c.AttemptID = "bad-result"
	c.IntentSHA256 = bad.SHA256
	c.Stdout = "{}"
	if _, err = s.ReportExternalAttempt(ctx, c, now); !errors.Is(err, trial.ErrInvalid) {
		t.Fatal("invalid success accepted", err)
	}
	c.Stdout = report.Stdout
	c.IntentSHA256 = strings.Repeat("d", 64)
	if _, err = s.ReportExternalAttempt(ctx, c, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal(err)
	}
	c.IntentSHA256 = bad.SHA256
	c.ReportedStatus = "incomplete"
	c.Stdout = strings.Repeat("x", (64<<10)+1)
	if _, err = s.ReportExternalAttempt(ctx, c, now); err != nil {
		t.Fatal("oversized comparison failure lost", err)
	}
	// Missing report remains visible, never interpreted as an absent attempt.
	if _, err = s.StartExternalAttempt(ctx, command("still-pending"), now); err != nil {
		t.Fatal(err)
	}
	history, err := s.ExternalAttempts(ctx, job.Command.RequestID)
	if err != nil || len(history) != 9 {
		t.Fatal(len(history), err)
	}
	history[0].Intent.Command.JobID = "mutated"
	if a, err := s.ExternalAttempt(ctx, "bad-result"); err != nil || a.Intent.Command.JobID == "mutated" {
		t.Fatal("aliased history", err)
	}
	if pg, ok := s.(*storage.Postgres); ok {
		conn, err := pgx.Connect(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		i, err := s.StartExternalAttempt(ctx, command("rollback"), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, "ALTER TABLE mantik_external_trial_attempt_outputs ADD CONSTRAINT reject_report CHECK(id!='rollback')"); err != nil {
			t.Fatal(err)
		}
		c := report
		c.AttemptID = "rollback"
		c.IntentSHA256 = i.SHA256
		if _, err = s.ReportExternalAttempt(ctx, c, now); err == nil {
			t.Fatal("failed output write acknowledged")
		}
		a, err := s.ExternalAttempt(ctx, "rollback")
		if err != nil || a.Outcome != nil {
			t.Fatal("partial terminal survived", err)
		}
		if _, err = conn.Exec(ctx, "ALTER TABLE mantik_external_trial_attempt_outputs DROP CONSTRAINT reject_report"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReportExternalAttempt(ctx, c, now); err != nil {
			t.Fatal(err)
		}
		pg.Close()
		reopened, err := storage.OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		a, err = s.ExternalAttempt(ctx, "same")
		if err != nil || !reflect.DeepEqual(*a.Outcome, completed) {
			t.Fatal("restart changed outcome", err)
		}
		if output, err := s.ExternalAttemptOutput(ctx, "same", "stdout"); err != nil || output.Content != report.Stdout {
			t.Fatal("restart changed raw output", err)
		}
	}
	// Clock rollback is refused; exact terminal retries still succeed at capacity.
	if _, err = s.StartExternalAttempt(ctx, command("backward"), func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, trial.ErrConflict) {
		t.Fatal(err)
	}
	history, err = s.ExternalAttempts(ctx, job.Command.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for n := len(history); n < workertrial.MaxAttempts; n++ {
		if _, err = s.StartExternalAttempt(ctx, command(fmt.Sprintf("capacity-%02d", n)), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.StartExternalAttempt(ctx, command("overflow"), now); !errors.Is(err, trial.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = s.StartExternalAttempt(ctx, command("same"), noClock); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportExternalAttempt(ctx, report, noClock); err != nil {
		t.Fatal(err)
	}
	full, err := s.(storage.ExternalHistoryStore).ExternalTrialHistory(ctx, job.TrialID)
	if err != nil || full.Validate() != nil || full.AttemptCount != workertrial.MaxAttempts {
		t.Fatal("full catalogue truncated", err)
	}
	after, _ := s.Snapshot(ctx)
	native, err := s.TrialRun(ctx, run.AttemptID)
	declared, err2 := s.ExternalJob(ctx, job.Command.RequestID)
	if err != nil || err2 != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(native, run) || !reflect.DeepEqual(job, declared) {
		t.Fatal("reported research changed source or operations", err, err2)
	}
}

func TestPostgresContractsExternalAttemptUpgrade(t *testing.T) {
	ctx := context.Background()
	s, config := externalPostgres(t, "attemptupgrade")
	r, run, at := externalSource(t, s)
	job, err := s.DeclareExternalJob(ctx, externalCommand(r, run, "before-upgrade"), func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, "DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_inventory; ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DELETE FROM mantik_schema WHERE version>=18"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := storage.OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	actual, err := upgraded.ExternalJob(ctx, job.Command.RequestID)
	if err != nil || !reflect.DeepEqual(actual, job) {
		t.Fatal("upgrade changed source declaration", err)
	}
	if _, err = upgraded.StartExternalAttempt(ctx, workertrial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "after-upgrade", JobID: job.Command.RequestID, DeclarationSHA256: job.SHA256}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=18"); err != nil {
		t.Fatal(err)
	}
	if bad, err := storage.OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
