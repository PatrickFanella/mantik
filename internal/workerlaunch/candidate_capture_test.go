package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func captureFixture(t *testing.T, invalidOutput bool) (CandidateCapture, candidateattempt.Intent, string) {
	t.Helper()
	j, data := behaviorFixture(t)
	i := registeredIntent(t, j, "one")
	d := journalWorker(t, j)
	if invalidOutput {
		d.output = []byte{0xff, 0, 0xfe}
	}
	root := filepath.Join(t.TempDir(), "bound")
	a, err := runRegisteredCandidateBehavior(context.Background(), root, "one", j, data, j.Plan.Candidate.Toolchain.ImageSHA256, &i, d)
	if err != nil || a.Outcome == nil {
		t.Fatal(a, err)
	}
	c, err := CaptureRegisteredCandidate(context.Background(), root, i)
	if err != nil || d.starts != 1 || d.kills != 0 {
		t.Fatal(c, err)
	}
	return c, i, root
}

func sealCapture(c *CandidateCapture) { c.SHA256 = ""; c.SHA256 = workerplan.Hash(*c) }
func cloneCapture(t *testing.T, c CandidateCapture) CandidateCapture {
	t.Helper()
	raw, _ := json.Marshal(c)
	var out CandidateCapture
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCandidateCapturePortableExactAndHistorical(t *testing.T) {
	c, i, root := captureFixture(t, false)
	if c.VerifyIntent(i) != nil || c.ExecutionVerified || c.CurrentHostStateKnown || c.QualificationCredit {
		t.Fatal("capture inflated authority")
	}
	again, err := CaptureRegisteredCandidate(context.Background(), root, i)
	if err != nil || !reflect.DeepEqual(c, again) {
		t.Fatal("repeated export changed capture", err)
	}
	raw, _ := json.Marshal(c)
	read, err := ReadCandidateCapture(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(c, read) {
		t.Fatal("portable readback changed artifacts", err)
	}
	// A portable capture remains readable after its local evidence is unavailable;
	// successful validation makes no claim about that host's present state.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if c.VerifyIntent(i) != nil {
		t.Fatal("portable validation accessed local host")
	}
	i.Sequence++
	i.SHA256 = ""
	i.SHA256 = workerplan.Hash(i)
	if c.VerifyIntent(i) == nil {
		t.Fatal("capture rebound to another retained service receipt")
	}
}

func TestCandidateCaptureOpaqueFailureAndBinaryBytes(t *testing.T) {
	c, _, _ := captureFixture(t, true)
	if c.Attempt.Outcome.Receipt.Status != "incomplete" || !bytes.Equal(c.Stdout, []byte{0xff, 0, 0xfe}) || c.Attempt.Outcome.Receipt.WorkerStatus != "" {
		t.Fatal("invalid output was omitted or promoted")
	}
	c.Stderr = []byte{0xff, 0, 0xfe, '\n'}
	r := &c.Attempt.Outcome.Receipt
	r.StderrBytes, r.StderrSHA256 = len(c.Stderr), workerplan.Digest(c.Stderr)
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	o := c.Attempt.Outcome
	o.SHA256 = ""
	o.SHA256 = workerplan.Hash(*o)
	sealCapture(&c)
	raw, _ := json.Marshal(c)
	read, err := ReadCandidateCapture(bytes.NewReader(raw))
	if err != nil || !bytes.Equal(read.Stdout, c.Stdout) || !bytes.Equal(read.Stderr, c.Stderr) {
		t.Fatal("opaque failure bytes changed", err)
	}
	// Hashes are consistency checks, not independent host attestations: a caller
	// can reseal an otherwise consistent historical capture. Authority stays false.
	if read.ExecutionVerified || read.CurrentHostStateKnown || read.QualificationCredit {
		t.Fatal("caller-reported bytes gained execution credit")
	}
}

func TestCandidateCaptureRejectsRehashedContradictions(t *testing.T) {
	original, _, _ := captureFixture(t, false)
	for _, mutate := range []func(*CandidateCapture){
		func(c *CandidateCapture) { c.ExecutionVerified = true },
		func(c *CandidateCapture) { c.CurrentHostStateKnown = true },
		func(c *CandidateCapture) { c.QualificationCredit = true },
		func(c *CandidateCapture) { c.Authority = "host_verified" },
		func(c *CandidateCapture) { c.Attempt.Outcome = nil },
		func(c *CandidateCapture) { c.Attempt.Intent.ServiceIntent = nil },
		func(c *CandidateCapture) { c.Stdout = append(c.Stdout, '\n') },
		func(c *CandidateCapture) { c.Stderr = nil },
		func(c *CandidateCapture) { c.PreStart = []byte("{}") },
		func(c *CandidateCapture) { c.JournalRoot += "/different" },
		func(c *CandidateCapture) { c.Acquisition.ImageSHA256 = "sha256:" + strings.Repeat("b", 64) },
		func(c *CandidateCapture) { c.Attempt.Outcome.Receipt.WorkerResultSHA256 = strings.Repeat("a", 64) },
	} {
		c := cloneCapture(t, original)
		mutate(&c)
		sealCapture(&c)
		if c.Validate() == nil {
			t.Fatal("rehashed inconsistent capture accepted")
		}
	}
	raw, _ := json.Marshal(original)
	for _, bad := range [][]byte{
		append(append([]byte{}, raw...), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"schema":`, `"unknown":true,"schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)),
		bytes.Repeat([]byte(" "), MaxCandidateCaptureBytes+1),
	} {
		if _, err := ReadCandidateCapture(bytes.NewReader(bad)); err == nil {
			t.Fatal("ambiguous or oversized capture accepted")
		}
	}
}

func TestCandidateCapturePendingAndWholeHistoryRefusal(t *testing.T) {
	ctx := context.Background()
	c, expected, root := captureFixture(t, false)
	j, data := expected.Command.Job, mustCandidateDataset(t, root, "one")
	i := registeredIntent(t, j, "pending")
	if _, _, _, err := reserveRegisteredCandidate(ctx, root, "pending", j, data, j.Plan.Candidate.Toolchain.ImageSHA256, &i); err != nil {
		t.Fatal(err)
	}
	if got, err := CaptureRegisteredCandidate(ctx, root, i); !errors.Is(err, ErrUnresolved) || got.SHA256 != "" {
		t.Fatal("pending request invented capture", err)
	}
	if err := os.WriteFile(root+"/intent-pending.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := CaptureRegisteredCandidate(ctx, root, expected); err == nil || got.SHA256 != "" {
		t.Fatal("selected export hid unrelated corruption", err)
	}
	if c.Validate() != nil {
		t.Fatal("historical portable capture was coupled to current journal")
	}
}

func mustCandidateDataset(t *testing.T, root, id string) []byte {
	t.Helper()
	raw, err := os.ReadFile(attemptPath(root, id) + "/input/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
