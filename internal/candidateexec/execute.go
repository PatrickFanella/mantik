package candidateexec

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type capture struct {
	raw       []byte
	truncated bool
}

func (b *capture) Write(raw []byte) (int, error) {
	n := len(raw)
	room := MaxOutput - len(b.raw)
	if len(raw) > room {
		raw = raw[:room]
		b.truncated = true
	}
	b.raw = append(b.raw, raw...)
	return n, nil
}
func (b *capture) text() string { return strings.ToValidUTF8(string(b.raw), "?") }

// prepare changes only the package name to link the fixed no-import harness.
// Reject compiler directives, harness names and print shadowing. Source remains
// pinned verbatim in the job; this deterministic transformation is not admission.
func prepare(source []byte) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "candidate.go", source, parser.ParseComments)
	if err != nil || f.Name.Name != "candidate" || len(f.Imports) != 0 || strings.Contains(string(source), "//go:") {
		return nil, ErrInvalid
	}
	valid, entry := true, false
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && (strings.HasPrefix(id.Name, "_mantik") || id.Name == "println" || (id.Obj != nil && (id.Name == "int64" || id.Name == "bool"))) {
			valid = false
		}
		return true
	})
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			if fn.Name.Name == "Entry" {
				entry = true
			}
			if fn.Name.Name == "main" {
				valid = false
			}
		}
	}
	if !valid || !entry {
		return nil, ErrInvalid
	}
	start, end := fset.Position(f.Name.Pos()).Offset, fset.Position(f.Name.End()).Offset
	out := append([]byte{}, source[:start]...)
	out = append(out, []byte("main")...)
	out = append(out, source[end:]...)
	return out, nil
}

func command(ctx context.Context, dir, path string, args ...string) (*capture, *capture, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOMAXPROCS=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	out, stderr := &capture{}, &capture{}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	return out, stderr, err
}

// Execute runs only inside the dedicated behavior image/profile. It is not a
// host isolation attestation. The CLI additionally requires fixed /worker paths.
func Execute(ctx context.Context, j Job, dataset, source, harness []byte) (Result, error) {
	if j.Validate() != nil {
		return Result{}, ErrInvalid
	}
	r := Result{Schema: ResultSchema, JobSHA256: j.SHA256, PlanSHA256: j.Plan.SHA256, CandidateSHA256: j.Plan.Candidate.SHA256, Status: "failed", Values: []bool{}, Authority: "synthetic_candidate_behavior_worker_only"}
	finish := func(reason string) (Result, error) {
		r.ReasonCode = reason
		r.Missing = len(j.Plan.Cases) - len(r.Values)
		for i, v := range r.Values {
			if v == j.Plan.Cases[i].Expected {
				r.Matched++
			} else {
				r.Mismatched++
			}
		}
		r.AllAnnotationsMatch = r.Status == "completed" && r.Mismatched == 0
		r.SHA256 = workerplan.Hash(r)
		return r, r.Validate(j)
	}
	if ctx.Err() != nil {
		return finish("cancelled_before_build")
	}
	if j.Plan.VerifyDataset(dataset) != nil || string(source) != j.Plan.Candidate.Source.Text || string(harness) != Harness(j.Plan) {
		return finish("input_mismatch")
	}
	main, err := prepare(source)
	if err != nil {
		return finish("source_rejected")
	}
	dir, err := os.MkdirTemp("/tmp", "mantik-behavior-")
	if err != nil {
		return finish("workspace_failed")
	}
	defer os.RemoveAll(dir)
	for name, raw := range map[string][]byte{"candidate.go": main, "harness.go": harness} {
		if os.WriteFile(filepath.Join(dir, name), raw, 0600) != nil {
			return finish("workspace_failed")
		}
	}
	build, cancel := context.WithTimeout(ctx, time.Duration(BuildSeconds)*time.Second)
	defer cancel()
	for _, tool := range []string{"compile", "link"} {
		out, stderr, err := command(build, dir, "/compiler/"+tool, "-V=full")
		if err != nil || out.truncated || stderr.truncated || strings.TrimSpace(out.text()) != tool+" version "+workerplan.Runtime || stderr.text() != "" {
			diagnostic := &capture{}
			_, _ = diagnostic.Write(out.raw)
			_, _ = diagnostic.Write(stderr.raw)
			r.Diagnostics = diagnostic.text()
			r.DiagnosticsTruncated = diagnostic.truncated || out.truncated || stderr.truncated
			if ctx.Err() != nil {
				return finish("build_cancelled")
			}
			if build.Err() != nil {
				return finish("build_timeout")
			}
			return finish("toolchain_mismatch")
		}
	}
	for _, step := range []struct {
		tool string
		args []string
	}{
		{"compile", []string{"-complete", "-p", "main", "-o", dir + "/main.a", dir + "/candidate.go", dir + "/harness.go"}},
		{"link", []string{"-importcfg", "/compiler/runtime.cfg", "-o", dir + "/candidate", dir + "/main.a"}},
	} {
		out, stderr, err := command(build, dir, "/compiler/"+step.tool, step.args...)
		diagnostic := &capture{}
		_, _ = diagnostic.Write(out.raw)
		_, _ = diagnostic.Write(stderr.raw)
		r.Diagnostics = diagnostic.text()
		r.DiagnosticsTruncated = diagnostic.truncated || out.truncated || stderr.truncated
		if ctx.Err() != nil {
			return finish("build_cancelled")
		}
		if build.Err() != nil {
			return finish("build_timeout")
		}
		if r.DiagnosticsTruncated {
			return finish("build_output_limit")
		}
		if err != nil {
			return finish("build_failed")
		}
	}
	r.BuildCompleted = true
	r.RunAttempted = true
	run, stop := context.WithTimeout(ctx, time.Duration(RunSeconds)*time.Second)
	defer stop()
	out, stderr, err := command(run, dir, dir+"/candidate")
	r.RunStdout, r.RunStderr = out.text(), stderr.text()
	r.RunOutputTruncated = out.truncated || stderr.truncated
	var valid bool
	r.Values, valid = parseValues(r.RunStderr, len(j.Plan.Cases))
	if ctx.Err() != nil {
		return finish("run_cancelled")
	}
	if run.Err() != nil {
		return finish("run_timeout")
	}
	if r.RunOutputTruncated {
		return finish("run_output_limit")
	}
	if err != nil {
		return finish("run_failed")
	}
	if !valid || len(out.raw) != 0 {
		return finish("run_output_invalid")
	}
	r.Status = "completed"
	for i, v := range r.Values {
		if v != j.Plan.Cases[i].Expected {
			r.Status = "mismatch"
		}
	}
	return finish("fixture_compared")
}
