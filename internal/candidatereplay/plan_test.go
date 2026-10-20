package candidatereplay

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (candidatetest.Plan, []byte, time.Time) {
	t.Helper()
	d, _ := replay.FixtureDataset()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	keys := []string{"z-late", "b-tie", "a-future", "a-tie"}
	received := []int{20, 3, 2, 5}
	available := []int{10, 5, 30, 5}
	for i := range d.Records {
		var o domain.Observation
		_ = json.Unmarshal([]byte(d.Records[i].Original), &o)
		o.ID = keys[i]
		o.OccurredAt = base.Add(time.Duration(i) * time.Second)
		o.ReceivedAt = base.Add(time.Duration(received[i]) * time.Second)
		raw, _ := json.Marshal(o)
		r := &d.Records[i]
		r.Key = o.ID
		r.OccurredAt = o.OccurredAt
		r.ReceivedAt = o.ReceivedAt
		r.AvailableAt = base.Add(time.Duration(available[i]) * time.Second)
		r.Original = string(raw)
		r.SHA256 = replay.Digest(r.Original)
	}
	d, err := replay.Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(c, raw, []bool{true, false, true, false})
	if err != nil {
		t.Fatal(err)
	}
	return p, raw, base
}
func TestCutoffVisibilityTieOrderAndExactInputExport(t *testing.T) {
	parent, raw, base := fixture(t)
	before, _ := json.Marshal(parent)
	early, err := New(parent, raw, base.Add(4*time.Second))
	if err != nil || early.Status != "no_visible_inputs" || early.Job != nil || early.Dataset != nil || len(early.Exposures) != 4 || len(early.ReplayOrder) != 0 {
		t.Fatal(early, err)
	}
	if _, err := early.DatasetBytes(raw); err != ErrNoInputs {
		t.Fatal(err)
	}
	middle, err := New(parent, raw, base.Add(5*time.Second))
	if err != nil || middle.Status != "declared" || !reflect.DeepEqual(middle.ReplayOrder, []string{"a-tie-r1", "b-tie-r1"}) || len(middle.Exposures) != 4 {
		t.Fatal(middle, err)
	}
	if middle.Exposures[0].Disposition != "excluded_future" || middle.Exposures[2].Disposition != "excluded_future" || middle.Job.Plan.Cases[0].Expected || middle.Job.Plan.Cases[1].Expected {
		t.Fatal(middle)
	}
	late, err := New(parent, raw, base.Add(20*time.Second))
	if err != nil || !reflect.DeepEqual(late.ReplayOrder, []string{"a-tie-r1", "b-tie-r1", "z-late-r1"}) || !late.Job.Plan.Cases[2].Expected {
		t.Fatal(late, err)
	}
	if !late.Exposures[0].VisibleAt.Equal(base.Add(20*time.Second)) || !late.Exposures[1].VisibleAt.Equal(base.Add(5*time.Second)) {
		t.Fatal("used occurrence/only-one-availability-clock")
	}
	all, err := New(parent, raw, base.Add(30*time.Second))
	if err != nil || !reflect.DeepEqual(all.ReplayOrder, []string{"a-tie-r1", "b-tie-r1", "z-late-r1", "a-future-r1"}) {
		t.Fatal(all, err)
	}
	data, err := middle.DatasetBytes(raw)
	if err != nil || middle.Job.Plan.VerifyDataset(data) != nil || len(data) == 0 || data[len(data)-1] == '\n' {
		t.Fatal(err)
	}
	if middle.Job.Plan.Candidate.Source.SHA256 != parent.Candidate.Source.SHA256 || middle.Job.Plan.Candidate.SHA256 == parent.Candidate.SHA256 || middle.Job.Plan.Candidate.Toolchain != parent.Candidate.Toolchain || !reflect.DeepEqual(middle.Job.Plan.Candidate.Hypothesis, parent.Candidate.Hypothesis) {
		t.Fatal("source/hypothesis/toolchain lineage lost")
	}
	after, _ := json.Marshal(parent)
	if !bytes.Equal(before, after) {
		t.Fatal("parent mutated")
	}
	repeated, err := New(parent, raw, base.Add(5*time.Second))
	if err != nil || !reflect.DeepEqual(middle, repeated) {
		t.Fatal("unstable replay")
	}
	encoded, _ := json.Marshal(middle)
	read, err := Read(bytes.NewReader(encoded))
	if err != nil || read.Verify(raw) != nil {
		t.Fatal(err)
	}
}
func TestFullParentPinsAndRehashedTamperingRejected(t *testing.T) {
	parent, raw, base := fixture(t)
	p, err := New(parent, raw, base.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Plan){
		func(p *Plan) { p.Exposures[0].VisibleAt = p.Exposures[0].VisibleAt.Add(-time.Second) },
		func(p *Plan) { p.Job.Plan.Cases[0].Expected = true },
		func(p *Plan) { p.ReplayOrder[0], p.ReplayOrder[1] = p.ReplayOrder[1], p.ReplayOrder[0] },
		func(p *Plan) { p.Cutoff = p.Cutoff.Add(time.Second) },
		func(p *Plan) { p.QualificationCredit = true },
		func(p *Plan) { p.ExecutionStarted = true },
	} {
		encoded, _ := json.Marshal(p)
		var bad Plan
		_ = json.Unmarshal(encoded, &bad)
		change(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if bad.Verify(raw) == nil {
			t.Fatal("rehash substituted replay lineage")
		}
	}
	if p.Verify(append(raw, '\n')) == nil {
		t.Fatal("changed original bytes")
	}
	for _, at := range []time.Time{{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := New(parent, raw, at); err == nil {
			t.Fatal("invalid cutoff")
		}
	}
	encoded, _ := json.Marshal(p)
	for _, wire := range []string{string(encoded) + " {}", strings.Replace(string(encoded), `"schema":`, `"Schema":`, 1), strings.Replace(string(encoded), `"schema":`, `"schema":"duplicate","schema":`, 1)} {
		if _, err := Read(strings.NewReader(wire)); err == nil {
			t.Fatal("ambiguous plan")
		}
	}
}

func TestAnnotationChangesKeepIdenticalDatasetAndCandidateIdentity(t *testing.T) {
	parent, raw, base := fixture(t)
	first, err := New(parent, raw, base.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := candidatetest.New(parent.Candidate, raw, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(changed, raw, base.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.Dataset.SHA256 != second.Dataset.SHA256 || first.Job.Plan.Candidate.SHA256 != second.Job.Plan.Candidate.SHA256 || first.Job.SHA256 == second.Job.SHA256 || first.SHA256 == second.SHA256 {
		t.Fatal("annotations renamed input data or failed to bind job")
	}
}
