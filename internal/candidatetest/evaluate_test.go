package candidatetest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func fixture(t *testing.T) (Plan, []byte, Recording) {
	t.Helper()
	data, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(data)
	p, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{panic(\"must not execute\")}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	expected := []bool{false, true, false, true}
	plan, err := New(p, raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	r := Recording{Schema: RecordingSchema, AttemptID: "fixture-attempt", PlanSHA256: plan.SHA256, CandidateSHA256: p.SHA256, Status: "reported_completed", ReasonCode: "completed", Outputs: []Output{}}
	for _, c := range plan.Cases {
		v := c.Expected
		r.Outputs = append(r.Outputs, Output{CaseID: c.ID, Value: &v})
	}
	return plan, raw, r
}
func compare(t *testing.T, p Plan, d []byte, r Recording) Report {
	t.Helper()
	raw, _ := json.Marshal(r)
	report, err := Evaluate(p, d, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return report
}
func TestAllCasesComparedAndNoExecutionCredit(t *testing.T) {
	p, d, r := fixture(t)
	report := compare(t, p, d, r)
	if report.Status != "compared" || !report.AllAnnotationsMatch || report.Matched != 4 || report.ExecutionVerified || report.QualificationCredit || report.Authority != "caller_reported_fixture_annotation_comparison_only" {
		t.Fatal(report)
	}
	if repeated := compare(t, p, d, r); !reflect.DeepEqual(report, repeated) {
		t.Fatal("identical artifacts changed comparison")
	}
	*r.Outputs[0].Value = !*r.Outputs[0].Value
	*r.Outputs[3].Value = !*r.Outputs[3].Value
	report = compare(t, p, d, r)
	if report.AllAnnotationsMatch || report.Matched != 2 || report.Mismatched != 2 || report.Cases[0].Disposition != "mismatch" || report.Cases[3].Disposition != "mismatch" {
		t.Fatal(report)
	}
}
func TestFailuresMissingAndDuplicateOutputsRemainVisible(t *testing.T) {
	for _, status := range []string{"reported_failed", "unknown"} {
		p, d, r := fixture(t)
		r.Status = status
		r.ReasonCode = "runtime_failed"
		report := compare(t, p, d, r)
		if report.Status != "incomplete" || report.AllAnnotationsMatch || report.Matched != 4 || report.Recording.Status != status {
			t.Fatal(report)
		}
	}
	p, d, r := fixture(t)
	extra := true
	r.Outputs = r.Outputs[:3]
	r.Outputs = append(r.Outputs, Output{CaseID: p.Cases[0].ID, Value: &extra}, Output{CaseID: "unrecognized-case", Value: &extra})
	report := compare(t, p, d, r)
	if report.Status != "incomplete" || report.AllAnnotationsMatch || report.Missing != 1 || report.Duplicates != 1 || len(report.Unmatched) != 1 || len(report.Cases[0].Reported) != 2 || len(report.Recording.Outputs) != 5 {
		t.Fatal(report)
	}
	// Reports are detached from the caller's recording, including Boolean pointers.
	*report.Recording.Outputs[0].Value = !*report.Recording.Outputs[0].Value
	if *report.Recording.Outputs[0].Value == *r.Outputs[0].Value {
		t.Fatal("report aliases caller data")
	}
}
func TestPlanPinsAnnotationsAndDerivesInputsAgain(t *testing.T) {
	p, d, r := fixture(t)
	changed, err := New(p.Candidate, d, []bool{true, true, false, true})
	if err != nil || changed.ID == p.ID {
		t.Fatal("annotation change retained identity", err)
	}
	raw, _ := json.Marshal(p)
	read, err := ReadPlan(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(p, read) {
		t.Fatal(err)
	}
	forged := read
	forged.Cases = append([]Case(nil), read.Cases...)
	forged.Cases[0].PriceCents++
	forged.SHA256 = planHash(forged)
	forged.ID = "candidate-test-" + forged.SHA256
	if forged.Validate() != nil || forged.VerifyDataset(d) == nil {
		t.Fatal("plan substituted a different dataset input")
	}
	raw, _ = json.Marshal(r)
	if _, err := Evaluate(changed, d, bytes.NewReader(raw)); err == nil {
		t.Fatal("recording accepted under changed expectations")
	}
	if _, err := Evaluate(p, append(d, '\n'), bytes.NewReader(raw)); err == nil {
		t.Fatal("changed raw dataset accepted")
	}
}
func TestStrictWireAndCapacity(t *testing.T) {
	p, d, r := fixture(t)
	for _, expect := range []string{"[true,null,false,true]", "[]", "[0]", strings.Repeat(" ", 4097)} {
		if _, err := ReadExpectations(strings.NewReader(expect)); err == nil {
			t.Fatal("missing/invalid expectations accepted")
		}
	}
	for _, change := range []func(*Recording){
		func(r *Recording) { r.Outputs[0].Value = nil }, func(r *Recording) { r.Status = "completed" }, func(r *Recording) { r.PlanSHA256 = "wrong" }, func(r *Recording) { r.CandidateSHA256 = "wrong" }, func(r *Recording) { r.Outputs = make([]Output, 2*MaxCases+1) },
	} {
		raw, _ := json.Marshal(r)
		var bad Recording
		_ = json.Unmarshal(raw, &bad)
		change(&bad)
		raw, _ = json.Marshal(bad)
		if _, err := Evaluate(p, d, bytes.NewReader(raw)); err == nil {
			t.Fatal("invalid recording accepted")
		}
	}
	raw, _ := json.Marshal(r)
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1), strings.Repeat(" ", MaxRecordingBytes+1)} {
		if _, _, err := ReadRecording(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous recording accepted")
		}
	}
}

func TestDatasetCannotFilterBadOrDuplicateCases(t *testing.T) {
	p, raw, _ := fixture(t)
	for _, kind := range []string{"too many", "duplicate", "quarantined"} {
		t.Run(kind, func(t *testing.T) {
			var d replay.Dataset
			_ = json.Unmarshal(raw, &d)
			switch kind {
			case "too many":
				for len(d.Records) <= MaxCases {
					d.Records = append(d.Records, d.Records[0])
				}
			case "duplicate":
				d.Records = append(d.Records, d.Records[0])
			case "quarantined":
				d.Records[0].Quality = "quarantined"
				d.Records[0].Reason = "fixture_gap"
			}
			d, err := replay.Seal(d)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(d)
			c, err := candidate.New([]byte(p.Candidate.Source.Text), p.Candidate.Hypothesis.Declaration, data, p.Candidate.Toolchain.ImageSHA256)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := New(c, data, make([]bool, len(d.Records))); err == nil {
				t.Fatal("unsupported input was filtered or admitted")
			}
		})
	}
}
