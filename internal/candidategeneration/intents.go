package candidategeneration

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const IntentSchema = "mantik-candidate-generation-intent-v1"
const IntentHistorySchema = "mantik-candidate-generation-intent-history-v1"
const IntentArtifactSchema = "mantik-candidate-generation-intent-artifact-v1"
const MaxIntents = 64
const MaxIntentBytes = MaxPlanBytes + 4096
const MaxIntentHistoryBytes = MaxIntents*MaxIntentBytes + 4096

var ErrConflict = errors.New("candidate generation intent binding or clock conflict")
var ErrCapacity = errors.New("candidate generation intent capacity reached")
var ErrNotFound = errors.New("candidate generation intent or publication not found")
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)

type Command struct {
	ReceiverID        string `json:"receiver_id"`
	RequestID         string `json:"request_id"`
	PublicationID     string `json:"publication_id"`
	PublicationSHA256 string `json:"publication_sha256"`
	Plan              Plan   `json:"plan"`
}

func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.PublicationID) || !digest.MatchString(c.PublicationSHA256) || c.Plan.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

type Intent struct {
	Schema                string    `json:"schema"`
	Command               Command   `json:"command"`
	Sequence              int       `json:"sequence"`
	CreatedAt             time.Time `json:"created_at"`
	Status                string    `json:"status"`
	Authority             string    `json:"authority"`
	ProviderCalled        bool      `json:"provider_called"`
	ModelIdentityVerified bool      `json:"model_identity_verified"`
	SourceGenerated       bool      `json:"source_generated"`
	QualificationCredit   bool      `json:"qualification_credit"`
	SHA256                string    `json:"sha256"`
}

func (i Intent) Validate() error {
	if i.Schema != IntentSchema || i.Command.Validate() != nil || i.Sequence < 1 || i.Sequence > MaxIntents || !validTime(i.CreatedAt) || i.CreatedAt.Before(i.Command.Plan.RequestedAt) || i.Status != "intent_retained" || i.Authority != "candidate_generation_intent_only" || i.ProviderCalled || i.ModelIdentityVerified || i.SourceGenerated || i.QualificationCredit {
		return ErrInvalid
	}
	sha := i.SHA256
	i.SHA256 = ""
	raw, err := json.Marshal(i)
	if err != nil || len(raw) > MaxIntentBytes || sha != workerplan.Hash(i) {
		return ErrInvalid
	}
	return nil
}
func newIntent(c Command, p hypothesis.Publication, sequence int, at time.Time) (Intent, error) {
	if c.Validate() != nil {
		return Intent{}, ErrInvalid
	}
	sealed, err := hypothesis.Seal(p.Sealed.Declaration)
	if err != nil || !reflect.DeepEqual(sealed, p.Sealed) || p.Schema != hypothesis.PublicationSchema || p.ID != c.PublicationID || p.ReceiverID != c.ReceiverID || workerplan.Hash(p) != c.PublicationSHA256 || !p.OriginBindingVerified || !reflect.DeepEqual(c.Plan.Hypothesis, p.Sealed) || !validTime(p.PublishedAt) || c.Plan.RequestedAt.Before(p.PublishedAt) || !validTime(at) || at.Before(c.Plan.RequestedAt) {
		return Intent{}, ErrConflict
	}
	encoded, _ := json.Marshal(c)
	var detached Command
	_ = json.Unmarshal(encoded, &detached)
	i := Intent{Schema: IntentSchema, Command: detached, Sequence: sequence, CreatedAt: at.UTC(), Status: "intent_retained", Authority: "candidate_generation_intent_only"}
	i.SHA256 = workerplan.Hash(i)
	return i, i.Validate()
}
func (i Intent) VerifyBinding(p hypothesis.Publication) error {
	if i.Validate() != nil {
		return ErrInvalid
	}
	expected, err := newIntent(i.Command, p, i.Sequence, i.CreatedAt)
	if err != nil || expected.SHA256 != i.SHA256 {
		return ErrConflict
	}
	return nil
}
func ValidateIntents(receiverID string, all []Intent) error {
	if !receiver.MatchString(receiverID) || len(all) > MaxIntents {
		return ErrInvalid
	}
	seen := map[string]bool{}
	var last time.Time
	for n, i := range all {
		if i.Validate() != nil || i.Command.ReceiverID != receiverID || i.Sequence != n+1 || seen[i.Command.RequestID] || i.CreatedAt.Before(last) {
			return ErrInvalid
		}
		seen[i.Command.RequestID] = true
		last = i.CreatedAt
	}
	return nil
}

