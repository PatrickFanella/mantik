package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var ErrMissing = errors.New("owned worker container is missing")

type driver interface {
	command(context.Context, ...string) ([]byte, []byte, error)
}
type dockerCLI struct{ config string }
type capped struct {
	bytes.Buffer
	limit int
}

func (w *capped) Write(p []byte) (int, error) {
	n := len(p)
	if n > w.limit-w.Len() {
		p = p[:w.limit-w.Len()]
		_, _ = w.Buffer.Write(p)
		return len(p), errors.New("docker output limit exceeded")
	}
	return w.Buffer.Write(p)
}
func (d dockerCLI) command(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/docker", args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "DOCKER_HOST=unix:///var/run/docker.sock", "DOCKER_CONFIG=" + d.config}
	out, stderr := &capped{limit: workerplan.MaxResultBytes}, &capped{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	return out.Bytes(), stderr.Bytes(), err
}

type snapshot struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct {
		User       string
		Env        []string
		Entrypoint []string
		Cmd        []string
		Labels     map[string]string
		Tty        bool
	}
	HostConfig struct {
		Tmpfs          map[string]string
		ReadonlyRootfs bool
		NetworkMode    string
		CapDrop        []string
		CapAdd         []string
		SecurityOpt    []string
		Memory         int64
		MemorySwap     int64
		NanoCpus       int64
		PidsLimit      int64
		Privileged     bool
		PidMode        string
		IpcMode        string
		UTSMode        string
		UsernsMode     string
		Binds          []string
		Devices        []json.RawMessage
		PortBindings   map[string]json.RawMessage
		RestartPolicy  struct{ Name string }
		LogConfig      struct {
			Type   string
			Config map[string]string
		}
	}
	Mounts []struct {
		Source      string
		Destination string
		RW          bool
		Type        string
	}
	State struct {
		Running    bool
		Pid        int
		ExitCode   int
		Status     string
		StartedAt  time.Time
		FinishedAt time.Time
	}
}

func inspect(ctx context.Context, d driver, a Acquisition) (snapshot, error) {
	out, stderr, err := d.command(ctx, "container", "inspect", a.ContainerName)
	if err != nil {
		if strings.Contains(string(stderr), "No such container:") || strings.Contains(string(stderr), "No such object:") {
			return snapshot{}, ErrMissing
		}
		return snapshot{}, errors.New("cannot inspect owned worker container")
	}
	var all []snapshot
	if json.Unmarshal(out, &all) != nil || len(all) != 1 {
		return snapshot{}, workerplan.ErrInvalid
	}
	s := all[0]
	if s.Name != "/"+a.ContainerName || s.Config.Labels[ownerLabel] != a.ID || s.Config.Labels[jobLabel] != a.JobSHA256 || !containerID.MatchString(s.ID) || s.Image != a.ImageSHA256 {
		return snapshot{}, ErrConflict
	}
	return s, nil
}
func workerArgs(j workerplan.Job) []string {
	return []string{"worker-execute", "--job", "/worker/job.json", "--job-sha256", j.SHA256, "--input", "/worker/dataset.json"}
}
func verify(s snapshot, a Acquisition, j workerplan.Job) error {
	return verifyExecution(s, a, nativeExecution(j))
}
func verifyExecution(s snapshot, a Acquisition, p execution) error {
	h, c := s.HostConfig, s.Config
	r := p.resources
	if s.Image != p.image || c.User != "65532:65532" || c.Tty || !reflect.DeepEqual(c.Entrypoint, p.entrypoint) || !reflect.DeepEqual(c.Cmd, p.args) || !reflect.DeepEqual(c.Env, p.env) || !h.ReadonlyRootfs || h.NetworkMode != "none" || !reflect.DeepEqual(h.CapDrop, []string{"ALL"}) || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges:true"}) || h.Memory != r.MemoryBytes || h.MemorySwap != r.MemoryBytes || h.NanoCpus != int64(r.CPUMillis)*1000000 || h.PidsLimit != int64(r.PIDs) || h.Privileged || h.PidMode != "" || h.IpcMode != "private" || h.UTSMode != "" || h.UsernsMode != "" || len(h.Binds) != 0 || len(h.Devices) != 0 || len(h.PortBindings) != 0 || h.RestartPolicy.Name != "no" || h.LogConfig.Type != "local" || !reflect.DeepEqual(h.LogConfig.Config, map[string]string{"max-size": "20m", "max-file": "1", "compress": "false"}) {
		return workerplan.ErrInvalid
	}
	expectedTmp := map[string]string(nil)
	if p.behaviorTmp {
		expectedTmp = map[string]string{"/tmp": "rw,exec,nosuid,nodev,size=67108864,mode=1777"}
	}
	if len(h.Tmpfs) != len(expectedTmp) || (p.behaviorTmp && !reflect.DeepEqual(h.Tmpfs, expectedTmp)) {
		return workerplan.ErrInvalid
	}
	bind, tmp := 0, 0
	for _, m := range s.Mounts {
		if m.Source == a.Root+"/input" && m.Destination == "/worker" && !m.RW && m.Type == "bind" {
			bind++
			continue
		}
		if p.behaviorTmp && m.Source == "" && m.Destination == "/tmp" && m.RW && m.Type == "tmpfs" {
			tmp++
			continue
		}
		return workerplan.ErrInvalid
	}
	if bind != 1 || tmp > 1 {
		return workerplan.ErrInvalid
	}
	return nil
}
func createExecutionArgs(a Acquisition, p execution) []string {
	args := []string{"create", "--pull=never", "--name", a.ContainerName, "--label", ownerLabel + "=" + a.ID, "--label", jobLabel + "=" + p.sha, "--read-only", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "64", "--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1", "--user", "65532:65532", "--restart", "no", "--log-driver", "local", "--log-opt", "max-size=20m", "--log-opt", "max-file=1", "--log-opt", "compress=false", "--mount", "type=bind,source=" + a.Root + "/input,destination=/worker,readonly", "--entrypoint", p.entrypoint[0], p.image}
	if p.behaviorTmp {
		args = append(args[:len(args)-1], "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=67108864,mode=1777", p.image)
	}
	return append(args, p.args...)
}

var _ io.Writer = (*capped)(nil)
