package candidatebuild

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Compiler = "/compiler/compile"

type compilerCall func(context.Context, ...string) ([]byte, bool, error)

type diagnosticBuffer struct {
	raw       []byte
	truncated bool
}

func (b *diagnosticBuffer) Write(raw []byte) (int, error) {
	n := len(raw)
	remaining := MaxDiagnostics - len(b.raw)
	if len(raw) > remaining {
		b.truncated = true
		raw = raw[:remaining]
	}
	b.raw = append(b.raw, raw...)
	return n, nil
}

var _ io.Writer = (*diagnosticBuffer)(nil)

func compile(ctx context.Context, args ...string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, Compiler, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=local", "CGO_ENABLED=0"}
	cmd.Dir = "/worker"
	cmd.WaitDelay = time.Second
	out := &diagnosticBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	return out.raw, out.truncated, err
}

// Execute is the container-side adapter, not a host isolation attestation.
// Its fixed compiler writes a discarded archive; candidate init/functions never run.
func Execute(ctx context.Context, j Job, source, harness []byte) (Result, error) {
	return execute(ctx, j, source, harness, compile)
}
func execute(ctx context.Context, j Job, source, harness []byte, run compilerCall) (Result, error) {
	if j.Validate() != nil {
		return Result{}, ErrInvalid
	}
	r := Result{Schema: ResultSchema, JobSHA256: j.SHA256, CandidateSHA256: j.Candidate.SHA256, Interface: Interface, Status: "failed", Authority: "candidate_compile_conformance_only"}
	finish := func(code string, raw []byte, truncated bool) (Result, error) {
		r.ReasonCode = code
		r.Diagnostics = strings.ToValidUTF8(string(raw), "?")
		r.DiagnosticsTruncated = truncated
		r.DiagnosticsSHA256 = workerplan.Digest([]byte(r.Diagnostics))
		r.SHA256 = workerplan.Hash(r)
		return r, r.Validate(j)
	}
	if ctx.Err() != nil {
		return finish("cancelled_before_compile", nil, false)
	}
	if !ValidInputs(j, source, harness) {
		return finish("input_mismatch", nil, false)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "candidate.go", source, 0)
	if err != nil || f.Name.Name != "candidate" || len(f.Imports) != 0 {
		return finish("interface_rejected", []byte("requires valid Go package candidate with no imports"), false)
	}
	entry := false
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Entry" && fn.Recv == nil {
			entry = true
		}
	}
	if !entry {
		return finish("interface_rejected", []byte("requires a top-level Entry function"), false)
	}
	work, cancel := context.WithTimeout(ctx, time.Duration(CompileSeconds)*time.Second)
	defer cancel()
	version, truncated, err := run(work, "-V=full")
	if ctx.Err() != nil {
		return finish("cancelled_before_compile", nil, false)
	}
	if err != nil {
		return finish("compiler_unavailable", version, truncated)
	}
	if truncated || strings.TrimSpace(string(version)) != "compile version "+workerplan.Runtime {
		return finish("compiler_version_mismatch", version, truncated)
	}
	r.CompilerVersionChecked = true
	raw, truncated, err := run(work, "-complete", "-p", "candidate", "-o", "/dev/null", "/worker/candidate.go", "/worker/interface.go")
	if ctx.Err() != nil {
		return finish("compile_cancelled", raw, truncated)
	}
	if work.Err() != nil {
		return finish("compile_timeout", raw, truncated)
	}
	if truncated {
		return finish("diagnostic_limit", raw, true)
	}
	if err != nil {
		return finish("compile_failed", raw, false)
	}
	r.Status = "completed"
	r.SourceCompiled = true
	return finish("interface_compiled", raw, false)
}
