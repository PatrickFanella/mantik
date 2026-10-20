// Package generatedcandidate defines explicit links from retained caller-recorded
// generation output to new candidate registrations. It neither persists records
// nor calls providers, compiles code or activates strategies.
package generatedcandidate

import (
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"reflect"
	"regexp"
	"slices"
	"time"
)

const LinkSchema = "mantik-generated-candidate-registration-link-v1"
const RegistrySchema = "mantik-generated-candidate-registry-v1"
const ArtifactSchema = "mantik-generated-candidate-registration-artifact-v1"
const MaxCommandBytes = 4096
const MaxLinkBytes = 4096
const MaxRegistryBytes = candidatelineage.MaxRegistryBytes + candidatelineage.MaxRegistrations*(MaxLinkBytes+256) + 4096
const MaxArtifactBytes = candidategeneration.MaxOutcomeArtifactBytes + candidatelineage.MaxBytes + MaxLinkBytes + 1024

var ErrInvalid = errors.New("invalid generated candidate registration evidence")
var ErrConflict = errors.New("generated candidate registration binding or clock conflict")
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999
}
func detach[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// The owner selects the candidate from its own exact retained recording; a caller
// cannot substitute source bytes in this command.
type Command struct {
	ReceiverID          string `json:"receiver_id"`
	RequestID           string `json:"request_id"`
	ParentID            string `json:"parent_id,omitempty"`
	GenerationRequestID string `json:"generation_request_id"`
	IntentSHA256        string `json:"intent_sha256"`
	OutcomeSHA256       string `json:"outcome_sha256"`
	RecordingSHA256     string `json:"recording_sha256"`
	PlanSHA256          string `json:"plan_sha256"`
	CandidateSHA256     string `json:"candidate_sha256"`
}

func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.GenerationRequestID) || c.ParentID != "" && (!review.ID.MatchString(c.ParentID) || c.ParentID == c.RequestID) || !digest.MatchString(c.IntentSHA256) || !digest.MatchString(c.OutcomeSHA256) || !digest.MatchString(c.RecordingSHA256) || !digest.MatchString(c.PlanSHA256) || !digest.MatchString(c.CandidateSHA256) {
		return ErrInvalid
	}
	return nil
}
func ReadCommand(input io.Reader) (Command, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxCommandBytes+1))
	var c Command
	if err != nil || workerplan.ReadBoundedShape(raw, &c, MaxCommandBytes) != nil || c.Validate() != nil {
		return Command{}, ErrInvalid
	}
	return c, nil
}

type Link struct {
	Schema                  string    `json:"schema"`
	Command                 Command   `json:"command"`
	RegistrationSequence    int       `json:"registration_sequence"`
	RegistrationSHA256      string    `json:"registration_sha256"`
	PublicationID           string    `json:"publication_id"`
	PublicationSHA256       string    `json:"publication_sha256"`
	OutcomeRecordedAt       time.Time `json:"outcome_recorded_at"`
	RegisteredAt            time.Time `json:"registered_at"`
	Authority               string    `json:"authority"`
	ProviderCalled          bool      `json:"provider_called"`
	ModelAuthorshipVerified bool      `json:"model_authorship_verified"`
	SourceCompiled          bool      `json:"source_compiled"`
	SourceExecuted          bool      `json:"source_executed"`
	QualificationCredit     bool      `json:"qualification_credit"`
	SHA256                  string    `json:"sha256"`
}

func (l Link) Validate() error {
	if l.Schema != LinkSchema || l.Command.Validate() != nil || l.RegistrationSequence < 1 || l.RegistrationSequence > candidatelineage.MaxRegistrations || !digest.MatchString(l.RegistrationSHA256) || !review.ID.MatchString(l.PublicationID) || !digest.MatchString(l.PublicationSHA256) || !validTime(l.OutcomeRecordedAt) || !validTime(l.RegisteredAt) || l.RegisteredAt.Before(l.OutcomeRecordedAt) || l.Authority != "recorded_generation_registration_link_only" || l.ProviderCalled || l.ModelAuthorshipVerified || l.SourceCompiled || l.SourceExecuted || l.QualificationCredit {
		return ErrInvalid
	}
	sha := l.SHA256
	l.SHA256 = ""
	raw, err := json.Marshal(l)
	if err != nil || len(raw) > MaxLinkBytes || sha != workerplan.Hash(l) {
		return ErrInvalid
	}
	return nil
}
func (l Link) VerifyRegistration(r candidatelineage.Registration) error {
	if l.Validate() != nil || r.Validate() != nil || r.ReceiverID != l.Command.ReceiverID || r.ID != l.Command.RequestID || r.ParentID != l.Command.ParentID || r.Sequence != l.RegistrationSequence || r.SHA256 != l.RegistrationSHA256 || r.PublicationID != l.PublicationID || r.PublicationSHA256 != l.PublicationSHA256 || r.Candidate.SHA256 != l.Command.CandidateSHA256 || !r.RegisteredAt.Equal(l.RegisteredAt) {
		return ErrConflict
	}
	return nil
}

