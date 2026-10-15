package researchworker

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (workerplan.Job, []byte) {
	t.Helper()
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := experiment.DefaultRecipe(d)
	if err != nil {
		t.Fatal(err)
	}
	// Raw transport bytes and canonical native input bytes have distinct pins.
	raw, _ := json.MarshalIndent(d, "", "  ")
	raw = append(raw, '\n')
	j, err := Declare("job-one", "sha256:"+strings.Repeat("a", 64), recipe, raw)
	if err != nil {
		t.Fatal(err)
	}
	return j, raw
}
func TestProtocolMatchesActualNativeEngine(t *testing.T) {
	j, raw := fixture(t)
	r, err := Execute(context.Background(), j, bytes.NewReader(raw))
	if err != nil || r.Validate(j) != nil || r.Status != "completed" || !r.DeadlineMet || !r.InputVerified || r.QualificationCredit || r.IsolationVerified || r.ImageIdentityVerified {
		t.Fatal(r, err)
	}
	var d replay.Dataset
	_ = json.Unmarshal(raw, &d)
	native, err := experiment.Execute(context.Background(), workerplan.NativeAttemptID(j), j.Recipe, d)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(native)
	if string(expected) != r.NativeResult || workerplan.Digest(expected) != r.NativeResultSHA256 {
		t.Fatal("protocol changed native evidence")
	}
	for i, want := range []int64{-6000, -3000, 0, -7200} {
		if native.Results[i].Metrics.MarkedPnLCents != want {
			t.Fatal("fixture economics changed")
		}
	}
	retry, err := Execute(context.Background(), j, bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(r, retry) {
		t.Fatal("repeated successful computation diverged", err)
	}
	wire, _ := json.MarshalIndent(r, "", "  ")
	decoded, err := workerplan.ReadResult(bytes.NewReader(wire), j)
	if err != nil || !reflect.DeepEqual(r, decoded) {
		t.Fatal("result protocol round trip", err)
	}
	jobWire, _ := json.MarshalIndent(j, "", "  ")
	readJob, err := workerplan.ReadJob(bytes.NewReader(jobWire))
	if err != nil || !reflect.DeepEqual(j, readJob) {
		t.Fatal("job protocol round trip", err)
	}
	if j.Input.SHA256 == j.Input.CanonicalSHA256 {
		t.Fatal("raw/canonical identity accidentally conflated")
	}
}

type unreadable struct{}

func (unreadable) Read([]byte) (int, error) { panic("cancelled job read input") }
func TestCancellationAndAdmissionFailures(t *testing.T) {
	j, raw := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Execute(ctx, j, unreadable{})
	if err != nil || r.Status != "failed" || r.ErrorCode != "cancelled_before_input" || r.InputVerified || r.NativeResult != "" || r.DeadlineMet {
		t.Fatal(r, err)
	}
	if _, err = Execute(context.Background(), j, strings.NewReader("wrong")); err == nil {
		t.Fatal("unbound input accepted")
	}
	changed := j
	changed.Recipe.DatasetID = "missing-dataset"
	changed.Recipe, err = experiment.Seal(changed.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = workerplan.Seal(changed)
	if err != nil {
		t.Fatal(err)
	}
	r, err = Execute(context.Background(), changed, bytes.NewReader(raw))
	if err != nil || r.Status != "failed" || r.ErrorCode != "dataset_pin_mismatch" || r.NativeResult == "" {
		t.Fatal("admission failure discarded", r, err)
	}
	canonicalMismatch := j
	canonicalMismatch.Input.CanonicalSHA256 = strings.Repeat("0", 64)
	canonicalMismatch, _ = workerplan.Seal(canonicalMismatch)
	r, err = Execute(context.Background(), canonicalMismatch, bytes.NewReader(raw))
	if err != nil || r.ErrorCode != "canonical_input_mismatch" || !r.InputVerified {
		t.Fatal(r, err)
	}
}
func TestStrictJobAndResultBoundaries(t *testing.T) {
	j, raw := fixture(t)
	wire, _ := json.Marshal(j)
	for _, bad := range []string{
		strings.Replace(string(wire), `"id":`, `"id":"duplicate","id":`, 1),
		strings.Replace(string(wire), `"id":`, `"ID":`, 1),
		strings.Replace(string(wire), `"schema":`, `"credentials":{},"schema":`, 1),
		string(wire) + ` {}`, string([]byte{0xff}), strings.Repeat(" ", workerplan.MaxJobBytes+1),
	} {
		if _, err := workerplan.ReadJob(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous job accepted")
		}
	}
	for _, change := range []func(*workerplan.Job){
		func(j *workerplan.Job) { j.ImageSHA256 = "image:latest" }, func(j *workerplan.Job) { j.Resources.PIDs++ }, func(j *workerplan.Job) { j.OutputReference = "../../result" }, func(j *workerplan.Job) { j.Tool = "arbitrary-code" }, func(j *workerplan.Job) { j.QualificationCredit = true },
	} {
		copy := j
		change(&copy)
		if _, err := workerplan.Seal(copy); err == nil {
			t.Fatal("unsupported authority/environment/resource accepted")
		}
	}
	r, err := Execute(context.Background(), j, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*workerplan.Result){
		func(r *workerplan.Result) { r.IsolationVerified = true }, func(r *workerplan.Result) { r.ImageIdentityVerified = true }, func(r *workerplan.Result) { r.QualificationCredit = true }, func(r *workerplan.Result) { r.JobSHA256 = strings.Repeat("0", 64) },
		func(r *workerplan.Result) {
			r.NativeResult = strings.Replace(r.NativeResult, `"qualified":false`, `"qualified":true`, 1)
			r.NativeResultSHA256 = workerplan.Digest([]byte(r.NativeResult))
			r.NativeResultBytes = len(r.NativeResult)
		},
		func(r *workerplan.Result) {
			r.NativeResult = strings.Replace(r.NativeResult, `"actions":2`, `"actions":null`, 1)
			r.NativeResultSHA256 = workerplan.Digest([]byte(r.NativeResult))
			r.NativeResultBytes = len(r.NativeResult)
		},
	} {
		copy := r
		change(&copy)
		copy.SHA256 = ""
		copy.SHA256 = workerplan.Hash(copy)
		if copy.Validate(j) == nil {
			t.Fatal("forged result authority/binding accepted")
		}
	}
}
