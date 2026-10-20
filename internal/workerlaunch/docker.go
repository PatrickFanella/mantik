package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerreceipt"
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

type snapshot = workerreceipt.Snapshot

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
	return workerreceipt.VerifyProfile(s, a, receiptProfile(p))
}
func createExecutionArgs(a Acquisition, p execution) []string {
	args := []string{"create", "--pull=never", "--name", a.ContainerName, "--label", ownerLabel + "=" + a.ID, "--label", jobLabel + "=" + p.sha, "--read-only", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "64", "--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1", "--user", "65532:65532", "--restart", "no", "--log-driver", "local", "--log-opt", "max-size=20m", "--log-opt", "max-file=1", "--log-opt", "compress=false", "--mount", "type=bind,source=" + a.Root + "/input,destination=/worker,readonly", "--entrypoint", p.entrypoint[0], p.image}
	if p.behaviorTmp {
		args = append(args[:len(args)-1], "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=67108864,mode=1777", p.image)
	}
	return append(args, p.args...)
}

var _ io.Writer = (*capped)(nil)
