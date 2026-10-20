package candidateexec

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Job, []byte) {
	t.Helper()
	d, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(d)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(p int64)bool{return p<=10000}\n"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(c, raw, []bool{false, true, false, true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := New(p, raw)
	if err != nil {
		t.Fatal(err)
	}
	return j, raw
}
func completed(j Job) Result {
	r := Result{Schema: ResultSchema, JobSHA256: j.SHA256, PlanSHA256: j.Plan.SHA256, CandidateSHA256: j.Plan.Candidate.SHA256, Status: "completed", ReasonCode: "fixture_compared", BuildCompleted: true, RunAttempted: true, Values: []bool{false, true, false, true}, Matched: 4, AllAnnotationsMatch: true, RunStderr: "_mantik 0 false\n_mantik 1 true\n_mantik 2 false\n_mantik 3 true\n", Authority: "synthetic_candidate_behavior_worker_only"}
	r.SHA256 = workerplan.Hash(r)
	return r
}
func TestResultPinsCountsStatusAndOutput(t *testing.T) {
	j, _ := fixture(t)
	r := completed(j)
	raw, _ := json.Marshal(r)
	if _, err := ReadResult(bytes.NewReader(raw), j); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Result){
		func(r *Result) { r.Values[0] = true }, func(r *Result) { r.Matched = 3 }, func(r *Result) { r.Missing = 1 }, func(r *Result) { r.Status = "mismatch" }, func(r *Result) { r.RunAttempted = false }, func(r *Result) { r.BuildCompleted = false }, func(r *Result) { r.PlanSHA256 = "wrong" }, func(r *Result) { r.QualificationCredit = true }, func(r *Result) { r.IsolationVerified = true }, func(r *Result) { r.ImageIdentityVerified = true }, func(r *Result) { r.RunStderr += "extra\n" }, func(r *Result) { r.RunStdout = "extra" }, func(r *Result) { r.RunOutputTruncated = true },
	} {
		var bad Result
		_ = json.Unmarshal(raw, &bad)
		change(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if bad.Validate(j) == nil {
			t.Fatal("rehashed invalid result accepted", bad)
		}
	}
	for _, wire := range []string{string(raw) + " {}", strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"bad","schema":`, 1)} {
		if _, err := ReadResult(strings.NewReader(wire), j); err == nil {
			t.Fatal("ambiguous result")
		}
	}
	// A failed run retains its valid prefix and never becomes all-matching.
	r = completed(j)
	r.Status = "failed"
	r.ReasonCode = "run_failed"
	r.Values = r.Values[:2]
	r.RunStderr = "_mantik 0 false\n_mantik 1 true\npanic: fixture\n"
	r.Matched = 2
	r.Missing = 2
	r.AllAnnotationsMatch = false
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	if r.Validate(j) != nil {
		t.Fatal(r)
	}
}
func TestPlansAndFailureBeforeBuild(t *testing.T) {
	j, raw := fixture(t)
	if _, err := New(j.Plan, append(raw, '\n')); err == nil {
		t.Fatal("dataset pin")
	}
	wire, _ := json.Marshal(j)
	if _, err := ReadJob(bytes.NewReader(wire)); err != nil {
		t.Fatal(err)
	}
	j.Resources.PIDs++
	j.SHA256 = ""
	j.SHA256 = workerplan.Hash(j)
	if j.Validate() == nil {
		t.Fatal("resource override")
	}
	j, raw = fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Execute(ctx, j, raw, []byte(j.Plan.Candidate.Source.Text), []byte(Harness(j.Plan)))
	if err != nil || r.ReasonCode != "cancelled_before_build" || r.Missing != 4 || r.RunAttempted {
		t.Fatal(r, err)
	}
	r, err = Execute(context.Background(), j, raw, []byte("changed"), []byte(Harness(j.Plan)))
	if err != nil || r.ReasonCode != "input_mismatch" {
		t.Fatal(r, err)
	}
}
func TestSourcePreparationAndOutputProtocol(t *testing.T) {
	for _, source := range []string{"package candidate\nfunc Entry(int64)bool{return true}", "package candidate\nfunc init(){panic(1)}\nfunc Entry(int64)bool{return false}"} {
		out, err := prepare([]byte(source))
		if err != nil || !strings.HasPrefix(string(out), "package main") {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"package wrong\nfunc Entry(int64)bool{return true}", "package candidate\nimport \"os\"\nfunc Entry(int64)bool{return true}", "package candidate\nvar Entry=func(int64)bool{return true}", "package candidate\nfunc println(){}\nfunc Entry(int64)bool{return true}", "package candidate\nfunc _mantikFake(){}\nfunc Entry(int64)bool{return true}", "package candidate\n//go:nosplit\nfunc Entry(int64)bool{return true}", "package candidate\nfunc main(){}\nfunc Entry(int64)bool{return true}", "package candidate\ntype int64 = int32\nfunc Entry(int64)bool{return true}", "package candidate\ntype bool = int\nfunc Entry(int64)bool{return 1}"} {
		if _, err := prepare([]byte(source)); err == nil {
			t.Fatal(source)
		}
	}
	for _, raw := range []string{"_mantik 0 true", "_mantik 1 true\n", "_mantik 0 true\n_mantik 0 false\n", "garbage\n", "_mantik 0 true\nextra\n"} {
		if _, valid := parseValues(raw, 1); valid {
			t.Fatal(raw)
		}
	}
	values, valid := parseValues("_mantik 0 false\n", 1)
	if !valid || len(values) != 1 || values[0] {
		t.Fatal(values, valid)
	}
	b := &capture{}
	n, _ := b.Write(bytes.Repeat([]byte{'x'}, MaxOutput+1))
	if n != MaxOutput+1 || !b.truncated || len(b.raw) != MaxOutput {
		t.Fatal("capture bound")
	}
}
