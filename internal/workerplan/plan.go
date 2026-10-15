// Package workerplan defines offline research jobs without a transport,
// credential store, execution engine, database or operational authority.
package workerplan

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
)

const JobSchema = "mantik-research-worker-job-v1"
const ResultSchema = "mantik-research-worker-result-v1"
const Tool = "native-fixture-adapter-v1"
const Runtime = "go1.26.9"
const MaxJobBytes = (1 << 20) + 4096
const MaxInputBytes = 8 << 20
const MaxNativeBytes = 8 << 20
const MaxResultBytes = 16 << 20
const WallSeconds = 30

var ErrInvalid = errors.New("invalid or unsupported research worker protocol")
var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var image = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Input struct {
	Reference       string `json:"reference"`
	MediaType       string `json:"media_type"`
	Bytes           int    `json:"bytes"`
	SHA256          string `json:"sha256"`
	CanonicalSHA256 string `json:"canonical_sha256"`
}
type Resources struct {
	WallSeconds    int   `json:"wall_seconds"`
	MemoryBytes    int64 `json:"memory_bytes"`
	CPUMillis      int   `json:"cpu_millis"`
	PIDs           int   `json:"pids"`
	MaxInputBytes  int   `json:"max_input_bytes"`
	MaxResultBytes int   `json:"max_result_bytes"`
}

func Profile() Resources {
	return Resources{WallSeconds, 256 << 20, 1000, 64, MaxInputBytes, MaxResultBytes}
}

type Job struct {
	Schema              string                `json:"schema"`
	ID                  string                `json:"id"`
	Tool                string                `json:"tool"`
	Runtime             string                `json:"runtime"`
	ImageSHA256         string                `json:"image_sha256"`
	Recipe              experimentplan.Recipe `json:"recipe"`
	Input               Input                 `json:"input"`
	Resources           Resources             `json:"resources"`
	OutputReference     string                `json:"output_reference"`
	OutputMediaType     string                `json:"output_media_type"`
	Authority           string                `json:"authority"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}

func Hash(value any) string    { raw, _ := json.Marshal(value); return Digest(raw) }
func Digest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func Seal(j Job) (Job, error) {
	if j.Schema != JobSchema || !identifier.MatchString(j.ID) || j.Tool != Tool || j.Runtime != Runtime || !image.MatchString(j.ImageSHA256) || j.Recipe.Validate() != nil || j.Input.Reference != "dataset.json" || j.Input.MediaType != "application/json" || j.Input.Bytes < 1 || j.Input.Bytes > MaxInputBytes || !digest.MatchString(j.Input.SHA256) || !digest.MatchString(j.Input.CanonicalSHA256) || j.Resources != Profile() || j.OutputReference != "native-attempt.json" || j.OutputMediaType != "application/json" || j.Authority != "research_worker_declaration_only" || j.QualificationCredit {
		return Job{}, ErrInvalid
	}
	raw, _ := json.Marshal(j.Recipe)
	if len(raw) > 1<<20 {
		return Job{}, ErrInvalid
	}
	j.SHA256 = ""
	j.SHA256 = Hash(j)
	raw, _ = json.Marshal(j)
	if len(raw) > MaxJobBytes {
		return Job{}, ErrInvalid
	}
	// Detach caller-owned recipe slices and definitions.
	var copy Job
	if json.Unmarshal(raw, &copy) != nil {
		return Job{}, ErrInvalid
	}
	return copy, nil
}
func (j Job) Validate() error {
	sealed, err := Seal(j)
	if err != nil || sealed.SHA256 != j.SHA256 {
		return ErrInvalid
	}
	return nil
}
func NativeAttemptID(j Job) string { return "worker-" + Digest([]byte(j.ID)) }

type Result struct {
	Schema                string `json:"schema"`
	JobID                 string `json:"job_id"`
	JobSHA256             string `json:"job_sha256"`
	Status                string `json:"status"`
	ErrorCode             string `json:"error_code,omitempty"`
	InputVerified         bool   `json:"input_verified"`
	DeadlineMet           bool   `json:"deadline_met"`
	NativeResult          string `json:"native_result,omitempty"`
	NativeResultSHA256    string `json:"native_result_sha256,omitempty"`
	NativeResultBytes     int    `json:"native_result_bytes"`
	EnforcementScope      string `json:"enforcement_scope"`
	ImageIdentityVerified bool   `json:"image_identity_verified"`
	IsolationVerified     bool   `json:"isolation_verified"`
	Authority             string `json:"authority"`
	QualificationCredit   bool   `json:"qualification_credit"`
	SHA256                string `json:"sha256"`
}
type nativeHeader struct {
	ID            string `json:"id"`
	RecipeSHA256  string `json:"recipe_sha256"`
	InputSHA256   string `json:"input_sha256"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code,omitempty"`
	EvidenceClass string `json:"evidence_class"`
	Qualified     bool   `json:"qualified"`
	Results       []struct {
		MethodID     string `json:"method_id"`
		MethodSHA256 string `json:"method_sha256"`
		Metrics      struct {
			Observations *int64 `json:"observations"`
			Actions      *int64 `json:"actions"`
			Realized     *int64 `json:"realized_pnl_cents"`
			Marked       *int64 `json:"marked_pnl_cents"`
		} `json:"metrics"`
		Replay json.RawMessage `json:"replay,omitempty"`
	} `json:"results"`
}

