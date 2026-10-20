// Package candidatereplay derives explicit-cutoff synthetic worker inputs while
// retaining every parent exposure. It does not execute or qualify candidates.
package candidatereplay

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-candidate-replay-plan-v1"
const MaxBytes = workerplan.MaxInputBytes + 2*candidateexec.MaxJobBytes + 32768

var ErrInvalid = errors.New("invalid candidate cutoff replay plan")
var ErrNoInputs = errors.New("replay cutoff has no visible inputs; no executable job exists")

type Exposure struct {
	ParentIndex int       `json:"parent_index"`
	CaseID      string    `json:"case_id"`
	VisibleAt   time.Time `json:"visible_at"`
	Disposition string    `json:"disposition"`
}
type Plan struct {
	Schema              string             `json:"schema"`
	Parent              candidatetest.Plan `json:"parent"`
	Cutoff              time.Time          `json:"cutoff"`
	Exposures           []Exposure         `json:"exposures"`
	ReplayOrder         []string           `json:"replay_order"`
	Dataset             *replay.Dataset    `json:"dataset"`
	Job                 *candidateexec.Job `json:"job"`
	Status              string             `json:"status"`
	Authority           string             `json:"authority"`
	ExecutionStarted    bool               `json:"execution_started"`
	QualificationCredit bool               `json:"qualification_credit"`
	SHA256              string             `json:"sha256"`
}

func validTime(at time.Time) bool { return !at.IsZero() && at.Year() > 0 && at.Year() <= 9999 }
func New(parent candidatetest.Plan, raw []byte, cutoff time.Time) (Plan, error) {
	if !validTime(cutoff) || parent.VerifyDataset(raw) != nil {
		return Plan{}, ErrInvalid
	}
	var data replay.Dataset
	if workerplan.ReadDatasetShape(raw, &data) != nil {
		return Plan{}, ErrInvalid
	}
	// Detach parent provenance and never reorder caller-owned records/cases.
	encoded, _ := json.Marshal(parent)
	var detached candidatetest.Plan
	if json.Unmarshal(encoded, &detached) != nil {
		return Plan{}, ErrInvalid
	}
	p := Plan{Schema: Schema, Parent: detached, Cutoff: cutoff.UTC(), Exposures: []Exposure{}, ReplayOrder: []string{}, Status: "no_visible_inputs", Authority: "synthetic_candidate_replay_declaration_only"}
	indices := []int{}
	for i, r := range data.Records {
		exposure := Exposure{ParentIndex: i, CaseID: parent.Cases[i].ID, VisibleAt: r.VisibleAt().UTC(), Disposition: "excluded_future"}
		if !r.VisibleAt().After(p.Cutoff) {
			exposure.Disposition = "included"
			indices = append(indices, i)
		}
		p.Exposures = append(p.Exposures, exposure)
	}
	sort.Slice(indices, func(i, j int) bool {
		a, b := data.Records[indices[i]], data.Records[indices[j]]
		if !a.VisibleAt().Equal(b.VisibleAt()) {
			return a.VisibleAt().Before(b.VisibleAt())
		}
		return a.Key < b.Key
	})
	if len(indices) > 0 {
		dataset := replay.Dataset{Schema: replay.DatasetSchema, ID: "replay-" + workerplan.Hash(struct {
			DatasetSHA256 string    `json:"dataset_sha256"`
			Cutoff        time.Time `json:"cutoff"`
		}{parent.Candidate.Dataset.CanonicalSHA256, p.Cutoff}), Version: "v1", EvidenceClass: data.EvidenceClass, Records: []replay.Record{}}
		expected := []bool{}
		for _, index := range indices {
			dataset.Records = append(dataset.Records, data.Records[index])
			p.ReplayOrder = append(p.ReplayOrder, parent.Cases[index].ID)
			expected = append(expected, parent.Cases[index].Expected)
		}
		sealed, err := replay.Seal(dataset)
		if err != nil {
			return Plan{}, ErrInvalid
		}
		datasetRaw, _ := json.Marshal(sealed)
		derived, err := candidate.New([]byte(parent.Candidate.Source.Text), parent.Candidate.Hypothesis.Declaration, datasetRaw, parent.Candidate.Toolchain.ImageSHA256)
		if err != nil {
			return Plan{}, ErrInvalid
		}
		fixture, err := candidatetest.New(derived, datasetRaw, expected)
		if err != nil {
			return Plan{}, ErrInvalid
		}
		job, err := candidateexec.New(fixture, datasetRaw)
		if err != nil {
			return Plan{}, ErrInvalid
		}
		p.Dataset = &sealed
		p.Job = &job
		p.Status = "declared"
	}
	p.SHA256 = workerplan.Hash(p)
	return p, p.Validate()
}