// AppendIntent validates a retained prefix supplied by its owner. Storage must
// validate all publication/dataset dependencies before calling this function.
// An exact retry never consults the clock, including after capacity is reached.
func AppendIntent(all []Intent, c Command, p hypothesis.Publication, raw []byte, now func() time.Time) (Intent, bool, error) {
	if c.Validate() != nil || ValidateIntents(c.ReceiverID, all) != nil || c.Plan.VerifyDataset(raw) != nil {
		return Intent{}, false, ErrInvalid
	}
	for _, i := range all {
		if i.Command.RequestID == c.RequestID {
			expected, err := newIntent(c, p, i.Sequence, i.CreatedAt)
			if err != nil || expected.SHA256 != i.SHA256 {
				return Intent{}, false, ErrConflict
			}
			return expected, true, nil
		}
	}
	if p.ID == "" {
		return Intent{}, false, ErrNotFound
	}
	if len(all) == MaxIntents {
		return Intent{}, false, ErrCapacity
	}
	at := now()
	if len(all) > 0 && at.Before(all[len(all)-1].CreatedAt) {
		return Intent{}, false, ErrConflict
	}
	i, err := newIntent(c, p, len(all)+1, at)
	return i, false, err
}

type IntentHistory struct {
	Schema              string   `json:"schema"`
	ReceiverID          string   `json:"receiver_id"`
	Intents             []Intent `json:"intents"`
	Authority           string   `json:"authority"`
	ProviderCalled      bool     `json:"provider_called"`
	SourceGenerated     bool     `json:"source_generated"`
	QualificationCredit bool     `json:"qualification_credit"`
	SHA256              string   `json:"sha256"`
}

func IntentsFor(receiverID string, all []Intent) (IntentHistory, error) {
	h := IntentHistory{Schema: IntentHistorySchema, ReceiverID: receiverID, Intents: append([]Intent{}, all...), Authority: "retained_candidate_generation_intents_only"}
	raw, _ := json.Marshal(h)
	var detached IntentHistory
	_ = json.Unmarshal(raw, &detached)
	detached.SHA256 = workerplan.Hash(detached)
	return detached, detached.Validate()
}
func (h IntentHistory) Validate() error {
	if h.Schema != IntentHistorySchema || h.Intents == nil || ValidateIntents(h.ReceiverID, h.Intents) != nil || h.Authority != "retained_candidate_generation_intents_only" || h.ProviderCalled || h.SourceGenerated || h.QualificationCredit {
		return ErrInvalid
	}
	sha := h.SHA256
	h.SHA256 = ""
	if sha != workerplan.Hash(h) {
		return ErrInvalid
	}
	return nil
}

type IntentArtifact struct {
	Schema  string `json:"schema"`
	Intent  Intent `json:"intent"`
	Dataset []byte `json:"dataset"`
}

func (a IntentArtifact) Validate() error {
	if a.Schema != IntentArtifactSchema || a.Intent.Validate() != nil || a.Intent.Command.Plan.VerifyDataset(a.Dataset) != nil {
		return ErrInvalid
	}
	return nil
}

// Submission carries the exact dataset, including its original whitespace.
type Submission struct {
	Command Command `json:"command"`
	Dataset []byte  `json:"dataset"`
}

const MaxSubmissionBytes = MaxIntentBytes + 4*((workerplan.MaxInputBytes+2)/3) + 1024
const MaxIntentArtifactBytes = MaxSubmissionBytes + 1024

func (s Submission) Validate() error {
	if s.Command.Validate() != nil || s.Command.Plan.VerifyDataset(s.Dataset) != nil {
		return ErrInvalid
	}
	return nil
}
func ReadSubmission(input io.Reader) (Submission, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxSubmissionBytes+1))
	var s Submission
	if err != nil || workerplan.ReadBoundedShape(raw, &s, MaxSubmissionBytes) != nil || s.Validate() != nil {
		return Submission{}, ErrInvalid
	}
	return s, nil
}
