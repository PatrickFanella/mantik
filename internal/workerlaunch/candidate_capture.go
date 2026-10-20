package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const CandidateCaptureSchema = "mantik-candidate-outcome-capture-v1"
const MaxCandidateCaptureBytes = maxIntentBytes + 4*((workerplan.MaxResultBytes+2)/3) + 2*(4*(((64<<10)+2)/3)) + (64 << 10)

// CandidateCapture is a portable caller-reported copy of retained artifacts.
// It cannot establish that its submitter inspected or controlled a real host.
type CandidateCapture struct {
	Schema                string           `json:"schema"`
	JournalRoot           string           `json:"journal_root"`
	Attempt               CandidateAttempt `json:"attempt"`
	Acquisition           Acquisition      `json:"acquisition"`
	PreStart              []byte           `json:"pre_start"`
	Stdout                []byte           `json:"stdout"`
	Stderr                []byte           `json:"stderr"`
	Authority             string           `json:"authority"`
	ExecutionVerified     bool             `json:"execution_verified"`
	CurrentHostStateKnown bool             `json:"current_host_state_known"`
	QualificationCredit   bool             `json:"qualification_credit"`
	SHA256                string           `json:"sha256"`
}

func validateCandidateOutcomeEvidence(i CandidateIntent, o CandidateOutcome, a Acquisition, stdout, stderr []byte) error {
	if i.validate() != nil || o.Schema != "mantik-candidate-attempt-outcome-v1" || o.IntentSHA256 != i.SHA256 || o.CapturedAt.Before(i.CreatedAt) || o.CapturedAt.Before(o.Receipt.CollectedAt) || o.CapturedAt.Year() < 1 || o.CapturedAt.Year() > 9999 || o.Authority != "local_candidate_host_receipt_capture_only" || o.CurrentHostStateKnown || o.QualificationCredit {
		return ErrConflict
	}
	sha := o.SHA256
	o.SHA256 = ""
	if sha != workerplan.Hash(o) || a.validateExecution(a.Root, behaviorExecution(i.Job), i.Job.Plan.Candidate.Toolchain.ImageSHA256) != nil {
		return ErrConflict
	}
	r := o.Receipt
	sha = r.SHA256
	r.SHA256 = ""
	if sha != workerplan.Hash(r) || r.Schema != "mantik-candidate-behavior-host-receipt-v1" || r.Authority != "synthetic_candidate_behavior_host_receipt_only" || r.QualificationCredit || r.AcquisitionSHA256 != a.SHA256 || r.ImageSHA256 != a.ImageSHA256 || !containerID.MatchString(r.ContainerID) {
		return ErrConflict
	}
	switch r.Status {
	case "completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted":
	default:
		return ErrConflict
	}
	if len(stdout) > workerplan.MaxResultBytes || len(stderr) > 64<<10 || len(stdout) != r.StdoutBytes || len(stderr) != r.StderrBytes || workerplan.Digest(stdout) != r.StdoutSHA256 || workerplan.Digest(stderr) != r.StderrSHA256 {
		return ErrConflict
	}
	result, resultErr := candidateexec.ReadResult(bytes.NewReader(stdout), i.Job)
	if resultErr == nil {
		if r.WorkerResultSHA256 != result.SHA256 || r.WorkerStatus != result.Status {
			return ErrConflict
		}
	} else if r.WorkerResultSHA256 != "" || r.WorkerStatus != "" {
		return ErrConflict
	}
	if r.Status == "completed" || r.Status == "mismatch" || r.Status == "failed" {
		if resultErr != nil || result.Status != r.Status || result.ReasonCode != r.ReasonCode || !r.OutputComplete || !r.ProfileVerified || !r.HostDeadlineMet || r.ExitCode != 1 && r.Status != "completed" || r.Status == "completed" && r.ExitCode != 0 {
			return ErrConflict
		}
	}
	return nil
}

