package workertrial

import (
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const AttemptSchema = "mantik-external-trial-attempt-v1"
const OutcomeSchema = "mantik-external-trial-outcome-v1"
const MaxAttempts = 64
const MaxStdout = 16 << 20
const MaxStderr = 64 << 10

// JSON escaping can expand each retained text byte sixfold.
const MaxReportBytes = 6*(MaxStdout+MaxStderr) + 4096

type AttemptCommand struct {
	ReceiverID        string `json:"receiver_id"`
	RequestID         string `json:"request_id"`
	JobID             string `json:"job_id"`
	DeclarationSHA256 string `json:"declaration_sha256"`
}

func (c AttemptCommand) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.JobID) || !hex.MatchString(c.DeclarationSHA256) {
		return trial.ErrInvalid
	}
	return nil
}

type Intent struct {
	Schema              string         `json:"schema"`
	Command             AttemptCommand `json:"command"`
	JobSHA256           string         `json:"job_sha256"`
	CreatedAt           time.Time      `json:"created_at"`
	Authority           string         `json:"authority"`
	ExecutionStarted    bool           `json:"execution_started"`
	HostStateKnown      bool           `json:"host_state_known"`
	QualificationCredit bool           `json:"qualification_credit"`
	SHA256              string         `json:"sha256"`
}

func (i Intent) Validate() error {
	expected := i.SHA256
	i.SHA256 = ""
	if i.Schema != AttemptSchema || i.Command.Validate() != nil || !hex.MatchString(i.JobSHA256) || !validTime(i.CreatedAt) || i.Authority != "research_external_attempt_intent_only" || i.ExecutionStarted || i.HostStateKnown || i.QualificationCredit || expected != trial.Hash(i) {
		return trial.ErrInvalid
	}
	return nil
}
func Start(c AttemptCommand, r Record, at time.Time) (Intent, error) {
	if c.Validate() != nil {
		return Intent{}, trial.ErrInvalid
	}
	if r.Validate() != nil || c.JobID != r.Command.RequestID || c.ReceiverID != r.Command.ReceiverID || c.DeclarationSHA256 != r.SHA256 || !validTime(at) || at.Before(r.DeclaredAt) {
		return Intent{}, trial.ErrConflict
	}
	i := Intent{Schema: AttemptSchema, Command: c, JobSHA256: r.Job.SHA256, CreatedAt: at.UTC(), Authority: "research_external_attempt_intent_only"}
	i.SHA256 = trial.Hash(i)
	return i, i.Validate()
}

