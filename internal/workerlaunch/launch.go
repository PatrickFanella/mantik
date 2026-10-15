// Package workerlaunch owns explicit local Docker worker attempts and private
// acquisition/result receipts. It never restarts or dispatches service work.
package workerlaunch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const ownerLabel = "tv.subcult.mantik.worker.launch"
const jobLabel = "tv.subcult.mantik.worker.job"

var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var launchID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var ErrConflict = errors.New("worker launch identity, artifact or ownership conflict")
var ErrBusy = errors.New("worker launcher holds the attempt lock")
var ErrRunning = errors.New("owned worker is running before its deadline; inspect again or explicitly cancel")

type Acquisition struct {
	Schema        string    `json:"schema"`
	ID            string    `json:"id"`
	Root          string    `json:"root"`
	JobSHA256     string    `json:"job_sha256"`
	ImageSHA256   string    `json:"image_sha256"`
	ContainerName string    `json:"container_name"`
	CreatedAt     time.Time `json:"created_at"`
	Deadline      time.Time `json:"deadline"`
	SHA256        string    `json:"sha256"`
}
type preStart struct {
	AcquisitionSHA256 string
	Snapshot          snapshot
	SHA256            string
}
type Receipt struct {
	Schema              string    `json:"schema"`
	AcquisitionSHA256   string    `json:"acquisition_sha256"`
	ContainerID         string    `json:"container_id,omitempty"`
	ImageSHA256         string    `json:"image_sha256"`
	Status              string    `json:"status"`
	ReasonCode          string    `json:"reason_code"`
	ExitCode            int       `json:"exit_code"`
	FinishedAt          time.Time `json:"finished_at,omitzero"`
	CollectedAt         time.Time `json:"collected_at"`
	ProfileVerified     bool      `json:"profile_verified"`
	HostDeadlineMet     bool      `json:"host_deadline_met"`
	OutputComplete      bool      `json:"output_complete"`
	StdoutSHA256        string    `json:"stdout_sha256"`
	StdoutBytes         int       `json:"stdout_bytes"`
	StderrSHA256        string    `json:"stderr_sha256"`
	StderrBytes         int       `json:"stderr_bytes"`
	WorkerResultSHA256  string    `json:"worker_result_sha256,omitempty"`
	WorkerStatus        string    `json:"worker_status,omitempty"`
	Authority           string    `json:"authority"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}

func (a Acquisition) validate(root string, j workerplan.Job, approved string) error {
	expected := a.SHA256
	a.SHA256 = ""
	if a.Schema != "mantik-worker-acquisition-v1" || a.Root != root || !launchID.MatchString(a.ID) || a.ContainerName != "mantik-worker-"+strings.ToLower(a.ID) || a.JobSHA256 != j.SHA256 || a.ImageSHA256 != approved || approved != j.ImageSHA256 || a.CreatedAt.IsZero() || a.CreatedAt.Year() < 1 || a.CreatedAt.Year() > 9999 || !a.Deadline.Equal(a.CreatedAt.Add(workerplan.WallSeconds*time.Second)) || expected != workerplan.Hash(a) {
		return ErrConflict
	}
	return nil
}
func load(root, approved string) (Acquisition, workerplan.Job, error) {
	raw, err := readFile(root, "job.json", workerplan.MaxJobBytes)
	if err != nil {
		return Acquisition{}, workerplan.Job{}, err
	}
	j, err := workerplan.ReadJob(bytes.NewReader(raw))
	if err != nil {
		return Acquisition{}, j, err
	}
	raw, err = readFile(root, "acquisition.json", 8192)
	if err != nil {
		return Acquisition{}, j, err
	}
	var a Acquisition
	if json.Unmarshal(raw, &a) != nil || a.validate(root, j, approved) != nil {
		return a, j, ErrConflict
	}
	return a, j, nil
}
func Launch(ctx context.Context, root string, j workerplan.Job, input io.Reader, approved string) (Receipt, error) {
	return launch(ctx, root, j, input, approved, nil)
}
func launch(ctx context.Context, root string, j workerplan.Job, input io.Reader, approved string, d driver) (Receipt, error) {
	if ctx.Err() != nil {
		return Receipt{}, ctx.Err()
	}
	if j.Validate() != nil || approved != j.ImageSHA256 {
		return Receipt{}, ErrConflict
	}
	raw, err := io.ReadAll(io.LimitReader(input, workerplan.MaxInputBytes+1))
	if err != nil || len(raw) != j.Input.Bytes || workerplan.Digest(raw) != j.Input.SHA256 {
		return Receipt{}, ErrConflict
	}
	root, err = directory(root, true)
	if err != nil {
		return Receipt{}, err
	}
	unlock, err := lock(ctx, root)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	if err = os.Mkdir(root+"/input", 0755); err != nil {
		return Receipt{}, err
	}
	if err = os.Mkdir(root+"/docker-config", 0700); err != nil {
		return Receipt{}, err
	}
	if err = os.Chmod(root+"/input", 0755); err != nil {
		return Receipt{}, err
	}
	jobRaw, _ := json.Marshal(j)
	for _, file := range []struct {
		directory, name string
		raw             []byte
		mode            os.FileMode
	}{{root, "job.json", jobRaw, 0600}, {root + "/input", "job.json", jobRaw, 0444}, {root + "/input", "dataset.json", raw, 0444}} {
		if err = writeOnce(file.directory, file.name, file.raw, file.mode); err != nil {
			return Receipt{}, err
		}
	}
	a := Acquisition{Schema: "mantik-worker-acquisition-v1", ID: rand.Text(), Root: root, JobSHA256: j.SHA256, ImageSHA256: approved, CreatedAt: time.Now().UTC()}
	a.ContainerName = "mantik-worker-" + strings.ToLower(a.ID)
	a.Deadline = a.CreatedAt.Add(workerplan.WallSeconds * time.Second)
	a.SHA256 = workerplan.Hash(a)
	if err = writeJSON(root, "acquisition.json", a); err != nil {
		return Receipt{}, err
	}
	if d == nil {
		d = dockerCLI{root + "/docker-config"}
	}
	work, cancel := context.WithDeadline(ctx, a.Deadline)
	defer cancel()
	control, stop := context.WithTimeout(work, 5*time.Second)
	out, createStderr, createErr := d.command(control, createArgs(a, j)...)
	stop()
	if createErr != nil {
		_ = writeOnce(root, "create.stdout", out, 0600)
		_ = writeOnce(root, "create.stderr", createStderr, 0600)
		return Receipt{}, errors.New("container creation uncertain; reconcile the retained attempt")
	}
	id := strings.TrimSpace(string(out))
	if !containerID.MatchString(id) {
		return Receipt{}, ErrConflict
	}
	control, stop = context.WithTimeout(work, 5*time.Second)
	s, err := inspect(control, d, a)
	stop()
	if err != nil {
		return Receipt{}, err
	}
	if s.ID != id || verify(s, a, j) != nil || s.State.Running || s.State.Pid != 0 || s.State.Status != "created" {
		return Receipt{}, ErrConflict
	}
	pre := preStart{AcquisitionSHA256: a.SHA256, Snapshot: s}
	pre.SHA256 = workerplan.Hash(pre)
	if err = writeJSON(root, "pre-start.json", pre); err != nil {
		return Receipt{}, err
	}
	control, stop = context.WithTimeout(work, 5*time.Second)
	startOut, startStderr, startErr := d.command(control, "start", id)
	stop()
	if startErr != nil {
		_ = writeOnce(root, "start.stdout", startOut, 0600)
		_ = writeOnce(root, "start.stderr", startStderr, 0600)
	}
	if startErr == nil {
		_, _, _ = d.command(work, "wait", id)
	}
	reason := ""
	if ctx.Err() != nil {
		reason = "cancelled"
	} else if !time.Now().Before(a.Deadline) {
		reason = "timed_out"
	}
	return collect(root, a, j, d, reason, true)
}
func Reconcile(ctx context.Context, root, approved string, cancel bool) (Receipt, error) {
	return reconcile(ctx, root, approved, cancel, nil)
}
func reconcile(ctx context.Context, root, approved string, cancel bool, d driver) (Receipt, error) {
	root, err := directory(root, false)
	if err != nil {
		return Receipt{}, err
	}
	unlock, err := lock(ctx, root)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	a, j, err := load(root, approved)
	if err != nil {
		return Receipt{}, err
	}
	if d == nil {
		d = dockerCLI{root + "/docker-config"}
	}
	reason := ""
	if cancel {
		reason = "cancelled"
	}
	return collect(root, a, j, d, reason, false)
}
func collect(root string, a Acquisition, j workerplan.Job, d driver, reason string, allowStop bool) (Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := inspect(ctx, d, a)
	if err != nil {
		return Receipt{}, err
	} // Missing/uncertain handles remain unresolved.
	if verify(s, a, j) != nil {
		return Receipt{}, ErrConflict
	}
	_, receiptErr := os.Lstat(root + "/receipt.json")
	wasRunning := s.State.Running
	if s.State.Running {
		if receiptErr == nil {
			return Receipt{}, ErrConflict
		}
		if reason == "" && !time.Now().Before(a.Deadline) {
			reason = "timed_out"
		}
		if !allowStop && reason == "" {
			return Receipt{}, ErrRunning
		}
		if reason == "" {
			reason = "wait_interrupted"
		}
		if _, _, err = d.command(ctx, "kill", "--signal", "KILL", s.ID); err != nil {
			return Receipt{}, errors.New("owned worker stop uncertain; reconcile again")
		}
		s, err = inspect(ctx, d, a)
		if err != nil || s.State.Running || s.State.Pid != 0 || verify(s, a, j) != nil {
			return Receipt{}, ErrConflict
		}
	}
	if !wasRunning && reason == "cancelled" {
		reason = ""
	}
	if reason == "timed_out" && !s.State.FinishedAt.IsZero() && s.State.FinishedAt.Before(a.Deadline) {
		reason = ""
	}
	if reason == "" && !s.State.FinishedAt.IsZero() && !s.State.FinishedAt.Before(a.Deadline) {
		reason = "timed_out"
	}
	if s.State.Pid != 0 {
		return Receipt{}, ErrConflict
	}
	if raw, err := readFile(root, "receipt.json", 16384); err == nil {
		var r Receipt
		expected := ""
		if json.Unmarshal(raw, &r) == nil {
			expected = r.SHA256
			r.SHA256 = ""
		}
		if expected == "" || workerplan.Hash(r) != expected || r.AcquisitionSHA256 != a.SHA256 || r.ContainerID != s.ID || r.ImageSHA256 != s.Image || !r.FinishedAt.Equal(s.State.FinishedAt) || r.ExitCode != s.State.ExitCode {
			return Receipt{}, ErrConflict
		}
		if r.Schema != "mantik-worker-host-receipt-v1" || r.Authority != "synthetic_host_launch_receipt_only" || r.QualificationCredit || r.StdoutBytes < 0 || r.StdoutBytes > workerplan.MaxResultBytes || r.StderrBytes < 0 || r.StderrBytes > 64<<10 {
			return Receipt{}, ErrConflict
		}
		r.SHA256 = expected
		for _, artifact := range []struct {
			name, sha string
			bytes     int
		}{{"stdout.json", r.StdoutSHA256, r.StdoutBytes}, {"stderr.txt", r.StderrSHA256, r.StderrBytes}} {
			body, err := readFile(root, artifact.name, int64(artifact.bytes))
			if err != nil || len(body) != artifact.bytes || workerplan.Digest(body) != artifact.sha {
				return Receipt{}, ErrConflict
			}
		}
		return r, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, err
	}
	r := Receipt{Schema: "mantik-worker-host-receipt-v1", AcquisitionSHA256: a.SHA256, ContainerID: s.ID, ImageSHA256: s.Image, Status: "incomplete", ReasonCode: "no_valid_worker_result", ExitCode: s.State.ExitCode, FinishedAt: s.State.FinishedAt, CollectedAt: time.Now().UTC(), Authority: "synthetic_host_launch_receipt_only"}
	if raw, err := readFile(root, "pre-start.json", 65536); err == nil {
		var pre preStart
		if json.Unmarshal(raw, &pre) == nil {
			expected := pre.SHA256
			pre.SHA256 = ""
			r.ProfileVerified = expected == workerplan.Hash(pre) && pre.AcquisitionSHA256 == a.SHA256 && pre.Snapshot.ID == s.ID && pre.Snapshot.State.Status == "created" && !pre.Snapshot.State.Running && verify(pre.Snapshot, a, j) == nil
		}
	}
	r.HostDeadlineMet = !s.State.FinishedAt.IsZero() && !s.State.FinishedAt.Before(a.CreatedAt) && s.State.FinishedAt.Before(a.Deadline)
	out, stderr, captureErr := d.command(ctx, "logs", s.ID)
	r.OutputComplete = captureErr == nil
	r.StdoutSHA256, r.StdoutBytes = workerplan.Digest(out), len(out)
	r.StderrSHA256, r.StderrBytes = workerplan.Digest(stderr), len(stderr)
	if err = writeOnce(root, "stdout.json", out, 0600); err != nil {
		return Receipt{}, err
	}
	if err = writeOnce(root, "stderr.txt", stderr, 0600); err != nil {
		return Receipt{}, err
	}
	result, resultErr := workerplan.ReadResult(bytes.NewReader(out), j)
	if resultErr == nil {
		r.WorkerResultSHA256 = result.SHA256
		r.WorkerStatus = result.Status
	}
	if resultErr == nil && r.OutputComplete && r.ProfileVerified && r.HostDeadlineMet && result.DeadlineMet {
		if s.State.ExitCode == 0 && result.Status == "completed" {
			r.Status, r.ReasonCode = "completed", "validated_worker_result"
		} else if s.State.ExitCode == 1 && result.Status == "failed" {
			r.Status, r.ReasonCode = "failed", result.ErrorCode
		}
	}
	if !r.ProfileVerified {
		r.Status, r.ReasonCode = "incomplete", "missing_pre_start_receipt"
	}
	if reason != "" {
		r.Status, r.ReasonCode = reason, reason
	}
	r.SHA256 = workerplan.Hash(r)
	if err = writeJSON(root, "receipt.json", r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}
