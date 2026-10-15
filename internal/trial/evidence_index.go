package trial

import (
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const EvidenceIndexSchema = "mantik-trial-evidence-index-v1"

type EvidenceRef struct {
	AttemptID     string    `json:"attempt_id"`
	StartedAt     time.Time `json:"started_at"`
	AttemptSHA256 string    `json:"attempt_sha256"`
	RunSHA256     string    `json:"run_sha256,omitempty"`
}
type EvidenceIndex struct {
	Schema             string        `json:"schema"`
	TrialID            string        `json:"trial_id"`
	HypothesisID       string        `json:"hypothesis_id"`
	RegistrationSHA256 string        `json:"registration_sha256"`
	Attempts           []EvidenceRef `json:"attempts"`
	SHA256             string        `json:"sha256"`
}

func compareRef(a, b EvidenceRef) int {
	if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
		return c
	}
	return strings.Compare(a.AttemptID, b.AttemptID)
}
func (index EvidenceIndex) Validate() error {
	if index.Schema != EvidenceIndexSchema || !review.ID.MatchString(index.TrialID) || !hypothesis.QuestionID.MatchString(index.HypothesisID) || !digest.MatchString(index.RegistrationSHA256) || index.Attempts == nil || len(index.Attempts) > MaxAttempts {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for i, ref := range index.Attempts {
		if !review.ID.MatchString(ref.AttemptID) || !validTime(ref.StartedAt) || !digest.MatchString(ref.AttemptSHA256) || ref.RunSHA256 != "" && !digest.MatchString(ref.RunSHA256) || seen[ref.AttemptID] || i > 0 && compareRef(index.Attempts[i-1], ref) >= 0 {
			return ErrInvalid
		}
		seen[ref.AttemptID] = true
	}
	expected := index.SHA256
	index.SHA256 = ""
	if expected != Hash(index) {
		return ErrInvalid
	}
	return nil
}

// MakeEvidenceIndex pins only metadata from one storage snapshot. Result bodies
// are retrieved individually and checked against these references.
func MakeEvidenceIndex(r Registration, attempts []Attempt, runs []Run) (EvidenceIndex, error) {
	if r.Validate() != nil || len(attempts) > MaxAttempts || len(runs) > len(attempts) {
		return EvidenceIndex{}, ErrInvalid
	}
	byID := map[string]Run{}
	for _, run := range runs {
		if run.Validate() != nil || run.TrialID != r.ID || run.RegistrationSHA256 != r.SHA256 || run.ReceiverID != r.ReceiverID || byID[run.AttemptID].AttemptID != "" {
			return EvidenceIndex{}, ErrInvalid
		}
		byID[run.AttemptID] = run
	}
	index := EvidenceIndex{Schema: EvidenceIndexSchema, TrialID: r.ID, HypothesisID: r.HypothesisID, RegistrationSHA256: r.SHA256, Attempts: []EvidenceRef{}}
	for _, a := range attempts {
		if a.Validate() != nil || a.TrialID != r.ID || a.RegistrationSHA256 != r.SHA256 || a.ReceiverID != r.ReceiverID {
			return EvidenceIndex{}, ErrInvalid
		}
		ref := EvidenceRef{AttemptID: a.ID, StartedAt: a.StartedAt, AttemptSHA256: a.SHA256}
		if run, ok := byID[a.ID]; ok {
			if a.Status != "pending" || run.IntentSHA256 != a.IntentSHA256 {
				return EvidenceIndex{}, ErrInvalid
			}
			ref.RunSHA256 = run.SHA256
			delete(byID, a.ID)
		}
		index.Attempts = append(index.Attempts, ref)
	}
	if len(byID) != 0 {
		return EvidenceIndex{}, ErrInvalid
	}
	slices.SortFunc(index.Attempts, compareRef)
	index.SHA256 = Hash(index)
	return index, index.Validate()
}
