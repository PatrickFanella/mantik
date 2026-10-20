package candidatelineage

import (
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const RegistrySchema = "mantik-candidate-registry-v1"
const ArtifactSchema = "mantik-candidate-dataset-v1"
const MaxSubmissionBytes = MaxBytes + (workerplan.MaxInputBytes+2)/3*4 + 256
const MaxRegistryBytes = MaxRegistrations*MaxBytes + 4096

// Submission transports exact dataset bytes as JSON base64, avoiding changes to
// input whitespace or Unicode. Its command still carries the raw/canonical pins.
type Submission struct {
	Command Command `json:"command"`
	Dataset []byte  `json:"dataset"`
}

func ReadSubmission(input io.Reader) (Submission, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxSubmissionBytes+1))
	var s Submission
	if err != nil || workerplan.ReadBoundedShape(raw, &s, MaxSubmissionBytes) != nil || s.Command.Validate() != nil || s.Command.Candidate.VerifyDataset(s.Dataset) != nil {
		return Submission{}, ErrInvalid
	}
	return s, nil
}

type Registry struct {
	Schema              string         `json:"schema"`
	ReceiverID          string         `json:"receiver_id"`
	Registrations       []Registration `json:"registrations"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	ExecutionStarted    bool           `json:"execution_started"`
	SHA256              string         `json:"sha256"`
}

func registryHash(r Registry) string { r.SHA256 = ""; return workerplan.Hash(r) }

func RegistryFor(receiverID string, rows []Registration) (Registry, error) {
	if rows == nil {
		rows = []Registration{}
	}
	r := Registry{Schema: RegistrySchema, ReceiverID: receiverID, Registrations: detached(rows), Authority: "retained_candidate_declarations_only"}
	r.SHA256 = registryHash(r)
	return r, r.Validate()
}

func (r Registry) Validate() error {
	if r.Schema != RegistrySchema || r.Registrations == nil || ValidateHistory(r.ReceiverID, r.Registrations) != nil || r.Authority != "retained_candidate_declarations_only" || r.QualificationCredit || r.ExecutionStarted || r.SHA256 != registryHash(r) {
		return ErrInvalid
	}
	return nil
}

// Artifact is captured with its declaration in one backend snapshot. It does
// not attest worker execution or current provider availability.
type Artifact struct {
	Schema       string       `json:"schema"`
	Registration Registration `json:"registration"`
	Dataset      []byte       `json:"dataset"`
}

func (a Artifact) Validate() error {
	if a.Schema != ArtifactSchema || a.Registration.Validate() != nil || a.Registration.Candidate.VerifyDataset(a.Dataset) != nil {
		return ErrInvalid
	}
	return nil
}
