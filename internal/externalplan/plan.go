// Package externalplan declares the fixed Python comparison boundary. It owns
// no worker process, service, database, provider or operational authority.
package externalplan

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const JobSchema = "mantik-external-fixture-worker-job-v1"
const ResultSchema = "mantik-backtesting-fixture-comparison-v1"
const Tool = "backtesting-fixture-adapter-v1"
const Runtime = "python3.13.9"
const LibraryVersion = "0.6.6"
const DatasetSHA = "b3f9c5ae2e61f45eda833f0ea2af67565c30aa9885df5e747a696c0e41ac446d"
const RecipeSHA = "9da1c82a0bdbdab99dcf61dd3c59cb2b70b75482639b48f953063cb7752a7d8e"
const CanonicalInputSHA = "6bbf76684be7cb102f174f2d3cc3f56bab9df5ac5dd587d718ca5c59726337a8"
const MaxResultBytes = (64 << 10) + 1

var hex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var id = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var image = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Artifact struct {
	Reference string `json:"reference"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}
type Metrics struct {
	Observations *int64 `json:"observations"`
	Actions      *int64 `json:"actions"`
	Realized     *int64 `json:"realized_pnl_cents"`
	Marked       *int64 `json:"marked_pnl_cents"`
}

func (m Metrics) values() map[string]*int64 {
	return map[string]*int64{"observations": m.Observations, "actions": m.Actions, "realized_pnl_cents": m.Realized, "marked_pnl_cents": m.Marked}
}
func (m Metrics) valid() bool {
	for _, value := range m.values() {
		if value == nil || *value < -1e12 || *value > 1e12 {
			return false
		}
	}
	return *m.Observations >= 0 && *m.Actions >= 0
}

type NativeMethod struct {
	MethodID     string          `json:"method_id"`
	MethodSHA256 string          `json:"method_sha256"`
	Metrics      Metrics         `json:"metrics"`
	Replay       json.RawMessage `json:"replay,omitempty"`
}
type Native struct {
	ID            string         `json:"id"`
	RecipeSHA256  string         `json:"recipe_sha256"`
	InputSHA256   string         `json:"input_sha256"`
	Status        string         `json:"status"`
	EvidenceClass string         `json:"evidence_class"`
	Qualified     *bool          `json:"qualified"`
	Results       []NativeMethod `json:"results"`
}
type Job struct {
	Schema              string                `json:"schema"`
	ID                  string                `json:"id"`
	Tool                string                `json:"tool"`
	Runtime             string                `json:"runtime"`
	LibraryVersion      string                `json:"library_version"`
	ImageSHA256         string                `json:"image_sha256"`
	AdapterSHA256       string                `json:"adapter_sha256"`
	RequirementsSHA256  string                `json:"requirements_sha256"`
	Recipe              experimentplan.Recipe `json:"recipe"`
	Inputs              []Artifact            `json:"inputs"`
	NativeReference     Native                `json:"native_reference"`
	Resources           workerplan.Resources  `json:"resources"`
	Transport           string                `json:"transport"`
	OutputReference     string                `json:"output_reference"`
	Authority           string                `json:"authority"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}

