package candidatebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T, source string) Job {
	t.Helper()
	d, _ := dataset.FixtureDataset()
	raw, _ := json.Marshal(d)
	p, err := candidate.New([]byte(source), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	j, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

const good = "package candidate\nfunc Entry(price int64)bool{return price<=10000}\nfunc init(){panic(\"never execute\")}\n"

func TestCompileContractAndFixedCommand(t *testing.T) {
	j := fixture(t, good)
	calls := 0
	run := func(_ context.Context, args ...string) ([]byte, bool, error) {
		calls++
		if calls == 1 {
			if !reflect.DeepEqual(args, []string{"-V=full"}) {
				t.Fatal(args)
			}
			return []byte("compile version go1.26.9\n"), false, nil
		}
		if !reflect.DeepEqual(args, []string{"-complete", "-p", "candidate", "-o", "/dev/null", "/worker/candidate.go", "/worker/interface.go"}) {
			t.Fatal(args)
		}
		return nil, false, nil
	}
	r, err := execute(context.Background(), j, []byte(good), []byte(Harness), run)
	if err != nil || r.Status != "completed" || !r.SourceCompiled || r.SourceExecuted || r.Validate(j) != nil || calls != 2 {
		t.Fatal(r, err, calls)
	}
	raw, _ := json.Marshal(r)
	again, err := ReadResult(bytes.NewReader(raw), j)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal(err)
	}
	for _, change := range []func(*Result){func(r *Result) { r.SourceExecuted = true }, func(r *Result) { r.QualificationCredit = true }, func(r *Result) { r.IsolationVerified = true }, func(r *Result) { r.ImageIdentityVerified = true }, func(r *Result) { r.CandidateSHA256 = strings.Repeat("b", 64) }} {
		bad := r
		change(&bad)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if bad.Validate(j) == nil {
			t.Fatal("forged worker authority/binding accepted")
		}
	}
	other := fixture(t, good+"\n")
	if r.Validate(other) == nil {
		t.Fatal("result accepted for different source")
	}
}

func TestCompilerFailuresAndUnexecutedInputs(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		version            string
		failure            bool
		truncated          bool
	}{
		{"parse", "not Go", "interface_rejected", "", false, false},
		{"package", "package main\nfunc Entry(int64)bool{return true}", "interface_rejected", "", false, false},
		{"imports", "package candidate\nimport \"os\"\nfunc Entry(int64)bool{return true}", "interface_rejected", "", false, false},
		{"function var", "package candidate\nvar Entry func(int64)bool", "interface_rejected", "", false, false},
		{"version", good, "compiler_version_mismatch", "compile version go1.26.8", false, false},
		{"type failure", good, "compile_failed", "compile version go1.26.9", true, false},
		{"diagnostics", good, "diagnostic_limit", "compile version go1.26.9", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, tc.source)
			calls := 0
			run := func(_ context.Context, args ...string) ([]byte, bool, error) {
				calls++
				if calls == 1 {
					return []byte(tc.version), false, nil
				}
				if tc.failure {
					return []byte("compiler diagnostic"), tc.truncated, errors.New("compile failed")
				}
				return nil, false, nil
			}
			r, err := execute(context.Background(), j, []byte(tc.source), []byte(Harness), run)
			if err != nil || r.ReasonCode != tc.want || r.Status != "failed" || r.SourceCompiled || r.Validate(j) != nil {
				t.Fatal(r, err)
			}
			if tc.want == "interface_rejected" && calls != 0 {
				t.Fatal("rejected source reached compiler")
			}
		})
	}
	j := fixture(t, good)
	calls := 0
	run := func(context.Context, ...string) ([]byte, bool, error) {
		calls++
		return nil, false, errors.New("unavailable")
	}
	r, err := execute(context.Background(), j, []byte(good+" "), []byte(Harness), run)
	if err != nil || r.ReasonCode != "input_mismatch" || calls != 0 {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err = execute(ctx, j, []byte(good), []byte(Harness), run)
	if err != nil || r.ReasonCode != "cancelled_before_compile" || calls != 0 {
		t.Fatal(r, err)
	}
	r, err = execute(context.Background(), j, []byte(good), []byte(Harness), run)
	if err != nil || r.ReasonCode != "compiler_unavailable" {
		t.Fatal(r, err)
	}
}

func TestBuildWireAndBoundedDiagnostics(t *testing.T) {
	j := fixture(t, good)
	raw, _ := json.Marshal(j)
	for _, bad := range []string{string(raw) + " {}", strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1), strings.Repeat(" ", MaxJobBytes+1)} {
		if _, err := ReadJob(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid job accepted")
		}
	}
	changed := j
	changed.Resources.MemoryBytes++
	changed.SHA256 = ""
	changed.SHA256 = workerplan.Hash(changed)
	if changed.Validate() == nil {
		t.Fatal("resource override accepted")
	}
	if _, err := ReadJob(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var b diagnosticBuffer
	input := bytes.Repeat([]byte("x"), MaxDiagnostics+100)
	n, err := b.Write(input)
	if err != nil || n != len(input) || len(b.raw) != MaxDiagnostics || !b.truncated {
		t.Fatal("diagnostics not bounded")
	}
}

func TestCompilerCancellationRetainsFailure(t *testing.T) {
	j := fixture(t, good)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	run := func(context.Context, ...string) ([]byte, bool, error) {
		calls++
		if calls == 1 {
			return []byte("compile version go1.26.9"), false, nil
		}
		cancel()
		return []byte{0xff}, false, context.Canceled
	}
	r, err := execute(ctx, j, []byte(good), []byte(Harness), run)
	if err != nil || r.Status != "failed" || r.ReasonCode != "compile_cancelled" || !r.CompilerVersionChecked || r.SourceCompiled || r.Diagnostics != "?" || r.Validate(j) != nil {
		t.Fatal(r, err)
	}
}
