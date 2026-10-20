package workerlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func journalWorker(t *testing.T, j candidateexec.Job) *fakeDocker {
	t.Helper()
	p := behaviorExecution(j)
	r := candidateexec.Result{Schema: candidateexec.ResultSchema, JobSHA256: j.SHA256, PlanSHA256: j.Plan.SHA256, CandidateSHA256: j.Plan.Candidate.SHA256, Status: "completed", ReasonCode: "fixture_compared", BuildCompleted: true, RunAttempted: true, Values: []bool{}, Matched: len(j.Plan.Cases), AllAnnotationsMatch: true, Authority: "synthetic_candidate_behavior_worker_only"}
	for index, c := range j.Plan.Cases {
		r.Values = append(r.Values, c.Expected)
		r.RunStderr += fmt.Sprintf("_mantik %d %t\n", index, c.Expected)
	}
	r.SHA256 = workerplan.Hash(r)
	if r.Validate(j) != nil {
		t.Fatal(r)
	}
	raw, _ := json.Marshal(r)
	return &fakeDocker{t: t, profile: &p, output: raw}
}
func TestCandidateJournalRetriesAndFullHistory(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	d := journalWorker(t, j)
	root := filepath.Join(t.TempDir(), "journal")
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	first, err := runCandidateBehavior(ctx, root, "request-one", j, data, image, d)
	if err != nil || first.Outcome == nil || first.Outcome.Receipt.Status != "completed" || d.starts != 1 {
		t.Fatal(first, err)
	}
	again, err := runCandidateBehavior(ctx, root, "request-one", j, data, image, d)
	if err != nil || !reflect.DeepEqual(first, again) || d.starts != 1 {
		t.Fatal("request repeated physical execution", err)
	}
	second, err := runCandidateBehavior(ctx, root, "request-two", j, data, image, d)
	if err != nil || d.starts != 2 || second.Intent.SHA256 == first.Intent.SHA256 || second.Outcome.Receipt.AcquisitionSHA256 == first.Outcome.Receipt.AcquisitionSHA256 {
		t.Fatal(second, err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || len(h.Attempts) != 2 || h.Pending != 0 || h.RecordedStatuses["completed"] != 2 || h.CurrentHostStateKnown || h.QualificationCredit {
		t.Fatal(h, err)
	}
	// Wrong image/dataset and changed job must not allocate a new intent.
	if _, err := runCandidateBehavior(ctx, root, "wrong-dataset", j, append(data, '\n'), image, d); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := runCandidateBehavior(ctx, root, "wrong-image", j, data, "sha256:"+strings.Repeat("b", 64), d); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changed := behaviorFixtureDifferent(t, data)
	if _, err := runCandidateBehavior(ctx, root, "request-one", changed, data, image, d); !errors.Is(err, ErrConflict) || d.starts != 2 {
		t.Fatal("request ID rebound", err)
	}
	h, err = CandidateBehaviorHistory(ctx, root)
	if err != nil || len(h.Attempts) != 2 {
		t.Fatal(err)
	}
	// Retained output changes invalidate the whole history, rather than hiding a failed row.
	out := attemptPath(root, "request-two") + "/stdout.json"
	if err := os.WriteFile(out, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CandidateBehaviorHistory(ctx, root); !errors.Is(err, ErrConflict) {
		t.Fatal("changed output accepted", err)
	}
}
func behaviorFixtureDifferent(t *testing.T, data []byte) candidateexec.Job {
	t.Helper()
	j, _ := behaviorFixture(t)
	p := j.Plan
	p.Cases[0].Expected = !p.Cases[0].Expected
	p.ID = ""
	p.SHA256 = ""
	p.SHA256 = workerplan.Hash(p)
	p.ID = "candidate-test-" + p.SHA256
	changed, err := candidateexec.New(p, data)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}
func TestCandidateJournalPreAcquisitionAndLauncherLoss(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	root := filepath.Join(t.TempDir(), "journal")
	d := journalWorker(t, j)
	canonical, intent, fresh, err := reserveCandidate(ctx, root, "intent-only", j, data, image)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	a, err := runCandidateBehavior(ctx, root, "intent-only", j, data, image, d)
	if !errors.Is(err, ErrUnresolved) || a.Intent.SHA256 != intent.Intent.SHA256 || d.starts != 0 {
		t.Fatal("intent-only relaunched", a, err)
	}
	if _, err := os.Stat(attemptPath(canonical, "intent-only")); !os.IsNotExist(err) {
		t.Fatal("intent gap allocated handle")
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 1 || h.Attempts[0].Outcome != nil {
		t.Fatal(h, err)
	}
	d.interrupt = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("no interruption")
			}
		}()
		_, _ = runCandidateBehavior(ctx, root, "launcher-loss", j, data, image, d)
	}()
	h, err = CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 2 {
		t.Fatal(h, err)
	}
	if _, err := reconcileCandidateRequest(ctx, root, "launcher-loss", image, false, d); !errors.Is(err, ErrRunning) || d.starts != 1 || d.kills != 0 {
		t.Fatal("live handle changed", err)
	}
	cancelled, err := reconcileCandidateRequest(ctx, root, "launcher-loss", image, true, d)
	if err != nil || cancelled.Outcome == nil || cancelled.Outcome.Receipt.Status != "cancelled" || d.kills != 1 || d.starts != 1 {
		t.Fatal(cancelled, err)
	}
	h, err = CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 1 || h.RecordedStatuses["cancelled"] != 1 {
		t.Fatal(h, err)
	}
}
func TestCandidateJournalCollectsAfterReceiptGap(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	root := filepath.Join(t.TempDir(), "journal")
	d := journalWorker(t, j)
	canonical, a, fresh, err := reserveCandidate(ctx, root, "receipt-gap", j, data, image)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	r, err := launchCandidateBehavior(ctx, attemptPath(canonical, a.Intent.RequestID), j, data, image, d)
	if err != nil {
		t.Fatal(err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 1 {
		t.Fatal(h, err)
	}
	got, err := reconcileCandidateRequest(ctx, root, "receipt-gap", image, false, d)
	if err != nil || got.Outcome == nil || !reflect.DeepEqual(got.Outcome.Receipt, r) || d.starts != 1 {
		t.Fatal(got, err)
	}
}
func TestCandidateJournalConcurrentIntentAndCapacity(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	root := filepath.Join(t.TempDir(), "journal")
	var wg sync.WaitGroup
	freshCount := make(chan bool, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tries := 0; tries < 200; tries++ {
				_, _, fresh, err := reserveCandidate(ctx, root, "same", j, data, image)
				if errors.Is(err, ErrBusy) {
					time.Sleep(time.Millisecond)
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				freshCount <- fresh
				return
			}
			t.Error("lock contention did not settle")
		}()
	}
	wg.Wait()
	close(freshCount)
	count := 0
	for fresh := range freshCount {
		if fresh {
			count++
		}
	}
	if count != 1 {
		t.Fatal("multiple intents", count)
	}
	for n := 1; n < MaxCandidateAttempts; n++ {
		if _, _, _, err := reserveCandidate(ctx, root, fmt.Sprintf("attempt-%02d", n), j, data, image); err != nil {
			t.Fatal(n, err)
		}
	}
	if _, _, _, err := reserveCandidate(ctx, root, "overflow", j, data, image); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, _, fresh, err := reserveCandidate(ctx, root, "same", j, data, image); err != nil || fresh {
		t.Fatal("capacity blocked retry", err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || len(h.Attempts) != MaxCandidateAttempts || h.Pending != MaxCandidateAttempts {
		t.Fatal(err)
	}
	if err := os.Remove(root + "/intent-attempt-01.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := CandidateBehaviorHistory(ctx, root); !errors.Is(err, ErrConflict) {
		t.Fatal("sequence gap hidden", err)
	}
}
func TestCandidateJournalPrivatePathsAndUncommittedArtifacts(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	for _, id := range []string{"../escape", "", "bad/path"} {
		if _, _, _, err := reserveCandidate(ctx, filepath.Join(t.TempDir(), "journal"), id, j, data, image); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "journal")
	if _, _, _, err := reserveCandidate(ctx, root, "one", j, data, image); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/.pending-interrupted", []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || len(h.PendingArtifacts) != 1 {
		t.Fatal(h, err)
	}
	if _, _, _, err := reserveCandidate(ctx, root, "two", j, data, image); !errors.Is(err, ErrUnresolved) {
		t.Fatal("uncommitted artifact skipped", err)
	}
	if err := os.WriteFile(root+"/unknown", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CandidateBehaviorHistory(ctx, root); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign file adopted", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CandidateBehaviorHistory(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
