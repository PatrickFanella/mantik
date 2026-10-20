// Package candidateexec defines bounded synthetic Go behavior checks. Worker
// results are separate from host isolation receipts and grant no qualification.
package candidateexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const JobSchema = "mantik-candidate-behavior-job-v1"
const ResultSchema = "mantik-candidate-behavior-result-v1"
const MaxJobBytes = candidatetest.MaxPlanBytes + 8192
const MaxResultBytes = 1 << 20
const MaxOutput = 32 << 10
const BuildSeconds = 15
const RunSeconds = 5

var ErrInvalid = errors.New("invalid candidate behavior protocol")

type Job struct {
	Schema              string               `json:"schema"`
	Plan                candidatetest.Plan   `json:"plan"`
	HarnessSHA256       string               `json:"harness_sha256"`
	Resources           workerplan.Resources `json:"resources"`
	BuildSeconds        int                  `json:"build_seconds"`
	RunSeconds          int                  `json:"run_seconds"`
	Authority           string               `json:"authority"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}

// Harness preserves dataset order and candidate state across calls. It contains
// prices only: annotations are not compiled into the candidate executable.
func Harness(p candidatetest.Plan) string {
	var b strings.Builder
	b.WriteString("package main\nvar _ func(int64) bool = Entry\nfunc main(){\n")
	for i, c := range p.Cases {
		fmt.Fprintf(&b, "println(\"_mantik %d\", Entry(%d))\n", i, c.PriceCents)
	}
	b.WriteString("}\n")
	return b.String()
}
func New(p candidatetest.Plan, dataset []byte) (Job, error) {
	if p.VerifyDataset(dataset) != nil {
		return Job{}, ErrInvalid
	}
	raw, _ := json.Marshal(p)
	var detached candidatetest.Plan
	if json.Unmarshal(raw, &detached) != nil {
		return Job{}, ErrInvalid
	}
	j := Job{Schema: JobSchema, Plan: detached, HarnessSHA256: workerplan.Digest([]byte(Harness(detached))), Resources: workerplan.Profile(), BuildSeconds: BuildSeconds, RunSeconds: RunSeconds, Authority: "synthetic_candidate_behavior_job_only"}
	j.SHA256 = workerplan.Hash(j)
	return j, j.Validate()
}
func (j Job) Validate() error {
	if j.Schema != JobSchema || j.Plan.Validate() != nil || j.HarnessSHA256 != workerplan.Digest([]byte(Harness(j.Plan))) || j.Resources != workerplan.Profile() || j.BuildSeconds != BuildSeconds || j.RunSeconds != RunSeconds || j.Authority != "synthetic_candidate_behavior_job_only" || j.QualificationCredit {
		return ErrInvalid
	}
	expected := j.SHA256
	j.SHA256 = ""
	raw, _ := json.Marshal(j)
	if expected != workerplan.Hash(j) || len(raw) > MaxJobBytes {
		return ErrInvalid
	}
	return nil
}
func (j Job) Inputs(dataset []byte) map[string][]byte {
	return map[string][]byte{"candidate.go": []byte(j.Plan.Candidate.Source.Text), "harness.go": []byte(Harness(j.Plan)), "dataset.json": dataset}
}
func ReadJob(r io.Reader) (Job, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxJobBytes+1))
	var j Job
	if err != nil || workerplan.ReadBoundedShape(raw, &j, MaxJobBytes) != nil || j.Validate() != nil {
		return Job{}, ErrInvalid
	}
	return j, nil
}

type Result struct {
	Schema                string `json:"schema"`
	JobSHA256             string `json:"job_sha256"`
	PlanSHA256            string `json:"plan_sha256"`
	CandidateSHA256       string `json:"candidate_sha256"`
	Status                string `json:"status"`
	ReasonCode            string `json:"reason_code"`
	BuildCompleted        bool   `json:"build_completed"`
	RunAttempted          bool   `json:"run_attempted"`
	Values                []bool `json:"values"`
	Matched               int    `json:"matched"`
	Mismatched            int    `json:"mismatched"`
	Missing               int    `json:"missing"`
	AllAnnotationsMatch   bool   `json:"all_annotations_match"`
	Diagnostics           string `json:"diagnostics"`
	DiagnosticsTruncated  bool   `json:"diagnostics_truncated"`
	RunStdout             string `json:"run_stdout"`
	RunStderr             string `json:"run_stderr"`
	RunOutputTruncated    bool   `json:"run_output_truncated"`
	ImageIdentityVerified bool   `json:"image_identity_verified"`
	IsolationVerified     bool   `json:"isolation_verified"`
	QualificationCredit   bool   `json:"qualification_credit"`
	Authority             string `json:"authority"`
	SHA256                string `json:"sha256"`
}

func (r Result) Validate(j Job) error {
	if j.Validate() != nil || r.Schema != ResultSchema || r.JobSHA256 != j.SHA256 || r.PlanSHA256 != j.Plan.SHA256 || r.CandidateSHA256 != j.Plan.Candidate.SHA256 || r.Values == nil || len(r.Values) > len(j.Plan.Cases) || r.QualificationCredit || r.ImageIdentityVerified || r.IsolationVerified || r.Authority != "synthetic_candidate_behavior_worker_only" {
		return ErrInvalid
	}
	for _, s := range []string{r.Diagnostics, r.RunStdout, r.RunStderr} {
		if !utf8.ValidString(s) || len(s) > MaxOutput {
			return ErrInvalid
		}
	}
	matched := 0
	for i, v := range r.Values {
		if v == j.Plan.Cases[i].Expected {
			matched++
		}
	}
	if r.Matched != matched || r.Mismatched != len(r.Values)-matched || r.Missing != len(j.Plan.Cases)-len(r.Values) {
		return ErrInvalid
	}
	if r.AllAnnotationsMatch != (r.Status == "completed" && r.Mismatched == 0) {
		return ErrInvalid
	}
	if r.ReasonCode == "build_output_limit" && !r.DiagnosticsTruncated {
		return ErrInvalid
	}
	if r.ReasonCode == "run_output_limit" && !r.RunOutputTruncated {
		return ErrInvalid
	}
	switch r.Status {
	case "completed", "mismatch":
		values, valid := parseValues(r.RunStderr, len(j.Plan.Cases))
		if r.ReasonCode != "fixture_compared" || !r.BuildCompleted || !r.RunAttempted || r.DiagnosticsTruncated || r.RunOutputTruncated || r.RunStdout != "" || !valid || len(values) != len(j.Plan.Cases) || !same(values, r.Values) || r.Missing != 0 || (r.Status == "completed") != (r.Mismatched == 0) {
			return ErrInvalid
		}
	case "failed":
		if r.AllAnnotationsMatch {
			return ErrInvalid
		}
		switch r.ReasonCode {
		case "cancelled_before_build", "input_mismatch", "source_rejected", "toolchain_mismatch", "workspace_failed", "build_failed", "build_timeout", "build_cancelled", "build_output_limit":
			if r.BuildCompleted || r.RunAttempted || len(r.Values) != 0 || r.RunStdout != "" || r.RunStderr != "" || r.RunOutputTruncated {
				return ErrInvalid
			}
		case "run_failed", "run_timeout", "run_cancelled", "run_output_limit", "run_output_invalid":
			values, _ := parseValues(r.RunStderr, len(j.Plan.Cases))
			if !r.BuildCompleted || !r.RunAttempted || !same(values, r.Values) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != workerplan.Hash(r) {
		return ErrInvalid
	}
	return nil
}
func same(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func parseValues(raw string, max int) ([]bool, bool) {
	values := []bool{}
	if raw == "" {
		return values, false
	}
	lines := strings.Split(raw, "\n")
	for _, line := range lines[:len(lines)-1] {
		if len(values) >= max {
			return values, false
		}
		prefix := fmt.Sprintf("_mantik %d ", len(values))
		if line != prefix+"true" && line != prefix+"false" {
			return values, false
		}
		values = append(values, line == prefix+"true")
	}
	return values, lines[len(lines)-1] == "" && len(values) == max
}
func ReadResult(r io.Reader, j Job) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxResultBytes+1))
	var v Result
	if err != nil || workerplan.ReadBoundedShape(raw, &v, MaxResultBytes) != nil || v.Validate(j) != nil {
		return Result{}, ErrInvalid
	}
	return v, nil
}