func (r Result) Validate(j Job) error {
	if j.Validate() != nil || r.Schema != ResultSchema || r.JobID != j.ID || r.JobSHA256 != j.SHA256 || r.EnforcementScope != "artifact_bounds_and_cooperative_context_only" || r.ImageIdentityVerified || r.IsolationVerified || r.Authority != "synthetic_worker_protocol_only" || r.QualificationCredit {
		return ErrInvalid
	}
	if r.NativeResult == "" {
		if r.Status != "failed" || r.NativeResultSHA256 != "" || r.NativeResultBytes != 0 {
			return ErrInvalid
		}
		switch r.ErrorCode {
		case "cancelled_before_input":
			if r.InputVerified || r.DeadlineMet {
				return ErrInvalid
			}
		case "input_decode_failed", "canonical_input_mismatch", "output_limit_exceeded":
			if !r.InputVerified {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	} else {
		if !r.InputVerified || len(r.NativeResult) > MaxNativeBytes || len(r.NativeResult) != r.NativeResultBytes || Digest([]byte(r.NativeResult)) != r.NativeResultSHA256 {
			return ErrInvalid
		}
		var n nativeHeader
		if readJSON([]byte(r.NativeResult), &n) != nil || n.ID != NativeAttemptID(j) || n.RecipeSHA256 != j.Recipe.SHA256 || n.InputSHA256 != j.Input.CanonicalSHA256 || n.EvidenceClass != "synthetic_unqualified" || n.Qualified || n.Status != r.Status || n.ErrorCode != r.ErrorCode {
			return ErrInvalid
		}
		pins := append(append([]experimentplan.MethodPin{}, j.Recipe.Methods...), j.Recipe.Benchmarks...)
		if n.Status == "completed" {
			if n.ErrorCode != "" || len(n.Results) != len(pins) {
				return ErrInvalid
			}
		} else if n.Status != "failed" || !identifier.MatchString(n.ErrorCode) {
			return ErrInvalid
		}
		if len(n.Results) > len(pins) {
			return ErrInvalid
		}
		for i, m := range n.Results {
			if m.MethodID != pins[i].ID || m.MethodSHA256 != pins[i].SHA256 || m.Metrics.Observations == nil || m.Metrics.Actions == nil || m.Metrics.Realized == nil || m.Metrics.Marked == nil || *m.Metrics.Observations < 0 || *m.Metrics.Actions < 0 {
				return ErrInvalid
			}
		}
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != Hash(r) {
		return ErrInvalid
	}
	r.SHA256 = expected
	raw, _ := json.Marshal(r)
	if len(raw) > MaxResultBytes {
		return ErrInvalid
	}
	return nil
}