// NewLink validates complete typed evidence. Authoritative publication, parent,
// exact dataset and owner-snapshot checks remain the backend's responsibility.
// Append performs those checks against owner-supplied values for a new request.
func matchesGeneration(c Command, a candidategeneration.OutcomeArtifact) bool {
	return a.Recording.Candidate != nil && a.Recording.Status == "candidate_recorded" && c.ReceiverID == a.Intent.Command.ReceiverID && c.GenerationRequestID == a.Intent.Command.RequestID && c.IntentSHA256 == a.Intent.SHA256 && c.OutcomeSHA256 == a.Outcome.SHA256 && c.RecordingSHA256 == a.Recording.SHA256 && c.PlanSHA256 == a.Recording.Plan.SHA256 && c.CandidateSHA256 == a.Recording.Candidate.SHA256
}
func NewLink(c Command, r candidatelineage.Registration, a candidategeneration.OutcomeArtifact) (Link, error) {
	if c.Validate() != nil || r.Validate() != nil || a.Validate() != nil {
		return Link{}, ErrInvalid
	}
	i, o, rec := a.Intent, a.Outcome, a.Recording
	if rec.Candidate == nil || rec.Status != "candidate_recorded" || c.ReceiverID != i.Command.ReceiverID || c.GenerationRequestID != i.Command.RequestID || c.IntentSHA256 != i.SHA256 || c.OutcomeSHA256 != o.SHA256 || c.RecordingSHA256 != rec.SHA256 || c.PlanSHA256 != rec.Plan.SHA256 || c.CandidateSHA256 != rec.Candidate.SHA256 || r.ReceiverID != c.ReceiverID || r.ID != c.RequestID || r.ParentID != c.ParentID || r.PublicationID != i.Command.PublicationID || r.PublicationSHA256 != i.Command.PublicationSHA256 || !reflect.DeepEqual(r.Candidate, *rec.Candidate) || r.RegisteredAt.Before(o.RecordedAt) {
		return Link{}, ErrConflict
	}
	l := Link{Schema: LinkSchema, Command: c, RegistrationSequence: r.Sequence, RegistrationSHA256: r.SHA256, PublicationID: r.PublicationID, PublicationSHA256: r.PublicationSHA256, OutcomeRecordedAt: o.RecordedAt, RegisteredAt: r.RegisteredAt, Authority: "recorded_generation_registration_link_only"}
	l.SHA256 = workerplan.Hash(l)
	return l, l.Validate()
}

type Artifact struct {
	Schema       string                              `json:"schema"`
	Link         Link                                `json:"link"`
	Registration candidatelineage.Registration       `json:"registration"`
	Generation   candidategeneration.OutcomeArtifact `json:"generation"`
}

func NewArtifact(l Link, r candidatelineage.Registration, a candidategeneration.OutcomeArtifact) (Artifact, error) {
	v := Artifact{Schema: ArtifactSchema, Link: l, Registration: r, Generation: a}
	if v.Validate() != nil {
		return Artifact{}, ErrInvalid
	}
	return detach(v), nil
}
func (a Artifact) Validate() error {
	if a.Schema != ArtifactSchema || a.Link.VerifyRegistration(a.Registration) != nil {
		return ErrInvalid
	}
	expected, err := NewLink(a.Link.Command, a.Registration, a.Generation)
	if err != nil || !reflect.DeepEqual(expected, a.Link) {
		return ErrInvalid
	}
	return nil
}
func ReadArtifact(input io.Reader) (Artifact, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxArtifactBytes+1))
	var a Artifact
	if err != nil || workerplan.ReadBoundedShape(raw, &a, MaxArtifactBytes) != nil || a.Validate() != nil {
		return Artifact{}, ErrInvalid
	}
	return a, nil
}

// Registry includes manual declarations as well as linked declarations. An
// unlinked registration is not evidence that no external model produced it.
type Registry struct {
	Schema                  string                    `json:"schema"`
	ReceiverID              string                    `json:"receiver_id"`
	Candidates              candidatelineage.Registry `json:"candidates"`
	Links                   []Link                    `json:"links"`
	UnlinkedRegistrationIDs []string                  `json:"unlinked_registration_ids"`
	Authority               string                    `json:"authority"`
	ProviderCalled          bool                      `json:"provider_called"`
	ModelAuthorshipVerified bool                      `json:"model_authorship_verified"`
	QualificationCredit     bool                      `json:"qualification_credit"`
	SHA256                  string                    `json:"sha256"`
}

