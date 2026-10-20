// Package candidateattempt binds pre-execution worker intents to retained
// candidate registrations. An intent never proves that a process was launched.
package candidateattempt

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-candidate-worker-intent-v1"
const MaxAttempts = 64
const MaxBytes = candidateexec.MaxJobBytes + 4096

var ErrInvalid = errors.New("invalid candidate worker intent or history")
var ErrConflict = errors.New("candidate worker intent identity, registration, job or clock conflict")
var ErrCapacity = errors.New("candidate worker intent capacity reached")
var ErrNotFound = errors.New("candidate worker intent or registration not found")
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Command struct {
	ReceiverID         string            `json:"receiver_id"`
	RequestID          string            `json:"request_id"`
	RegistrationID     string            `json:"registration_id"`
	RegistrationSHA256 string            `json:"registration_sha256"`
	Job                candidateexec.Job `json:"job"`
}

type Intent struct {
	Schema              string    `json:"schema"`
	Command             Command   `json:"command"`
	Sequence            int       `json:"sequence"`
	CreatedAt           time.Time `json:"created_at"`
	Status              string    `json:"status"`
	Authority           string    `json:"authority"`
	ExecutionStarted    bool      `json:"execution_started"`
	HostStateKnown      bool      `json:"host_state_known"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}

func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.RegistrationID) || !digest.MatchString(c.RegistrationSHA256) || c.Job.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

func hash(i Intent) string { i.SHA256 = ""; return workerplan.Hash(i) }

func (i Intent) Validate() error {
	if i.Schema != Schema || i.Command.Validate() != nil || i.Sequence < 1 || i.Sequence > MaxAttempts || !validTime(i.CreatedAt) || i.Status != "intent_retained" || i.Authority != "candidate_worker_intent_only" || i.ExecutionStarted || i.HostStateKnown || i.QualificationCredit || i.SHA256 != hash(i) {
		return ErrInvalid
	}
	raw, err := json.Marshal(i)
	if err != nil || len(raw) > MaxBytes {
		return ErrInvalid
	}
	return nil
}

// Binding checks the immutable registration and job identities. Dataset bytes
// must also be checked with VerifyDataset before a new intent is retained or read.
func (c Command) Binding(r candidatelineage.Registration) error {
	if c.Validate() != nil || r.Validate() != nil || c.ReceiverID != r.ReceiverID || c.RegistrationID != r.ID || c.RegistrationSHA256 != r.SHA256 || !reflect.DeepEqual(c.Job.Plan.Candidate, r.Candidate) {
		return ErrConflict
	}
	return nil
}

func Start(c Command, r candidatelineage.Registration, raw []byte, sequence int, at time.Time) (Intent, error) {
	if c.Validate() != nil || sequence < 1 || sequence > MaxAttempts {
		return Intent{}, ErrInvalid
	}
	if c.Binding(r) != nil || c.Job.Plan.VerifyDataset(raw) != nil || !validTime(at) || at.Before(r.RegisteredAt) {
		return Intent{}, ErrConflict
	}
	encoded, _ := json.Marshal(c)
	var detached Command
	_ = json.Unmarshal(encoded, &detached)
	i := Intent{Schema: Schema, Command: detached, Sequence: sequence, CreatedAt: at.UTC(), Status: "intent_retained", Authority: "candidate_worker_intent_only"}
	i.SHA256 = hash(i)
	return i, i.Validate()
}

func (i Intent) VerifyBinding(r candidatelineage.Registration) error {
	if i.Validate() != nil || i.Command.Binding(r) != nil || i.CreatedAt.Before(r.RegisteredAt) {
		return ErrInvalid
	}
	return nil
}

func ValidateHistory(receiverID string, all []Intent) error {
	if !receiver.MatchString(receiverID) || len(all) > MaxAttempts {
		return ErrInvalid
	}
	seen := map[string]bool{}
	var last time.Time
	for n, i := range all {
		if i.Validate() != nil || i.Command.ReceiverID != receiverID || i.Sequence != n+1 || seen[i.Command.RequestID] || i.CreatedAt.Before(last) {
			return ErrInvalid
		}
		seen[i.Command.RequestID], last = true, i.CreatedAt
	}
	return nil
}

// Append validates the whole prefix before retries. It retains another physical
// attempt only under a distinct request ID, even for an identical logical job.
func Append(all []Intent, c Command, r candidatelineage.Registration, raw []byte, now func() time.Time) (Intent, error) {
	if c.Validate() != nil || ValidateHistory(c.ReceiverID, all) != nil {
		return Intent{}, ErrInvalid
	}
	for _, i := range all {
		if i.Command.RequestID == c.RequestID {
			if !reflect.DeepEqual(i.Command, c) || i.VerifyBinding(r) != nil || c.Job.Plan.VerifyDataset(raw) != nil {
				return Intent{}, ErrConflict
			}
			encoded, _ := json.Marshal(i)
			var detached Intent
			_ = json.Unmarshal(encoded, &detached)
			return detached, nil
		}
	}
	if len(all) == MaxAttempts {
		return Intent{}, ErrCapacity
	}
	at := now()
	if len(all) > 0 && at.Before(all[len(all)-1].CreatedAt) {
		return Intent{}, ErrConflict
	}
	return Start(c, r, raw, len(all)+1, at)
}

func Read(input io.Reader) (Command, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	var c Command
	if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxBytes) != nil || c.Validate() != nil {
		return Command{}, ErrInvalid
	}
	return c, nil
}
