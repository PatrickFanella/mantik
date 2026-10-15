package trial

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const RunSchema = "mantik-native-trial-run-v1"
const InputID = "builtin-synthetic-dataset-v1"
const EngineID = "native-experiment-v1"
const MaxArtifactBytes = 8 << 20
const RunSeconds = 30

// Run is current execution state. Attempt remains the historical intent receipt.
type Run struct {
	Schema              string    `json:"schema"`
	AttemptID           string    `json:"attempt_id"`
	InvocationID        string    `json:"invocation_id"`
	NativeAttemptID     string    `json:"native_attempt_id"`
	TrialID             string    `json:"trial_id"`
	ReceiverID          string    `json:"receiver_id"`
	IntentSHA256        string    `json:"intent_sha256"`
	RegistrationSHA256  string    `json:"registration_sha256"`
	RecipeSHA256        string    `json:"recipe_sha256"`
	Engine              string    `json:"engine"`
	InputID             string    `json:"input_id"`
	InputSHA256         string    `json:"input_sha256"`
	StartedAt           time.Time `json:"started_at"`
	Deadline            time.Time `json:"deadline"`
	FinishedAt          time.Time `json:"finished_at,omitzero"`
	Status              string    `json:"status"`
	ErrorCode           string    `json:"error_code,omitempty"`
	DeadlineMet         bool      `json:"deadline_met"`
	ResultSHA256        string    `json:"result_sha256,omitempty"`
	ResultBytes         int       `json:"result_bytes"`
	QualificationCredit bool      `json:"qualification_credit"`
	Authority           string    `json:"authority"`
	SHA256              string    `json:"sha256"`
}
type RunArtifact struct {
	AttemptID string `json:"attempt_id"`
	Kind      string `json:"kind"`
	SHA256    string `json:"sha256"`
	Content   string `json:"content"`
}
type RunWork struct {
	Run          Run
	Registration Registration
	Input        string
	Acquired     bool
}