func RegistryFor(candidates candidatelineage.Registry, links []Link) (Registry, error) {
	r := Registry{Schema: RegistrySchema, ReceiverID: candidates.ReceiverID, Candidates: candidates, Links: append([]Link{}, links...), UnlinkedRegistrationIDs: []string{}, Authority: "retained_generation_registration_links_only"}
	seen := map[string]bool{}
	for _, l := range links {
		seen[l.Command.RequestID] = true
	}
	for _, v := range candidates.Registrations {
		if !seen[v.ID] {
			r.UnlinkedRegistrationIDs = append(r.UnlinkedRegistrationIDs, v.ID)
		}
	}
	r = detach(r)
	r.SHA256 = workerplan.Hash(r)
	return r, r.Validate()
}
func (r Registry) Validate() error {
	if r.Schema != RegistrySchema || r.Candidates.Validate() != nil || r.ReceiverID != r.Candidates.ReceiverID || r.Links == nil || len(r.Links) > candidatelineage.MaxRegistrations || r.UnlinkedRegistrationIDs == nil || r.Authority != "retained_generation_registration_links_only" || r.ProviderCalled || r.ModelAuthorshipVerified || r.QualificationCredit {
		return ErrInvalid
	}
	last := 0
	seen := map[string]bool{}
	for _, l := range r.Links {
		if l.RegistrationSequence <= last || l.RegistrationSequence > len(r.Candidates.Registrations) || l.VerifyRegistration(r.Candidates.Registrations[l.RegistrationSequence-1]) != nil {
			return ErrInvalid
		}
		last = l.RegistrationSequence
		seen[l.Command.RequestID] = true
	}
	unlinked := []string{}
	for _, v := range r.Candidates.Registrations {
		if !seen[v.ID] {
			unlinked = append(unlinked, v.ID)
		}
	}
	if !slices.Equal(unlinked, r.UnlinkedRegistrationIDs) {
		return ErrInvalid
	}
	sha := r.SHA256
	r.SHA256 = ""
	if sha != workerplan.Hash(r) {
		return ErrInvalid
	}
	return nil
}

// Append consumes owner-validated complete snapshots. Storage must validate all
// existing generation artifacts/publications/datasets and persist the returned
// registration and link atomically. An existing unlinked registration cannot be
// adopted. Exact linked retries reuse original metadata without reading a clock.
func Append(history []candidatelineage.Registration, links []Link, c Command, p hypothesis.Publication, a candidategeneration.OutcomeArtifact, raw []byte, now func() time.Time) (Link, candidatelineage.Registration, bool, error) {
	if c.Validate() != nil || a.Validate() != nil {
		return Link{}, candidatelineage.Registration{}, false, ErrInvalid
	}
	registry, err := candidatelineage.RegistryFor(c.ReceiverID, history)
	if err != nil {
		return Link{}, candidatelineage.Registration{}, false, err
	}
	if _, err := RegistryFor(registry, links); err != nil {
		return Link{}, candidatelineage.Registration{}, false, err
	}
	if a.Recording.Candidate == nil || a.Intent.VerifyBinding(p) != nil || a.Recording.VerifyDataset(raw) != nil {
		return Link{}, candidatelineage.Registration{}, false, ErrConflict
	}
	if !matchesGeneration(c, a) {
		return Link{}, candidatelineage.Registration{}, false, ErrConflict
	}
	if c.ParentID != "" {
		found := false
		for _, r := range history {
			if r.ID == c.ParentID && r.Candidate.Hypothesis.DeclarationSHA256 == a.Recording.Candidate.Hypothesis.DeclarationSHA256 {
				found = true
			}
		}
		if !found {
			return Link{}, candidatelineage.Registration{}, false, ErrConflict
		}
	}
	legacy := candidatelineage.Command{ReceiverID: c.ReceiverID, RequestID: c.RequestID, PublicationID: p.ID, ParentID: c.ParentID, Candidate: *a.Recording.Candidate}
	for _, r := range history {
		if r.ID == c.RequestID {
			var previous *Link
			for n := range links {
				if links[n].Command.RequestID == c.RequestID {
					previous = &links[n]
					break
				}
			}
			if previous == nil || previous.Command != c {
				return Link{}, candidatelineage.Registration{}, false, ErrConflict
			}
			retained, err := candidatelineage.Append(history, legacy, p, r.RegisteredAt)
			if err != nil {
				return Link{}, candidatelineage.Registration{}, false, err
			}
			l, err := NewLink(c, retained, a)
			if err != nil || !reflect.DeepEqual(l, *previous) {
				return Link{}, candidatelineage.Registration{}, false, ErrConflict
			}
			return l, retained, true, nil
		}
	}
	if len(history) == candidatelineage.MaxRegistrations {
		return Link{}, candidatelineage.Registration{}, false, candidatelineage.ErrCapacity
	}
	r, err := candidatelineage.Append(history, legacy, p, now())
	if err != nil {
		return Link{}, candidatelineage.Registration{}, false, err
	}
	l, err := NewLink(c, r, a)
	if err != nil {
		return Link{}, candidatelineage.Registration{}, false, err
	}
	return l, r, false, nil
}
