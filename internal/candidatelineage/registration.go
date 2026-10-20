// Package candidatelineage defines publication-bound candidate declarations.
// Callers must supply authoritative publications and serialize persistence;
// these pure functions neither retain records nor attest a caller's history.
package candidatelineage

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-candidate-registration-v1"
const MaxRegistrations = 32
const MaxBytes = candidate.MaxPackageBytes + 4096

var ErrInvalid = errors.New("invalid candidate registration or lineage")
var ErrConflict = errors.New("candidate registration identity, publication, parent or clock conflict")
var ErrCapacity = errors.New("candidate registration capacity reached")
var ErrNotFound = errors.New("candidate registration or publication not found")
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Command struct {
	ReceiverID    string            `json:"receiver_id"`
	RequestID     string            `json:"request_id"`
	PublicationID string            `json:"publication_id"`
	ParentID      string            `json:"parent_id,omitempty"`
	Candidate     candidate.Package `json:"candidate"`
}

type Registration struct {
	Schema                      string            `json:"schema"`
	ID                          string            `json:"id"`
	ReceiverID                  string            `json:"receiver_id"`
	Sequence                    int               `json:"sequence"`
	PublicationID               string            `json:"publication_id"`
	PublicationSHA256           string            `json:"publication_sha256"`
	ParentID                    string            `json:"parent_id,omitempty"`
	ParentSHA256                string            `json:"parent_sha256,omitempty"`
	RegisteredAt                time.Time         `json:"registered_at"`
	Candidate                   candidate.Package `json:"candidate"`
	Status                      string            `json:"status"`
	Authority                   string            `json:"authority"`
	QualificationCredit         bool              `json:"qualification_credit"`
	DatasetAvailabilityVerified bool              `json:"dataset_availability_verified"`
	ExecutionStarted            bool              `json:"execution_started"`
	SHA256                      string            `json:"sha256"`
}

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}

func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.PublicationID) || c.ParentID != "" && (!review.ID.MatchString(c.ParentID) || c.ParentID == c.RequestID) || c.Candidate.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

func seal(r Registration) string {
	r.SHA256 = ""
	return workerplan.Hash(r)
}

