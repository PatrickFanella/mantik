// Package candidateproperty checks declared relations over retained synthetic
// worker samples. It does not execute source or establish unsampled properties.
package candidateproperty

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PlanSchema = "mantik-candidate-property-plan-v1"
const ReportSchema = "mantik-candidate-property-report-v1"
const SamePrice = "same_price_same_signal"
const Nonincreasing = "entry_nonincreasing_with_price"
const Nondecreasing = "entry_nondecreasing_with_price"
const MaxPlanBytes = candidateexec.MaxJobBytes + 8192

var ErrInvalid = errors.New("invalid candidate sample property protocol")

type Plan struct {
	Schema              string            `json:"schema"`
	Job                 candidateexec.Job `json:"job"`
	Properties          []string          `json:"properties"`
	Authority           string            `json:"authority"`
	QualificationCredit bool              `json:"qualification_credit"`
	SHA256              string            `json:"sha256"`
}

func validProperties(properties []string) bool {
	if len(properties) < 1 || len(properties) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range properties {
		switch p {
		case SamePrice, Nonincreasing, Nondecreasing:
		default:
			return false
		}
		if seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
func New(j candidateexec.Job, data []byte, properties []string) (Plan, error) {
	if j.Validate() != nil || j.Plan.VerifyDataset(data) != nil || !validProperties(properties) {
		return Plan{}, ErrInvalid
	}
	raw, _ := json.Marshal(j)
	var detached candidateexec.Job
	if json.Unmarshal(raw, &detached) != nil {
		return Plan{}, ErrInvalid
	}
	p := Plan{Schema: PlanSchema, Job: detached, Properties: append([]string{}, properties...), Authority: "caller_declared_sample_properties_only"}
	p.SHA256 = workerplan.Hash(p)
	return p, p.Validate()
}
func (p Plan) Validate() error {
	if p.Schema != PlanSchema || p.Job.Validate() != nil || !validProperties(p.Properties) || p.Authority != "caller_declared_sample_properties_only" || p.QualificationCredit {
		return ErrInvalid
	}
	expected := p.SHA256
	p.SHA256 = ""
	raw, _ := json.Marshal(p)
	if expected != workerplan.Hash(p) || len(raw) > MaxPlanBytes {
		return ErrInvalid
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
func ReadProperties(r io.Reader) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 4097))
	var properties []string
	if err != nil || workerplan.ReadBoundedShape(raw, &properties, 4096) != nil || !validProperties(properties) {
		return nil, ErrInvalid
	}
	return properties, nil
}

type Pair struct {
	LeftCaseID      string `json:"left_case_id"`
	RightCaseID     string `json:"right_case_id"`
	LeftPriceCents  int64  `json:"left_price_cents"`
	RightPriceCents int64  `json:"right_price_cents"`
	LeftValue       *bool  `json:"left_value"`
	RightValue      *bool  `json:"right_value"`
	Status          string `json:"status"`
}
type Check struct {
	Property      string `json:"property"`
	Status        string `json:"status"`
	EligiblePairs int    `json:"eligible_pairs"`
	CheckedPairs  int    `json:"checked_pairs"`
	MissingPairs  int    `json:"missing_pairs"`
	ViolatedPairs int    `json:"violated_pairs"`
	Pairs         []Pair `json:"pairs"`
}
type Report struct {
	Schema                       string               `json:"schema"`
	PropertyPlanSHA256           string               `json:"property_plan_sha256"`
	JobSHA256                    string               `json:"job_sha256"`
	ResultArtifactSHA256         string               `json:"result_artifact_sha256"`
	WorkerResult                 candidateexec.Result `json:"worker_result"`
	Checks                       []Check              `json:"checks"`
	Status                       string               `json:"status"`
	AllSamplePropertiesSatisfied bool                 `json:"all_sample_properties_satisfied"`
	AnnotationMatch              bool                 `json:"annotation_match"`
	Coverage                     string               `json:"coverage"`
	Authority                    string               `json:"authority"`
	ExecutionVerified            bool                 `json:"execution_verified"`
	QualificationCredit          bool                 `json:"qualification_credit"`
	SHA256                       string               `json:"sha256"`
}

func Evaluate(p Plan, data []byte, input io.Reader) (Report, error) {
	if p.Validate() != nil || p.Job.Plan.VerifyDataset(data) != nil {
		return Report{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(input, candidateexec.MaxResultBytes+1))
	if err != nil {
		return Report{}, ErrInvalid
	}
	result, err := candidateexec.ReadResult(bytes.NewReader(raw), p.Job)
	if err != nil {
		return Report{}, ErrInvalid
	}
	r := Report{Schema: ReportSchema, PropertyPlanSHA256: p.SHA256, JobSHA256: p.Job.SHA256, ResultArtifactSHA256: workerplan.Digest(raw), WorkerResult: result, Checks: []Check{}, Status: "passed", AnnotationMatch: result.AllAnnotationsMatch, Coverage: "retained_samples_only", Authority: "retained_worker_sample_property_comparison_only"}
	incomplete, violated := result.Status == "failed", false
	cases := p.Job.Plan.Cases
	for _, property := range p.Properties {
		check := Check{Property: property, Status: "passed", Pairs: []Pair{}}
		for left := 0; left < len(cases); left++ {
			for right := left + 1; right < len(cases); right++ {
				a, b := cases[left], cases[right]
				if (property == SamePrice) != (a.PriceCents == b.PriceCents) {
					continue
				}
				pair := Pair{LeftCaseID: a.ID, RightCaseID: b.ID, LeftPriceCents: a.PriceCents, RightPriceCents: b.PriceCents, Status: "missing_output"}
				if left < len(result.Values) {
					value := result.Values[left]
					pair.LeftValue = &value
				}
				if right < len(result.Values) {
					value := result.Values[right]
					pair.RightValue = &value
				}
				check.EligiblePairs++
				if pair.LeftValue == nil || pair.RightValue == nil {
					check.MissingPairs++
				} else {
					check.CheckedPairs++
					holds := *pair.LeftValue == *pair.RightValue
					if property != SamePrice {
						low, high := *pair.LeftValue, *pair.RightValue
						if a.PriceCents > b.PriceCents {
							low, high = high, low
						}
						if property == Nonincreasing {
							holds = low || !high
						} else {
							holds = !low || high
						}
					}
					pair.Status = "holds"
					if !holds {
						pair.Status = "violated"
						check.ViolatedPairs++
					}
				}
				check.Pairs = append(check.Pairs, pair)
			}
		}
		switch {
		case check.EligiblePairs == 0:
			check.Status = "not_applicable"
			incomplete = true
		case result.Status == "failed" || check.MissingPairs > 0:
			check.Status = "incomplete"
			incomplete = true
		case check.ViolatedPairs > 0:
			check.Status = "violated"
		}
		if check.ViolatedPairs > 0 {
			violated = true
		}
		r.Checks = append(r.Checks, check)
	}
	if violated {
		r.Status = "violated"
	}
	if incomplete {
		r.Status = "incomplete"
	}
	r.AllSamplePropertiesSatisfied = r.Status == "passed"
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