// Validate checks the artifact shape and identity. Verify additionally derives
// every exposure, ordered input and annotation from the exact parent dataset.
func (p Plan) Validate() error {
	if p.Schema != Schema || p.Parent.Validate() != nil || !validTime(p.Cutoff) || p.Exposures == nil || len(p.Exposures) != len(p.Parent.Cases) || p.ReplayOrder == nil || p.Authority != "synthetic_candidate_replay_declaration_only" || p.ExecutionStarted || p.QualificationCredit {
		return ErrInvalid
	}
	included := 0
	seen := map[string]bool{}
	for i, e := range p.Exposures {
		if e.ParentIndex != i || e.CaseID != p.Parent.Cases[i].ID || !validTime(e.VisibleAt) {
			return ErrInvalid
		}
		switch e.Disposition {
		case "included":
			if e.VisibleAt.After(p.Cutoff) {
				return ErrInvalid
			}
			included++
		case "excluded_future":
			if !e.VisibleAt.After(p.Cutoff) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	if len(p.ReplayOrder) != included {
		return ErrInvalid
	}
	for _, id := range p.ReplayOrder {
		found := false
		for _, e := range p.Exposures {
			if e.CaseID == id && e.Disposition == "included" {
				found = true
			}
		}
		if !found || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	switch p.Status {
	case "no_visible_inputs":
		if included != 0 || p.Dataset != nil || p.Job != nil {
			return ErrInvalid
		}
	case "declared":
		if included < 1 || p.Dataset == nil || p.Job == nil || p.Dataset.Validate() != nil || p.Job.Validate() != nil || len(p.Dataset.Records) != included || len(p.Job.Plan.Cases) != included {
			return ErrInvalid
		}
		raw, _ := json.Marshal(p.Dataset)
		if p.Job.Plan.VerifyDataset(raw) != nil {
			return ErrInvalid
		}
		c := p.Job.Plan.Candidate
		parent := p.Parent.Candidate
		if c.Source.SHA256 != parent.Source.SHA256 || !reflect.DeepEqual(c.Hypothesis, parent.Hypothesis) || c.Toolchain != parent.Toolchain {
			return ErrInvalid
		}
		for index, id := range p.ReplayOrder {
			if id != p.Job.Plan.Cases[index].ID {
				return ErrInvalid
			}
		}
	default:
		return ErrInvalid
	}
	expected := p.SHA256
	p.SHA256 = ""
	raw, _ := json.Marshal(p)
	if expected != workerplan.Hash(p) || len(raw) > MaxBytes {
		return ErrInvalid
	}
	return nil
}
func (p Plan) Verify(parentRaw []byte) error {
	if p.Validate() != nil {
		return ErrInvalid
	}
	expected, err := New(p.Parent, parentRaw, p.Cutoff)
	if err != nil || expected.SHA256 != p.SHA256 {
		return ErrInvalid
	}
	return nil
}
func Read(input io.Reader) (Plan, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	var p Plan
	if err != nil || workerplan.ReadBoundedShape(raw, &p, MaxBytes) != nil || p.Validate() != nil {
		return Plan{}, ErrInvalid
	}
	return p, nil
}
func (p Plan) DatasetBytes(parentRaw []byte) ([]byte, error) {
	if p.Verify(parentRaw) != nil {
		return nil, ErrInvalid
	}
	if p.Dataset == nil {
		return nil, ErrNoInputs
	}
	return json.Marshal(p.Dataset)
}
