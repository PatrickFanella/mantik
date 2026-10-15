package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type runBackend interface {
	attemptBackend
	TrialRunStore
}

func TestMemoryNativeRunLedger(t *testing.T) { runContracts(t, NewMemory()) }
func TestPostgresContractsNativeRunLedger(t *testing.T) {
	runContracts(t, timerPostgres(t, "nativeruns"))
}

// The storage contract tests validate binding, not engine metrics. Service tests
// exercise the real native engine against its built-in dataset.
func runResult(w trial.RunWork, status string) string {
	methods := []map[string]string{}
	code := ""
	if status == "completed" {
		for _, pin := range append(w.Registration.Recipe.Methods, w.Registration.Recipe.Benchmarks...) {
			methods = append(methods, map[string]string{"method_id": pin.ID, "method_sha256": pin.SHA256})
		}
	} else {
		code = "dataset_pin_mismatch"
	}
	raw, _ := json.Marshal(map[string]any{"id": w.Run.NativeAttemptID, "recipe_sha256": w.Run.RecipeSHA256, "input_sha256": w.Run.InputSHA256, "status": status, "error_code": code, "evidence_class": "synthetic_unqualified", "qualified": false, "results": methods})
	return string(raw)
}
func runContracts(t *testing.T, s runBackend) {
	ctx := context.Background()
	r, at := attemptFixture(t, s)
	now := func() time.Time { return at }
	receiver := r.ReceiverID
	start := func(id string) {
		t.Helper()
		if _, err := s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: receiver, RequestID: id, TrialID: r.ID}, now); err != nil {
			t.Fatal(err)
		}
	}
	start("run")
	before, _ := s.Snapshot(ctx)
	intentBefore, _ := s.TrialAttempt(ctx, "run")
	var wg sync.WaitGroup
	var winners atomic.Int32
	var work trial.RunWork
	var mu sync.Mutex
	for range 8 {
		wg.Go(func() {
			w, err := s.BeginTrialRun(ctx, receiver, "run", `{"fixture":"input"}`, "invocation", now)
			if err == nil && w.Acquired {
				winners.Add(1)
				mu.Lock()
				work = w
				mu.Unlock()
			} else if !errors.Is(err, trial.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("multiple run owners", winners.Load())
	}
	if _, err := s.CancelTrialAttempt(ctx, receiver, "run", now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("running intent cancelled", err)
	}
	if _, err := s.BeginTrialRun(ctx, "receiver-abcdefghijklmnop", "run", `{}`, "foreign", now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	raw := runResult(work, "completed")
	if _, err := s.FinishTrialRun(ctx, receiver, "run", "foreign", raw, now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("foreign invocation committed", err)
	}
	completed, err := s.FinishTrialRun(ctx, receiver, "run", work.Run.InvocationID, raw, now)
	if err != nil || completed.Validate() != nil || completed.Status != "completed" {
		t.Fatal(completed, err)
	}
	retry, err := s.BeginTrialRun(ctx, receiver, "run", `{}`, "another", func() time.Time { panic("terminal retry clock") })
	if err != nil || retry.Acquired || !reflect.DeepEqual(retry.Run, completed) {
		t.Fatal("terminal run reexecuted", err)
	}
	result, err := s.TrialRunArtifact(ctx, "run", "result")
	if err != nil || result.Content != raw || result.SHA256 != completed.ResultSHA256 {
		t.Fatal(err)
	}
	start("cancelled")
	if _, err = s.CancelTrialAttempt(ctx, receiver, "cancelled", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginTrialRun(ctx, receiver, "cancelled", `{}`, "owner", now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("cancelled intent executed", err)
	}
	start("interrupted")
	pending, err := s.BeginTrialRun(ctx, receiver, "interrupted", `{"retained":true}`, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveTrialRun(ctx, receiver, "interrupted", now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("premature resolution", err)
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
		actual, err := s.TrialRun(ctx, "run")
		if err != nil || !reflect.DeepEqual(actual, completed) {
			t.Fatal("restart lost terminal run", err)
		}
		actual, err = s.TrialRun(ctx, "interrupted")
		if err != nil || !reflect.DeepEqual(actual, pending.Run) {
			t.Fatal("restart replaced owner or deadline", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_trial_run_artifacts ADD CONSTRAINT reject_result CHECK(attempt_id!='interrupted' OR result_text IS NULL)"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FinishTrialRun(ctx, receiver, "interrupted", "owner", runResult(pending, "completed"), now); err == nil {
			t.Fatal("failed result persistence acknowledged")
		}
		actual, err = s.TrialRun(ctx, "interrupted")
		if err != nil || actual.Status != "running" || actual.ResultSHA256 != "" {
			t.Fatal("partial terminal metadata", err)
		}
		if _, err = s.TrialRunArtifact(ctx, "interrupted", "result"); !errors.Is(err, trial.ErrNotFound) {
			t.Fatal("partial result artifact", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_trial_run_artifacts DROP CONSTRAINT reject_result"); err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(trial.RunSeconds * time.Second)
	incomplete, err := s.ResolveTrialRun(ctx, receiver, "interrupted", now)
	if err != nil || incomplete.Status != "incomplete" || incomplete.Validate() != nil {
		t.Fatal(incomplete, err)
	}
	if _, err = s.ResolveTrialRun(ctx, receiver, "interrupted", func() time.Time { panic("resolution retry clock") }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishTrialRun(ctx, receiver, "interrupted", "owner", runResult(pending, "completed"), now); !errors.Is(err, trial.ErrConflict) {
		t.Fatal("resolved invocation overwrote history", err)
	}
	input, err := s.TrialRunArtifact(ctx, "interrupted", "input")
	if err != nil || input.Content != `{"retained":true}` {
		t.Fatal("interrupted input lost", err)
	}
	start("failed")
	failedWork, err := s.BeginTrialRun(ctx, receiver, "failed", `{}`, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.FinishTrialRun(ctx, receiver, "failed", "owner", runResult(failedWork, "failed"), now)
	if err != nil || failed.Status != "failed" {
		t.Fatal(failed, err)
	}
	start("cancel-race")
	var acquireErr, cancelErr error
	var acquired trial.RunWork
	barrier := make(chan struct{})
	wg.Go(func() {
		<-barrier
		acquired, acquireErr = s.BeginTrialRun(ctx, receiver, "cancel-race", `{}`, "race-owner", now)
	})
	wg.Go(func() { <-barrier; _, cancelErr = s.CancelTrialAttempt(ctx, receiver, "cancel-race", now) })
	close(barrier)
	wg.Wait()
	if acquireErr == nil {
		if !acquired.Acquired || !errors.Is(cancelErr, trial.ErrConflict) {
			t.Fatal("run and cancellation both won", acquireErr, cancelErr)
		}
	} else if cancelErr != nil || !errors.Is(acquireErr, trial.ErrConflict) {
		t.Fatal("invalid acquisition/cancellation race", acquireErr, cancelErr)
	}
	after, _ := s.Snapshot(ctx)
	intentAfter, _ := s.TrialAttempt(ctx, "run")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(intentBefore, intentAfter) {
		t.Fatal("native run mutated operational state or historical intent")
	}
}
func TestPostgresContractsNativeRunUpgrade(t *testing.T) {
	s := timerPostgres(t, "runupgrade")
	r, at := attemptFixture(t, s)
	ctx := context.Background()
	a, err := s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "retained", TrialID: r.ID}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DELETE FROM mantik_schema WHERE version=16"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	actual, err := upgraded.TrialAttempt(ctx, "retained")
	if err != nil || !reflect.DeepEqual(a, actual) {
		t.Fatal("upgrade changed intent", err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=16"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("changed migration admitted")
	}
}
