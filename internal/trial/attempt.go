package trial

import (
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const AttemptSchema = "mantik-trial-attempt-intent-v1"
const AttemptsSchema = "mantik-trial-attempt-history-v1"
const MaxAttempts = 32

// Attempt is an execution intent, not a receipt claiming execution or results.
type Attempt struct {
	Schema              string    `json:"schema"`
	ID                  string    `json:"id"`
	ReceiverID          string    `json:"receiver_id"`
	TrialID             string    `json:"trial_id"`
	RegistrationSHA256  string    `json:"registration_sha256"`
	HypothesisID        string    `json:"hypothesis_id"`
	RecipeSHA256        string    `json:"recipe_sha256"`
	DatasetID           string    `json:"dataset_id"`
	DatasetVersion      string    `json:"dataset_version"`
	DatasetSHA256       string    `json:"dataset_sha256"`
	StartedAt           time.Time `json:"started_at"`
	ClosedAt            time.Time `json:"closed_at,omitzero"`
	Status              string    `json:"status"`
	ReasonCode          string    `json:"reason_code,omitempty"`
	ExecutionStarted    bool      `json:"execution_started"`
	QualificationCredit bool      `json:"qualification_credit"`
	Authority           string    `json:"authority"`
	IntentSHA256        string    `json:"intent_sha256"`
	SHA256              string    `json:"sha256"`
}
type AttemptHistory struct {
	Schema   string    `json:"schema"`
	TrialID  string    `json:"trial_id"`
	Attempts []Attempt `json:"attempts"`
}
type AttemptCommand struct {
	ReceiverID string `json:"receiver_id"`
	RequestID  string `json:"request_id"`
	TrialID    string `json:"trial_id"`
}

func (c AttemptCommand) Validate() error {
	if !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.TrialID) {
		return ErrInvalid
	}
	return nil
}
func SameAttempt(a Attempt, c AttemptCommand) bool {
	return a.ReceiverID == c.ReceiverID && a.TrialID == c.TrialID
}
func StartAttempt(c AttemptCommand, r Registration, at time.Time) (Attempt, error) {
	if err := c.Validate(); err != nil {
		return Attempt{}, err
	}
	if r.Validate() != nil || r.ID != c.TrialID || r.ReceiverID != c.ReceiverID || !validTime(at) || at.Before(r.RegisteredAt) {
		return Attempt{}, ErrConflict
	}
	a := Attempt{Schema: AttemptSchema, ID: c.RequestID, ReceiverID: c.ReceiverID, TrialID: r.ID, RegistrationSHA256: r.SHA256, HypothesisID: r.HypothesisID, RecipeSHA256: r.Recipe.SHA256, DatasetID: r.Recipe.DatasetID, DatasetVersion: r.Recipe.DatasetVersion, DatasetSHA256: r.Recipe.DatasetSHA256, StartedAt: at.UTC(), Status: "pending", Authority: "research_attempt_intent_only"}
	a.IntentSHA256 = intentHash(a)
	a.SHA256 = Hash(a)
	return a, nil
}
func CancelAttempt(a Attempt, at time.Time) (Attempt, error) {
	if a.Validate() != nil {
		return Attempt{}, ErrInvalid
	}
	if a.Status == "cancelled" {
		return a, nil
	}
	if a.Status != "pending" || !validTime(at) || at.Before(a.StartedAt) {
		return Attempt{}, ErrConflict
	}
	a.Status = "cancelled"
	a.ReasonCode = "operator_cancelled_before_execution"
	a.ClosedAt = at.UTC()
	a.SHA256 = ""
	a.SHA256 = Hash(a)
	return a, nil
}
func (a Attempt) Validate() error {
	if a.Schema != AttemptSchema || !review.ID.MatchString(a.ID) || !review.ID.MatchString(a.TrialID) || !digest.MatchString(a.RegistrationSHA256) || !digest.MatchString(a.RecipeSHA256) || !digest.MatchString(a.DatasetSHA256) || !validTime(a.StartedAt) || a.ExecutionStarted || a.QualificationCredit || a.Authority != "research_attempt_intent_only" {
		return ErrInvalid
	}
	switch a.Status {
	case "pending":
		if !a.ClosedAt.IsZero() || a.ReasonCode != "" {
			return ErrInvalid
		}
	case "cancelled":
		if !validTime(a.ClosedAt) || a.ClosedAt.Before(a.StartedAt) || a.ReasonCode != "operator_cancelled_before_execution" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if a.IntentSHA256 != intentHash(a) {
		return ErrInvalid
	}
	expected := a.SHA256
	a.SHA256 = ""
	if expected != Hash(a) {
		return ErrInvalid
	}
	return nil
}

func intentHash(a Attempt) string {
	a.Status = "pending"
	a.ReasonCode = ""
	a.ClosedAt = time.Time{}
	a.IntentSHA256 = ""
	a.SHA256 = ""
	return Hash(a)
}
