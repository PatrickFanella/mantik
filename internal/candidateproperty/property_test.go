package candidateproperty

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T, prices []int64, values []bool, failed bool) (candidateexec.Job, []byte, []byte) {
	t.Helper()
	data, _ := replay.FixtureDataset()
	data.Records = data.Records[:len(prices)]
	for i, price := range prices {
		var o domain.Observation
		_ = json.Unmarshal([]byte(data.Records[i].Original), &o)
		o.PriceCents = price
		raw, _ := json.Marshal(o)
		data.Records[i].Original = string(raw)
		data.Records[i].SHA256 = replay.Digest(string(raw))
	}
	data, err := replay.Seal(data)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]bool, len(prices))
	for i := range expected {
		expected[i] = true
	}
	plan, err := candidatetest.New(c, raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	r := candidateexec.Result{Schema: candidateexec.ResultSchema, JobSHA256: j.SHA256, PlanSHA256: j.Plan.SHA256, CandidateSHA256: c.SHA256, Status: "completed", ReasonCode: "fixture_compared", BuildCompleted: true, RunAttempted: true, Values: append([]bool{}, values...), Missing: len(prices) - len(values), Authority: "synthetic_candidate_behavior_worker_only"}
	for i, v := range values {
		r.RunStderr += fmt.Sprintf("_mantik %d %t\n", i, v)
		if v {
			r.Matched++
		} else {
			r.Mismatched++
		}
	}
	if r.Mismatched > 0 {
		r.Status = "mismatch"
	}
	if failed {
		r.Status = "failed"
		r.ReasonCode = "run_failed"
		r.RunStderr += "panic: fixture\n"
	}
	r.AllAnnotationsMatch = r.Status == "completed"
	r.SHA256 = workerplan.Hash(r)
	if r.Validate(j) != nil {
		t.Fatal(r)
	}
	result, _ := json.Marshal(r)
	return j, raw, result
}
func check(t *testing.T, j candidateexec.Job, data, result []byte, properties ...string) Report {
	t.Helper()
	p, err := New(j, data, properties)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Evaluate(p, data, bytes.NewReader(result))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestEveryEligiblePairAndViolationPreserved(t *testing.T) {
	j, data, result := fixture(t, []int64{100, 100, 200, 50}, []bool{true, false, true, true}, false)
	r := check(t, j, data, result, SamePrice, Nonincreasing, Nondecreasing)
	if r.Status != "violated" || r.AllSamplePropertiesSatisfied || r.AnnotationMatch || r.ExecutionVerified || r.QualificationCredit || len(r.Checks) != 3 {
		t.Fatal(r)
	}
	for index, wantPairs := range []int{1, 5, 5} {
		c := r.Checks[index]
		if c.EligiblePairs != wantPairs || c.CheckedPairs != wantPairs || c.MissingPairs != 0 || c.ViolatedPairs != 1 || c.Status != "violated" || len(c.Pairs) != wantPairs {
			t.Fatal(c)
		}
	}
	if repeated := check(t, j, data, result, SamePrice, Nonincreasing, Nondecreasing); !reflect.DeepEqual(r, repeated) {
		t.Fatal("unstable report")
	}
	// The relation follows prices, not observation order, and annotations are separate.
	j, data, result = fixture(t, []int64{200, 50, 100, 150}, []bool{false, true, true, false}, false)
	r = check(t, j, data, result, Nonincreasing)
	if r.Status != "passed" || !r.AllSamplePropertiesSatisfied || r.AnnotationMatch || r.Checks[0].CheckedPairs != 6 {
		t.Fatal(r)
	}
}
func TestIncompleteAndInapplicableNeverPassVacuously(t *testing.T) {
	j, data, result := fixture(t, []int64{100, 100, 200, 50}, []bool{true, false}, true)
	r := check(t, j, data, result, SamePrice, Nonincreasing)
	if r.Status != "incomplete" || r.AllSamplePropertiesSatisfied || r.Checks[0].Status != "incomplete" || r.Checks[0].ViolatedPairs != 1 || r.Checks[1].MissingPairs != 5 || r.Checks[0].Pairs[0].Status != "violated" {
		t.Fatal(r)
	}
	// Even full matching values in a failed worker cannot satisfy properties.
	j, data, result = fixture(t, []int64{100, 100, 200, 50}, []bool{true, true, true, true}, true)
	r = check(t, j, data, result, SamePrice, Nonincreasing)
	if r.Status != "incomplete" || r.AllSamplePropertiesSatisfied || r.Checks[1].CheckedPairs != 5 {
		t.Fatal(r)
	}
	j, data, result = fixture(t, []int64{1, 2, 3, 4}, []bool{true, true, true, true}, false)
	r = check(t, j, data, result, SamePrice)
	if r.Status != "incomplete" || r.Checks[0].Status != "not_applicable" || r.Checks[0].EligiblePairs != 0 || !r.AnnotationMatch {
		t.Fatal(r)
	}
	j, data, result = fixture(t, []int64{1}, []bool{true}, false)
	r = check(t, j, data, result, Nondecreasing)
	if r.Status != "incomplete" {
		t.Fatal(r)
	}
}
func TestStrictPropertyPlansAndPins(t *testing.T) {
	j, data, result := fixture(t, []int64{100, 100, 200, 50}, []bool{true, true, true, true}, false)
	for _, wire := range []string{`[]`, `null`, `["unsupported"]`, `["same_price_same_signal","same_price_same_signal"]`, `[null]`, `["same_price_same_signal"] []`, strings.Repeat(" ", 4097)} {
		if _, err := ReadProperties(strings.NewReader(wire)); err == nil {
			t.Fatal(wire)
		}
	}
	properties := []string{SamePrice}
	p, err := New(j, data, properties)
	if err != nil {
		t.Fatal(err)
	}
	properties[0] = Nonincreasing
	if p.Properties[0] != SamePrice {
		t.Fatal("caller alias")
	}
	changed, err := New(j, data, []string{Nonincreasing})
	if err != nil || changed.SHA256 == p.SHA256 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	read, err := ReadPlan(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(read, p) {
		t.Fatal(err)
	}
	for _, wire := range []string{string(raw) + " {}", strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)} {
		if _, err := ReadPlan(strings.NewReader(wire)); err == nil {
			t.Fatal("ambiguous plan")
		}
	}
	if _, err := Evaluate(p, append(data, '\n'), bytes.NewReader(result)); err == nil {
		t.Fatal("changed raw dataset")
	}
	j2, _, otherResult := fixture(t, []int64{100, 100, 201, 50}, []bool{true, true, true, true}, false)
	if j2.SHA256 == j.SHA256 {
		t.Fatal("fixture pin unchanged")
	}
	if _, err := Evaluate(p, data, bytes.NewReader(otherResult)); err == nil {
		t.Fatal("other worker result")
	}
	for _, raw := range [][]byte{nil, []byte("{}"), append(result, []byte(" {}")...)} {
		if _, err := Evaluate(p, data, bytes.NewReader(raw)); err == nil {
			t.Fatal("invalid result")
		}
	}
	p.QualificationCredit = true
	p.SHA256 = ""
	p.SHA256 = workerplan.Hash(p)
	if p.Validate() == nil {
		t.Fatal("authority escalated")
	}
}

func TestAdjacentPricesAtFixtureUpperBoundRemainDistinct(t *testing.T) {
	j, data, result := fixture(t, []int64{99999999, 100000000}, []bool{false, true}, false)
	r := check(t, j, data, result, Nonincreasing, SamePrice)
	if r.Checks[0].EligiblePairs != 1 || r.Checks[0].ViolatedPairs != 1 || r.Checks[1].EligiblePairs != 0 || r.Status != "incomplete" {
		t.Fatal(r)
	}
}
