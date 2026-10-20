package workerlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func behaviorFixture(t *testing.T) (candidateexec.Job, []byte) {
	t.Helper()
	d, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(d)
	p, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}\n"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := candidatetest.New(p, raw, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	return j, raw
}
func TestBehaviorHostLifecycleAndProtocolFence(t *testing.T) {
	j, data := behaviorFixture(t)
	p := behaviorExecution(j)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, profile: &p}
	p.outcome = func([]byte) (outcome, error) {
		return outcome{sha: strings.Repeat("b", 64), workerStatus: "completed", status: "completed", reason: "fixture_compared", exit: 0, eligible: true}, nil
	}
	r, err := launchExecution(context.Background(), root, p, j.Inputs(data), p.image, d)
	if err != nil || r.Schema != "mantik-candidate-behavior-host-receipt-v1" || r.Status != "completed" || !r.ProfileVerified || d.starts != 1 {
		t.Fatal(r, err)
	}
	again, err := reconcileCandidateBehavior(context.Background(), root, p.image, false, d)
	if err != nil || again.SHA256 != r.SHA256 || d.starts != 1 {
		t.Fatal(again, err)
	}
	if _, err := reconcile(context.Background(), root, p.image, true, d); err == nil || d.kills != 0 {
		t.Fatal("native accepted candidate attempt")
	}
	if _, err := reconcileCandidateBuild(context.Background(), root, p.image, true, d); err == nil || d.kills != 0 {
		t.Fatal("compile accepted behavior attempt")
	}
	if _, err := reconcileExternal(context.Background(), root, p.image, true, d); err == nil || d.kills != 0 {
		t.Fatal("external accepted candidate attempt")
	}
	if _, err := launchCandidateBehavior(context.Background(), root, j, data, p.image, d); !errors.Is(err, ErrConflict) {
		t.Fatal("attempt reused", err)
	}
	if err := os.Chmod(root+"/input/harness.go", 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/input/harness.go", []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileCandidateBehavior(context.Background(), root, p.image, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("changed harness accepted", err)
	}
}
func TestBehaviorInterruptedHandleAndChangedProfile(t *testing.T) {
	j, data := behaviorFixture(t)
	p := behaviorExecution(j)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, profile: &p, interrupt: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing interruption")
			}
		}()
		_, _ = launchCandidateBehavior(context.Background(), root, j, data, p.image, d)
	}()
	if _, err := reconcileCandidateBehavior(context.Background(), root, p.image, false, d); !errors.Is(err, ErrRunning) || d.kills != 0 || d.starts != 1 {
		t.Fatal(err)
	}
	d.s.HostConfig.Tmpfs["/tmp"] = "rw,exec,size=134217728"
	if _, err := reconcileCandidateBehavior(context.Background(), root, p.image, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("changed tmpfs accepted", err)
	}
	d.s.HostConfig.Tmpfs["/tmp"] = "rw,exec,nosuid,nodev,size=67108864,mode=1777"
	d.s.Config.Env = append(d.s.Config.Env, "SECRET=forbidden")
	if _, err := reconcileCandidateBehavior(context.Background(), root, p.image, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("changed profile stopped", err)
	}
	d.s.Config.Env = p.env
	r, err := reconcileCandidateBehavior(context.Background(), root, p.image, true, d)
	if err != nil || r.Status != "cancelled" || d.kills != 1 || d.starts != 1 {
		t.Fatal(r, err)
	}
}
