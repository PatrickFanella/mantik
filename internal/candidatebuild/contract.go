// Package candidatebuild defines compile-only research jobs. A compiler pass
// does not execute candidate source or establish its trading behavior.
package candidatebuild

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const JobSchema = "mantik-candidate-build-job-v1"
const ResultSchema = "mantik-candidate-build-result-v1"
const Interface = candidate.Interface
const Harness = "package candidate\nvar _ func(int64) bool = Entry\n"
const MaxJobBytes = candidate.MaxPackageBytes + 8192
const MaxResultBytes = 512 << 10
const MaxDiagnostics = 64 << 10
const CompileSeconds = 20

var ErrInvalid = errors.New("invalid or unsupported candidate build protocol")

type Job struct {
	Schema              string               `json:"schema"`
	Candidate           candidate.Package    `json:"candidate"`
	Interface           string               `json:"interface"`
	HarnessSHA256       string               `json:"harness_sha256"`
	Platform            string               `json:"platform"`
	Resources           workerplan.Resources `json:"resources"`
	CompileSeconds      int                  `json:"compile_seconds"`
	Authority           string               `json:"authority"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}

func New(p candidate.Package) (Job, error) {
	if p.Validate() != nil {
		return Job{}, ErrInvalid
	}
	// Detach nested declaration slices and retain the original candidate identity.
	raw, _ := json.Marshal(p)
	var detached candidate.Package
	if json.Unmarshal(raw, &detached) != nil {
		return Job{}, ErrInvalid
	}
	j := Job{Schema: JobSchema, Candidate: detached, Interface: Interface, HarnessSHA256: workerplan.Digest([]byte(Harness)), Platform: "linux_amd64", Resources: workerplan.Profile(), CompileSeconds: CompileSeconds, Authority: "candidate_compile_job_only"}
	j.SHA256 = workerplan.Hash(j)
	return j, j.Validate()
}

func (j Job) Validate() error {
	if j.Schema != JobSchema || j.Candidate.Validate() != nil || j.Interface != Interface || j.HarnessSHA256 != workerplan.Digest([]byte(Harness)) || j.Platform != "linux_amd64" || j.Resources != workerplan.Profile() || j.CompileSeconds != CompileSeconds || j.Authority != "candidate_compile_job_only" || j.QualificationCredit {
		return ErrInvalid
	}
	expected := j.SHA256
	j.SHA256 = ""
	if expected != workerplan.Hash(j) {
		return ErrInvalid
	}
	raw, _ := json.Marshal(j)
	if len(raw) > MaxJobBytes {
		return ErrInvalid
	}
	return nil
}

func (j Job) Inputs() map[string][]byte {
	return map[string][]byte{"candidate.go": []byte(j.Candidate.Source.Text), "interface.go": []byte(Harness)}
}

type Result struct {
	Schema                 string `json:"schema"`
	JobSHA256              string `json:"job_sha256"`
	CandidateSHA256        string `json:"candidate_sha256"`
	Interface              string `json:"interface"`
	Status                 string `json:"status"`
	ReasonCode             string `json:"reason_code"`
	CompilerVersionChecked bool   `json:"compiler_version_checked"`
	SourceCompiled         bool   `json:"source_compiled"`
	SourceExecuted         bool   `json:"source_executed"`
	Diagnostics            string `json:"diagnostics"`
	DiagnosticsSHA256      string `json:"diagnostics_sha256"`
	DiagnosticsTruncated   bool   `json:"diagnostics_truncated"`
	ImageIdentityVerified  bool   `json:"image_identity_verified"`
	IsolationVerified      bool   `json:"isolation_verified"`
	Authority              string `json:"authority"`
	QualificationCredit    bool   `json:"qualification_credit"`
	SHA256                 string `json:"sha256"`
}

func (r Result) Validate(j Job) error {
	if j.Validate() != nil || r.Schema != ResultSchema || r.JobSHA256 != j.SHA256 || r.CandidateSHA256 != j.Candidate.SHA256 || r.Interface != Interface || r.SourceExecuted || r.ImageIdentityVerified || r.IsolationVerified || r.Authority != "candidate_compile_conformance_only" || r.QualificationCredit || !utf8.ValidString(r.Diagnostics) || len(r.Diagnostics) > MaxDiagnostics || r.DiagnosticsSHA256 != workerplan.Digest([]byte(r.Diagnostics)) {
		return ErrInvalid
	}
	switch r.Status {
	case "completed":
		if r.ReasonCode != "interface_compiled" || !r.CompilerVersionChecked || !r.SourceCompiled || r.DiagnosticsTruncated {
			return ErrInvalid
		}
	case "failed":
		if r.SourceCompiled {
			return ErrInvalid
		}
		switch r.ReasonCode {
		case "cancelled_before_compile", "input_mismatch", "interface_rejected", "compiler_unavailable", "compiler_version_mismatch":
			if r.CompilerVersionChecked {
				return ErrInvalid
			}
		case "compile_failed", "compile_timeout", "compile_cancelled", "diagnostic_limit":
			if !r.CompilerVersionChecked {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if r.ReasonCode == "diagnostic_limit" && !r.DiagnosticsTruncated {
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

func ReadJob(reader io.Reader) (Job, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxJobBytes+1))
	var j Job
	if err != nil || workerplan.ReadBoundedShape(raw, &j, MaxJobBytes) != nil || j.Validate() != nil {
		return Job{}, ErrInvalid
	}
	return j, nil
}

func ReadResult(reader io.Reader, j Job) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxResultBytes+1))
	var r Result
	if err != nil || workerplan.ReadBoundedShape(raw, &r, MaxResultBytes) != nil || r.Validate(j) != nil {
		return Result{}, ErrInvalid
	}
	return r, nil
}

func ValidInputs(j Job, source, harness []byte) bool {
	return j.Validate() == nil && reflect.DeepEqual(source, []byte(j.Candidate.Source.Text)) && string(harness) == Harness
}
