package candidatetest

import (
	"encoding/json"
	"io"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const RecordingSchema = "mantik-candidate-fixture-recording-v1"
const ReportSchema = "mantik-candidate-fixture-comparison-v1"
const MaxRecordingBytes = 32 << 10

var requestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type Output struct {
	CaseID string `json:"case_id"`
	Value  *bool  `json:"value"`
}
type Recording struct {
	Schema          string   `json:"schema"`
	AttemptID       string   `json:"attempt_id"`
	PlanSHA256      string   `json:"plan_sha256"`
	CandidateSHA256 string   `json:"candidate_sha256"`
	Status          string   `json:"status"`
	ReasonCode      string   `json:"reason_code"`
	Outputs         []Output `json:"outputs"`
}
type Comparison struct {
	Case        Case   `json:"case"`
	Reported    []bool `json:"reported"`
	Disposition string `json:"disposition"`
}
type Report struct {
	Schema              string       `json:"schema"`
	PlanSHA256          string       `json:"plan_sha256"`
	CandidateSHA256     string       `json:"candidate_sha256"`
	RecordingSHA256     string       `json:"recording_sha256"`
	Recording           Recording    `json:"recording"`
	Status              string       `json:"status"`
	Cases               []Comparison `json:"cases"`
	Unmatched           []Output     `json:"unmatched"`
	Matched             int          `json:"matched"`
	Mismatched          int          `json:"mismatched"`
	Missing             int          `json:"missing"`
	Duplicates          int          `json:"duplicates"`
	AllAnnotationsMatch bool         `json:"all_annotations_match"`
	Authority           string       `json:"authority"`
	ExecutionVerified   bool         `json:"execution_verified"`
	QualificationCredit bool         `json:"qualification_credit"`
	SHA256              string       `json:"sha256"`
}

func ReadRecording(r io.Reader) (Recording, []byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxRecordingBytes+1))
	var record Recording
	if err != nil || workerplan.ReadBoundedShape(raw, &record, MaxRecordingBytes) != nil || record.Schema != RecordingSchema || !requestID.MatchString(record.AttemptID) || record.Outputs == nil || len(record.Outputs) > 2*MaxCases {
		return Recording{}, nil, ErrInvalid
	}
	switch record.Status {
	case "reported_completed":
		if record.ReasonCode != "completed" {
			return Recording{}, nil, ErrInvalid
		}
	case "reported_failed", "unknown":
		if !requestID.MatchString(record.ReasonCode) {
			return Recording{}, nil, ErrInvalid
		}
	default:
		return Recording{}, nil, ErrInvalid
	}
	for _, o := range record.Outputs {
		if !caseID.MatchString(o.CaseID) || o.Value == nil {
			return Recording{}, nil, ErrInvalid
		}
	}
	return record, raw, nil
}

// Evaluate checks every supplied output against every predeclared case. It
// consumes recordings, not host execution attestations or strategy evaluations.
func Evaluate(p Plan, rawDataset []byte, input io.Reader) (Report, error) {
	if p.VerifyDataset(rawDataset) != nil {
		return Report{}, ErrInvalid
	}
	record, raw, err := ReadRecording(input)
	if err != nil || record.PlanSHA256 != p.SHA256 || record.CandidateSHA256 != p.Candidate.SHA256 {
		return Report{}, ErrInvalid
	}
	r := Report{Schema: ReportSchema, PlanSHA256: p.SHA256, CandidateSHA256: p.Candidate.SHA256, RecordingSHA256: workerplan.Digest(raw), Recording: record, Cases: []Comparison{}, Unmatched: []Output{}, Authority: "caller_reported_fixture_annotation_comparison_only"}
	known := map[string]bool{}
	outputs := map[string][]bool{}
	for _, c := range p.Cases {
		known[c.ID] = true
	}
	for _, o := range record.Outputs {
		if !known[o.CaseID] {
			r.Unmatched = append(r.Unmatched, o)
		} else {
			outputs[o.CaseID] = append(outputs[o.CaseID], *o.Value)
		}
	}
	for _, c := range p.Cases {
		values := outputs[c.ID]
		if values == nil {
			values = []bool{}
		}
		row := Comparison{Case: c, Reported: values}
		switch {
		case len(values) == 0:
			row.Disposition = "missing"
			r.Missing++
		case len(values) > 1:
			row.Disposition = "duplicate"
			r.Duplicates += len(values) - 1
		case values[0] != c.Expected:
			row.Disposition = "mismatch"
			r.Mismatched++
		default:
			row.Disposition = "match"
			r.Matched++
		}
		r.Cases = append(r.Cases, row)
	}
	r.Status = "compared"
	if record.Status != "reported_completed" || r.Missing > 0 || r.Duplicates > 0 || len(r.Unmatched) > 0 {
		r.Status = "incomplete"
	}
	r.AllAnnotationsMatch = r.Status == "compared" && r.Mismatched == 0 && r.Matched == len(p.Cases)
	raw, _ = json.Marshal(r)
	var detached Report
	if json.Unmarshal(raw, &detached) != nil {
		return Report{}, ErrInvalid
	}
	detached.SHA256 = workerplan.Hash(detached)
	return detached, nil
}
