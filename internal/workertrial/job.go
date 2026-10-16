// Package workertrial binds an external job declaration to a retained native
// trial. A declaration starts no process and grants no execution authority.
package workertrial

import (
	"encoding/json"
	"io"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-external-trial-job-v1"
const MaxJobs = 32

var hex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var image = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Command struct {
	ReceiverID         string `json:"receiver_id"`
	RequestID          string `json:"request_id"`
	AttemptID          string `json:"attempt_id"`
	NativeRunSHA256    string `json:"native_run_sha256"`
	ImageSHA256        string `json:"image_sha256"`
	AdapterSHA256      string `json:"adapter_sha256"`
	RequirementsSHA256 string `json:"requirements_sha256"`
}

func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.AttemptID) || !hex.MatchString(c.NativeRunSHA256) || !image.MatchString(c.ImageSHA256) || !hex.MatchString(c.AdapterSHA256) || !hex.MatchString(c.RequirementsSHA256) {
		return trial.ErrInvalid
	}
	return nil
}

type Record struct {
	Schema              string           `json:"schema"`
	Command             Command          `json:"command"`
	TrialID             string           `json:"trial_id"`
	HypothesisID        string           `json:"hypothesis_id"`
	RegistrationSHA256  string           `json:"registration_sha256"`
	NativeDeadlineMet   bool             `json:"native_deadline_met"`
	DeclaredAt          time.Time        `json:"declared_at"`
	Status              string           `json:"status"`
	ExecutionStarted    bool             `json:"execution_started"`
	HostStateKnown      bool             `json:"host_state_known"`
	Job                 externalplan.Job `json:"job"`
	Authority           string           `json:"authority"`
	QualificationCredit bool             `json:"qualification_credit"`
	SHA256              string           `json:"sha256"`
}

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}
func (r Record) Validate() error {
	c, j := r.Command, r.Job
	if r.Schema != Schema || c.Validate() != nil || !review.ID.MatchString(r.TrialID) || !hypothesis.QuestionID.MatchString(r.HypothesisID) || !hex.MatchString(r.RegistrationSHA256) || !validTime(r.DeclaredAt) || r.Status != "declared" || r.ExecutionStarted || r.HostStateKnown || r.Authority != "research_external_job_declaration_only" || r.QualificationCredit || j.Validate() != nil || j.ID != c.RequestID || j.ImageSHA256 != c.ImageSHA256 || j.AdapterSHA256 != c.AdapterSHA256 || j.RequirementsSHA256 != c.RequirementsSHA256 {
		return trial.ErrInvalid
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != trial.Hash(r) {
		return trial.ErrInvalid
	}
	return nil
}
func Build(c Command, registration trial.Registration, run trial.Run, input, result string, at time.Time) (Record, map[string][]byte, error) {
	if c.Validate() != nil {
		return Record{}, nil, trial.ErrInvalid
	}
	if registration.Validate() != nil || run.Validate() != nil || run.Status != "completed" || run.SHA256 != c.NativeRunSHA256 || run.AttemptID != c.AttemptID || run.ReceiverID != c.ReceiverID || registration.ReceiverID != c.ReceiverID || run.TrialID != registration.ID || run.RegistrationSHA256 != registration.SHA256 || run.RecipeSHA256 != registration.Recipe.SHA256 || !validTime(at) || at.Before(run.FinishedAt) || run.InputSHA256 != workerplan.Digest([]byte(input)) || run.ResultSHA256 != workerplan.Digest([]byte(result)) || run.ResultBytes != len(result) {
		return Record{}, nil, trial.ErrConflict
	}
	recipe, _ := json.Marshal(registration.Recipe)
	inputs := map[string][]byte{"dataset.json": []byte(input), "recipe.json": recipe, "native.json": []byte(result)}
	j, err := externalplan.Declare(c.RequestID, c.ImageSHA256, c.AdapterSHA256, c.RequirementsSHA256, inputs)
	if err != nil {
		return Record{}, nil, trial.ErrInvalid
	}
	if j.NativeReference.ID != run.NativeAttemptID {
		return Record{}, nil, trial.ErrConflict
	}
	r := Record{Schema: Schema, Command: c, TrialID: registration.ID, HypothesisID: registration.HypothesisID, RegistrationSHA256: registration.SHA256, NativeDeadlineMet: run.DeadlineMet, DeclaredAt: at.UTC(), Status: "declared", Job: j, Authority: "research_external_job_declaration_only"}
	r.SHA256 = trial.Hash(r)
	return r, inputs, r.Validate()
}
func Read(input io.Reader) (Command, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(raw) > 4096 {
		return Command{}, trial.ErrInvalid
	}
	var c Command
	if workerplan.ReadDatasetShape(raw, &c) != nil {
		return c, trial.ErrInvalid
	}
	return c, c.Validate()
}

type Artifact struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	SHA256    string `json:"sha256"`
	Content   string `json:"content"`
}

func Input(r Record, name, raw string) (Artifact, error) {
	if r.Validate() != nil {
		return Artifact{}, trial.ErrInvalid
	}
	for _, a := range r.Job.Inputs {
		if a.Reference == name {
			if len(raw) != a.Bytes || workerplan.Digest([]byte(raw)) != a.SHA256 {
				return Artifact{}, trial.ErrInvalid
			}
			return Artifact{r.Command.RequestID, name, a.SHA256, raw}, nil
		}
	}
	return Artifact{}, trial.ErrInvalid
}
