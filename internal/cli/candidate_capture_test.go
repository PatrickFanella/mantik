package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// A consistent caller-reported incomplete receipt, without host attestation.
func cliCaptureFixture(t *testing.T) workerlaunch.CandidateCapture {
	t.Helper()
	d, _ := dataset.FixtureDataset()
	data, _ := json.Marshal(d)
	image := "sha256:" + strings.Repeat("a", 64)
	c, err := candidate.New([]byte("invalid source remains declared"), hypothesis.Fixture(), data, image)
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(c, data, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(p, data)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := candidateattempt.Intent{Schema: candidateattempt.Schema, Command: candidateattempt.Command{ReceiverID: "receiver-abcdefghijklmnop", RequestID: "one", RegistrationID: "root", RegistrationSHA256: strings.Repeat("b", 64), Job: j}, Sequence: 1, CreatedAt: at, Status: "intent_retained", Authority: "candidate_worker_intent_only"}
	s.SHA256 = workerplan.Hash(s)
	return cliCaptureForIntent(t, s)
}

// Construct a synthetic caller report from this intent; no worker was executed.
func cliCaptureForIntent(t *testing.T, s candidateattempt.Intent) workerlaunch.CandidateCapture {
	t.Helper()
	at, j, image := s.CreatedAt, s.Command.Job, s.Command.Job.Plan.Candidate.Toolchain.ImageSHA256
	i := workerlaunch.CandidateIntent{Schema: "mantik-registered-candidate-attempt-intent-v1", JournalID: "candidate-journal-ABCDEFGHIJKLMNOP", RequestID: s.Command.RequestID, Sequence: 1, Job: j, ServiceIntent: &s, CreatedAt: at.Add(time.Second), Authority: "local_candidate_attempt_intent_only"}
	i.SHA256 = workerplan.Hash(i)
	root := filepath.Join(t.TempDir(), "unavailable-journal")
	a := workerlaunch.Acquisition{Schema: "mantik-candidate-behavior-acquisition-v1", ID: "captured-owner", Root: filepath.Join(root, "attempt-"+s.Command.RequestID), JobSHA256: j.SHA256, ImageSHA256: image, ContainerName: "mantik-worker-captured-owner", CreatedAt: at.Add(2 * time.Second), Deadline: at.Add(32 * time.Second)}
	a.SHA256 = workerplan.Hash(a)
	stdout, stderr := []byte{0xff, 0, 0xfe}, []byte{'e', 0, 0xff, '\n'}
	r := workerlaunch.Receipt{Schema: "mantik-candidate-behavior-host-receipt-v1", AcquisitionSHA256: a.SHA256, ContainerID: strings.Repeat("c", 64), ImageSHA256: image, Status: "incomplete", ReasonCode: "missing_pre_start_receipt", ExitCode: 1, FinishedAt: at.Add(3 * time.Second), CollectedAt: at.Add(4 * time.Second), HostDeadlineMet: true, OutputComplete: true, StdoutSHA256: workerplan.Digest(stdout), StdoutBytes: len(stdout), StderrSHA256: workerplan.Digest(stderr), StderrBytes: len(stderr), Authority: "synthetic_candidate_behavior_host_receipt_only"}
	r.SHA256 = workerplan.Hash(r)
	o := workerlaunch.CandidateOutcome{Schema: "mantik-candidate-attempt-outcome-v1", IntentSHA256: i.SHA256, CapturedAt: at.Add(5 * time.Second), Receipt: r, Authority: "local_candidate_host_receipt_capture_only"}
	o.SHA256 = workerplan.Hash(o)
	capture := workerlaunch.CandidateCapture{Schema: workerlaunch.CandidateCaptureSchema, JournalRoot: root, Attempt: workerlaunch.CandidateAttempt{Intent: i, Outcome: &o}, Acquisition: a, PreStart: []byte{}, Stdout: stdout, Stderr: stderr, Authority: "caller_reported_candidate_outcome_capture_only"}
	capture.SHA256 = workerplan.Hash(capture)
	if capture.Validate() != nil {
		t.Fatal("invalid caller-reported fixture")
	}
	return capture
}

func TestCandidateCaptureCLIExactOutputAndPins(t *testing.T) {
	c := cliCaptureFixture(t)
	raw, _ := json.Marshal(c)
	file := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	i := c.Attempt.Intent.ServiceIntent
	base := []string{"candidate-capture-inspect", "--capture", file, "--capture-sha256", c.SHA256, "--receiver-id", i.Command.ReceiverID, "--request-id", i.Command.RequestID, "--intent-sha256", i.SHA256}
	for artifact, expected := range map[string][]byte{"stdout": c.Stdout, "stderr": c.Stderr} {
		var out, diag bytes.Buffer
		if err := Run(context.Background(), append(append([]string{}, base...), "--artifact", artifact), &out, &diag); err != nil || !bytes.Equal(out.Bytes(), expected) {
			t.Fatal("raw artifact changed", artifact, err, out.Bytes())
		}
	}
	var out, diag bytes.Buffer
	if err := Run(context.Background(), base, &out, &diag); err != nil {
		t.Fatal(err)
	}
	var read workerlaunch.CandidateCapture
	if json.Unmarshal(out.Bytes(), &read) != nil || !reflect.DeepEqual(c, read) {
		t.Fatal("capture changed")
	}
	for _, flag := range []string{"--capture-sha256", "--receiver-id", "--request-id", "--intent-sha256"} {
		args := append([]string{}, base...)
		for n := range args {
			if args[n] == flag {
				args[n+1] = strings.Repeat("d", 64)
			}
		}
		out.Reset()
		if err := Run(context.Background(), args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("wrong pin emitted output", flag)
		}
	}
	for _, encoded := range [][]byte{
		append(append([]byte{}, raw...), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"schema":`, `"unknown":true,"schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)),
	} {
		if err := os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := Run(context.Background(), base, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("ambiguous capture emitted output")
		}
	}
	for _, args := range [][]string{
		{"candidate-worker-capture"}, {"candidate-capture-inspect"},
		{"candidate-worker-capture", "--cancel"}, {"candidate-worker-capture", "--api-url", "http://127.0.0.1:1"},
		append(append([]string{}, base...), "unexpected"), append(append([]string{}, base...), "--artifact", "job"),
	} {
		out.Reset()
		if err := Run(context.Background(), args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("invalid command accepted", args)
		}
	}
	for _, command := range []string{"candidate-worker-capture", "candidate-capture-inspect"} {
		if err := Run(context.Background(), []string{command, "--help"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
	}
}
