package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatebuild"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/researchworker"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type fakeDocker struct {
	t             *testing.T
	j             workerplan.Job
	input         []byte
	s             snapshot
	starts, kills int
	interrupt     bool
	foreign       bool
	waitHook      func()
	profile       *execution
	output        []byte
}

func (f *fakeDocker) command(ctx context.Context, args ...string) ([]byte, []byte, error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	switch args[0] {
	case "create":
		root := ""
		for i, arg := range args {
			if arg == "--mount" {
				root = strings.TrimSuffix(strings.TrimPrefix(args[i+1], "type=bind,source="), "/input,destination=/worker,readonly")
			}
		}
		a, j, err := load(root, f.j.ImageSHA256)
		p := nativeExecution(j)
		if f.profile != nil {
			if f.profile.acquisitionSchema == "mantik-candidate-behavior-acquisition-v1" {
				var job candidateexec.Job
				a, job, err = loadCandidateBehavior(root, f.profile.image)
				p = behaviorExecution(job)
			} else if f.profile.acquisitionSchema == "mantik-candidate-build-acquisition-v1" {
				var candidateJob candidatebuild.Job
				a, candidateJob, err = loadCandidateBuild(root, f.profile.image)
				p = candidateExecution(candidateJob)
			} else {
				var externalJob externalplan.Job
				a, externalJob, err = loadExternal(root, f.profile.image)
				p = externalExecution(externalJob)
			}
		}
		if err != nil {
			f.t.Fatal("create before durable acquisition", err)
		}
		f.s = snapshot{ID: strings.Repeat("a", 64), Name: "/" + a.ContainerName, Image: p.image}
		f.s.Config.User = "65532:65532"
		f.s.Config.Env = p.env
		f.s.Config.Entrypoint = p.entrypoint
		f.s.Config.Cmd = p.args
		f.s.Config.Labels = map[string]string{ownerLabel: a.ID, jobLabel: p.sha}
		if p.behaviorTmp {
			f.s.HostConfig.Tmpfs = map[string]string{"/tmp": "rw,exec,nosuid,nodev,size=67108864,mode=1777"}
		}
		f.s.HostConfig.ReadonlyRootfs = true
		f.s.HostConfig.NetworkMode = "none"
		f.s.HostConfig.CapDrop = []string{"ALL"}
		f.s.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
		f.s.HostConfig.Memory = p.resources.MemoryBytes
		f.s.HostConfig.MemorySwap = p.resources.MemoryBytes
		f.s.HostConfig.NanoCpus = 1000000000
		f.s.HostConfig.PidsLimit = 64
		f.s.HostConfig.IpcMode = "private"
		f.s.HostConfig.RestartPolicy.Name = "no"
		f.s.HostConfig.LogConfig.Type = "local"
		f.s.HostConfig.LogConfig.Config = map[string]string{"max-size": "20m", "max-file": "1", "compress": "false"}
		if json.Unmarshal([]byte(`[{"Source":"`+root+`/input","Destination":"/worker","RW":false,"Type":"bind"}]`), &f.s.Mounts) != nil {
			f.t.Fatal("mount fixture")
		}
		f.s.State.Status = "created"
		if f.foreign {
			f.s.Config.Labels[ownerLabel] = "foreign"
		}
		return []byte(f.s.ID), nil, nil
	case "container":
		raw, _ := json.Marshal([]snapshot{f.s})
		return raw, nil, nil
	case "start":
		f.starts++
		f.s.State.Status = "running"
		f.s.State.Running = true
		f.s.State.Pid = 123
		f.s.State.StartedAt = time.Now().UTC()
		return nil, nil, nil
	case "wait":
		if f.interrupt {
			panic("simulated host process loss")
		}
		if f.waitHook != nil {
			f.waitHook()
		}
		f.s.State.Running = false
		f.s.State.Pid = 0
		f.s.State.Status = "exited"
		f.s.State.FinishedAt = time.Now().UTC()
		return nil, nil, nil
	case "kill":
		f.kills++
		f.s.State.Running = false
		f.s.State.Pid = 0
		f.s.State.Status = "exited"
		f.s.State.ExitCode = 137
		f.s.State.FinishedAt = time.Now().UTC()
		return nil, nil, nil
	case "logs":
		if f.profile != nil {
			if f.output != nil {
				return f.output, nil, nil
			}
			return []byte("{}"), nil, nil
		}
		if f.s.State.ExitCode == 137 {
			return nil, nil, nil
		}
		r, err := researchworker.Execute(context.Background(), f.j, bytes.NewReader(f.input))
		if err != nil {
			f.t.Fatal(err)
		}
		raw, _ := json.Marshal(r)
		return raw, nil, nil
	}
	f.t.Fatal(args)
	return nil, nil, errors.New("unexpected call")
}
func fixture(t *testing.T) (workerplan.Job, []byte) {
	t.Helper()
	d, _ := replay.FixtureDataset()
	recipe, _ := experiment.DefaultRecipe(d)
	raw, _ := json.Marshal(d)
	j, err := researchworker.Declare("test-job", "sha256:"+strings.Repeat("b", 64), recipe, raw)
	if err != nil {
		t.Fatal(err)
	}
	return j, raw
}
func TestLaunchAndReconcileNeverReexecute(t *testing.T) {
	j, raw := fixture(t)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, j: j, input: raw}
	r, err := launch(context.Background(), root, j, bytes.NewReader(raw), j.ImageSHA256, d)
	if err != nil || r.Status != "completed" || !r.ProfileVerified || !r.HostDeadlineMet || d.starts != 1 {
		t.Fatal(r, err)
	}
	retry, err := reconcile(context.Background(), root, j.ImageSHA256, false, d)
	if err != nil || retry.SHA256 != r.SHA256 || d.starts != 1 {
		t.Fatal("terminal attempt changed or restarted", err)
	}
	if _, err = launch(context.Background(), root, j, bytes.NewReader(raw), j.ImageSHA256, d); !errors.Is(err, ErrConflict) {
		t.Fatal("existing attempt relaunched", err)
	}
	d.s.State.Running = true
	d.s.State.Pid = 234
	if _, err = reconcile(context.Background(), root, j.ImageSHA256, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("restarted terminal handle killed or trusted", err)
	}
}
func TestInterruptedHostRetainsHandleAndExplicitCancellation(t *testing.T) {
	j, raw := fixture(t)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, j: j, input: raw, interrupt: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing simulated interruption")
			}
		}()
		_, _ = launch(context.Background(), root, j, bytes.NewReader(raw), j.ImageSHA256, d)
	}()
	if _, err := os.Stat(root + "/acquisition.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(context.Background(), root, j.ImageSHA256, false, d); !errors.Is(err, ErrRunning) || d.kills != 0 || d.starts != 1 {
		t.Fatal("live owned handle restarted/stopped by observation", err)
	}
	r, err := reconcile(context.Background(), root, j.ImageSHA256, true, d)
	if err != nil || r.Status != "cancelled" || d.kills != 1 || d.starts != 1 || r.WorkerResultSHA256 != "" {
		t.Fatal(r, err)
	}
}
func TestForeignAndChangedPolicyNeverStart(t *testing.T) {
	j, raw := fixture(t)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, j: j, input: raw, foreign: true}
	if _, err := launch(context.Background(), root, j, bytes.NewReader(raw), j.ImageSHA256, d); !errors.Is(err, ErrConflict) || d.starts != 0 || d.kills != 0 {
		t.Fatal("foreign container touched", err)
	}
	if _, err := reconcile(context.Background(), root, j.ImageSHA256, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("foreign cancellation", err)
	}
	if _, err := launch(context.Background(), filepath.Join(t.TempDir(), "unapproved"), j, bytes.NewReader(raw), "sha256:"+strings.Repeat("0", 64), d); !errors.Is(err, ErrConflict) {
		t.Fatal("unapproved image accepted", err)
	}
}
func TestAtomicArtifactsAndOutputBounds(t *testing.T) {
	root := t.TempDir()
	if err := writeOnce(root, "artifact", []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeOnce(root, "artifact", []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeOnce(root, "artifact", []byte("changed"), 0600); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	buf := &capped{limit: 3}
	if _, err := buf.Write([]byte("abcdef")); err == nil || buf.String() != "abc" {
		t.Fatal("unbounded output")
	}
	unlock, err := lock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err = lock(context.Background(), root); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent launcher lock accepted", err)
	}
}

func TestProfileRejectsAuthorityAndResourceChanges(t *testing.T) {
	j, raw := fixture(t)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, j: j, input: raw}
	if _, err := launch(context.Background(), root, j, bytes.NewReader(raw), j.ImageSHA256, d); err != nil {
		t.Fatal(err)
	}
	a, _, err := load(root, j.ImageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*snapshot){
		func(s *snapshot) {
			s.HostConfig.Tmpfs = map[string]string{"/tmp": "rw,exec,nosuid,nodev,size=67108864,mode=1777"}
		},
		func(s *snapshot) { s.HostConfig.NetworkMode = "host" }, func(s *snapshot) { s.Config.Env = append(s.Config.Env, "DATABASE_URL=forbidden") },
		func(s *snapshot) { s.Mounts[0].RW = true }, func(s *snapshot) { s.HostConfig.Memory++ }, func(s *snapshot) { s.Config.Entrypoint = []string{"sh"} },
		func(s *snapshot) { s.HostConfig.Privileged = true }, func(s *snapshot) { s.HostConfig.LogConfig.Config["compress"] = "true" },
	} {
		body, _ := json.Marshal(d.s)
		var copy snapshot
		_ = json.Unmarshal(body, &copy)
		change(&copy)
		if verify(copy, a, j) == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
	info, err := os.Stat(root + "/input/dataset.json")
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatal("worker input permissions", err)
	}
}
