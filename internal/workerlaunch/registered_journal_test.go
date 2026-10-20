package workerlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func registeredIntent(t *testing.T, j candidateexec.Job, id string) candidateattempt.Intent {
	t.Helper()
	i := candidateattempt.Intent{Schema: candidateattempt.Schema, Command: candidateattempt.Command{ReceiverID: "receiver-1234567890123456", RequestID: id, RegistrationID: "root", RegistrationSHA256: strings.Repeat("b", 64), Job: j}, Sequence: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: "intent_retained", Authority: "candidate_worker_intent_only"}
	i.SHA256 = workerplan.Hash(i)
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestRegisteredJournalLifecycleAndReceiverFence(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	i := registeredIntent(t, j, "one")
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	root := filepath.Join(t.TempDir(), "bound")
	d := journalWorker(t, j)
	first, err := runRegisteredCandidateBehavior(ctx, root, "one", j, data, image, &i, d)
	if err != nil || first.Outcome == nil || first.Intent.ServiceIntent == nil || !reflect.DeepEqual(*first.Intent.ServiceIntent, i) || d.starts != 1 {
		t.Fatal(first, err)
	}
	got, err := runRegisteredCandidateBehavior(ctx, root, "one", j, data, image, &i, d)
	if err != nil || !reflect.DeepEqual(first, got) || d.starts != 1 {
		t.Fatal("retry changed or relaunched", err)
	}
	got, err = reconcileRegisteredCandidateRequest(ctx, root, "one", image, false, &i, d)
	if err != nil || !reflect.DeepEqual(first, got) || d.starts != 1 {
		t.Fatal("reconcile changed original outcome", err)
	}
	// A different receiver or any changed service receipt cannot adopt this owner.
	for _, change := range []func(*candidateattempt.Intent){
		func(x *candidateattempt.Intent) { x.Command.ReceiverID = "receiver-abcdefghijklmnop" },
		func(x *candidateattempt.Intent) { x.Command.RegistrationSHA256 = strings.Repeat("c", 64) },
		func(x *candidateattempt.Intent) { x.Command.RegistrationID = "child" },
		func(x *candidateattempt.Intent) { x.Sequence++ },
		func(x *candidateattempt.Intent) { x.CreatedAt = x.CreatedAt.Add(time.Second) },
	} {
		x := i
		change(&x)
		x.SHA256 = ""
		x.SHA256 = workerplan.Hash(x)
		if x.Validate() != nil {
			t.Fatal("invalid adversarial fixture")
		}
		if _, err := runRegisteredCandidateBehavior(ctx, root, "one", j, data, image, &x, d); !errors.Is(err, ErrConflict) {
			t.Fatal("changed receipt adopted", err)
		}
		if a, err := reconcileRegisteredCandidateRequest(ctx, root, "one", image, true, &x, d); !errors.Is(err, ErrConflict) || a.Intent.SHA256 != "" || d.kills != 0 {
			t.Fatal("changed receipt touched host", a, err)
		}
	}
	if _, err := runCandidateBehavior(ctx, root, "unbound", j, data, image, d); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy request mixed into bound journal", err)
	}
	if _, err := reconcileCandidateRequest(ctx, root, "one", image, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("unbound reconcile adopted service receipt", err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Schema != RegisteredCandidateJournalSchema || h.ReceiverID != i.Command.ReceiverID || len(h.Attempts) != 1 || h.CurrentHostStateKnown || h.QualificationCredit {
		t.Fatal(h, err)
	}
	// Histories include the full receipt, not just a caller-reported job hash.
	bad := first.Intent
	substitute := i
	substitute.Command.Job = behaviorFixtureDifferent(t, data)
	substitute.SHA256 = ""
	substitute.SHA256 = workerplan.Hash(substitute)
	bad.ServiceIntent = &substitute
	bad.SHA256 = ""
	bad.SHA256 = workerplan.Hash(bad)
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(root+"/intent-one.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CandidateBehaviorHistory(ctx, root); !errors.Is(err, ErrConflict) {
		t.Fatal("rehashed substituted service job accepted", err)
	}
	if d.starts != 1 || d.kills != 0 {
		t.Fatal("fences mutated original handle")
	}
}

func TestRegisteredJournalPreAcquisitionAndLauncherLoss(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	i := registeredIntent(t, j, "intent-only")
	root := filepath.Join(t.TempDir(), "bound")
	d := journalWorker(t, j)
	_, reserved, fresh, err := reserveRegisteredCandidate(ctx, root, "intent-only", j, data, image, &i)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	a, err := runRegisteredCandidateBehavior(ctx, root, "intent-only", j, data, image, &i, d)
	if !errors.Is(err, ErrUnresolved) || a.Intent.SHA256 != reserved.Intent.SHA256 || d.starts != 0 {
		t.Fatal("pre-acquisition gap relaunched", a, err)
	}
	i = registeredIntent(t, j, "lost")
	d.interrupt = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("launcher not interrupted")
			}
		}()
		_, _ = runRegisteredCandidateBehavior(ctx, root, "lost", j, data, image, &i, d)
	}()
	if _, err := runRegisteredCandidateBehavior(ctx, root, "lost", j, data, image, &i, d); !errors.Is(err, ErrRunning) || d.starts != 1 || d.kills != 0 {
		t.Fatal("live original handle changed", err)
	}
	a, err = reconcileRegisteredCandidateRequest(ctx, root, "lost", image, true, &i, d)
	if err != nil || a.Outcome == nil || a.Outcome.Receipt.Status != "cancelled" || d.starts != 1 || d.kills != 1 {
		t.Fatal(a, err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 1 || len(h.Attempts) != 2 || h.Attempts[0].Outcome != nil || h.RecordedStatuses["cancelled"] != 1 {
		t.Fatal(h, err)
	}
}

func TestRegisteredJournalCannotRebindLegacyOrEmptyOwner(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	i := registeredIntent(t, j, "one")
	for _, withIntent := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "legacy")
		if withIntent {
			if _, _, _, err := reserveCandidate(ctx, root, "one", j, data, image); err != nil {
				t.Fatal(err)
			}
		} else {
			_, unlock, err := openCandidateJournal(ctx, root, true)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		}
		if _, _, _, err := reserveRegisteredCandidate(ctx, root, "one", j, data, image, &i); !errors.Is(err, ErrConflict) {
			t.Fatal("legacy owner rebound", err)
		}
	}
	root := filepath.Join(t.TempDir(), "empty-bound")
	_, unlock, err := openCandidateJournalFor(ctx, root, true, i.Command.ReceiverID)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	x := i
	x.Command.ReceiverID = "receiver-abcdefghijklmnop"
	x.SHA256 = ""
	x.SHA256 = workerplan.Hash(x)
	if _, _, _, err := reserveRegisteredCandidate(ctx, root, "one", j, data, image, &x); !errors.Is(err, ErrConflict) {
		t.Fatal("empty owner rebound", err)
	}
	// Invalid pins reject before creating a new journal.
	for _, change := range []func(*candidateattempt.Intent){
		func(x *candidateattempt.Intent) { x.QualificationCredit = true },
		func(x *candidateattempt.Intent) {
			x.Command.RequestID = strings.Repeat("a", 81)
			x.SHA256 = ""
			x.SHA256 = workerplan.Hash(*x)
		},
	} {
		x := i
		change(&x)
		root := filepath.Join(t.TempDir(), "absent")
		if _, err := RunRegisteredCandidateBehavior(ctx, root, x, data, image); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("journal created before pins validated")
		}
	}
}