func Profile() workerplan.Resources {
	r := workerplan.Profile()
	r.MaxResultBytes = MaxResultBytes
	return r
}
func pins(j Job) []experimentplan.MethodPin {
	return append(append([]experimentplan.MethodPin{}, j.Recipe.Methods...), j.Recipe.Benchmarks...)
}
func (n Native) valid(j Job) bool {
	if !id.MatchString(n.ID) || n.RecipeSHA256 != RecipeSHA || n.InputSHA256 != CanonicalInputSHA || n.Status != "completed" || n.EvidenceClass != "synthetic_unqualified" || n.Qualified == nil || *n.Qualified || len(n.Results) != 4 {
		return false
	}
	for i, pin := range pins(j) {
		m := n.Results[i]
		if m.MethodID != pin.ID || m.MethodSHA256 != pin.SHA256 || !m.Metrics.valid() {
			return false
		}
	}
	return true
}
func Seal(j Job) (Job, error) {
	if j.Schema != JobSchema || !id.MatchString(j.ID) || j.Tool != Tool || j.Runtime != Runtime || j.LibraryVersion != LibraryVersion || !image.MatchString(j.ImageSHA256) || !hex.MatchString(j.AdapterSHA256) || !hex.MatchString(j.RequirementsSHA256) || j.Recipe.Validate() != nil || j.Recipe.SHA256 != RecipeSHA || len(j.Inputs) != 3 || j.Resources != Profile() || j.Transport != "single_comparison_stdout_64k_local_logs_v1" || j.OutputReference != "comparison.json" || j.Authority != "external_fixture_worker_declaration_only" || j.QualificationCredit || !j.NativeReference.valid(j) {
		return Job{}, workerplan.ErrInvalid
	}
	for i, name := range []string{"dataset.json", "recipe.json", "native.json"} {
		a := j.Inputs[i]
		if a.Reference != name || a.Bytes < 1 || a.Bytes > workerplan.MaxInputBytes || !hex.MatchString(a.SHA256) {
			return Job{}, workerplan.ErrInvalid
		}
	}
	// Replay bodies are input artifacts, not part of the compact declaration.
	for _, m := range j.NativeReference.Results {
		if len(m.Replay) != 0 {
			return Job{}, workerplan.ErrInvalid
		}
	}
	j.SHA256 = ""
	j.SHA256 = workerplan.Hash(j)
	raw, _ := json.Marshal(j)
	if len(raw) > workerplan.MaxJobBytes {
		return Job{}, workerplan.ErrInvalid
	}
	var detached Job
	_ = json.Unmarshal(raw, &detached)
	return detached, nil
}
func (j Job) Validate() error {
	s, err := Seal(j)
	if err != nil || s.SHA256 != j.SHA256 {
		return workerplan.ErrInvalid
	}
	return nil
}

// exportedSeal accepts the exact compact field order used by Go fixture exports.
func exportedSeal(raw []byte, expected string) bool {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return false
	}
	body := compact.Bytes()
	suffix := []byte(`,"sha256":"` + expected + `"}`)
	if !bytes.HasSuffix(body, suffix) {
		return false
	}
	unsigned := append(bytes.Clone(body[:len(body)-len(suffix)]), []byte(`,"sha256":""}`)...)
	return workerplan.Digest(unsigned) == expected
}
func Declare(jobID, imageSHA, adapterSHA, requirementsSHA string, artifacts map[string][]byte) (Job, error) {
	j := Job{Schema: JobSchema, ID: jobID, Tool: Tool, Runtime: Runtime, LibraryVersion: LibraryVersion, ImageSHA256: imageSHA, AdapterSHA256: adapterSHA, RequirementsSHA256: requirementsSHA, Resources: Profile(), Transport: "single_comparison_stdout_64k_local_logs_v1", OutputReference: "comparison.json", Authority: "external_fixture_worker_declaration_only"}
	if len(artifacts) != 3 {
		return Job{}, workerplan.ErrInvalid
	}
	for _, name := range []string{"dataset.json", "recipe.json", "native.json"} {
		raw, ok := artifacts[name]
		if !ok || len(raw) == 0 || len(raw) > workerplan.MaxInputBytes {
			return Job{}, workerplan.ErrInvalid
		}
		j.Inputs = append(j.Inputs, Artifact{name, len(raw), workerplan.Digest(raw)})
	}
	if !exportedSeal(artifacts["dataset.json"], DatasetSHA) || !exportedSeal(artifacts["recipe.json"], RecipeSHA) || workerplan.ReadDatasetShape(artifacts["recipe.json"], &j.Recipe) != nil || workerplan.ReadDatasetShape(artifacts["native.json"], &j.NativeReference) != nil {
		return Job{}, workerplan.ErrInvalid
	}
	for i := range j.NativeReference.Results {
		j.NativeReference.Results[i].Replay = nil
	}
	return Seal(j)
}
func (j Job) ValidateInputs(artifacts map[string][]byte) error {
	if j.Validate() != nil {
		return workerplan.ErrInvalid
	}
	rebuilt, err := Declare(j.ID, j.ImageSHA256, j.AdapterSHA256, j.RequirementsSHA256, artifacts)
	if err != nil || !reflect.DeepEqual(j, rebuilt) {
		return workerplan.ErrInvalid
	}
	return nil
}
func ReadJob(input io.Reader) (Job, error) {
	raw, err := io.ReadAll(io.LimitReader(input, workerplan.MaxJobBytes+1))
	if err != nil || len(raw) > workerplan.MaxJobBytes {
		return Job{}, workerplan.ErrInvalid
	}
	var j Job
	if workerplan.ReadDatasetShape(raw, &j) != nil {
		return Job{}, workerplan.ErrInvalid
	}
	return j, j.Validate()
}