func (c CandidateCapture) Validate() error {
	i, o, a := c.Attempt.Intent, c.Attempt.Outcome, c.Acquisition
	if c.Schema != CandidateCaptureSchema || !filepath.IsAbs(c.JournalRoot) || filepath.Clean(c.JournalRoot) != c.JournalRoot || i.ServiceIntent == nil || o == nil || a.Root != attemptPath(c.JournalRoot, i.RequestID) || c.Authority != "caller_reported_candidate_outcome_capture_only" || c.ExecutionVerified || c.CurrentHostStateKnown || c.QualificationCredit || len(c.PreStart) > 64<<10 || c.Stdout == nil || c.Stderr == nil || c.PreStart == nil {
		return ErrConflict
	}
	if validateCandidateOutcomeEvidence(i, *o, a, c.Stdout, c.Stderr) != nil || a.CreatedAt.Before(i.CreatedAt) || o.Receipt.CollectedAt.Before(a.CreatedAt) || o.Receipt.CollectedAt.IsZero() || o.Receipt.CollectedAt.Year() < 1 || o.Receipt.CollectedAt.Year() > 9999 {
		return ErrConflict
	}
	r := o.Receipt
	if !r.FinishedAt.IsZero() && (r.FinishedAt.Before(a.CreatedAt) || r.FinishedAt.After(r.CollectedAt)) {
		return ErrConflict
	}
	expectedDeadline := !r.FinishedAt.IsZero() && !r.FinishedAt.Before(a.CreatedAt) && r.FinishedAt.Before(a.Deadline)
	if r.HostDeadlineMet != expectedDeadline {
		return ErrConflict
	}
	var pre preStart
	verified := false
	if workerplan.ReadBoundedShape(c.PreStart, &pre, 64<<10) == nil {
		sha := pre.SHA256
		pre.SHA256 = ""
		verified = sha == workerplan.Hash(pre) && pre.AcquisitionSHA256 == a.SHA256 && pre.Snapshot.ID == r.ContainerID && pre.Snapshot.State.Status == "created" && !pre.Snapshot.State.Running && verifyExecution(pre.Snapshot, a, behaviorExecution(i.Job)) == nil
	}
	if r.ProfileVerified != verified {
		return ErrConflict
	}
	sha := c.SHA256
	c.SHA256 = ""
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxCandidateCaptureBytes || sha != workerplan.Hash(c) {
		return ErrConflict
	}
	return nil
}

// VerifyIntent must be given the receiving service's own retained intent, rather
// than trusting the receipt embedded in a caller's capture.
func (c CandidateCapture) VerifyIntent(expected candidateattempt.Intent) error {
	if expected.Validate() != nil || c.Validate() != nil || !reflect.DeepEqual(c.Attempt.Intent.ServiceIntent, &expected) {
		return ErrConflict
	}
	return nil
}

func ReadCandidateCapture(input io.Reader) (CandidateCapture, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxCandidateCaptureBytes+1))
	var c CandidateCapture
	if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxCandidateCaptureBytes) != nil || c.Validate() != nil {
		return CandidateCapture{}, ErrConflict
	}
	return c, nil
}

// CaptureRegisteredCandidate only reads retained evidence. It does not inspect,
// reconcile, launch or stop Docker handles, and cannot turn a pending row into an outcome.
func CaptureRegisteredCandidate(ctx context.Context, root string, expected candidateattempt.Intent) (CandidateCapture, error) {
	if expected.Validate() != nil || !launchID.MatchString(expected.Command.RequestID) {
		return CandidateCapture{}, ErrConflict
	}
	root, unlock, err := openCandidateJournal(ctx, root, false)
	if err != nil {
		return CandidateCapture{}, err
	}
	defer unlock()
	h, err := candidateHistoryLocked(root)
	if err != nil {
		return CandidateCapture{}, err
	}
	if h.ReceiverID != expected.Command.ReceiverID {
		return CandidateCapture{}, ErrConflict
	}
	for _, attempt := range h.Attempts {
		if attempt.Intent.RequestID != expected.Command.RequestID {
			continue
		}
		if !reflect.DeepEqual(attempt.Intent.ServiceIntent, &expected) {
			return CandidateCapture{}, ErrConflict
		}
		if attempt.Outcome == nil {
			return CandidateCapture{}, ErrUnresolved
		}
		path := attemptPath(root, expected.Command.RequestID)
		a, _, err := loadCandidateBehavior(path, expected.Command.Job.Plan.Candidate.Toolchain.ImageSHA256)
		if err != nil {
			return CandidateCapture{}, err
		}
		pre, err := readFile(path, "pre-start.json", 64<<10)
		if errors.Is(err, os.ErrNotExist) {
			pre, err = []byte{}, nil
		}
		if err != nil {
			return CandidateCapture{}, err
		}
		stdout, err := readFile(path, "stdout.json", workerplan.MaxResultBytes)
		if err != nil {
			return CandidateCapture{}, err
		}
		stderr, err := readFile(path, "stderr.txt", 64<<10)
		if err != nil {
			return CandidateCapture{}, err
		}
		c := CandidateCapture{Schema: CandidateCaptureSchema, JournalRoot: root, Attempt: attempt, Acquisition: a, PreStart: pre, Stdout: stdout, Stderr: stderr, Authority: "caller_reported_candidate_outcome_capture_only"}
		c.SHA256 = workerplan.Hash(c)
		return c, c.VerifyIntent(expected)
	}
	return CandidateCapture{}, os.ErrNotExist
}