func TestRegisteredJournalConcurrentReservationAndReceiptGap(t *testing.T) {
	ctx := context.Background()
	j, data := behaviorFixture(t)
	i := registeredIntent(t, j, "one")
	image := j.Plan.Candidate.Toolchain.ImageSHA256
	root := filepath.Join(t.TempDir(), "bound")
	var wg sync.WaitGroup
	freshCount := make(chan bool, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tries := 0; tries < 200; tries++ {
				_, _, fresh, err := reserveRegisteredCandidate(ctx, root, "one", j, data, image, &i)
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
			t.Error("contention did not settle")
		}()
	}
	wg.Wait()
	close(freshCount)
	fresh := 0
	for v := range freshCount {
		if v {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("multiple intent reservations", fresh)
	}
	d := journalWorker(t, j)
	r, err := launchCandidateBehavior(ctx, attemptPath(root, "one"), j, data, image, d)
	if err != nil {
		t.Fatal(err)
	}
	h, err := CandidateBehaviorHistory(ctx, root)
	if err != nil || h.Pending != 1 || len(h.Attempts) != 1 {
		t.Fatal(h, err)
	}
	a, err := reconcileRegisteredCandidateRequest(ctx, root, "one", image, false, &i, d)
	if err != nil || a.Outcome == nil || !reflect.DeepEqual(a.Outcome.Receipt, r) || d.starts != 1 {
		t.Fatal("receipt gap caused relaunch", a, err)
	}
}