func artifactValid(raw string) bool {
	return len(raw) > 0 && len(raw) <= MaxArtifactBytes && utf8.ValidString(raw) && json.Valid([]byte(raw))
}
func BeginRun(a Attempt, r Registration, input, invocation string, at time.Time) (Run, error) {
	if a.Validate() != nil || r.Validate() != nil || a.Status != "pending" || a.TrialID != r.ID || a.ReceiverID != r.ReceiverID || a.RegistrationSHA256 != r.SHA256 || a.RecipeSHA256 != r.Recipe.SHA256 || !artifactValid(input) || !review.ID.MatchString(invocation) || !validTime(at) || !validTime(at.Add(RunSeconds*time.Second)) || at.Before(a.StartedAt) {
		return Run{}, ErrConflict
	}
	run := Run{Schema: RunSchema, AttemptID: a.ID, InvocationID: invocation, NativeAttemptID: "trial-" + news.SHA256(a.ID), TrialID: r.ID, ReceiverID: a.ReceiverID, IntentSHA256: a.IntentSHA256, RegistrationSHA256: r.SHA256, RecipeSHA256: r.Recipe.SHA256, Engine: EngineID, InputID: InputID, InputSHA256: news.SHA256(input), StartedAt: at.UTC(), Deadline: at.UTC().Add(RunSeconds * time.Second), Status: "running", Authority: "synthetic_research_execution_only"}
	run.SHA256 = Hash(run)
	return run, nil
}
func (r Run) Validate() error {
	if r.Schema != RunSchema || !review.ID.MatchString(r.AttemptID) || !review.ID.MatchString(r.InvocationID) || r.NativeAttemptID != "trial-"+news.SHA256(r.AttemptID) || r.Engine != EngineID || r.InputID != InputID || !digest.MatchString(r.InputSHA256) || !digest.MatchString(r.IntentSHA256) || !digest.MatchString(r.RegistrationSHA256) || !digest.MatchString(r.RecipeSHA256) || !validTime(r.StartedAt) || !validTime(r.Deadline) || !r.Deadline.Equal(r.StartedAt.Add(RunSeconds*time.Second)) || r.QualificationCredit || r.Authority != "synthetic_research_execution_only" {
		return ErrInvalid
	}
	switch r.Status {
	case "running":
		if !r.FinishedAt.IsZero() || r.ResultSHA256 != "" || r.ResultBytes != 0 || r.ErrorCode != "" || r.DeadlineMet {
			return ErrInvalid
		}
	case "completed", "failed":
		if !validTime(r.FinishedAt) || r.FinishedAt.Before(r.StartedAt) || !digest.MatchString(r.ResultSHA256) || r.ResultBytes < 1 || r.ResultBytes > MaxArtifactBytes || r.DeadlineMet != r.FinishedAt.Before(r.Deadline) || r.Status == "completed" && r.ErrorCode != "" || r.Status == "failed" && !review.ID.MatchString(r.ErrorCode) {
			return ErrInvalid
		}
	case "incomplete":
		if !validTime(r.FinishedAt) || r.FinishedAt.Before(r.Deadline) || r.ErrorCode != "no_retained_result_at_deadline" || r.ResultSHA256 != "" || r.ResultBytes != 0 || r.DeadlineMet {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != Hash(r) {
		return ErrInvalid
	}
	return nil
}

// FinishRun accepts only application-generated native results, never HTTP input.
func FinishRun(run Run, registration Registration, invocation, raw string, at time.Time) (Run, error) {
	if run.Validate() != nil || run.Status != "running" || run.InvocationID != invocation || registration.Validate() != nil || run.RegistrationSHA256 != registration.SHA256 || !artifactValid(raw) || !validTime(at) || at.Before(run.StartedAt) {
		return Run{}, ErrConflict
	}
	var result struct {
		ID            string `json:"id"`
		RecipeSHA256  string `json:"recipe_sha256"`
		InputSHA256   string `json:"input_sha256"`
		Status        string `json:"status"`
		ErrorCode     string `json:"error_code"`
		EvidenceClass string `json:"evidence_class"`
		Qualified     bool   `json:"qualified"`
		Results       []struct {
			MethodID     string `json:"method_id"`
			MethodSHA256 string `json:"method_sha256"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.ID != run.NativeAttemptID || result.RecipeSHA256 != run.RecipeSHA256 || result.InputSHA256 != run.InputSHA256 || result.EvidenceClass != "synthetic_unqualified" || result.Qualified || result.Status != "completed" && result.Status != "failed" {
		return Run{}, ErrInvalid
	}
	pins := append(append([]experimentplan.MethodPin{}, registration.Recipe.Methods...), registration.Recipe.Benchmarks...)
	if len(result.Results) > len(pins) || result.Status == "completed" && (len(result.Results) != len(pins) || result.ErrorCode != "") || result.Status == "failed" && !review.ID.MatchString(result.ErrorCode) {
		return Run{}, ErrInvalid
	}
	for i, method := range result.Results {
		if method.MethodID != pins[i].ID || method.MethodSHA256 != pins[i].SHA256 {
			return Run{}, ErrInvalid
		}
	}
	run.Status = result.Status
	run.ErrorCode = result.ErrorCode
	run.FinishedAt = at.UTC()
	run.DeadlineMet = at.Before(run.Deadline)
	run.ResultSHA256 = news.SHA256(raw)
	run.ResultBytes = len(raw)
	run.SHA256 = ""
	run.SHA256 = Hash(run)
	return run, nil
}

// ResolveRun records a missing durable result, not proof that computation stopped.
func ResolveRun(run Run, at time.Time) (Run, error) {
	if run.Validate() != nil {
		return Run{}, ErrInvalid
	}
	if run.Status == "incomplete" {
		return run, nil
	}
	if run.Status != "running" || !validTime(at) || at.Before(run.Deadline) {
		return Run{}, ErrConflict
	}
	run.Status = "incomplete"
	run.ErrorCode = "no_retained_result_at_deadline"
	run.FinishedAt = at.UTC()
	run.SHA256 = ""
	run.SHA256 = Hash(run)
	return run, nil
}
