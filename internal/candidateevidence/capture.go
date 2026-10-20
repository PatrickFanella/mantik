// Package candidateevidence validates portable caller-reported candidate evidence.
// It owns value contracts only; validation does not access a host or execute code.
package candidateevidence

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerreceipt"
)

const MaxCandidateAttempts = 64
const MaxIntentBytes = candidateexec.MaxJobBytes + candidateattempt.MaxBytes + 8192

var ErrConflict = workerreceipt.ErrConflict
var journalIdentity = regexp.MustCompile(`^candidate-journal-[A-Z2-7]{16,64}$`)
var launchID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Acquisition = workerreceipt.Acquisition
type Receipt = workerreceipt.Receipt
type preStart = workerreceipt.PreStart

func attemptPath(root, id string) string { return filepath.Join(root, "attempt-"+id) }
func BehaviorProfile(j candidateexec.Job) workerreceipt.Profile {
	return workerreceipt.Profile{JobSHA256: j.SHA256, ImageSHA256: j.Plan.Candidate.Toolchain.ImageSHA256, AcquisitionSchema: "mantik-candidate-behavior-acquisition-v1", Resources: j.Resources, Entrypoint: []string{"/app/mantik"}, Args: []string{"candidate-behavior-execute", "--job", "/worker/job.json", "--job-sha256", j.SHA256}, Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, BehaviorTmp: true}
}

type Intent struct {
	Schema              string                   `json:"schema"`
	JournalID           string                   `json:"journal_id"`
	RequestID           string                   `json:"request_id"`
	Sequence            int                      `json:"sequence"`
	Job                 candidateexec.Job        `json:"job"`
	ServiceIntent       *candidateattempt.Intent `json:"service_intent,omitempty"`
	CreatedAt           time.Time                `json:"created_at"`
	Authority           string                   `json:"authority"`
	ExecutionStarted    bool                     `json:"execution_started"`
	QualificationCredit bool                     `json:"qualification_credit"`
	SHA256              string                   `json:"sha256"`
}

func (i Intent) Validate() error {
	schema := "mantik-candidate-attempt-intent-v1"
	if i.ServiceIntent != nil {
		schema = "mantik-registered-candidate-attempt-intent-v1"
		s := i.ServiceIntent
		if s.Validate() != nil || s.Command.RequestID != i.RequestID || !reflect.DeepEqual(s.Command.Job, i.Job) || i.CreatedAt.Before(s.CreatedAt) {
			return ErrConflict
		}
	}
	if i.Schema != schema || !journalIdentity.MatchString(i.JournalID) || !launchID.MatchString(i.RequestID) || i.Sequence < 1 || i.Sequence > MaxCandidateAttempts || i.Job.Validate() != nil || i.CreatedAt.IsZero() || i.CreatedAt.Year() < 1 || i.CreatedAt.Year() > 9999 || i.Authority != "local_candidate_attempt_intent_only" || i.ExecutionStarted || i.QualificationCredit {
		return ErrConflict
	}
	expected := i.SHA256
	i.SHA256 = ""
	if expected != workerplan.Hash(i) {
		return ErrConflict
	}
	return nil
}

type Outcome struct {
	Schema                string    `json:"schema"`
	IntentSHA256          string    `json:"intent_sha256"`
	CapturedAt            time.Time `json:"captured_at"`
	Receipt               Receipt   `json:"receipt"`
	Authority             string    `json:"authority"`
	CurrentHostStateKnown bool      `json:"current_host_state_known"`
	QualificationCredit   bool      `json:"qualification_credit"`
	SHA256                string    `json:"sha256"`
}
type Attempt struct {
	Intent  Intent   `json:"intent"`
	Outcome *Outcome `json:"outcome"`
}

const Schema = "mantik-candidate-outcome-capture-v1"
const MaxBytes = MaxIntentBytes + 4*((workerplan.MaxResultBytes+2)/3) + 2*(4*(((64<<10)+2)/3)) + (64 << 10)

