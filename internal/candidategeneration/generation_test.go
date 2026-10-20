package candidategeneration

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Plan, []byte, time.Time) {
	t.Helper()
	d, _ := dataset.FixtureDataset()
	raw, _ := json.Marshal(d)
	raw = append(raw, '\n')
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := NewPlan(hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64), "gpt-6.1-sol", "Propose a finite synthetic threshold example.\n", at)
	if err != nil {
		t.Fatal(err)
	}
	return p, raw, at
}
func TestRecordedGenerationPreservesInputsAndRejectedEvidence(t *testing.T) {
	p, raw, at := fixture(t)
	if p.VerifyDataset(raw) != nil || p.Validate() != nil || p.ProviderCalled || p.ModelIdentityVerified || p.SourceGenerated || p.QualificationCredit {
		t.Fatal("invalid plan")
	}
	if p.VerifyDataset(append(append([]byte{}, raw...), ' ')) == nil {
		t.Fatal("changed exact dataset accepted")
	}
	for _, test := range []struct {
		name, source, reason string
		output               []byte
	}{
		{name: "go", source: "package candidate\nfunc Entry(p int64)bool{return p<=10000}\n", reason: "source_payload_retained"},
		{name: "invalid-go", source: "not Go, retained for compilation failure", reason: "source_payload_retained"},
		{name: "missing", output: []byte{}, reason: "missing_output"},
		{name: "binary", output: []byte{0xff, 0, 0xfe}, reason: "invalid_output"},
		{name: "partial", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":`), reason: "invalid_output"},
		{name: "markdown", output: []byte("```go\npackage candidate\n```"), reason: "invalid_output"},
		{name: "null", output: []byte(`null`), reason: "invalid_output"},
		{name: "empty-source", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":""}`), reason: "invalid_source"},
		{name: "nul-source", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":"\u0000"}`), reason: "invalid_source"},
		{name: "unknown", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":"go","extra":true}`), reason: "invalid_output"},
		{name: "duplicate", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":"a","source":"b"}`), reason: "invalid_output"},
		{name: "case", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","Source":"go"}`), reason: "invalid_output"},
		{name: "trailing", output: []byte(`{"schema":"mantik-candidate-generation-output-v1","source":"go"}{}`), reason: "invalid_output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := test.output
			if test.source != "" {
				output, _ = json.Marshal(Output{OutputSchema, test.source})
			}
			r, err := NewRecording(p, raw, output, at.Add(time.Second))
			if err != nil || r.Validate() != nil || r.VerifyDataset(raw) != nil || r.ReasonCode != test.reason || !bytes.Equal(output, r.Output) || r.OutputBytes != len(output) || r.OutputSHA256 != workerplan.Digest(output) {
				t.Fatal(r, err)
			}
			if r.ProviderCalled || r.ModelAuthorshipVerified || r.SourceCompiled || r.SourceExecuted || r.ServiceBindingVerified || r.QualificationCredit {
				t.Fatal("false authority")
			}
			if test.source != "" {
				if r.Status != "candidate_recorded" || r.Candidate == nil || r.Candidate.Source.Text != test.source || r.Candidate.SourceCompiled || r.Candidate.SourceExecuted || r.Candidate.VerifyDataset(raw) != nil {
					t.Fatal("candidate binding changed")
				}
			} else if r.Status != "output_rejected" || r.Candidate != nil {
				t.Fatal("rejected output invented a candidate")
			}
			r2, err := NewRecording(p, raw, output, at.Add(time.Second))
			if err != nil || !reflect.DeepEqual(r, r2) {
				t.Fatal("identical recording changed", err)
			}
			encoded, _ := json.Marshal(r)
			read, err := ReadRecording(bytes.NewReader(encoded))
			if err != nil || !reflect.DeepEqual(read, r) {
				t.Fatal("read changed", err)
			}
			if len(output) > 0 {
				output[0] ^= 1
				if r.Validate() != nil {
					t.Fatal("caller mutated captured bytes")
				}
			}
		})
	}
}
func TestGenerationRejectsChangedPinsAuthorityAndTime(t *testing.T) {
	p, raw, at := fixture(t)
	oversizedSource, _ := json.Marshal(Output{OutputSchema, strings.Repeat("x", candidate.MaxSourceBytes+1)})
	rejected, err := NewRecording(p, raw, oversizedSource, at)
	if err != nil || rejected.ReasonCode != "invalid_source" || rejected.Candidate != nil {
		t.Fatal("oversized source lost rejection", err)
	}
	output, _ := json.Marshal(Output{OutputSchema, "package candidate\n"})
	r, err := NewRecording(p, raw, output, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(r)
	for _, kind := range []string{"output", "candidate", "hypothesis", "dataset", "model", "prompt", "plan-flag", "record-flag", "time", "status", "reason", "null-output"} {
		t.Run(kind, func(t *testing.T) {
			var bad Recording
			_ = json.Unmarshal(encoded, &bad)
			switch kind {
			case "output":
				bad.Output = []byte(`{}`)
				bad.OutputBytes = 2
				bad.OutputSHA256 = workerplan.Digest(bad.Output)
			case "candidate":
				other, err := candidate.New([]byte("different source"), p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256)
				if err != nil {
					t.Fatal(err)
				}
				bad.Candidate = &other
			case "hypothesis":
				bad.Plan.Hypothesis.Declaration.Label = "changed"
			case "dataset":
				bad.Plan.Dataset.SHA256 = strings.Repeat("b", 64)
			case "model":
				bad.Plan.ModelLabel = "bad model label"
			case "prompt":
				bad.Plan.Prompt += " extra"
			case "plan-flag":
				bad.Plan.ProviderCalled = true
			case "record-flag":
				bad.ModelAuthorshipVerified = true
			case "time":
				bad.CapturedAt = at.Add(-time.Second)
			case "status":
				bad.Status = "output_rejected"
			case "reason":
				bad.ReasonCode = "missing_output"
			case "null-output":
				bad.Output = nil
			}
			bad.Plan.SHA256 = ""
			bad.Plan.SHA256 = workerplan.Hash(bad.Plan)
			bad.SHA256 = ""
			bad.SHA256 = workerplan.Hash(bad)
			if bad.Validate() == nil {
				t.Fatal("self-hashed forgery accepted", kind)
			}
		})
	}
	if _, err := NewRecording(p, raw, output, at.Add(-time.Second)); err == nil {
		t.Fatal("backdated output accepted")
	}
	if _, err := NewRecording(p, append(append([]byte{}, raw...), ' '), output, at); err == nil {
		t.Fatal("changed raw dataset accepted")
	}
	if _, err := NewRecording(p, raw, bytes.Repeat([]byte("x"), MaxOutputBytes+1), at); err == nil {
		t.Fatal("oversized output accepted")
	}
	for _, need := range []string{"", strings.Repeat("x", MaxRequirementBytes+1), "nul\x00", "invalid\xff"} {
		if _, err := NewPlan(p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256, p.ModelLabel, need, at); err == nil {
			t.Fatal("invalid requirement accepted")
		}
	}
	planRaw, _ := json.Marshal(p)
	for _, isPlan := range []bool{false, true} {
		good := encoded
		limit := MaxRecordingBytes
		if isPlan {
			good = planRaw
			limit = MaxPlanBytes
		}
		for _, bad := range [][]byte{append(append([]byte{}, good...), []byte("{}")...), []byte(strings.Replace(string(good), `"schema":`, `"schema":"duplicate","schema":`, 1)), []byte(strings.Replace(string(good), `"schema":`, `"Schema":`, 1)), []byte(strings.Replace(string(good), `"schema":`, `"extra":true,"schema":`, 1)), bytes.Repeat([]byte(" "), limit+1)} {
			if isPlan {
				if _, err := ReadPlan(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous plan accepted")
				}
			} else {
				if _, err := ReadRecording(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous recording accepted")
				}
			}
		}
	}
}
