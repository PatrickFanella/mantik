// Package candidategeneration retains offline generation plans and exact recorded
// outputs. It has no provider client, compiler, process or service capability.
package candidategeneration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PlanSchema = "mantik-candidate-generation-plan-v1"
const RecordingSchema = "mantik-candidate-generation-recording-v1"
const OutputSchema = "mantik-candidate-generation-output-v1"
const MaxRequirementBytes = 4096
const MaxPlanBytes = candidate.MaxPackageBytes + 32768
const MaxOutputBytes = 6*candidate.MaxSourceBytes + 4096
const MaxRecordingBytes = MaxPlanBytes + 4*((MaxOutputBytes+2)/3) + candidate.MaxPackageBytes + 16384

var ErrInvalid = errors.New("invalid or unsupported recorded candidate generation")
var modelLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999
}
func validText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !bytes.ContainsRune([]byte(s), 0)
}

type Plan struct {
	Schema                string              `json:"schema"`
	ModelLabel            string              `json:"model_label"`
	Requirement           string              `json:"requirement"`
	Hypothesis            hypothesis.Result   `json:"hypothesis"`
	Dataset               workerplan.Input    `json:"dataset"`
	Toolchain             candidate.Toolchain `json:"toolchain"`
	Interface             string              `json:"interface"`
	Prompt                string              `json:"prompt"`
	RequestedAt           time.Time           `json:"requested_at"`
	Authority             string              `json:"authority"`
	ProviderCalled        bool                `json:"provider_called"`
	ModelIdentityVerified bool                `json:"model_identity_verified"`
	SourceGenerated       bool                `json:"source_generated"`
	QualificationCredit   bool                `json:"qualification_credit"`
	SHA256                string              `json:"sha256"`
}

func prompt(p Plan) string {
	context, _ := json.Marshal(struct {
		Requirement string              `json:"requirement"`
		Hypothesis  hypothesis.Result   `json:"hypothesis"`
		Dataset     workerplan.Input    `json:"dataset"`
		Toolchain   candidate.Toolchain `json:"toolchain"`
		Interface   string              `json:"interface"`
	}{p.Requirement, p.Hypothesis, p.Dataset, p.Toolchain, p.Interface})
	return "Propose synthetic research Go source only. Return exactly one JSON object with schema mantik-candidate-generation-output-v1 and source containing the original Go text. Do not use Markdown. The supported interface is package candidate with func Entry(int64) bool. No imports, main, Go directives, provider/service credentials or external I/O. Validation will run separately and does not activate a strategy or establish profitability.\nContext:\n" + string(context)
}
func NewPlan(h hypothesis.Declaration, rawDataset []byte, image, model, requirement string, at time.Time) (Plan, error) {
	// Reuse the existing hypothesis/dataset/toolchain validation and pinning rules.
	// This internal placeholder is never exported as a candidate or executed.
	pins, err := candidate.New([]byte("package candidate\n"), h, rawDataset, image)
	if err != nil {
		return Plan{}, ErrInvalid
	}
	p := Plan{Schema: PlanSchema, ModelLabel: model, Requirement: requirement, Hypothesis: pins.Hypothesis, Dataset: pins.Dataset, Toolchain: pins.Toolchain, Interface: candidate.Interface, RequestedAt: at.UTC(), Authority: "recorded_candidate_generation_plan_only"}
	p.Prompt = prompt(p)
	p.SHA256 = workerplan.Hash(p)
	return p, p.Validate()
}
func (p Plan) Validate() error {
	sealed, err := hypothesis.Seal(p.Hypothesis.Declaration)
	if err != nil || !reflect.DeepEqual(sealed, p.Hypothesis) || p.Schema != PlanSchema || !modelLabel.MatchString(p.ModelLabel) || !validText(p.Requirement, MaxRequirementBytes) || p.Hypothesis.Declaration.EvidenceClass != "synthetic_unqualified" || p.Hypothesis.Declaration.Instrument != "DEMO" || p.Interface != candidate.Interface || !validTime(p.RequestedAt) || p.Authority != "recorded_candidate_generation_plan_only" || p.ProviderCalled || p.ModelIdentityVerified || p.SourceGenerated || p.QualificationCredit {
		return ErrInvalid
	}
	d := p.Dataset
	if d.Reference != "dataset.json" || d.MediaType != "application/json" || d.Bytes < 1 || d.Bytes > workerplan.MaxInputBytes || !digest.MatchString(d.SHA256) || !digest.MatchString(d.CanonicalSHA256) || p.Toolchain.Runtime != workerplan.Runtime || !imageID.MatchString(p.Toolchain.ImageSHA256) || p.Prompt != prompt(p) {
		return ErrInvalid
	}
	sha := p.SHA256
	p.SHA256 = ""
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxPlanBytes || sha != workerplan.Hash(p) {
		return ErrInvalid
	}
	return nil
}
func (p Plan) VerifyDataset(raw []byte) error {
	if p.Validate() != nil {
		return ErrInvalid
	}
	pins, err := candidate.New([]byte("package candidate\n"), p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256)
	if err != nil || !reflect.DeepEqual(pins.Dataset, p.Dataset) {
		return ErrInvalid
	}
	return nil
}