func (r Registration) Validate() error {
	c := Command{ReceiverID: r.ReceiverID, RequestID: r.ID, PublicationID: r.PublicationID, ParentID: r.ParentID, Candidate: r.Candidate}
	if c.Validate() != nil || r.Schema != Schema || r.Sequence < 1 || r.Sequence > MaxRegistrations || !digest.MatchString(r.PublicationSHA256) || !validTime(r.RegisteredAt) || r.Status != "registered" || r.Authority != "candidate_registration_declaration_only" || r.QualificationCredit || r.DatasetAvailabilityVerified || r.ExecutionStarted || r.SHA256 != seal(r) {
		return ErrInvalid
	}
	if r.ParentID == "" && r.ParentSHA256 != "" || r.ParentID != "" && !digest.MatchString(r.ParentSHA256) {
		return ErrInvalid
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxBytes {
		return ErrInvalid
	}
	return nil
}

// detached prevents changes to nested hypothesis slices from rewriting a pin.
func detached[T any](v T) T {
	raw, _ := json.Marshal(v)
	var result T
	_ = json.Unmarshal(raw, &result)
	return result
}

// Register binds a declaration to a supplied authoritative publication and
// optional earlier parent. Candidate source remains caller-supplied, including
// invalid Go; no dataset bytes are loaded and no worker is started.
func Register(c Command, p hypothesis.Publication, parent *Registration, sequence int, at time.Time) (Registration, error) {
	if c.Validate() != nil || sequence < 1 || sequence > MaxRegistrations {
		return Registration{}, ErrInvalid
	}
	sealed, err := hypothesis.Seal(p.Sealed.Declaration)
	if err != nil || !reflect.DeepEqual(sealed, p.Sealed) || !reflect.DeepEqual(c.Candidate.Hypothesis, p.Sealed) || p.Schema != hypothesis.PublicationSchema || p.ID != c.PublicationID || p.ReceiverID != c.ReceiverID || !p.OriginBindingVerified || !validTime(p.PublishedAt) || !validTime(at) || at.Before(p.PublishedAt) {
		return Registration{}, ErrConflict
	}
	parentHash := ""
	if c.ParentID != "" {
		if parent == nil || parent.Validate() != nil || parent.ID != c.ParentID || parent.ReceiverID != c.ReceiverID || parent.Candidate.Hypothesis.DeclarationSHA256 != c.Candidate.Hypothesis.DeclarationSHA256 || parent.Sequence >= sequence || at.Before(parent.RegisteredAt) {
			return Registration{}, ErrConflict
		}
		parentHash = parent.SHA256
	} else if parent != nil {
		return Registration{}, ErrConflict
	}
	r := Registration{Schema: Schema, ID: c.RequestID, ReceiverID: c.ReceiverID, Sequence: sequence, PublicationID: p.ID, PublicationSHA256: workerplan.Hash(p), ParentID: c.ParentID, ParentSHA256: parentHash, RegisteredAt: at.UTC(), Candidate: detached(c.Candidate), Status: "registered", Authority: "candidate_registration_declaration_only"}
	r.SHA256 = seal(r)
	if r.Validate() != nil {
		return Registration{}, ErrInvalid
	}
	return r, nil
}

// VerifyBinding rechecks a retained row against its original authoritative
// publication and parent. Validate alone checks structure, not record ownership.
func (r Registration) VerifyBinding(p hypothesis.Publication, parent *Registration) error {
	if r.Validate() != nil {
		return ErrInvalid
	}
	c := Command{ReceiverID: r.ReceiverID, RequestID: r.ID, PublicationID: r.PublicationID, ParentID: r.ParentID, Candidate: r.Candidate}
	expected, err := Register(c, p, parent, r.Sequence, r.RegisteredAt)
	if err != nil || expected.SHA256 != r.SHA256 {
		return ErrConflict
	}
	return nil
}

// ValidateHistory checks the entire ordered prefix, including unrelated roots.
// It rejects gaps, duplicate requests, missing/forward parents and broken pins.
// A consistent supplied prefix cannot prove that its owner retained all attempts.
func ValidateHistory(receiverID string, history []Registration) error {
	if !receiver.MatchString(receiverID) || len(history) > MaxRegistrations {
		return ErrInvalid
	}
	seen := map[string]Registration{}
	var last time.Time
	for i, r := range history {
		if r.Validate() != nil || r.ReceiverID != receiverID || r.Sequence != i+1 || r.RegisteredAt.Before(last) {
			return ErrInvalid
		}
		if _, exists := seen[r.ID]; exists {
			return ErrInvalid
		}
		if r.ParentID != "" {
			p, ok := seen[r.ParentID]
			if !ok || p.SHA256 != r.ParentSHA256 || p.Candidate.Hypothesis.DeclarationSHA256 != r.Candidate.Hypothesis.DeclarationSHA256 {
				return ErrInvalid
			}
		}
		seen[r.ID], last = r, r.RegisteredAt
	}
	return nil
}

// Append validates the full retained prefix before handling retries. Exact
// request retries keep their original time and sequence even at capacity or
// after clock rollback. A distinct request is another declaration, even when
// its candidate bytes are identical. Storage must atomically persist the result.
func Append(history []Registration, c Command, p hypothesis.Publication, at time.Time) (Registration, error) {
	if c.Validate() != nil || ValidateHistory(c.ReceiverID, history) != nil {
		return Registration{}, ErrInvalid
	}
	var parent *Registration
	var previous *Registration
	for i := range history {
		if history[i].ID == c.ParentID {
			parent = &history[i]
		}
		if history[i].ID == c.RequestID {
			previous = &history[i]
		}
	}
	if previous != nil {
		r, err := Register(c, p, parent, previous.Sequence, previous.RegisteredAt)
		if err != nil || r.SHA256 != previous.SHA256 {
			return Registration{}, ErrConflict
		}
		return detached(*previous), nil
	}
	if len(history) == MaxRegistrations {
		return Registration{}, ErrCapacity
	}
	if len(history) > 0 && at.Before(history[len(history)-1].RegisteredAt) {
		return Registration{}, ErrConflict
	}
	return Register(c, p, parent, len(history)+1, at)
}

func Read(input io.Reader) (Command, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	var c Command
	if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxBytes) != nil || c.Validate() != nil {
		return Command{}, ErrInvalid
	}
	return c, nil
}