// Capture is a portable caller-reported copy of retained artifacts.
// It cannot establish that its submitter inspected or controlled a real host.
type Capture struct {
	Schema                string      `json:"schema"`
	JournalRoot           string      `json:"journal_root"`
	Attempt               Attempt     `json:"attempt"`
	Acquisition           Acquisition `json:"acquisition"`
	PreStart              []byte      `json:"pre_start"`
	Stdout                []byte      `json:"stdout"`
	Stderr                []byte      `json:"stderr"`
	Authority             string      `json:"authority"`
	ExecutionVerified     bool        `json:"execution_verified"`
	CurrentHostStateKnown bool        `json:"current_host_state_known"`
	QualificationCredit   bool        `json:"qualification_credit"`
	SHA256                string      `json:"sha256"`
}

func ValidateOutcomeEvidence(i Intent, o Outcome, a Acquisition, stdout, stderr []byte) error {
	if i.Validate() != nil || o.Schema != "mantik-candidate-attempt-outcome-v1" || o.IntentSHA256 != i.SHA256 || o.CapturedAt.Before(i.CreatedAt) || o.CapturedAt.Before(o.Receipt.CollectedAt) || o.CapturedAt.Year() < 1 || o.CapturedAt.Year() > 9999 || o.Authority != "local_candidate_host_receipt_capture_only" || o.CurrentHostStateKnown || o.QualificationCredit {
		return ErrConflict
	}
	sha := o.SHA256
	o.SHA256 = ""
	if sha != workerplan.Hash(o) || a.Validate(a.Root, BehaviorProfile(i.Job), i.Job.Plan.Candidate.Toolchain.ImageSHA256) != nil {
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

func (c Capture) Validate() error {
	i, o, a := c.Attempt.Intent, c.Attempt.Outcome, c.Acquisition
	if c.Schema != Schema || !filepath.IsAbs(c.JournalRoot) || filepath.Clean(c.JournalRoot) != c.JournalRoot || i.ServiceIntent == nil || o == nil || a.Root != attemptPath(c.JournalRoot, i.RequestID) || c.Authority != "caller_reported_candidate_outcome_capture_only" || c.ExecutionVerified || c.CurrentHostStateKnown || c.QualificationCredit || len(c.PreStart) > 64<<10 || c.Stdout == nil || c.Stderr == nil || c.PreStart == nil {
		return ErrConflict
	}
	if ValidateOutcomeEvidence(i, *o, a, c.Stdout, c.Stderr) != nil || a.CreatedAt.Before(i.CreatedAt) || o.Receipt.CollectedAt.Before(a.CreatedAt) || o.Receipt.CollectedAt.IsZero() || o.Receipt.CollectedAt.Year() < 1 || o.Receipt.CollectedAt.Year() > 9999 {
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
		verified = sha == workerplan.Hash(pre) && pre.AcquisitionSHA256 == a.SHA256 && pre.Snapshot.ID == r.ContainerID && pre.Snapshot.State.Status == "created" && !pre.Snapshot.State.Running && workerreceipt.VerifyProfile(pre.Snapshot, a, BehaviorProfile(i.Job)) == nil
	}
	if r.ProfileVerified != verified {
		return ErrConflict
	}
	sha := c.SHA256
	c.SHA256 = ""
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxBytes || sha != workerplan.Hash(c) {
		return ErrConflict
	}
	return nil
}

// VerifyIntent must be given the receiving service's own retained intent, rather
// than trusting the receipt embedded in a caller's capture.
func (c Capture) VerifyIntent(expected candidateattempt.Intent) error {
	if expected.Validate() != nil || c.Validate() != nil || !reflect.DeepEqual(c.Attempt.Intent.ServiceIntent, &expected) {
		return ErrConflict
	}
	return nil
}

func Read(input io.Reader) (Capture, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	var c Capture
	if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxBytes) != nil || c.Validate() != nil {
		return Capture{}, ErrConflict
	}
	return c, nil
}