// ReportedStatus is a caller statement. Neither retention time nor this status
// proves a process was launched, stopped, isolated or finished before a deadline.
type ReportCommand struct {
	ReceiverID     string `json:"receiver_id"`
	AttemptID      string `json:"attempt_id"`
	IntentSHA256   string `json:"intent_sha256"`
	ReportedStatus string `json:"reported_status"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
}

func (c ReportCommand) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.AttemptID) || !hex.MatchString(c.IntentSHA256) || !text(c.Stdout, MaxStdout) || !text(c.Stderr, MaxStderr) {
		return trial.ErrInvalid
	}
	switch c.ReportedStatus {
	case "completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted":
		return nil
	}
	return trial.ErrInvalid
}
func text(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

type Outcome struct {
	Schema                    string    `json:"schema"`
	AttemptID                 string    `json:"attempt_id"`
	IntentSHA256              string    `json:"intent_sha256"`
	RequestSHA256             string    `json:"request_sha256"`
	ReportedStatus            string    `json:"reported_status"`
	RetainedAt                time.Time `json:"retained_at"`
	StdoutSHA256              string    `json:"stdout_sha256"`
	StdoutBytes               int       `json:"stdout_bytes"`
	StderrSHA256              string    `json:"stderr_sha256"`
	StderrBytes               int       `json:"stderr_bytes"`
	ComparisonValid           bool      `json:"comparison_valid"`
	ComparisonSHA256          string    `json:"comparison_sha256,omitempty"`
	ComparisonStatus          string    `json:"comparison_status,omitempty"`
	AllComparableMetricsMatch *bool     `json:"all_comparable_metrics_match,omitempty"`
	Authority                 string    `json:"authority"`
	HostStateKnown            bool      `json:"host_state_known"`
	QualificationCredit       bool      `json:"qualification_credit"`
	SHA256                    string    `json:"sha256"`
}

func (o Outcome) Validate(i Intent) error {
	expected := o.SHA256
	o.SHA256 = ""
	c := ReportCommand{ReceiverID: i.Command.ReceiverID, AttemptID: o.AttemptID, IntentSHA256: o.IntentSHA256, ReportedStatus: o.ReportedStatus}
	if i.Validate() != nil || o.Schema != OutcomeSchema || c.Validate() != nil || o.AttemptID != i.Command.RequestID || o.IntentSHA256 != i.SHA256 || !hex.MatchString(o.RequestSHA256) || !validTime(o.RetainedAt) || o.RetainedAt.Before(i.CreatedAt) || !hex.MatchString(o.StdoutSHA256) || o.StdoutBytes < 0 || o.StdoutBytes > MaxStdout || !hex.MatchString(o.StderrSHA256) || o.StderrBytes < 0 || o.StderrBytes > MaxStderr || o.Authority != "caller_reported_external_attempt_only" || o.HostStateKnown || o.QualificationCredit || expected != trial.Hash(o) {
		return trial.ErrInvalid
	}
	if o.ComparisonValid {
		if !hex.MatchString(o.ComparisonSHA256) || o.AllComparableMetricsMatch == nil || (o.ComparisonStatus != "compared" && o.ComparisonStatus != "failed") || o.ComparisonStatus == "failed" && *o.AllComparableMetricsMatch {
			return trial.ErrInvalid
		}
	} else if o.ComparisonSHA256 != "" || o.ComparisonStatus != "" || o.AllComparableMetricsMatch != nil {
		return trial.ErrInvalid
	}
	if o.ReportedStatus == "completed" && (!o.ComparisonValid || o.ComparisonStatus != "compared" || !*o.AllComparableMetricsMatch) || o.ReportedStatus == "mismatch" && (!o.ComparisonValid || o.ComparisonStatus != "compared" || *o.AllComparableMetricsMatch) || o.ReportedStatus == "failed" && (!o.ComparisonValid || o.ComparisonStatus != "failed") {
		return trial.ErrInvalid
	}
	return nil
}
func Finish(i Intent, r Record, c ReportCommand, at time.Time) (Outcome, error) {
	if c.Validate() != nil {
		return Outcome{}, trial.ErrInvalid
	}
	if i.Validate() != nil || r.Validate() != nil || i.Command.JobID != r.Command.RequestID || i.Command.DeclarationSHA256 != r.SHA256 || i.JobSHA256 != r.Job.SHA256 || c.ReceiverID != i.Command.ReceiverID || c.AttemptID != i.Command.RequestID || c.IntentSHA256 != i.SHA256 || !validTime(at) || at.Before(i.CreatedAt) {
		return Outcome{}, trial.ErrConflict
	}
	o := Outcome{Schema: OutcomeSchema, AttemptID: c.AttemptID, IntentSHA256: i.SHA256, RequestSHA256: trial.Hash(c), ReportedStatus: c.ReportedStatus, RetainedAt: at.UTC(), StdoutSHA256: workerplan.Digest([]byte(c.Stdout)), StdoutBytes: len(c.Stdout), StderrSHA256: workerplan.Digest([]byte(c.Stderr)), StderrBytes: len(c.Stderr), Authority: "caller_reported_external_attempt_only"}
	if result, err := externalplan.ReadResult(strings.NewReader(c.Stdout), r.Job); err == nil {
		o.ComparisonValid = true
		o.ComparisonSHA256 = result.SHA256
		o.ComparisonStatus = result.Status
		o.AllComparableMetricsMatch = result.AllMatch
	}
	o.SHA256 = trial.Hash(o)
	return o, o.Validate(i)
}

type Attempt struct {
	Intent  Intent   `json:"intent"`
	Outcome *Outcome `json:"outcome"`
}

func (a Attempt) Validate() error {
	if a.Intent.Validate() != nil {
		return trial.ErrInvalid
	}
	if a.Outcome != nil {
		return a.Outcome.Validate(a.Intent)
	}
	return nil
}
func Output(a Attempt, name, raw string) (Artifact, error) {
	if a.Validate() != nil || a.Outcome == nil {
		return Artifact{}, trial.ErrConflict
	}
	sha, n := "", 0
	switch name {
	case "stdout":
		sha, n = a.Outcome.StdoutSHA256, a.Outcome.StdoutBytes
	case "stderr":
		sha, n = a.Outcome.StderrSHA256, a.Outcome.StderrBytes
	default:
		return Artifact{}, trial.ErrInvalid
	}
	if len(raw) != n || workerplan.Digest([]byte(raw)) != sha {
		return Artifact{}, trial.ErrInvalid
	}
	return Artifact{a.Intent.Command.RequestID, name, sha, raw}, nil
}
func ReadAttempt(input io.Reader) (AttemptCommand, error) {
	var c AttemptCommand
	err := readCommand(input, 4096, &c)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}
func ReadReport(input io.Reader) (ReportCommand, error) {
	var c ReportCommand
	err := readCommand(input, MaxReportBytes, &c)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}
func readCommand(input io.Reader, limit int, v any) error {
	raw, err := io.ReadAll(io.LimitReader(input, int64(limit)+1))
	if err != nil || len(raw) > limit || workerplan.ReadBoundedShape(raw, v, limit) != nil {
		return trial.ErrInvalid
	}
	return nil
}