type Output struct {
	Schema string `json:"schema"`
	Source string `json:"source"`
}

// outputSource accepts invalid Go deliberately. Syntax, interface, safety and
// behavior are checks for the isolated workers, not reasons to erase a draft.
func outputSource(raw []byte) (string, string) {
	if len(raw) == 0 {
		return "", "missing_output"
	}
	var o Output
	if workerplan.ReadBoundedShape(raw, &o, MaxOutputBytes) != nil || o.Schema != OutputSchema {
		return "", "invalid_output"
	}
	if !validText(o.Source, candidate.MaxSourceBytes) {
		return "", "invalid_source"
	}
	return o.Source, "source_payload_retained"
}

type Recording struct {
	Schema                  string             `json:"schema"`
	Plan                    Plan               `json:"plan"`
	Output                  []byte             `json:"output"`
	OutputBytes             int                `json:"output_bytes"`
	OutputSHA256            string             `json:"output_sha256"`
	CapturedAt              time.Time          `json:"captured_at"`
	Status                  string             `json:"status"`
	ReasonCode              string             `json:"reason_code"`
	Candidate               *candidate.Package `json:"candidate,omitempty"`
	Authority               string             `json:"authority"`
	ProviderCalled          bool               `json:"provider_called"`
	ModelIdentityVerified   bool               `json:"model_identity_verified"`
	ModelAuthorshipVerified bool               `json:"model_authorship_verified"`
	SourceCompiled          bool               `json:"source_compiled"`
	SourceExecuted          bool               `json:"source_executed"`
	ServiceBindingVerified  bool               `json:"service_binding_verified"`
	QualificationCredit     bool               `json:"qualification_credit"`
	SHA256                  string             `json:"sha256"`
}

func NewRecording(p Plan, rawDataset, output []byte, at time.Time) (Recording, error) {
	if p.VerifyDataset(rawDataset) != nil || len(output) > MaxOutputBytes {
		return Recording{}, ErrInvalid
	}
	encoded, _ := json.Marshal(p)
	var detached Plan
	_ = json.Unmarshal(encoded, &detached)
	r := Recording{Schema: RecordingSchema, Plan: detached, Output: append([]byte{}, output...), OutputBytes: len(output), OutputSHA256: workerplan.Digest(output), CapturedAt: at.UTC(), Status: "output_rejected", Authority: "caller_recorded_candidate_generation_only"}
	src, reason := outputSource(output)
	r.ReasonCode = reason
	if reason == "source_payload_retained" {
		c, err := candidate.New([]byte(src), p.Hypothesis.Declaration, rawDataset, p.Toolchain.ImageSHA256)
		if err != nil {
			return Recording{}, ErrInvalid
		}
		r.Candidate = &c
		r.Status = "candidate_recorded"
	}
	r.SHA256 = workerplan.Hash(r)
	return r, r.Validate()
}
func (r Recording) Validate() error {
	if r.Schema != RecordingSchema || r.Plan.Validate() != nil || r.Output == nil || len(r.Output) > MaxOutputBytes || r.OutputBytes != len(r.Output) || r.OutputSHA256 != workerplan.Digest(r.Output) || !validTime(r.CapturedAt) || r.CapturedAt.Before(r.Plan.RequestedAt) || r.Authority != "caller_recorded_candidate_generation_only" || r.ProviderCalled || r.ModelIdentityVerified || r.ModelAuthorshipVerified || r.SourceCompiled || r.SourceExecuted || r.ServiceBindingVerified || r.QualificationCredit {
		return ErrInvalid
	}
	src, reason := outputSource(r.Output)
	if reason != r.ReasonCode {
		return ErrInvalid
	}
	if reason == "source_payload_retained" {
		c := r.Candidate
		if r.Status != "candidate_recorded" || c == nil || c.Validate() != nil || c.Source.Text != src || !reflect.DeepEqual(c.Hypothesis, r.Plan.Hypothesis) || !reflect.DeepEqual(c.Dataset, r.Plan.Dataset) || c.Toolchain != r.Plan.Toolchain {
			return ErrInvalid
		}
	} else if r.Status != "output_rejected" || r.Candidate != nil {
		return ErrInvalid
	}
	sha := r.SHA256
	r.SHA256 = ""
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxRecordingBytes || sha != workerplan.Hash(r) {
		return ErrInvalid
	}
	return nil
}
func (r Recording) VerifyDataset(raw []byte) error {
	if r.Validate() != nil || r.Plan.VerifyDataset(raw) != nil {
		return ErrInvalid
	}
	if r.Candidate != nil && r.Candidate.VerifyDataset(raw) != nil {
		return ErrInvalid
	}
	return nil
}
func ReadPlan(input io.Reader) (Plan, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxPlanBytes+1))
	var p Plan
	if err != nil || workerplan.ReadBoundedShape(raw, &p, MaxPlanBytes) != nil || p.Validate() != nil {
		return Plan{}, ErrInvalid
	}
	return p, nil
}
func ReadRecording(input io.Reader) (Recording, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxRecordingBytes+1))
	var r Recording
	if err != nil || workerplan.ReadBoundedShape(raw, &r, MaxRecordingBytes) != nil || r.Validate() != nil {
		return Recording{}, ErrInvalid
	}
	return r, nil
}
