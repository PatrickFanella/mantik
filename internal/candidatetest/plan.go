// Package candidatetest declares synthetic signal annotations and compares
// caller-reported recordings. It never executes candidate source.
package candidatetest

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatebuild"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PlanSchema = "mantik-candidate-fixture-test-plan-v1"
const MaxCases = 64
const MaxPlanBytes = candidate.MaxPackageBytes + 16384

var ErrInvalid = errors.New("invalid or unsupported candidate fixture test")
var caseID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

type Case struct {
	ID         string `json:"id"`
	PriceCents int64  `json:"price_cents"`
	Expected   bool   `json:"expected"`
}
type Plan struct {
	Schema              string            `json:"schema"`
	ID                  string            `json:"id"`
	Candidate           candidate.Package `json:"candidate"`
	Interface           string            `json:"interface"`
	Cases               []Case            `json:"cases"`
	Authority           string            `json:"authority"`
	QualificationCredit bool              `json:"qualification_credit"`
	SHA256              string            `json:"sha256"`
}

func planHash(p Plan) string { p.ID, p.SHA256 = "", ""; return workerplan.Hash(p) }
func datasetCases(p candidate.Package, raw []byte) ([]Case, error) {
	if p.VerifyDataset(raw) != nil {
		return nil, ErrInvalid
	}
	var data dataset.Dataset
	if workerplan.ReadDatasetShape(raw, &data) != nil || len(data.Records) < 1 || len(data.Records) > MaxCases {
		return nil, ErrInvalid
	}
	cases := make([]Case, 0, len(data.Records))
	seen := map[string]bool{}
	for _, record := range data.Records {
		o, err := record.ExecutionObservation()
		if err != nil || seen[o.ID] {
			return nil, ErrInvalid
		}
		seen[o.ID] = true
		cases = append(cases, Case{ID: o.ID, PriceCents: o.PriceCents})
	}
	return cases, nil
}
func New(p candidate.Package, rawDataset []byte, expected []bool) (Plan, error) {
	cases, err := datasetCases(p, rawDataset)
	if err != nil || len(expected) != len(cases) {
		return Plan{}, ErrInvalid
	}
	for i := range cases {
		cases[i].Expected = expected[i]
	}
	raw, _ := json.Marshal(p)
	var detached candidate.Package
	if json.Unmarshal(raw, &detached) != nil {
		return Plan{}, ErrInvalid
	}
	plan := Plan{Schema: PlanSchema, Candidate: detached, Interface: candidatebuild.Interface, Cases: cases, Authority: "caller_declared_fixture_annotations_only"}
	plan.SHA256 = planHash(plan)
	plan.ID = "candidate-test-" + plan.SHA256
	return plan, plan.Validate()
}
func (p Plan) Validate() error {
	if p.Schema != PlanSchema || p.Candidate.Validate() != nil || p.Interface != candidatebuild.Interface || len(p.Cases) < 1 || len(p.Cases) > MaxCases || p.Authority != "caller_declared_fixture_annotations_only" || p.QualificationCredit || p.SHA256 != planHash(p) || p.ID != "candidate-test-"+p.SHA256 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range p.Cases {
		if !caseID.MatchString(c.ID) || c.PriceCents <= 0 || seen[c.ID] {
			return ErrInvalid
		}
		seen[c.ID] = true
	}
	raw, _ := json.Marshal(p)
	if len(raw) > MaxPlanBytes {
		return ErrInvalid
	}
	return nil
}

// VerifyDataset derives every case again. A rehashed plan cannot substitute
// different input prices under an existing dataset pin.
func (p Plan) VerifyDataset(raw []byte) error {
	if p.Validate() != nil {
		return ErrInvalid
	}
	cases, err := datasetCases(p.Candidate, raw)
	if err != nil || len(cases) != len(p.Cases) {
		return ErrInvalid
	}
	for i, c := range cases {
		c.Expected = p.Cases[i].Expected
		if !reflect.DeepEqual(c, p.Cases[i]) {
			return ErrInvalid
		}
	}
	return nil
}
func ReadPlan(r io.Reader) (Plan, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxPlanBytes+1))
	var p Plan
	if err != nil || workerplan.ReadBoundedShape(raw, &p, MaxPlanBytes) != nil || p.Validate() != nil {
		return Plan{}, ErrInvalid
	}
	return p, nil
}

// ReadExpectations requires explicit Boolean values; missing/null entries do
// not silently become false annotations.
func ReadExpectations(r io.Reader) ([]bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 4097))
	var supplied []*bool
	if err != nil || workerplan.ReadBoundedShape(raw, &supplied, 4096) != nil || len(supplied) < 1 || len(supplied) > MaxCases {
		return nil, ErrInvalid
	}
	out := make([]bool, len(supplied))
	for i, v := range supplied {
		if v == nil {
			return nil, ErrInvalid
		}
		out[i] = *v
	}
	return out, nil
}
